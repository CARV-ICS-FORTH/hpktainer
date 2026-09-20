package podhandler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"hpk/internal/compute"
	"hpk/internal/compute/endpoint"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSavePodToFile_AtomicWrite(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hpk-save-pod-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	compute.HPK = endpoint.HPK(tmpDir)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "test-c"}},
		},
	}

	podDir := compute.HPK.Pod(client.ObjectKeyFromObject(pod))
	if err := os.MkdirAll(podDir.JobDir(), 0755); err != nil {
		t.Fatalf("failed to create job dir: %v", err)
	}

	ctx := context.Background()
	if err := SavePodToFile(ctx, pod); err != nil {
		t.Fatalf("SavePodToFile failed: %v", err)
	}

	crdPath := podDir.EncodedJSONPath()
	if _, err := os.Stat(crdPath); err != nil {
		t.Fatalf("expected crd file at %s, got error: %v", crdPath, err)
	}

	tmpFiles, _ := filepath.Glob(filepath.Join(filepath.Dir(crdPath), "*.tmp"))
	if len(tmpFiles) > 0 {
		t.Fatalf("expected no tmp files to remain, but found: %v", tmpFiles)
	}

	loadedPod, err := LoadPodFromFile(crdPath)
	if err != nil {
		t.Fatalf("LoadPodFromFile failed: %v", err)
	}
	if loadedPod.Name != pod.Name || loadedPod.Namespace != pod.Namespace {
		t.Errorf("loaded pod mismatch: got %s/%s, want %s/%s", loadedPod.Namespace, loadedPod.Name, pod.Namespace, pod.Name)
	}
}

func TestDeletePod_CleanRemoval(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hpk-delete-pod-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	compute.HPK = endpoint.HPK(tmpDir)

	podKey := client.ObjectKey{Namespace: "default", Name: "my-pod"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podKey.Name,
			Namespace:   podKey.Namespace,
			Annotations: map[string]string{"hpk.io/pause-pid": "999999"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", ContainerID: "process://999998"},
			},
		},
	}

	podDir := compute.HPK.Pod(podKey)
	if err := os.MkdirAll(podDir.JobDir(), 0755); err != nil {
		t.Fatalf("failed to create job dir: %v", err)
	}

	if err := SavePodToFile(context.Background(), pod); err != nil {
		t.Fatalf("failed to save pod: %v", err)
	}

	ok := DeletePod(podKey)
	if !ok {
		t.Fatalf("expected DeletePod to return true")
	}

	if _, err := os.Stat(podDir.String()); !os.IsNotExist(err) {
		t.Errorf("expected pod directory %s to be removed, but still exists", podDir.String())
	}
}

func TestGetPodIPFromNetns_Fallback(t *testing.T) {
	// For a non-existent PID, should fallback cleanly without hanging
	ip, err := GetPodIPFromNetns(999999)
	if err != nil {
		t.Fatalf("unexpected error from fallback: %v", err)
	}
	if ip == "" {
		t.Errorf("expected non-empty IP from fallback")
	}
}
