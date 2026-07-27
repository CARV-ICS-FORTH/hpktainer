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

func TestResolveProcessPIDFromControlFiles_PrefersPauseJobID(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hpk-podhandler-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "c1"},
			},
		},
	}

	podDir := endpoint.HPK(tmpDir).Pod(client.ObjectKeyFromObject(pod))
	if err := os.MkdirAll(podDir.ControlFileDir(), 0755); err != nil {
		t.Fatalf("failed to create control file dir: %v", err)
	}

	// Write main container jobid (100)
	mainJobIDPath := podDir.Container("c1").IDPath()
	if err := os.WriteFile(mainJobIDPath, []byte("pid://100"), 0644); err != nil {
		t.Fatalf("failed to write main container jobid: %v", err)
	}

	// Without pause.jobid, should resolve main container PID 100
	pid, err := resolveProcessPIDFromControlFiles(pod, podDir, compute.DefaultLogger)
	if err != nil {
		t.Fatalf("expected resolution from main container, got err: %v", err)
	}
	if pid != "100" {
		t.Errorf("expected PID 100, got %s", pid)
	}

	// Write pause.jobid (200)
	pauseJobIDPath := podDir.PauseJobIDPath()
	if err := os.WriteFile(pauseJobIDPath, []byte("pid://200"), 0644); err != nil {
		t.Fatalf("failed to write pause jobid: %v", err)
	}

	// With pause.jobid, should resolve pause PID 200
	pid, err = resolveProcessPIDFromControlFiles(pod, podDir, compute.DefaultLogger)
	if err != nil {
		t.Fatalf("expected resolution from pause jobid, got err: %v", err)
	}
	if pid != "200" {
		t.Errorf("expected PID 200, got %s", pid)
	}
}

func TestPauseJobIDPath(t *testing.T) {
	podDir := endpoint.PodPath("/tmp/.hpk/default/my-pod")
	expected := filepath.Join("/tmp/.hpk/default/my-pod/controlfiles", "pause.jobid")
	if podDir.PauseJobIDPath() != expected {
		t.Errorf("expected %s, got %s", expected, podDir.PauseJobIDPath())
	}
}

func TestResolveSecondaryContainerPIDs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hpk-podhandler-secondary-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "c1"},
				{Name: "c2"},
			},
		},
	}

	podDir := endpoint.HPK(tmpDir).Pod(client.ObjectKeyFromObject(pod))
	if err := os.MkdirAll(podDir.ControlFileDir(), 0755); err != nil {
		t.Fatalf("failed to create control file dir: %v", err)
	}

	_ = os.WriteFile(podDir.PauseJobIDPath(), []byte("pid://200"), 0644)
	_ = os.WriteFile(podDir.Container("c1").IDPath(), []byte("pid://301"), 0644)
	_ = os.WriteFile(podDir.Container("c2").IDPath(), []byte("pid://302"), 0644)

	secondary := resolveSecondaryContainerPIDs(pod, podDir, "200")
	if len(secondary) != 2 {
		t.Fatalf("expected 2 secondary PIDs, got %d (%v)", len(secondary), secondary)
	}
	if secondary[0] != "301" || secondary[1] != "302" {
		t.Errorf("expected [301 302], got %v", secondary)
	}
}

func TestResolveProcessPIDFromControlFiles_WithStartTime(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hpk-podhandler-starttime-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "c1"},
			},
		},
	}

	podDir := endpoint.HPK(tmpDir).Pod(client.ObjectKeyFromObject(pod))
	if err := os.MkdirAll(podDir.ControlFileDir(), 0755); err != nil {
		t.Fatalf("failed to create control file dir: %v", err)
	}

	pauseJobIDPath := podDir.PauseJobIDPath()
	if err := os.WriteFile(pauseJobIDPath, []byte("pid://200:1234567"), 0644); err != nil {
		t.Fatalf("failed to write pause jobid: %v", err)
	}

	pid, err := resolveProcessPIDFromControlFiles(pod, podDir, compute.DefaultLogger)
	if err != nil {
		t.Fatalf("expected resolution from pause jobid, got err: %v", err)
	}
	if pid != "200:1234567" {
		t.Errorf("expected PID '200:1234567', got %s", pid)
	}

	mainJobIDPath := podDir.Container("c1").IDPath()
	if err := os.WriteFile(mainJobIDPath, []byte("pid://301:7654321"), 0644); err != nil {
		t.Fatalf("failed to write main container jobid: %v", err)
	}

	secondary := resolveSecondaryContainerPIDs(pod, podDir, pid)
	if len(secondary) != 1 || secondary[0] != "301:7654321" {
		t.Errorf("expected secondary PID ['301:7654321'], got %v", secondary)
	}
}

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

	tmpPath := crdPath + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected tmp file %s to be removed, but it exists", tmpPath)
	}

	loadedPod, err := LoadPodFromFile(crdPath)
	if err != nil {
		t.Fatalf("LoadPodFromFile failed: %v", err)
	}
	if loadedPod.Name != pod.Name || loadedPod.Namespace != pod.Namespace {
		t.Errorf("loaded pod mismatch: got %s/%s, want %s/%s", loadedPod.Namespace, loadedPod.Name, pod.Namespace, pod.Name)
	}
}
