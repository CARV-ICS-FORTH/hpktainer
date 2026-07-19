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
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
)

var ErrInvalidJob = errors.New("invalid job id")

// KillProcessByPID terminates a process by its PID using syscall.Kill.
// Returns an error if the process cannot be terminated.
func KillProcessByPID(pidStr string) (string, error) {
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

	return "", nil
}

