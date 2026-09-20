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

package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hpk/internal/compute"
	"hpk/internal/compute/endpoint"
	PodHandler "hpk/internal/compute/podhandler"
	"hpk/internal/compute/runtime"
	"hpk/pkg/container"

	"github.com/go-logr/logr"
	"github.com/virtual-kubelet/virtual-kubelet/errdefs"
	vkapi "github.com/virtual-kubelet/virtual-kubelet/node/api"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	statsv1alpha1 "k8s.io/kubelet/pkg/apis/stats/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// InitConfig is the config passed to initialize a registered provider.
type InitConfig struct {
	NodeName   string
	InternalIP string
	DaemonPort int32

	BuildVersion string

	RestConfig *rest.Config

	PauseImage string
}

func isPodOwnedByNode(pod *corev1.Pod, ownNode string) bool {
	if ownNode == "" || pod == nil || pod.Spec.NodeName == "" {
		return true
	}
	return pod.Spec.NodeName == ownNode
}

func loadPodIfOwned(path endpoint.PodPath, ownNode string) (*corev1.Pod, error) {
	pod, err := PodHandler.LoadPodFromFile(path.EncodedJSONPath())
	if err != nil {
		return nil, err
	}
	if !isPodOwnedByNode(pod, ownNode) {
		return nil, nil
	}
	return pod, nil
}

// VirtualK8S implements the virtual-kubelet provider interface and stores pods in memory.
type VirtualK8S struct {
	InitConfig

	Logger logr.Logger

	updatedPod func(*corev1.Pod)
}

// NewVirtualK8S reads a kubeconfig file and sets up a client to interact
// with the execution environment. It is designed to restore missing state after a restart.
func NewVirtualK8S(config InitConfig) (*VirtualK8S, error) {
	logger := zap.New(zap.UseDevMode(true))

	/*---------------------------------------------------
	 * Initialize HPK Environment
	 *---------------------------------------------------*/
	if err := runtime.Initialize(config.PauseImage); err != nil {
		return nil, fmt.Errorf("Failed to initialize HPK paths '%s': %w", compute.HPK.String(), err)
	}

	/*---------------------------------------------------
	 * Handle Corrupted Pods (With missing state)
	 *---------------------------------------------------*/
	var corruptedPods []endpoint.PodPath

	// move corrupted pods to a centralized dir for inspection.
	// Valid are considered the pods with a Pod description.
	if err := compute.HPK.WalkPodDirectories(func(podpath endpoint.PodPath) error {
		if pod, err := loadPodIfOwned(podpath, config.NodeName); err == nil && pod == nil {
			return nil
		}

		ok, info := podpath.PodEnvironmentIsOK()
		if !ok {
			corruptedPods = append(corruptedPods, podpath)

			compute.DefaultLogger.Info("Corrupted Pod has been detected",
				"reason", info,
				"path", podpath,
			)
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("pod scanning has failed: %w", err)
	}

	for _, path := range corruptedPods {
		archivedPodPath := strings.ReplaceAll(string(path), compute.HPK.String(), compute.HPK.CorruptedDir())

		// ensure that path prefix exists.
		archivedNamespacePath := filepath.Dir(archivedPodPath)

		if err := os.MkdirAll(archivedNamespacePath, endpoint.PodGlobalDirectoryPermissions); err != nil {
			return nil, fmt.Errorf("basepath '%s' error: %w", archivedNamespacePath, err)
		}

		// move corrupted pod to the archived namespace.
	retry:
		if err := os.Rename(string(path), archivedPodPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				archivedPodPath = fmt.Sprintf("%s-%d", archivedPodPath, time.Now().Second())
				goto retry
			}

			if errors.Is(err, os.ErrNotExist) {
				logger.Info("Skipping corrupted pod archive move because source no longer exists",
					"from", path,
					"to", archivedPodPath,
					"error", err,
				)
				continue
			}

			return nil, fmt.Errorf("moving error of corrupted pod: %w", err)
		}

		logger.Info("Moved corrupted pod to archive",
			"from", path,
			"to", archivedPodPath,
		)
	}

	return &VirtualK8S{
		InitConfig: config,
		Logger:     logger,
	}, nil
}

// CreatePod takes a Kubernetes Pod and deploys it within the provider.
func (v *VirtualK8S) CreatePod(ctx context.Context, pod *corev1.Pod) error {
	podKey := client.ObjectKeyFromObject(pod)
	logger := v.Logger.WithValues("obj", podKey)

	logger.Info("[K8s] -> CreatePod")

	defer func() {
		logger.Info("[K8s] <- CreatePod")
	}()

	pod.Status.HostIP = v.InitConfig.InternalIP

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error(fmt.Errorf("%v", r), "Recovered panic in CreatePod goroutine", "pod", podKey)
				compute.PodError(pod, "PanicError", "internal panic occurred during pod creation: %v", r)
				v.saveAndNotifyPod("panic", pod)
			}
		}()

		PodHandler.CreatePod(context.Background(), pod, func(p *corev1.Pod) {
			v.saveAndNotifyPod("podhandler", p)
		})

		v.saveAndNotifyPod("create_complete", pod)
	}()

	return nil
}

