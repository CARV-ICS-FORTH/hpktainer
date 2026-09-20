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

package podhandler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hpk/internal/compute"
	"hpk/internal/compute/endpoint"
	"hpk/internal/compute/image"
	"hpk/internal/compute/runtime"
	kubecontainer "hpk/pkg/container"
	"hpk/pkg/hostutil"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	mounter "k8s.io/utils/mount"
)

// buildContainer replicates the container preparation behavior.
func (h *PodHandler) buildContainer(container *corev1.Container, containerStatus *corev1.ContainerStatus) (Container, error) {
	/*---------------------------------------------------
	 * Determine the effective security context
	 *---------------------------------------------------*/
	effectiSecurityContext := DetermineEffectiveSecurityContext(h.Pod, container)
	uid, gid := DetermineEffectiveRunAsUser(effectiSecurityContext)

	/*---------------------------------------------------
	 * Generate Environment Variables File
	 *---------------------------------------------------*/
	var b strings.Builder
	allEnvs := append(container.Env, h.podEnvVariables...)
	for _, envVar := range allEnvs {
		val := envVar.Value
		if val == ".status.podIP" {
			val = h.Pod.Status.PodIP
		}
		fmt.Fprintf(&b, "%s=%s\n", envVar.Name, EscapeSingleQuote(val))
	}

	envfilePath := h.podDirectory.Container(container.Name).EnvFilePath()
	if err := os.WriteFile(envfilePath, []byte(b.String()), endpoint.PodGlobalDirectoryPermissions); err != nil {
		return Container{}, fmt.Errorf("cannot write env file for container '%s' of pod '%s': %w", container.Name, h.podKey, err)
	}

	/*---------------------------------------------------
	 * Prepare Mountpoints
	 *---------------------------------------------------*/
	binds := make([]string, len(container.VolumeMounts))

	for i, mount := range container.VolumeMounts {
		hostPath := filepath.Join(h.podDirectory.VolumeDir(), mount.Name)

		subPath := mount.SubPath
		var err error
		if mount.SubPathExpr != "" {
			subPath, err = kubecontainer.ExpandContainerVolumeMounts(mount, h.podEnvVariables)
			if err != nil {
				return Container{}, fmt.Errorf("cannot expand env variables for container '%s' of pod '%s': %w", container.Name, h.podKey, err)
			}
		}

		if subPath != "" {
			if filepath.IsAbs(subPath) {
				return Container{}, fmt.Errorf("error SubPath '%s' must not be an absolute path", subPath)
			}

			subPathFile := filepath.Join(hostPath, subPath)

			subPathFileExists, err := mounter.PathExists(subPathFile)
			if err != nil {
				return Container{}, fmt.Errorf("could not determine if subPath exists. mount:'%v': %w", mount, err)
			}

			if !subPathFileExists {
				if mount.SubPath == "0" {
					if err := hostutil.SafeMakeDir(subPath, hostPath, endpoint.PodGlobalDirectoryPermissions); err != nil {
						return Container{}, fmt.Errorf("failed to create dir placeholder. subpath:'%s': %w", subPathFile, err)
					}
				} else {
					if err = os.WriteFile(subPathFile, []byte{}, endpoint.PodGlobalDirectoryPermissions); err != nil {
						return Container{}, fmt.Errorf("failed to create placeholder. subpath:'%s': %w", subPathFile, err)
					}
				}
			}

			hostPath = subPathFile
		}

		accessMode := "rw"
		if mount.ReadOnly {
			accessMode = "ro"
		}

		binds[i] = hostPath + ":" + mount.MountPath + ":" + accessMode
	}

	containerID := fmt.Sprintf("%s_%s_%s", h.Pod.GetNamespace(), h.Pod.GetName(), container.Name)
	/*---------------------------------------------------
	 * Prepare Container Image
	 *---------------------------------------------------*/
	var img *image.Image
	var err error

	if container.ImagePullPolicy == corev1.PullNever {
		img, err = image.ResolveLocal(compute.HPK.ImageDir(), container.Image)
	} else {
		img, err = image.Pull(compute.HPK.ImageDir(), image.Docker, container.Image)
	}

	if err != nil {
		return Container{}, fmt.Errorf("ImagePull error. Image:%s: %w", container.Image, err)
	}

	executionMode := "exec"
	if container.Command == nil {
		executionMode = "run"
	}

	/*---------------------------------------------------
	 * Prepare fields for Container Execution
	 *---------------------------------------------------*/
	containerPath := h.podDirectory.Container(container.Name)

	c := Container{
		InstanceName:  containerID,
		RunAsUser:     uid,
		RunAsGroup:    gid,
		ImageFilePath: img.Filepath,
		EnvFilePath:   containerPath.EnvFilePath(),
		Binds:         binds,
		Command:       kubecontainer.ExpandContainerCommandOnlyStatic(container.Command, container.Env),
		Args:          kubecontainer.ExpandContainerCommandOnlyStatic(container.Args, container.Env),
		ExecutionMode: executionMode,
		LogsPath:      containerPath.LogsPath(),
	}

	/*---------------------------------------------------
	 * Update Container Status Fields
	 *---------------------------------------------------*/
	containerStatus.Name = container.Name
	containerStatus.ContainerID = containerID
	containerStatus.Image = container.Image
	containerStatus.ImageID = img.Filepath

	return c, err
}

