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

// Package slurm contains code for accessing compute resources.
package slurm

import (
	"fmt"
	"os"

	"hpk/internal/compute"
	"hpk/pkg/process"
)

// SubmitJob executes a job script directly via bash in the background.
func SubmitJob(scriptFile string) (string, error) {
	outputFile := os.Getenv("HOME") + "/.hpk/logs.log"

	// Execute script directly via bash in background
	commandString := fmt.Sprintf("nohup bash -l -c 'source %s' > %s 2>&1 &", scriptFile, outputFile)
	out, err := process.Execute("bash", "-c", commandString)
	fmt.Println("Submitting (Direct bash mode): ", commandString)

	if err != nil {
		compute.SystemPanic(err, "job submission error. out : '%s'", out)
	}

	// For direct bash mode, return a placeholder job ID
	// The actual PID will be read from the container's jobid file by the event system
	return "0", nil
}
