// Copyright © 2022 FORTH-ICS
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package runtime contains code for managing compute environments.
package runtime

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var ErrInvalidJob = errors.New("invalid job id")

// isProcessDead checks whether a process has exited or is in zombie state ('Z').
func isProcessDead(pid int) bool {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	if err != nil {
		return false
	}

	// On POSIX systems, syscall.Kill(pid, 0) returns nil for zombie processes,
	// but syscall.Getpgid(pid) returns syscall.ESRCH once the process has exited.
	if _, err := syscall.Getpgid(pid); errors.Is(err, syscall.ESRCH) {
		return true
	}

	// Check /proc/<pid>/stat on Linux for 'Z' or 'X' state
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if idx := bytes.LastIndexByte(data, ')'); idx != -1 && idx+2 < len(data) {
			state := data[idx+2]
			if state == 'Z' || state == 'X' {
				return true
			}
		}
	}

	return false
}

// KillProcessByPID terminates a process by its PID using syscall.Kill with a default timeout of 35 seconds.
func KillProcessByPID(pidStr string) (string, error) {
	return KillProcessByPIDWithTimeout(pidStr, 35*time.Second)
}

// KillProcessByPIDWithTimeout sends SIGTERM to a process by its PID and polls for process exit until timeout.
// If the process does not exit within timeout, SIGKILL is sent to force termination.
func KillProcessByPIDWithTimeout(pidStr string, timeout time.Duration) (string, error) {
	pidStr = strings.TrimSpace(pidStr)
	if pidStr == "" {
		return "", ErrInvalidJob
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return "", fmt.Errorf("%w: invalid pid '%s': %v", ErrInvalidJob, pidStr, err)
	}

	/*
		Send SIGTERM using syscall.Kill to the process
		and allow it to close gracefully.
	*/
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		// If the process does not exist (ESRCH), consider it as already terminated.
		if errors.Is(err, syscall.ESRCH) {
			return "", ErrInvalidJob
		}

		return "", fmt.Errorf("could not kill process '%d': %w", pid, err)
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if isProcessDead(pid) {
			return "", nil
		}

		if time.Now().After(deadline) {
			break
		}

		<-ticker.C
	}

	// Timeout reached: escalate to SIGKILL
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return "", fmt.Errorf("could not SIGKILL process '%d': %w", pid, err)
	}

	killDeadline := time.Now().Add(5 * time.Second)
	for {
		if isProcessDead(pid) {
			return "", nil
		}

		if time.Now().After(killDeadline) {
			return "", fmt.Errorf("process '%d' did not exit after SIGKILL", pid)
		}

		<-ticker.C
	}
}

