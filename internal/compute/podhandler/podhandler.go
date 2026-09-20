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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"hpk/internal/compute"
	"hpk/internal/compute/endpoint"
	"hpk/internal/compute/image"
	"hpk/internal/compute/runtime"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func findContainerPID(parentPID int) int {
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, err := os.ReadDir("/proc")
		if err == nil {
			parentOf := make(map[int]int)
			var pids []int
			for _, entry := range entries {
				pid, err := strconv.Atoi(entry.Name())
				if err != nil || pid <= 0 {
					continue
				}
				pids = append(pids, pid)
				if statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
					idx := strings.LastIndex(string(statData), ")")
					if idx != -1 && idx+2 < len(statData) {
						statFields := strings.Fields(string(statData[idx+2:]))
						if len(statFields) >= 2 {
							if ppid, _ := strconv.Atoi(statFields[1]); ppid > 0 {
								parentOf[pid] = ppid
							}
						}
					}
				}
			}

			isDescendant := func(pid int) bool {
				curr := pid
				for {
					ppid, ok := parentOf[curr]
					if !ok || ppid <= 1 {
						return false
					}
					if ppid == parentPID {
						return true
					}
					curr = ppid
				}
			}

			// Priority 1: A descendant whose comm is hpk-pause
			for _, pid := range pids {
				if isDescendant(pid) {
					comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
					if strings.TrimSpace(string(comm)) == "hpk-pause" {
						return pid
					}
				}
			}

			// Priority 2: Any descendant whose network namespace differs from host /proc/1/ns/net
			hostNetns, _ := os.Readlink("/proc/1/ns/net")
			if hostNetns != "" {
				for _, pid := range pids {
					if isDescendant(pid) {
						ns, _ := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", pid))
						if ns != "" && ns != hostNetns {
							comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
							cStr := strings.TrimSpace(string(comm))
							if !strings.Contains(cStr, "fuse") {
								return pid
							}
						}
					}
				}
			}
		}

		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	return parentPID
}

