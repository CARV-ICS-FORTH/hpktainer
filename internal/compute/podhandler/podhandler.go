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

package podhandler

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"hpk/internal/compute"
	"hpk/internal/compute/endpoint"
	"hpk/internal/compute/image"
	"hpk/internal/compute/runtime"
	"hpk/pkg/filenotify"

	"errors"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ErrNoProcessIDInControlFiles = errors.New("no process id found in control files")

// LoadPodFromKey waits LoadPodFromFile with filePath discovery.
func LoadPodFromKey(podRef client.ObjectKey) (*corev1.Pod, error) {
	filePath := compute.HPK.Pod(podRef).EncodedJSONPath()

	return LoadPodFromFile(filePath)
}

// LoadPodFromFile will read, decode, and return a Pod from a file.
func LoadPodFromFile(filePath string) (*corev1.Pod, error) {
	if filePath == "" {
		return nil, fmt.Errorf("file path not specified")
	}

	podDef, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file path %s: %w", filePath, err)
	}

	if len(podDef) == 0 {
		return nil, fmt.Errorf("file was empty: %s", filePath)
	}

	var pod corev1.Pod

	if err := json.Unmarshal(podDef, &pod); err != nil {
		return nil, fmt.Errorf("failed decoding file '%s': %w", filePath, err)
	}

	return &pod, nil
}

func SavePodToFile(_ context.Context, pod *corev1.Pod) error {
	if pod == nil {
		return fmt.Errorf("empty pod")
	}

	podRef := client.ObjectKeyFromObject(pod)
	filePath := compute.HPK.Pod(podRef).EncodedJSONPath()

	podDef, err := json.Marshal(pod)
	if err != nil {
		return fmt.Errorf("failed encoding pod: %w", err)
	}

	if err := os.WriteFile(filePath, podDef, endpoint.PodSpecJsonFilePermissions); err != nil {
		return fmt.Errorf("failed to write file path '%s': %w", filePath, err)
	}

	return nil
}

func parseProcessPID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("empty process id")
	}

	if strings.HasPrefix(value, string(runtime.JobIDTypeProcess)) {
		value = strings.TrimPrefix(value, string(runtime.JobIDTypeProcess))
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("empty process id")
		}
	}

	if !runtime.IsProcessJobID(value) {
		return "", fmt.Errorf("invalid process id '%s'", raw)
	}

	return value, nil
}

func resolveProcessPIDFromControlFiles(pod *corev1.Pod, podDir endpoint.PodPath, logger logr.Logger) (string, error) {
	// First check main containers
	for _, container := range pod.Spec.Containers {
		jobIDPath := podDir.Container(container.Name).IDPath()
		if raw, ok := readStringFromFile(jobIDPath); ok {
			pid, err := parseProcessPID(raw)
			if err != nil {
				logger.Info(" * Invalid process id in control file", "path", jobIDPath, "value", raw, "err", err)

				continue
			}

			return pid, nil
		}
	}

	// Fallback to init containers
	for _, container := range pod.Spec.InitContainers {
		jobIDPath := podDir.Container(container.Name).IDPath()
		if raw, ok := readStringFromFile(jobIDPath); ok {
			pid, err := parseProcessPID(raw)
			if err != nil {
				logger.Info(" * Invalid process id in control file", "path", jobIDPath, "value", raw, "err", err)

				continue
			}

			return pid, nil
		}
	}

	// Fallback to wrapper process pid file if written
	wrapperPIDPath := filepath.Join("/tmp", fmt.Sprintf("%s_%s", pod.Namespace, pod.Name), ".pid")
	if raw, ok := readStringFromFile(wrapperPIDPath); ok {
		pid, err := parseProcessPID(raw)
		if err == nil {
			return pid, nil
		}
	}

	return "", ErrNoProcessIDInControlFiles
}

