package podhandler

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// ValidatePodCapabilities verifies that the requested pod specification is supported by skifflet
// before any local side effects or process launches occur.
func ValidatePodCapabilities(pod *corev1.Pod) error {
	var unsupported []string

	if len(pod.Spec.EphemeralContainers) > 0 {
		unsupported = append(unsupported, "Spec.EphemeralContainers")
	}

	if pod.Spec.HostNetwork {
		unsupported = append(unsupported, "Spec.HostNetwork")
	}
	if pod.Spec.HostPID {
		unsupported = append(unsupported, "Spec.HostPID")
	}
	if pod.Spec.HostIPC {
		unsupported = append(unsupported, "Spec.HostIPC")
	}

	// Validate container-level capabilities
	validateContainer := func(prefix string, c *corev1.Container) {
		if c.Lifecycle != nil && c.Lifecycle.PostStart != nil {
			unsupported = append(unsupported, fmt.Sprintf("%s.Lifecycle.PostStart", prefix))
		}
		// GRPC probes are not yet supported in this user-space runtime
		if c.StartupProbe != nil && c.StartupProbe.GRPC != nil {
			unsupported = append(unsupported, fmt.Sprintf("%s.StartupProbe.GRPC", prefix))
		}
		if c.LivenessProbe != nil && c.LivenessProbe.GRPC != nil {
			unsupported = append(unsupported, fmt.Sprintf("%s.LivenessProbe.GRPC", prefix))
		}
		if c.ReadinessProbe != nil && c.ReadinessProbe.GRPC != nil {
			unsupported = append(unsupported, fmt.Sprintf("%s.ReadinessProbe.GRPC", prefix))
		}
	}

	for i := range pod.Spec.InitContainers {
		validateContainer(fmt.Sprintf("Spec.InitContainers[%d]", i), &pod.Spec.InitContainers[i])
	}
	for i := range pod.Spec.Containers {
		validateContainer(fmt.Sprintf("Spec.Containers[%d]", i), &pod.Spec.Containers[i])
	}

	if len(unsupported) > 0 {
		return fmt.Errorf("unsupported features: %s", strings.Join(unsupported, ", "))
	}
	return nil
}