// GetPodIPFromNetns queries the network namespace of pausePID to find its assigned non-loopback IPv4 address.
func GetPodIPFromNetns(pausePID int) (string, error) {
	if testIP := os.Getenv("HPK_TEST_POD_IP"); testIP != "" {
		return testIP, nil
	}

	netnsPath := fmt.Sprintf("/proc/%d/ns/net", pausePID)
	if _, err := os.Stat(netnsPath); os.IsNotExist(err) {
		// Non-Linux or non-proc environment (e.g. testing)
		return "127.0.0.1", nil
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		cmd := exec.Command("nsenter", "-t", strconv.Itoa(pausePID), "-n", "ip", "-o", "-4", "addr", "show", "dev", "tap0")
		output, err := cmd.Output()
		if err != nil {
			cmd = exec.Command("nsenter", "-t", strconv.Itoa(pausePID), "-n", "ip", "-o", "-4", "addr", "show")
			output, err = cmd.Output()
		}
		if err == nil {
			lines := strings.Split(string(output), "\n")
			for _, line := range lines {
				fields := strings.Fields(line)
				for i, field := range fields {
					if field == "inet" && i+1 < len(fields) {
						ipCIDR := fields[i+1]
						ipStr := strings.Split(ipCIDR, "/")[0]
						parsed := net.ParseIP(ipStr)
						if parsed != nil && !parsed.IsLoopback() {
							return ipStr, nil
						}
					}
				}
			}
		}

		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	return "", fmt.Errorf("timeout waiting for pod IP in netns of PID %d", pausePID)
}

// DeletePod terminates all processes associated with a Pod and removes its directory structure.
func DeletePod(podKey client.ObjectKey, localPod *corev1.Pod) bool {
	logger := compute.DefaultLogger.WithValues("pod", podKey)

	podDir := compute.HPK.Pod(podKey)
	_, statErr := os.Stat(podDir.String())
	logger.Info(" * DeletePod invoked", "podDir", podDir.String(), "dirExists", statErr == nil)

	// 1. Resolve pause PID
	var pausePIDStr string
	if localPod != nil && localPod.Annotations != nil {
		pausePIDStr = localPod.Annotations["hpk.io/pause-pid"]
	}
	if pausePIDStr == "" && localPod != nil {
		pausePIDStr = runtime.GetPodID(localPod)
	}

	// 2. Resolve secondary container PIDs from ContainerStatuses
	var secondaryPIDs []string
	seen := make(map[string]bool)
	if pausePIDStr != "" {
		seen[pausePIDStr] = true
	}
	collectPIDs := func(statuses []corev1.ContainerStatus) {
		for _, s := range statuses {
			cid := strings.TrimPrefix(s.ContainerID, "process://")
			cid = strings.TrimPrefix(cid, "pid://")
			if cid != "" && !seen[cid] {
				seen[cid] = true
				secondaryPIDs = append(secondaryPIDs, cid)
			}
		}
	}
	if localPod != nil {
		collectPIDs(localPod.Status.InitContainerStatuses)
		collectPIDs(localPod.Status.ContainerStatuses)
		if localPod.Annotations != nil {
			if cpid := localPod.Annotations["hpk.io/container-pid"]; cpid != "" && !seen[cpid] {
				seen[cpid] = true
				secondaryPIDs = append(secondaryPIDs, cpid)
			}
		}
	}

	// 3. Terminate processes
	if pausePIDStr != "" || len(secondaryPIDs) > 0 {
		gracePeriod := 30 * time.Second
		if localPod != nil && localPod.Spec.TerminationGracePeriodSeconds != nil && *localPod.Spec.TerminationGracePeriodSeconds >= 0 {
			gracePeriod = time.Duration(*localPod.Spec.TerminationGracePeriodSeconds) * time.Second
		}
		timeout := gracePeriod + 5*time.Second

		out, err := runtime.KillPodProcessesWithTimeout(pausePIDStr, secondaryPIDs, timeout)
		if err != nil && !errors.Is(err, runtime.ErrInvalidJob) {
			logger.Info("WARNING: Failed to kill process by PID, proceeding to remove pod directory", "pid", pausePIDStr, "pod", podKey, "err", err, "out", out)
		} else {
			logger.Info(" * Process is terminated", "pid", pausePIDStr, "pod", podKey, "out", out)
		}
	}

	// 4. Remove Pod Directory (with retry for NFS file handle release)
	var removeErr error
	for attempt := 0; attempt < 10; attempt++ {
		removeErr = os.RemoveAll(podDir.String())
		if removeErr == nil || errors.Is(removeErr, fs.ErrNotExist) {
			removeErr = nil
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if removeErr != nil {
		if errors.Is(removeErr, fs.ErrPermission) {
			logger.Info(" * Failed to remove directory from host. Try using fakeroot container.", "err", removeErr)
			out, err := runtime.DefaultPauseImage.FakerootExec(
				[]string{"--mount", "type=bind,src=" + podDir.String() + ",dst=/pod"},
				[]string{"find", "/pod", "-mindepth", "1", "-delete"},
			)
			logger.Info(" * Result", "out", out)
			if err != nil {
				logger.Error(err, "failed to forcibly remove pod directory contents using fakeroot", "directory", podDir)
				return false
			}
			if err := os.RemoveAll(podDir.String()); err != nil {
				logger.Error(err, "failed to remove pod directory after fakeroot cleanup", "directory", podDir)
				return false
			}
		} else {
			logger.Error(removeErr, "failed to remove pod directory", "directory", podDir)
			return false
		}
	}

	logger.Info(" * Pod directory is removed")

	// 5. Garbage collect empty namespace directory
	namespaceDir := filepath.Dir(podDir.String())
	if empty, _ := endpoint.IsEmpty(namespaceDir); empty {
		if err := os.Remove(namespaceDir); err == nil {
			logger.Info(" * Namespace directory is removed")
		}
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

// CreatePod sets up the pod environment, launches the pause container, discovers the pod IP,
// configures DNS, and executes init and application containers in-process.
func CreatePod(ctx context.Context, pod *corev1.Pod, notify func(*corev1.Pod)) {
	podKey := client.ObjectKeyFromObject(pod)
	logger := compute.DefaultLogger.WithValues("pod", podKey)

	podEnvVars, err := FromServicesForPod(ctx, pod)
	if err != nil {
		compute.PodError(pod, "EnvVarError", "failed to list services when setting up env vars: %v", err)
		if notify != nil {
			notify(pod)
		}
		return
	}

	h := PodHandler{
		Pod:             pod,
		podKey:          podKey,
		podDirectory:    compute.HPK.Pod(podKey),
		logger:          logger,
		podEnvVariables: podEnvVars,
	}

	// Create directories
	if err := os.MkdirAll(h.podDirectory.JobDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		compute.PodError(pod, "PodDirectoryError", "Cant create pod directory '%s': %v", h.podDirectory.JobDir(), err)
		if notify != nil {
			notify(pod)
		}
		return
	}

	if err := os.MkdirAll(h.podDirectory.LogDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		compute.PodError(pod, "PodDirectoryError", "cannot create log directory '%s': %v", h.podDirectory.LogDir(), err)
		if notify != nil {
			notify(pod)
		}
		return
	}

	if err := os.MkdirAll(h.podDirectory.VolumeDir(), endpoint.PodGlobalDirectoryPermissions); err != nil {
		compute.PodError(pod, "PodDirectoryError", "cannot create volume directory '%s': %v", h.podDirectory.VolumeDir(), err)
		if notify != nil {
			notify(pod)
		}
		return
	}

	// Initialize statuses early so the pod is properly recognized as Pending with Waiting containers
	pod.Status.Phase = corev1.PodPending
	if pod.Status.InitContainerStatuses == nil {
		pod.Status.InitContainerStatuses = make([]corev1.ContainerStatus, len(pod.Spec.InitContainers))
		for i, c := range pod.Spec.InitContainers {
			pod.Status.InitContainerStatuses[i] = corev1.ContainerStatus{
				Name:  c.Name,
				Image: c.Image,
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{
						Reason:  "ContainerCreating",
						Message: "Init container is waiting to be created",
					},
				},
			}
		}
	}
	if pod.Status.ContainerStatuses == nil {
		pod.Status.ContainerStatuses = make([]corev1.ContainerStatus, len(pod.Spec.Containers))
		for i, c := range pod.Spec.Containers {
			pod.Status.ContainerStatuses[i] = corev1.ContainerStatus{
				Name:  c.Name,
				Image: c.Image,
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{
						Reason:  "ContainerCreating",
						Message: "Container is waiting to be created",
					},
				},
			}
		}
	}

	if notify != nil {
		notify(pod)
	}

	logger.Info(" * Pod Environment has been created")

	// Mount Volumes
	for _, vol := range h.Pod.Spec.Volumes {
		if err := h.mountVolumeSource(ctx, vol); err != nil {
			compute.PodError(pod, "VolumeError", "%v", err)
			if notify != nil {
				notify(pod)
			}
			return
		}
	}
	h.logger.Info(" * All volumes have been mounted")

	// Pull Pause Image
	pauseImage, err := image.Pull(compute.HPK.ImageDir(), image.Docker, compute.Environment.PauseImage)
	if err != nil {
		compute.PodError(pod, "ImagePullError", "ImagePull error. Image:%s: %v", compute.Environment.PauseImage, err)
		if notify != nil {
			notify(pod)
		}
		return
	}

	// Launch Pause Container via compute.Environment.ApptainerBin (without --host-networking)
	pauseArgs := []string{
		"exec",
		"--nv",
		"--cleanenv",
		"--writable-tmpfs",
		"--no-mount", "home,bind-paths",
		pauseImage.Filepath,
		"/entrypoint.sh",
		"/usr/local/bin/hpk-pause",
	}

	pauseCmd := exec.Command(compute.Environment.ApptainerBin, pauseArgs...)
	pauseCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := pauseCmd.Start(); err != nil {
		compute.PodError(pod, "PauseStartError", "failed to start pause container: %v", err)
		if notify != nil {
			notify(pod)
		}
		return
	}

	pausePID := pauseCmd.Process.Pid
	containerPID := findContainerPID(pausePID)
	pauseStartTime, _ := runtime.GetProcessStartTime(pausePID)
	pauseJobID := runtime.FormatProcessJobID(pausePID, pauseStartTime)

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations["hpk.io/pause-pid"] = pauseJobID
	containerStartTime, _ := runtime.GetProcessStartTime(containerPID)
	pod.Annotations["hpk.io/container-pid"] = runtime.FormatProcessJobID(containerPID, containerStartTime)
	runtime.SetPodID(pod, runtime.JobIDTypeProcess, pauseJobID)

	// Discover Pod IP from netns
	podIP, err := GetPodIPFromNetns(containerPID)
	if err != nil {
		_ = pauseCmd.Process.Kill()
		compute.PodError(pod, "NetnsIPError", "failed to discover pod IP from netns: %v", err)
		if notify != nil {
			notify(pod)
		}
		return
	}

	pod.Annotations["hpk.io/pod-ip"] = podIP
	pod.Status.PodIP = podIP
	pod.Status.PodIPs = []corev1.PodIP{{IP: podIP}}

	// Prepare DNS files in job directory
	if err := PrepareDNS(pod, h.podDirectory, compute.Environment.KubeDNS, podIP); err != nil {
		_ = pauseCmd.Process.Kill()
		compute.PodError(pod, "DNSError", "failed to prepare DNS: %v", err)
		if notify != nil {
			notify(pod)
		}
		return
	}

	// Prepare Containers (now that PodIP is known, .status.podIP env vars will have the correct IP)
	var initContainers []Container
	pod.Status.InitContainerStatuses = make([]corev1.ContainerStatus, len(pod.Spec.InitContainers))
	for i := range pod.Spec.InitContainers {
		initContainer := &pod.Spec.InitContainers[i]
		initContainerStatus := &pod.Status.InitContainerStatuses[i]
		c, err := h.buildContainer(initContainer, initContainerStatus)
		if err != nil {
			_ = pauseCmd.Process.Kill()
			compute.PodError(pod, "InitContainerError", "failed to materialize pod.Spec.InitContainers[%d]: %v", i, err)
			if notify != nil {
				notify(pod)
			}
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
			_ = pauseCmd.Process.Kill()
			compute.PodError(pod, "MainContainerError", "failed to materialize pod.Spec.Containers[%d]: %v", i, err)
			if notify != nil {
				notify(pod)
			}
			return
		}
		containers = append(containers, c)
	}

	// Update pod metadata with discovered IP and container initial states
	UpdateStatusFromRuntime(pod)
	if notify != nil {
		notify(pod)
	}

	// Monitor pause container lifetime in background
	go func() {
		_ = pauseCmd.Wait()
	}()

	// Execute Init Containers sequentially
	for i, c := range initContainers {
		initStatus := &pod.Status.InitContainerStatuses[i]
		args := c.BuildApptainerArgs(containerPID, h.podDirectory)

		cmd := exec.Command(compute.Environment.ApptainerBin, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		logFile, err := os.Create(c.LogsPath)
		if err == nil {
			cmd.Stdout = logFile
			cmd.Stderr = logFile
		}

		if err := cmd.Start(); err != nil {
			if logFile != nil {
				logFile.Close()
			}
			SetContainerTerminated(initStatus, 128)
			UpdateStatusFromRuntime(pod)
			if notify != nil {
				notify(pod)
			}
			return
		}

		startTime, _ := runtime.GetProcessStartTime(cmd.Process.Pid)
		SetContainerRunning(initStatus, cmd.Process.Pid, startTime)
		UpdateStatusFromRuntime(pod)
		if notify != nil {
			notify(pod)
		}

		waitErr := cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}

		exitCode := 0
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() >= 0 {
			exitCode = cmd.ProcessState.ExitCode()
		} else if waitErr != nil {
			exitCode = 1
		}

		SetContainerTerminated(initStatus, exitCode)
		UpdateStatusFromRuntime(pod)
		if notify != nil {
			notify(pod)
		}

		if exitCode != 0 {
			logger.Error(fmt.Errorf("init container %s exited with %d", c.InstanceName, exitCode), "init container failed")
			return
		}
	}

	// Execute Main Containers concurrently
	for i, c := range containers {
		containerStatus := &pod.Status.ContainerStatuses[i]
		args := c.BuildApptainerArgs(containerPID, h.podDirectory)

		cmd := exec.Command(compute.Environment.ApptainerBin, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		logFile, err := os.Create(c.LogsPath)
		if err == nil {
			cmd.Stdout = logFile
			cmd.Stderr = logFile
		}

		if err := cmd.Start(); err != nil {
			if logFile != nil {
				logFile.Close()
			}
			SetContainerTerminated(containerStatus, 128)
			UpdateStatusFromRuntime(pod)
			if notify != nil {
				notify(pod)
			}
			continue
		}

		startTime, _ := runtime.GetProcessStartTime(cmd.Process.Pid)
		SetContainerRunning(containerStatus, cmd.Process.Pid, startTime)
		UpdateStatusFromRuntime(pod)
		if notify != nil {
			notify(pod)
		}

		// Asynchronously wait for container termination
		go func(c Container, cs *corev1.ContainerStatus, cmd *exec.Cmd, logFile *os.File) {
			waitErr := cmd.Wait()
			if logFile != nil {
				logFile.Close()
			}

			exitCode := 0
			if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() >= 0 {
				exitCode = cmd.ProcessState.ExitCode()
			} else if waitErr != nil {
				exitCode = 1
			}

			SetContainerTerminated(cs, exitCode)
			UpdateStatusFromRuntime(pod)
			if notify != nil {
				notify(pod)
			}
		}(c, containerStatus, cmd, logFile)
	}
}
