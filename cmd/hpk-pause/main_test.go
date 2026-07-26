package main

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"hpk/internal/compute/endpoint"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Test for successful init container execution
func TestHandleInitContainers_Success(t *testing.T) {
	if _, err := exec.LookPath("apptainer"); err != nil {
		t.Skip("apptainer executable not found in PATH")
	}

	workDir := t.TempDir()
	annotations := make(map[string]string)
	annotations["workingDirectory"] = workDir
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-init-pod",
			Namespace:   "default",
			Annotations: annotations,
		},
		Spec: v1.PodSpec{
			InitContainers: []v1.Container{
				{Name: "test-init-container", Image: "busybox", Command: []string{"sh", "-c", "echo hello from init container"}},
			},
		},
	}
	podKey := client.ObjectKeyFromObject(pod)
	hpk := endpoint.HPK(pod.Annotations["workingDirectory"])
	podPath := hpk.Pod(podKey)
	logPath := podPath.Container("test-init-container").LogsPath()
	exitCodeFilePath := podPath.Container("test-init-container").ExitCodePath()

	if err := os.MkdirAll(string(podPath), 0750); err != nil {
		t.Errorf("create pod directory failed unexpectedly: %v", err)
	}
	if err := os.MkdirAll(string(podPath.ControlFileDir()), 0750); err != nil {
		t.Errorf("create pod directory failed unexpectedly: %v", err)
	}
	if err := os.MkdirAll(string(podPath.JobDir()), 0750); err != nil {
		t.Errorf("create pod directory failed unexpectedly: %v", err)
	}
	if err := os.MkdirAll(string(podPath.LogDir()), 0750); err != nil {
		t.Errorf("create pod directory failed unexpectedly: %v", err)
	}

	tracker := newContainerTracker()
	if err := handleInitContainers(pod, tracker); err != nil {
		t.Errorf("handleInitContainers failed unexpectedly: %v", err)
	}
	//  Verify log file contents (adjust the path as needed based on your implementation)
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Errorf("Error reading log file: %v", err)
	}

	expectedOutput := "hello from init container\n"
	if string(logData) != expectedOutput {
		t.Errorf("Unexpected log output. Got: %v, Expected: %v", string(logData), expectedOutput)
	}
	exitData, err := os.ReadFile(exitCodeFilePath)
	if err != nil {
		t.Errorf("Error reading exitCode file: %v", err)
	}

	if string(exitData) != fmt.Sprint(0) {
		t.Errorf("Unexpected exitCode. Got: %v, Expected: %v", string(exitData), 0)
	}
}

// Test for successful main container execution
func TestHandleContainers_Success(t *testing.T) {
	if _, err := exec.LookPath("apptainer"); err != nil {
		t.Skip("apptainer executable not found in PATH")
	}

	workDir := t.TempDir()
	annotations := make(map[string]string)
	annotations["workingDirectory"] = workDir
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-main-pod",
			Namespace:   "default",
			Annotations: annotations,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{Name: "test-main-container", Image: "busybox", Command: []string{"sh", "-c", "echo hello from main container"}},
			},
		},
	}
	podKey := client.ObjectKeyFromObject(pod)
	hpk := endpoint.HPK(pod.Annotations["workingDirectory"])
	podPath := hpk.Pod(podKey)
	logPath := podPath.Container("test-main-container").LogsPath()
	exitCodeFilePath := podPath.Container("test-main-container").ExitCodePath()

	if err := os.MkdirAll(string(podPath), 0750); err != nil {
		t.Errorf("create pod directory failed unexpectedly: %v", err)
	}
	if err := os.MkdirAll(string(podPath.ControlFileDir()), 0750); err != nil {
		t.Errorf("create pod directory failed unexpectedly: %v", err)
	}
	if err := os.MkdirAll(string(podPath.JobDir()), 0750); err != nil {
		t.Errorf("create pod directory failed unexpectedly: %v", err)
	}
	if err := os.MkdirAll(string(podPath.LogDir()), 0750); err != nil {
		t.Errorf("create pod directory failed unexpectedly: %v", err)
	}

	var wg sync.WaitGroup
	tracker := newContainerTracker()
	if err := handleContainers(pod, &wg, tracker); err != nil {
		t.Errorf("handleContainers failed unexpectedly: %v", err)
	}

	wg.Wait()
	//  Verify log file contents (adjust the path as needed based on your implementation)
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Errorf("Error reading log file: %v", err)
	}

	expectedOutput := "hello from main container\n"
	if string(logData) != expectedOutput {
		t.Errorf("Unexpected log output. Got: %v, Expected: %v", string(logData), expectedOutput)
	}

	exitData, err := os.ReadFile(exitCodeFilePath)
	if err != nil {
		t.Errorf("Error reading exitCode file: %v", err)
	}

	if string(exitData) != fmt.Sprint(0) {
		t.Errorf("Unexpected exitCode. Got: %v, Expected: %v", string(exitData), 0)
	}
}