// UpdatePod takes a Kubernetes Pod and updates it within the provider.
func (v *VirtualK8S) UpdatePod(ctx context.Context, pod *corev1.Pod) error {
	podKey := client.ObjectKeyFromObject(pod)
	logger := v.Logger.WithValues("obj", podKey)

	logger.Info("[K8s] -> UpdatePod",
		"version", pod.GetResourceVersion(),
		"phase", pod.Status.Phase,
	)

	defer logger.Info("[K8s] <- UpdatePod")

	localPod, err := PodHandler.LoadPodFromKey(podKey)
	if err != nil {
		return errdefs.NotFoundf("object not found")
	}

	localVersion, errLocal := strconv.ParseUint(localPod.ResourceVersion, 10, 64)
	newVersion, errNew := strconv.ParseUint(pod.ResourceVersion, 10, 64)

	isOlder := false
	if errLocal == nil && errNew == nil {
		isOlder = localVersion >= newVersion
	} else {
		isOlder = localPod.ResourceVersion == pod.ResourceVersion
	}

	if isOlder {
		logger.Info("Discard update since its ResourceVersion is not newer than local")
		return nil
	}

	if !runtime.HasJobID(pod) {
		logger.Info("Discard update because container is still starting")
		return nil
	}

	if err := PodHandler.SavePodToFile(ctx, pod); err != nil {
		logger.Error(err, "failed to save updated pod to file", "pod", podKey)
		return fmt.Errorf("failed to save updated pod '%s' to file: %w", podKey, err)
	}

	return nil
}

// DeletePod takes a Kubernetes Pod and deletes it from the provider.
func (v *VirtualK8S) DeletePod(ctx context.Context, pod *corev1.Pod) error {
	podKey := client.ObjectKeyFromObject(pod)
	logger := v.Logger.WithValues("obj", podKey)

	logger.Info("[K8s] -> DeletePod")

	if !isPodOwnedByNode(pod, v.NodeName) {
		logger.Info("[K8s] <- DeletePod (SKIPPED - pod owned by another node)", "podNode", pod.Spec.NodeName, "ownNode", v.NodeName)
		return nil
	}

	if localPod, err := PodHandler.LoadPodFromKey(podKey); err == nil {
		if !isPodOwnedByNode(localPod, v.NodeName) {
			logger.Info("[K8s] <- DeletePod (SKIPPED - local pod file owned by another node)", "podNode", localPod.Spec.NodeName, "ownNode", v.NodeName)
			return nil
		}
	}

	if !PodHandler.DeletePod(podKey) {
		logger.Info("[K8s] <- DeletePod (POD NOT FOUND)")
		return errdefs.NotFoundf("object not found")
	}

	logger.Info("[K8s] <- DeletePod (SUCCESS)")
	return nil
}

