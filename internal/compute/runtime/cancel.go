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

	// On macOS / BSD systems without /proc, syscall.Getpriority returns ESRCH for zombie processes.
	if _, err := syscall.Getpriority(0, pid); errors.Is(err, syscall.ESRCH) {
		return true
	}

	// On POSIX systems, syscall.Getpgid(pid) returns syscall.ESRCH once the process has been reaped.
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

// isExpectedProcess checks whether a process's /proc/<pid>/comm or /proc/<pid>/cmdline
// matches expected container or helper process names. This prevents signaling unrelated host processes
// if a PID has been recycled after a crash.
func isExpectedProcess(pid int) bool {
	// If /proc is not present (e.g. non-Linux systems), skip check
	if _, err := os.Stat("/proc"); os.IsNotExist(err) {
		return true
	}

	commBytes, commErr := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	cmdlineBytes, cmdErr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if commErr != nil && cmdErr != nil {
		// Cannot inspect proc entry; process may be exiting or inaccessible
		return true
	}

	comm := strings.ToLower(string(commBytes))
	cmdline := strings.ToLower(string(cmdlineBytes))

	expectedKeywords := []string{
		"apptainer", "hpk-pause", "singularity", "starter", "hpk",
		"sleep", "sh", "bash", "python", "node", "ruby", "perl", "go", "test",
	}
	for _, kw := range expectedKeywords {
		if strings.Contains(comm, kw) || strings.Contains(cmdline, kw) {
			return true
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
	return KillPodProcessesWithTimeout(pidStr, nil, timeout)
}

// KillPodProcessesWithTimeout sends SIGTERM to primaryPIDStr and polls for process exit until timeout.
// It uses adaptive polling (50 ms for the first second, then 250 ms) to reduce CPU overhead during grace periods.
// If timeout is reached, SIGKILL is sent to primaryPIDStr AND to all process groups (-pgid) and PIDs in secondaryPIDStrs.
func KillPodProcessesWithTimeout(primaryPIDStr string, secondaryPIDStrs []string, timeout time.Duration) (string, error) {
	primaryPIDStr = strings.TrimSpace(primaryPIDStr)
	if primaryPIDStr == "" {
		return "", ErrInvalidJob
	}

	primaryPID, err := strconv.Atoi(primaryPIDStr)
	if err != nil {
		return "", fmt.Errorf("%w: invalid pid '%s': %v", ErrInvalidJob, primaryPIDStr, err)
	}

	// Verify PID is an expected process to prevent stale PID signaling on recycled host PIDs
	if !isExpectedProcess(primaryPID) {
		return "", fmt.Errorf("%w: process '%d' does not match expected container process signature", ErrInvalidJob, primaryPID)
	}

	var secondaryPIDs []int
	for _, s := range secondaryPIDStrs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := strconv.Atoi(s); err == nil && p > 0 {
			if isExpectedProcess(p) {
				secondaryPIDs = append(secondaryPIDs, p)
			}
		}
	}

	/*
		Send SIGTERM using syscall.Kill to the primary process
		and allow it to close gracefully.
	*/
	if err := syscall.Kill(primaryPID, syscall.SIGTERM); err != nil {
		// If the process does not exist (ESRCH), consider it as already terminated.
		if errors.Is(err, syscall.ESRCH) {
			return "", ErrInvalidJob
		}

		return "", fmt.Errorf("could not kill process '%d': %w", primaryPID, err)
	}

	deadline := time.Now().Add(timeout)
	fastUntil := time.Now().Add(1 * time.Second)

	for {
		if isProcessDead(primaryPID) {
			return "", nil
		}

		if time.Now().After(deadline) {
			break
		}

		pollInterval := 50 * time.Millisecond
		if time.Now().After(fastUntil) {
			pollInterval = 250 * time.Millisecond
		}
		time.Sleep(pollInterval)
	}

	// Timeout reached: escalate to SIGKILL for primary PID
	if err := syscall.Kill(primaryPID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return "", fmt.Errorf("could not SIGKILL process '%d': %w", primaryPID, err)
	}

	// Escalate SIGKILL to secondary container process groups (-pgid) and PIDs as second-tier fallback
	for _, secPID := range secondaryPIDs {
		// Signal process group leadership first (-secPID)
		_ = syscall.Kill(-secPID, syscall.SIGKILL)
		// Signal container PID directly
		_ = syscall.Kill(secPID, syscall.SIGKILL)
	}

	killDeadline := time.Now().Add(5 * time.Second)

	for {
		if isProcessDead(primaryPID) {
			return "", nil
		}

		if time.Now().After(killDeadline) {
			return "", fmt.Errorf("process '%d' did not exit after SIGKILL", primaryPID)
		}

		time.Sleep(250 * time.Millisecond)
	}
}
