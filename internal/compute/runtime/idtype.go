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
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type JobIDType string

const (
	JobIDTypeProcess JobIDType = "pid://"
)

func SetPodID(pod *corev1.Pod, idType JobIDType, value string) {
	metav1.SetMetaDataAnnotation(&pod.ObjectMeta, "pod.hpk/id", string(idType)+value)
}

func SetContainerStatusID(status *corev1.ContainerStatus, typedValue string) {
	// ensure that the value follows an expected format.
	_ = parseIDType(typedValue)

	status.ContainerID = typedValue
}

func HasJobID(pod *corev1.Pod) bool {
	_, exists := pod.GetAnnotations()["pod.hpk/id"]

	return exists
}

func parseIDType(raw string) string {
	if strings.HasPrefix(raw, string(JobIDTypeProcess)) {
		parts := strings.Split(raw, string(JobIDTypeProcess))
		if len(parts) > 1 {
			return parts[1]
		}
	}

	return strings.TrimSpace(raw)
}



// IsProcessJobID checks if the given job ID represents a direct process PID.
// A job ID that consists only of digits is considered a process PID.
func IsProcessJobID(jobID string) bool {
	if strings.TrimSpace(jobID) == "" {
		return false
	}

	// If it's all digits, it's a process PID.
	for _, ch := range jobID {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