// GetPod retrieves a pod by name from the provider.
func (v *VirtualK8S) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	podKey := client.ObjectKey{Namespace: namespace, Name: name}
	logger := v.Logger.WithValues("obj", podKey)

	logger.Info("[K8s] -> GetPod")

	pod, err := PodHandler.LoadPodFromKey(podKey)
	if err != nil {
		logger.Info("[K8s] <- GetPod (POD NOT FOUND)")
		return nil, errdefs.NotFoundf("object not found")
	}

	logger.Info("[K8s] <- GetPod",
		"version", pod.GetResourceVersion(),
		"phase", pod.Status.Phase,
	)

	return pod.DeepCopy(), nil
}

// GetPodStatus retrieves the status of a pod by name from the provider.
func (v *VirtualK8S) GetPodStatus(ctx context.Context, namespace, name string) (*corev1.PodStatus, error) {
	podKey := client.ObjectKey{Namespace: namespace, Name: name}
	logger := v.Logger.WithValues("obj", podKey, "job")

	logger.Info("[K8s] -> GetPodStatus")

	pod, err := PodHandler.LoadPodFromKey(podKey)
	if err != nil {
		logger.Info("[K8s] <- GetPodStatus (POD NOT FOUND)")
		return nil, errdefs.NotFoundf("object not found")
	}

	logger.Info("[K8s] <- GetPodStatus",
		"version", pod.GetResourceVersion(),
		"phase", pod.Status.Phase,
	)

	return pod.Status.DeepCopy(), nil
}

// GetPods retrieves a list of all pods running on the provider.
func (v *VirtualK8S) GetPods(ctx context.Context) ([]*corev1.Pod, error) {
	v.Logger.Info("[K8s] -> GetPods")
	defer v.Logger.Info("[K8s] <- GetPods")

	var pods []*corev1.Pod

	if err := compute.HPK.WalkPodDirectories(func(path endpoint.PodPath) error {
		pod, err := loadPodIfOwned(path, v.NodeName)
		if err != nil {
			v.Logger.Info("Ignore Corrupted Pod Dir", "path", path, "error", err)
			return nil
		}

		if pod == nil {
			return nil
		}

		PodHandler.SyncContainerStatuses(pod)
		PodHandler.UpdateStatusFromRuntime(pod)

		pods = append(pods, pod)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to traverse pods: %w", err)
	}

	return pods, nil
}

// NotifyPods instructs the notifier to call the passed in function when
// the pod status changes.
func (v *VirtualK8S) NotifyPods(ctx context.Context, f func(*corev1.Pod)) {
	v.Logger.Info("[K8s] -> NotifyPods")
	defer v.Logger.Info("[K8s] <- NotifyPods")

	v.updatedPod = f

	/*-- periodic reconcile of non-terminal pods --*/
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				v.reconcileNonTerminalPods()
			}
		}
	}()
}

func (v *VirtualK8S) saveAndNotifyPod(source string, pod *corev1.Pod) {
	if pod == nil {
		v.Logger.Error(fmt.Errorf("nil pod received in saveAndNotifyPod"), "skipping notification", "source", source)
		return
	}

	podKey := client.ObjectKeyFromObject(pod)
	if err := PodHandler.SavePodToFile(context.Background(), pod); err != nil {
		v.Logger.Error(err, "Failed to persist updated pod status", "pod", podKey, "source", source)
	}

	if v.updatedPod != nil {
		v.updatedPod(pod)
		v.Logger.Info(" * K8s status is synchronized",
			"source", source,
			"version", pod.ResourceVersion,
			"phase", pod.Status.Phase,
		)
	}
}

func (v *VirtualK8S) reconcileNonTerminalPods() {
	if err := compute.HPK.WalkPodDirectories(func(path endpoint.PodPath) error {
		pod, err := loadPodIfOwned(path, v.NodeName)
		if err != nil || pod == nil {
			return nil
		}

		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			return nil
		}

		oldStatus := pod.Status.DeepCopy()
		PodHandler.UpdateStatusFromRuntime(pod)

		changed := oldStatus.Phase != pod.Status.Phase ||
			!apiequality.Semantic.DeepEqual(oldStatus.ContainerStatuses, pod.Status.ContainerStatuses) ||
			!apiequality.Semantic.DeepEqual(oldStatus.InitContainerStatuses, pod.Status.InitContainerStatuses)

		if changed {
			v.saveAndNotifyPod("reconciler", pod)
		}
		return nil
	}); err != nil {
		v.Logger.Error(err, "Periodic pod reconcile failed")
	}
}