/*
DeletePod takes a Pod Reference and deletes the Pod from the provider.
DeletePod may be called multiple times for the same pod.

Notice that by using the reference, we operate on the local copy instead of the remote. This serves two purposes:
1) We can extract updated information from .spec (Kubernetes only fetches .Status)
2) We can have "fresh" information that is not yet propagated to Kubernetes
*/
func DeletePod(podKey client.ObjectKey, watcher filenotify.FileWatcher) bool {
	logger := compute.DefaultLogger.WithValues("pod", podKey)

	localPod, err := LoadPodFromKey(podKey)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// This behavior may raise when trying to delete a deleted pod.
			// However, deleting a pod from the fs does not guarantee deletion.
			// For this reason, we just need to continue.
			return true
		}

		logger.Error(err, "failed to load pod for deletion")
		return false
	}

	podDir := compute.HPK.Pod(podKey)

	/*---------------------------------------------------
	 * Kill Direct Process
	 *---------------------------------------------------*/
	// Read PID exclusively from controlfiles.
	pid, err := resolveProcessPIDFromControlFiles(localPod, podDir, logger)
	if err != nil {
		if errors.Is(err, ErrNoProcessIDInControlFiles) {
			logger.Info(" * No process id found in control files; assuming process already exited", "pod", podKey)

			goto remove_pod
		}

		logger.Error(err, "failed to resolve process id from control files", "pod", podKey)
		goto remove_pod
	}

	logger.Info(" * Resolved process id from control files", "pid", pid)
	if strings.TrimSpace(pid) == "" {
		logger.Info(" * Empty process id resolved from control files; assuming process already exited", "pod", podKey)

		goto remove_pod
	}

	{
		out, err := runtime.KillProcessByPID(pid)
		if err != nil {
			if errors.Is(err, runtime.ErrInvalidJob) {
				logger.Info(" * No such process", "pid", pid, "pod", podKey)
				// the process does not exist, so it can be considered as deleted.
				goto remove_pod
			}

			logger.Error(err, "failed to kill process", "pid", pid, "pod", podKey, "out", out)
			goto remove_pod
		}

		logger.Info(" * Process is terminated", "pid", pid, "pod", podKey, "out", out)
	}

	/*---------------------------------------------------
	 * Remove watcher for Pod Directory
	 *---------------------------------------------------*/
remove_pod:
	// because fswatch does not work recursively, we cannot have the container directories nested within the pod.
	// instead, we use a flat directory in the format "podir/containername.{jid,stdout,stdour,...}"
	if err := watcher.Remove(podDir.ControlFileDir()); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Error(err, "deregister watcher for path has failed", "directory", podDir.ControlFileDir())
	}

	logger.Info(" * Pod Watcher has been removed.")

	/*---------------------------------------------------
	 * Remove Pod Directory
	 *---------------------------------------------------*/

	if err := os.RemoveAll(podDir.String()); err != nil {
		// if trying to remove directory from the host fails, try to delete it using a fakeroot container.
		if errors.Is(err, fs.ErrPermission) {
			compute.DefaultLogger.Info(" * Failed to remove directory from host. Try using fakeroot container.",
				"err", err,
			)

			// try to delete directory contents using the fakeroot from pause container.
			out, err := runtime.DefaultPauseImage.FakerootExec(
				[]string{"--mount", "type=bind,src=" + podDir.String() + ",dst=/pod"}, // mount the pod directory in apptainer
				[]string{"find", "/pod", "-mindepth", "1", "-delete"},                 // remove the pod directory contents using fakeroot
			)

			compute.DefaultLogger.Info(" * Result",
				"out", out,
				"debug", []string{"-B", podDir.String() + ":" + podDir.String() + ":rw"},
			)

			if err != nil {
				logger.Error(err, "failed to forcibly remove pod directory contents using fakeroot", "directory", podDir)
				return false
			}

			if err := os.RemoveAll(podDir.String()); err != nil {
				logger.Error(err, "failed to remove pod directory after fakeroot cleanup", "directory", podDir)
				return false
			}
		} else {
			logger.Error(err, "failed to remove pod directory", "directory", podDir)
			return false
		}
	}

	logger.Info(" * Pod directory is removed")

	/*---------------------------------------------------
	 * Garbage Collect Namespace
	 *---------------------------------------------------*/
	namespaceDir := filepath.Dir(podDir.String())
	if empty, _ := endpoint.IsEmpty(namespaceDir); empty {
		_ = os.RemoveAll(namespaceDir)

		logger.Info(" * Namespace directory is removed")
	}

	return true
}

type PodHandler struct {
	*corev1.Pod

	podKey client.ObjectKey

	podEnvVariables []corev1.EnvVar
	podDirectory    endpoint.PodPath

	logger logr.Logger
}

