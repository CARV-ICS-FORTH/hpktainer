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

// Package runtime contains code for accessing compute resources.
package runtime

import (
	"fmt"
	"path/filepath"

	"al.essio.dev/pkg/shellescape"
	"hpk/pkg/process"
)

// SubmitJob executes a job script directly via bash in the background.
func SubmitJob(scriptFile string) (string, error) {
	outputFile := filepath.Join(filepath.Dir(scriptFile), "submit.log")
	quotedScript := shellescape.Quote(scriptFile)
	quotedOutput := shellescape.Quote(outputFile)

	// Execute script directly via bash in background
	commandString := fmt.Sprintf("nohup bash -l -c 'source %s' >> %s 2>&1 &", quotedScript, quotedOutput)
	out, err := process.Execute("bash", "-c", commandString)
	fmt.Println("Submitting (Direct bash mode): ", commandString)

	if err != nil {
		return "", fmt.Errorf("job submission error. out: '%s': %w", out, err)
	}

	// For direct bash mode, return a placeholder job ID
	// The actual PID will be read from the container's jobid file by the event system
	return "0", nil
}