func (v *VirtualK8S) PortForward(ctx context.Context, namespace, pod string, port int32, stream io.ReadWriteCloser) error {
	podKey := client.ObjectKey{Namespace: namespace, Name: pod}
	logger := v.Logger.WithValues("obj", podKey)

	logger.Info("[K8s] receive PortForward", "pod", pod)
	return nil
}

func (v *VirtualK8S) GetStatsSummary(context.Context) (*statsv1alpha1.Summary, error) {
	v.Logger.Info("[K8s] -> GetStatsSummary")
	defer v.Logger.Info("[K8s] <- GetStatsSummary")

	return nil, errors.New("GetStatsSummary is not supported")
}

// GetContainerLogs retrieves the logs of a container by name from the provider.
func (v *VirtualK8S) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, opts vkapi.ContainerLogOpts) (io.ReadCloser, error) {
	podKey := client.ObjectKey{Namespace: namespace, Name: podName}
	logger := v.Logger.WithValues("obj", podKey)

	logger.Info("[K8s] -> GetContainerLogs", "container", containerName)
	defer logger.Info("[K8s] <- GetContainerLogs", "container", containerName)

	logfilePath := compute.HPK.Pod(podKey).Container(containerName).LogsPath()

	if opts.Follow {
		v.Logger.Info("[K8s] WARNING -- Log with \"follow\" is not yet supported by HPK")
	}

	if opts.Tail <= 0 {
		logs, err := os.Open(logfilePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return io.NopCloser(bytes.NewReader([]byte{})), nil
			}
			return nil, fmt.Errorf("unable to batch logs: %w", err)
		}
		return logs, nil
	}

	if opts.Tail > 0 {
		logs, err := container.GetTailLog(logfilePath, opts.Tail)
		if err != nil {
			return nil, fmt.Errorf("unable to batch logs: %w", err)
		}

		results := bytes.NewBuffer(nil)
		for _, nll := range logs {
			results.WriteString(nll + "\n")
		}
		return io.NopCloser(bytes.NewReader(results.Bytes())), nil
	}

	return io.NopCloser(bytes.NewReader([]byte{})), nil
}

// RunInContainer executes a command in a container in the pod.
func (v *VirtualK8S) RunInContainer(ctx context.Context, namespace, podName, containerName string, cmd []string, attach vkapi.AttachIO) error {
	podKey := client.ObjectKey{Namespace: namespace, Name: podName}
	logger := v.Logger.WithValues("obj", podKey)

	logger.Info("[K8s] -> RunInContainer", "container", containerName)
	defer logger.Info("[K8s] <- RunInContainer", "container", containerName)

	defer func() {
		if attach.Stdout() != nil {
			attach.Stdout().Close()
		}
		if attach.Stderr() != nil {
			attach.Stderr().Close()
		}
	}()

	req := compute.K8SClientset.RESTClient().
		Post().
		Namespace(namespace).
		Resource("pods").
		Name(podName).
		SubResource("exec").
		Timeout(0).
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   cmd,
			Stdin:     attach.Stdin() != nil,
			Stdout:    attach.Stdout() != nil,
			Stderr:    attach.Stderr() != nil,
			TTY:       attach.TTY(),
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(v.InitConfig.RestConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("could not make remote command: %w", err)
	}

	return exec.Stream(remotecommand.StreamOptions{
		Stdin:             attach.Stdin(),
		Stdout:            attach.Stdout(),
		Stderr:            attach.Stderr(),
		Tty:               attach.TTY(),
		TerminalSizeQueue: &termSize{attach: attach},
	})
}

type termSize struct {
	attach vkapi.AttachIO
}

func (t *termSize) Next() *remotecommand.TerminalSize {
	resize := <-t.attach.Resize()
	return &remotecommand.TerminalSize{
		Height: resize.Height,
		Width:  resize.Width,
	}
}
