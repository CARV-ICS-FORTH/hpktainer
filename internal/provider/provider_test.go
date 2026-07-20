package provider

import (
	"os"
	"testing"

	"hpk/internal/compute"
	"hpk/internal/compute/endpoint"
	PodHandler "hpk/internal/compute/podhandler"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReconcileNonTerminalPodsNotifiesOnlyOnChange(t *testing.T) {
	tmpDir := t.TempDir()
	compute.HPK = endpoint.HPK(tmpDir)

	podKey := client.ObjectKey{Namespace: "default", Name: "test-pod"}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: podKey.Namespace,
			Name:      podKey.Name,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "c1", Image: "alpine"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "c1",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
					},
				},
			},
		},
	}

	podDir := compute.HPK.Pod(podKey)
	_ = os.MkdirAll(podDir.JobDir(), 0755)
	_ = os.MkdirAll(podDir.ControlFileDir(), 0755)

	if err := PodHandler.SavePodToFile(nil, pod); err != nil {
		t.Fatalf("failed to save pod to file: %v", err)
	}

	var notifyCount int
	vk := &VirtualK8S{
		updatedPod: func(p *corev1.Pod) {
			notifyCount++
		},
	}

	// First call - PodHandler.UpdateStatusFromRuntime will calculate status from runtime
	vk.reconcileNonTerminalPods()

	// Save current pod status to disk so next reconcile has matching status
	podReloaded, err := PodHandler.LoadPodFromKey(podKey)
	if err != nil {
		t.Fatalf("failed to load pod: %v", err)
	}
	PodHandler.UpdateStatusFromRuntime(podReloaded)
	if err := PodHandler.SavePodToFile(nil, podReloaded); err != nil {
		t.Fatalf("failed to save updated pod: %v", err)
	}

	notifyCount = 0

	// Second reconcile without any runtime changes should NOT trigger updatedPod
	vk.reconcileNonTerminalPods()

	if notifyCount != 0 {
		t.Fatalf("expected 0 notifications when pod status did not change, got %d", notifyCount)
	}

	// Now simulate a change in runtime (e.g. exit code file created)
	exitCodePath := podDir.Container("c1").ExitCodePath()
	_ = os.WriteFile(exitCodePath, []byte("0"), 0644)

	vk.reconcileNonTerminalPods()

	if notifyCount != 1 {
		t.Fatalf("expected 1 notification when pod status changed, got %d", notifyCount)
	}
}
