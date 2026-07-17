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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// buildContainer replicates the behavior of
// https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/kuberuntime/kuberuntime_container.go
func (h *PodHandler) buildContainer(container *corev1.Container, containerStatus *corev1.ContainerStatus) (Container, error) {
	/*---------------------------------------------------
	 * Determine the effective security context
	 *---------------------------------------------------*/
	effectiSecurityContext := DetermineEffectiveSecurityContext(h.Pod, container)
	uid, gid := DetermineEffectiveRunAsUser(effectiSecurityContext)

	/*---------------------------------------------------
	 * Generate Environment Variables
	 *---------------------------------------------------*/
	envFileTemplate, err := ParseTemplate(GenerateEnvTemplate)
	if err != nil {
		return Container{}, fmt.Errorf("generate env template error: %w", err)
	}

	fields := GenerateEnvFields{
		Variables: append(container.Env, h.podEnvVariables...),
	}

	envFileContent := strings.Builder{}

	if err := envFileTemplate.Execute(&envFileContent, fields); err != nil {
		/*-- since both the template and fields are internal to the code, the evaluation should always succeed	--*/
		return Container{}, fmt.Errorf("failed to evaluate container execution template: %w", err)
	}

	envfilePath := h.podDirectory.Container(container.Name).EnvFilePath()

	if err := os.WriteFile(envfilePath, []byte(envFileContent.String()), endpoint.PodGlobalDirectoryPermissions); err != nil {
		return Container{}, fmt.Errorf("cannot write env file for container '%s' of pod '%s': %w", container.Name, h.podKey, err)
	}

	/*---------------------------------------------------
	 * Prepare Mountpoints
	 *---------------------------------------------------*/
	binds := make([]string, len(container.VolumeMounts))

	// check the code from https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/kubelet_pods.go#L196
	for i, mount := range container.VolumeMounts {
		hostPath := filepath.Join(h.podDirectory.VolumeDir(), mount.Name)

		subPath := mount.SubPath
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
				// Create the sub path now because if it's auto-created later when referenced, it may have an
				// incorrect ownership and mode.
				// The placeholder should normally be of the same type (dir or file) as the bind target.
				// However, at this point we do not have access to the bind.
				// For this reason, we follow the convention that dir should be marked "/path/subpath/" whereas
				// files should be marked as "/path/subpath".
				//
				// For the particular case of Argo, we know that "0" are always dirs.
				if mount.SubPath == "0" {
					if err := hostutil.SafeMakeDir(subPath, hostPath, endpoint.PodGlobalDirectoryPermissions); err != nil {
						return Container{}, fmt.Errorf("failed to create dir placeholder. subpath:'%s': %w", subPathFile, err)
					}
				} else {
					// A file is enough for all possible targets (symlink, device, pipe,
					// socket, ...), bind-mounting them into a file correctly changes type
					// of the target file.
					if err = os.WriteFile(subPathFile, []byte{}, endpoint.PodGlobalDirectoryPermissions); err != nil {
						return Container{}, fmt.Errorf("failed to create placeholder. subpath:'%s': %w", subPathFile, err)
					}
				}
			}

			// mount the subpath
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

	if container.ImagePullPolicy == corev1.PullNever {
		img, err = image.ResolveLocal(compute.HPK.ImageDir(), container.Image)
	} else {
		img, err = image.Pull(compute.HPK.ImageDir(), image.Docker, container.Image)
	}

	if err != nil {
		return Container{}, fmt.Errorf("ImagePull error. Image:%s: %w", container.Image, err)
	}

	// if there is no command, use the run mode, which will execute the runscript
	// defined in the Entrypoint of the image.
	executionMode := "exec"
	if container.Command == nil {
		executionMode = "run"
	}

	/*---------------------------------------------------
	 * Prepare fields for Container Template
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
		JobIDPath:     containerPath.IDPath(),
		ExitCodePath:  containerPath.ExitCodePath(),
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

/*************************************************************

		Load Container status from the FS

*************************************************************/

func SyncContainerStatuses(pod *corev1.Pod) {
	podKey := client.ObjectKeyFromObject(pod)
	podDir := compute.HPK.Pod(podKey)

	/*---------------------------------------------------
	 * Generic Handler for ContainerStatus
	 *---------------------------------------------------*/
	handleStatus := func(containerStatus *corev1.ContainerStatus) {
		prevState := containerStatus.State

		/*-- Presence of Exit Code indicates Terminated  State--*/
		exitCodePath := podDir.Container(containerStatus.Name).ExitCodePath()
		exitCode, exitCodeExists := readIntFromFile(exitCodePath)

		if exitCodeExists {
			// prepare some messages
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

			// Update LastTerminationState and RestartCount on initial transition to Terminated
			if prevState.Terminated == nil {
				containerStatus.LastTerminationState = prevState
				if exitCode != 0 {
					containerStatus.RestartCount++
				}
			}

			// set current status to terminate.
			containerStatus.State.Waiting = nil
			containerStatus.State.Running = nil
			containerStatus.State.Terminated = &corev1.ContainerStateTerminated{
				ExitCode:    int32(exitCode),
				Signal:      0,
				Reason:      reason,
				Message:     message,
				StartedAt:   startedAt,
				FinishedAt:  metav1.Now(), // fixme: get it from the file's ctime
				ContainerID: containerStatus.ContainerID,
			}

			return
		}

		jobIDPath := podDir.Container(containerStatus.Name).IDPath()
		jobID, jobIDExists := readStringFromFile(jobIDPath)

		/*-- Presence of Job ID indicated Running state (need to be set only once)--*/
		if jobIDExists {
			if containerStatus.State.Running == nil {
				runtime.SetContainerStatusID(containerStatus, jobID)

				containerStatus.State.Waiting = nil
				containerStatus.State.Running = &corev1.ContainerStateRunning{
					StartedAt: metav1.Now(), // fixme: we should get this info from the file's ctime
				}
				containerStatus.State.Terminated = nil

				/*-- todo: since we do not support probes, make everything to look ok --*/
				started := true
				containerStatus.Started = &started
				containerStatus.Ready = true
			}

			return
		}

		/*-- Lack of jobID indicates Waiting state --*/
		containerStatus.State.Waiting = &corev1.ContainerStateWaiting{
			Reason:  "ContainerStarting",
			Message: "Container is starting on the host",
		}
		containerStatus.State.Running = nil
		containerStatus.State.Terminated = nil
	}

	/*---------------------------------------------------
	 * Iterate containers and call the Generic Handler
	 *---------------------------------------------------*/
	for i := 0; i < len(pod.Status.InitContainerStatuses); i++ {
		handleStatus(&pod.Status.InitContainerStatuses[i])
	}

	for i := 0; i < len(pod.Status.ContainerStatuses); i++ {
		handleStatus(&pod.Status.ContainerStatuses[i])
	}
}