// SetContainerRunning marks the container status as running.
func SetContainerRunning(containerStatus *corev1.ContainerStatus, pid int, startTime uint64) {
	containerStatus.ContainerID = runtime.FormatProcessJobID(pid, startTime)
	containerStatus.State.Waiting = nil
	containerStatus.State.Running = &corev1.ContainerStateRunning{
		StartedAt: metav1.Now(),
	}
	containerStatus.State.Terminated = nil
	started := true
	containerStatus.Started = &started
	containerStatus.Ready = true
}

// SetContainerTerminated marks the container status as terminated.
func SetContainerTerminated(containerStatus *corev1.ContainerStatus, exitCode int) {
	prevState := containerStatus.State

	var reason, message string
	if exitCode == 0 {
		reason = "Completed"
		message = "Container successfully terminated"
	} else {
		reason = "Error(" + containerStatus.Name + ")"
		message = HumanReadableCode(exitCode)
	}

	startedAt := metav1.Time{}
	if prevState.Running != nil {
		startedAt = prevState.Running.StartedAt
	} else if prevState.Terminated != nil {
		startedAt = prevState.Terminated.StartedAt
	}

	if prevState.Terminated == nil {
		containerStatus.LastTerminationState = prevState
		if exitCode != 0 {
			containerStatus.RestartCount++
		}
	}

	containerStatus.Ready = false
	containerStatus.State.Waiting = nil
	containerStatus.State.Running = nil
	containerStatus.State.Terminated = &corev1.ContainerStateTerminated{
		ExitCode:    int32(exitCode),
		Signal:      0,
		Reason:      reason,
		Message:     message,
		StartedAt:   startedAt,
		FinishedAt:  metav1.Now(),
		ContainerID: containerStatus.ContainerID,
	}
}

// SyncContainerStatuses updates in-memory container statuses and verifies alive processes in /proc.
func SyncContainerStatuses(pod *corev1.Pod) {
	checkStatus := func(containerStatus *corev1.ContainerStatus) {
		if containerStatus.State.Terminated != nil {
			return
		}

		if containerStatus.State.Running != nil {
			// Check if process has died in /proc
			if containerStatus.ContainerID != "" {
				pid, startTime, err := runtime.ParseProcessJobID(containerStatus.ContainerID)
				if err == nil && pid > 0 {
					if runtime.IsProcessDead(pid) {
						SetContainerTerminated(containerStatus, 137)
						return
					}
					_ = startTime
				}
			}
			return
		}

		if containerStatus.State.Waiting == nil {
			containerStatus.State.Waiting = &corev1.ContainerStateWaiting{
				Reason:  "ContainerStarting",
				Message: "Container is starting",
			}
		}
	}

	for i := range pod.Status.InitContainerStatuses {
		checkStatus(&pod.Status.InitContainerStatuses[i])
	}
	for i := range pod.Status.ContainerStatuses {
		checkStatus(&pod.Status.ContainerStatuses[i])
	}
}