func TestContainerTracker(t *testing.T) {
	tracker := newContainerTracker()
	if tracker == nil {
		t.Fatalf("expected non-nil containerTracker")
	}

	// Signalling nil or empty tracker should not panic
	tracker.SignalAll(os.Interrupt)

	// Test adding and removing nil cmd
	tracker.Add(nil)
	tracker.Remove(nil)
}

func TestParseEnvVars_MultiLine(t *testing.T) {
	output := []byte("FOO=bar\nCERT=-----BEGIN CERTIFICATE-----\nMIIF...\n-----END CERTIFICATE-----\nBAZ=qux\nKUBERNETES_SERVICE_HOST=10.0.0.1\nKUBERNETES_SERVICE_PORT=6443\n")
	knownEnvs := []v1.EnvVar{
		{Name: "FOO"},
		{Name: "CERT"},
		{Name: "BAZ"},
	}

	envs := parseEnvVars(output, knownEnvs)
	if len(envs) != 5 {
		t.Fatalf("expected 5 env vars, got %d", len(envs))
	}

	if envs[0].Name != "FOO" || envs[0].Value != "bar" {
		t.Errorf("unexpected env[0]: %+v", envs[0])
	}

	expectedCert := "-----BEGIN CERTIFICATE-----\nMIIF...\n-----END CERTIFICATE-----"
	if envs[1].Name != "CERT" || envs[1].Value != expectedCert {
		t.Errorf("unexpected env[1]: %+v (expected cert value %q)", envs[1], expectedCert)
	}

	if envs[2].Name != "BAZ" || envs[2].Value != "qux" {
		t.Errorf("unexpected env[2]: %+v (expected qux without service var pollution)", envs[2])
	}

	if envs[3].Name != "KUBERNETES_SERVICE_HOST" || envs[3].Value != "10.0.0.1" {
		t.Errorf("unexpected env[3]: %+v", envs[3])
	}

	if envs[4].Name != "KUBERNETES_SERVICE_PORT" || envs[4].Value != "6443" {
		t.Errorf("unexpected env[4]: %+v", envs[4])
	}
}

func TestGetHostResolvConf_EnvConfig(t *testing.T) {
	t.Setenv("FALLBACK_DNS", "8.8.8.8")

	res := getHostResolvConf("10.0.0.1")
	if res == "" {
		t.Fatalf("expected non-empty resolv.conf output")
	}
}

func TestAnnounceIP_Permissions(t *testing.T) {
	workDir := t.TempDir()
	annotations := map[string]string{"workingDirectory": workDir}
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-ip-pod",
			Namespace:   "default",
			Annotations: annotations,
		},
	}
	podKey := client.ObjectKeyFromObject(pod)
	hpk := endpoint.HPK(workDir)
	podPath := hpk.Pod(podKey)

	if err := os.MkdirAll(string(podPath.ControlFileDir()), 0750); err != nil {
		t.Fatalf("failed to create control file dir: %v", err)
	}

	if err := announceIP(pod); err != nil {
		t.Fatalf("announceIP failed: %v", err)
	}

	info, err := os.Stat(podPath.IPAddressPath())
	if err != nil {
		t.Fatalf("failed to stat IP file: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0644 {
		t.Errorf("expected IP file perm 0644, got %o", perm)
	}
}

func TestResolveGracePeriod(t *testing.T) {
	t.Run("Default", func(t *testing.T) {
		pod := &v1.Pod{}
		if got := resolveGracePeriod(pod); got != 30*time.Second {
			t.Errorf("expected 30s default, got %v", got)
		}
	})

	t.Run("EnvVar", func(t *testing.T) {
		t.Setenv("TERMINATION_GRACE_PERIOD_SECONDS", "15")
		pod := &v1.Pod{}
		if got := resolveGracePeriod(pod); got != 15*time.Second {
			t.Errorf("expected 15s from env var, got %v", got)
		}
	})

	t.Run("PodSpecOverride", func(t *testing.T) {
		t.Setenv("TERMINATION_GRACE_PERIOD_SECONDS", "15")
		grace := int64(45)
		pod := &v1.Pod{
			Spec: v1.PodSpec{
				TerminationGracePeriodSeconds: &grace,
			},
		}
		if got := resolveGracePeriod(pod); got != 45*time.Second {
			t.Errorf("expected 45s from pod spec, got %v", got)
		}
	})

	t.Run("ZeroGracePeriod", func(t *testing.T) {
		grace := int64(0)
		pod := &v1.Pod{
			Spec: v1.PodSpec{
				TerminationGracePeriodSeconds: &grace,
			},
		}
		if got := resolveGracePeriod(pod); got != 0 {
			t.Errorf("expected 0s grace period, got %v", got)
		}
	})
}