func CreatePod(ctx context.Context, pod *corev1.Pod, watcher filenotify.FileWatcher, useTmp bool) {
	/*---------------------------------------------------
	 * Prepare the Pod Execution Environment
	 *---------------------------------------------------*/
	podKey := client.ObjectKeyFromObject(pod)
	logger := compute.DefaultLogger.WithValues("pod", podKey)

	podEnvVars, err := FromServicesForPod(ctx, pod)
	if err != nil {
		compute.PodError(pod, "EnvVarError", "failed to list services when setting up env vars: %v", err)
		_ = SavePodToFile(ctx, pod)
		return
	}

	h := PodHandler{
		Pod:             pod,
		podKey:          podKey,
		podDirectory:    compute.HPK.Pod(podKey),
		logger:          logger,
		podEnvVariables: podEnvVars,
	}

	for _, env := range h.podEnvVariables {
		logger.Info("env", "name", env.Name)
	}
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			logger.Info("container env", "name", env.Name)
		}
	}
	// create directory for the job environment.
	if err := os.MkdirAll(h.podDirectory.JobDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		compute.PodError(pod, "PodDirectoryError", "Cant create pod directory '%s': %v", h.podDirectory.JobDir(), err)
		_ = SavePodToFile(ctx, h.Pod)
		return
	}

	// create directory for logs.
	if err := os.MkdirAll(h.podDirectory.LogDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		compute.PodError(pod, "PodDirectoryError", "cannot create log directory '%s': %v", h.podDirectory.LogDir(), err)
		_ = SavePodToFile(ctx, h.Pod)
		return
	}

	// create directory for volumes.
	if err := os.MkdirAll(h.podDirectory.VolumeDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		compute.PodError(pod, "PodDirectoryError", "cannot create volume directory '%s': %v", h.podDirectory.VolumeDir(), err)
		_ = SavePodToFile(ctx, h.Pod)
		return
	}

	// create directory for control files.
	if err := os.MkdirAll(h.podDirectory.ControlFileDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		compute.PodError(pod, "PodDirectoryError", "cannot create control file directory '%s': %v", h.podDirectory.ControlFileDir(), err)
		_ = SavePodToFile(ctx, h.Pod)
		return
	}

	// Persist pod metadata early so in-progress pods are not considered corrupted
	// by startup reconciliation while volume setup is still running.
	if err := SavePodToFile(ctx, h.Pod); err != nil {
		compute.PodError(pod, "SavePodError", "failed to persist pod metadata early: %v", err)
		return
	}

	// watch for control files on the root directory of the pod.
	// because fswatch does not work recursively, we cannot have the container directories nested within the pod.
	// instead, we use a flat directory in the format "podir/containername.{jid,stdout,stdour,...}"
	if err := watcher.Add(h.podDirectory.ControlFileDir()); err != nil {
		if errors.Is(err, filenotify.ErrWatchExists) {
			logger.Info("Pod watcher already exists", "directory", h.podDirectory.ControlFileDir())
		} else {
			compute.PodError(pod, "WatcherError", "register watcher for path '%s' has failed: %v", h.podDirectory.ControlFileDir(), err)
			_ = SavePodToFile(ctx, h.Pod)
			return
		}
	}

	logger.Info(" * Pod Environment has been created ")

	/*---------------------------------------------------
	 * Mount Volumes
	 *---------------------------------------------------*/
	for _, vol := range h.Pod.Spec.Volumes {
		// h.Pod.Spec.Containers[0].VolumeMounts
		if err := h.mountVolumeSource(ctx, vol); err != nil {
			compute.PodError(pod, "VolumeError", "%v", err)
			_ = SavePodToFile(ctx, h.Pod)

			return
		}
	}

	h.logger.Info(" * All volumes have been mounted")

	/*---------------------------------------------------
	 * Build Container Commands
	 *---------------------------------------------------*/
	var initContainers []Container
	pod.Status.InitContainerStatuses = make([]corev1.ContainerStatus, len(pod.Spec.InitContainers))

	for i := range pod.Spec.InitContainers {
		initContainer := &pod.Spec.InitContainers[i]
		initContainerStatus := &pod.Status.InitContainerStatuses[i]

		c, err := h.buildContainer(initContainer, initContainerStatus)
		if err != nil {
			compute.PodError(pod, "InitContainerError", "failed to materialize pod.Spec.InitContainers[%d]", i)
			_ = SavePodToFile(ctx, h.Pod)

			return
		}

		initContainers = append(initContainers, c)
	}

	var containers []Container
	pod.Status.ContainerStatuses = make([]corev1.ContainerStatus, len(pod.Spec.Containers))

	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		containerStatus := &pod.Status.ContainerStatuses[i]

		c, err := h.buildContainer(container, containerStatus)
		if err != nil {
			compute.PodError(pod, "MainContainerError", "failed to materialize pod.Spec.Containers[%d]", i)
			_ = SavePodToFile(ctx, h.Pod)

			return
		}

		containers = append(containers, c)
	}

	/*---------------------------------------------------
	 * Handle Cgroups and Resource Reservation
	 *---------------------------------------------------*/
	// create cgroups for the pod
	if compute.Environment.EnableCgroupV2 {
		if _, err := os.Create(h.podDirectory.CgroupFilePath()); err != nil {
			compute.PodError(pod, "CgroupError", "Cant create cgroup configuration file '%s': %v", h.podDirectory.CgroupFilePath(), err)
			_ = SavePodToFile(ctx, h.Pod)
			return
		}

		logger.Info(" * Cgroups are set")
	}

	/*---------------------------------------------------
	 * Prepare Image for Pause Container
	 *---------------------------------------------------*/
	pauseImage, err := image.Pull(compute.HPK.ImageDir(), image.Docker, compute.Environment.PauseImage)
	if err != nil {
		compute.PodError(pod, "ImagePullError", "ImagePull error. Image:%s: %v", compute.Environment.PauseImage, err)
		_ = SavePodToFile(ctx, h.Pod)
		return
	}

	/*---------------------------------------------------
	 * Prepare Fields for Container Execution Templates
	 *---------------------------------------------------*/

	scriptTemplate, err := ParseTemplate(HostScriptTemplate)
	if err != nil {
		compute.PodError(pod, "TemplateError", "container execution template error: %v", err)
		_ = SavePodToFile(ctx, h.Pod)
		return
	}

	scriptFileContent := bytes.Buffer{}

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	// Set annotations from HostEnvironment
	pod.Annotations["kubeMasterHost"] = compute.Environment.KubeMasterHost
	pod.Annotations["containerRegistry"] = compute.Environment.ContainerRegistry
	pod.Annotations["apptainerBin"] = compute.Environment.ApptainerBin
	pod.Annotations["enableCgroupV2"] = fmt.Sprintf("%t", compute.Environment.EnableCgroupV2)
	pod.Annotations["workingDirectory"] = compute.Environment.WorkingDirectory
	pod.Annotations["kubeDNS"] = compute.Environment.KubeDNS

	// Set annotations from VirtualEnvironment
	pod.Annotations["cgroupFilePath"] = h.podDirectory.CgroupFilePath()
	pod.Annotations["ipAddressPath"] = h.podDirectory.IPAddressPath()
	pod.Annotations["stdoutPath"] = h.podDirectory.StdoutPath()
	pod.Annotations["stderrPath"] = h.podDirectory.StderrPath()
	pod.Annotations["sysErrorFilePath"] = h.podDirectory.SysErrorFilePath()

	pod.Annotations["PauseImage"] = compute.Environment.PauseImage

	if err := scriptTemplate.Execute(&scriptFileContent, JobFields{
		Pod:                h.podKey,
		PauseImageFilePath: pauseImage.Filepath,
		HostEnv:            compute.Environment,
		VirtualEnv: compute.VirtualEnvironment{
			PodDirectory:     h.podDirectory.String(),
			CgroupFilePath:   h.podDirectory.CgroupFilePath(),
			IPAddressPath:    h.podDirectory.IPAddressPath(),
			StdoutPath:       h.podDirectory.StdoutPath(),
			StderrPath:       h.podDirectory.StderrPath(),
			SysErrorFilePath: h.podDirectory.SysErrorFilePath(),
		},
		Containers: containers,
		UseTmp:     useTmp,
	}); err != nil {
		compute.PodError(pod, "TemplateError", "failed to evaluate container execution template: %v", err)
		_ = SavePodToFile(ctx, h.Pod)
		return
	}

	scriptFilePath := h.podDirectory.SubmitJobPath()

	if err := os.WriteFile(scriptFilePath, scriptFileContent.Bytes(), endpoint.ContainerJobPermissions); err != nil {
		compute.PodError(pod, "ScriptWriteError", "unable to write container execution script in file '%s': %v", scriptFilePath, err)
		_ = SavePodToFile(ctx, h.Pod)
		return
	}

	logger.Info(" * Container script has been generated")

	/*---------------------------------------------------
	 * Submit job directly, and store the JobID
	 *---------------------------------------------------*/
	jobID, err := runtime.SubmitJob(scriptFilePath)
	if err != nil {
		compute.PodError(pod, "JobSubmissionError", "failed to submit job: %v", err)
		_ = SavePodToFile(ctx, h.Pod)
		return
	}

	logger.Info(" * Job has been submitted", "jobID", jobID)

	// update pod with the job id
	runtime.SetPodID(h.Pod, runtime.JobIDTypeProcess, "0")

	// needed for subsequent GetPod()
	if err := SavePodToFile(ctx, h.Pod); err != nil {
		compute.PodError(pod, "SavePodError", "failed to persistent pod: %v", err)
		return
	}
}
