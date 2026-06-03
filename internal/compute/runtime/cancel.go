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
	"fmt"
	"strings"

	"errors"
	"hpk/pkg/process"
)



var ErrRety = errors.New("retry later")

var ErrInvalidJob = errors.New("invalid job id")

// KillProcessByPID terminates a process by its PID using kill command.
// Returns an error if the process cannot be terminated.
func KillProcessByPID(pid string) (string, error) {
	pid = strings.TrimSpace(pid)
	if pid == "" {
		return "", ErrInvalidJob
	}

	/*
	 Install trap for the signals INT and TERM to
	 terminate the process and its children.
	 Send SIGTERM using kill to the main process
	 and wait for it to close gracefully.
	*/
	out, err := process.Execute("pkill", "-P", pid)
	if err != nil {
		outStr := string(out)

		// if the process does not exist, consider it as terminated
		if strings.Contains(outStr, "No such process") {
			return outStr, ErrInvalidJob
		}

		return string(out), fmt.Errorf("Could not kill process: %w", err)
	}

	return string(out), nil
}
