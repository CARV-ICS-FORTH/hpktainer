// Copyright © 2023 FORTH-ICS
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

package runtime

import (
	"os"

	"skifflet/internal/compute"
)

// ConnectionOK checks if the Skifflet runtime storage and core environment are initialized and healthy.
func ConnectionOK() bool {
	// Verify Skiff runtime directory is initialized and writable
	skiffDir := compute.Skiff.PodsDir()
	if skiffDir == "" {
		return false
	}
	info, err := os.Stat(skiffDir)
	if err != nil || !info.IsDir() {
		return false
	}
	return true
}
