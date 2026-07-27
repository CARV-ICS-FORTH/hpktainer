package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hpk/internal/compute/endpoint"
	"hpk/internal/compute/podhandler"
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
	output := []byte("FOO=bar\x00CERT=-----BEGIN CERTIFICATE-----\nMIIF...\n-----END CERTIFICATE-----\x00TOKEN=first-line\nQUJDREVGRw==\x00BAR=baz\x00KUBERNETES_SERVICE_HOST=10.0.0.1\x00KUBERNETES_SERVICE_PORT=6443\x00")

	envs := parseEnvVars(output)
	if len(envs) != 6 {
		t.Fatalf("expected 6 env vars, got %d", len(envs))
	}

	if envs[0].Name != "FOO" || envs[0].Value != "bar" {
		t.Errorf("unexpected env[0]: %+v", envs[0])
	}

	expectedCert := "-----BEGIN CERTIFICATE-----\nMIIF...\n-----END CERTIFICATE-----"
	if envs[1].Name != "CERT" || envs[1].Value != expectedCert {
		t.Errorf("unexpected env[1]: %+v (expected cert value %q)", envs[1], expectedCert)
	}

	expectedToken := "first-line\nQUJDREVGRw=="
	if envs[2].Name != "TOKEN" || envs[2].Value != expectedToken {
		t.Errorf("unexpected env[2]: %+v (expected token value %q)", envs[2], expectedToken)
	}

	if envs[3].Name != "BAR" || envs[3].Value != "baz" {
		t.Errorf("unexpected env[3]: %+v (expected baz)", envs[3])
	}

	if envs[4].Name != "KUBERNETES_SERVICE_HOST" || envs[4].Value != "10.0.0.1" {
		t.Errorf("unexpected env[4]: %+v", envs[4])
	}

	if envs[5].Name != "KUBERNETES_SERVICE_PORT" || envs[5].Value != "6443" {
		t.Errorf("unexpected env[5]: %+v", envs[5])
	}
}

func TestGetHostResolvConf(t *testing.T) {
	fallbackIP := "8.8.8.8"
	t.Setenv("FALLBACK_DNS", fallbackIP)
	kubeDNS := "10.96.0.10"

	t.Run("LoopbackOnly_FallbackAppears", func(t *testing.T) {
		dir := t.TempDir()
		resolvFile := filepath.Join(dir, "resolv.conf")
		content := "nameserver 127.0.0.1\nnameserver 127.0.0.53\nsearch default.svc.cluster.local\n"
		if err := os.WriteFile(resolvFile, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write fixture: %v", err)
		}

		res := getHostResolvConf(kubeDNS, resolvFile)
		if !strings.Contains(res, "nameserver "+fallbackIP) {
			t.Errorf("expected fallback nameserver %s in resolv.conf output, got:\n%s", fallbackIP, res)
		}
		if strings.Contains(res, "nameserver 127.0.0.1") || strings.Contains(res, "nameserver 127.0.0.53") {
			t.Errorf("expected loopback nameservers to be filtered out, got:\n%s", res)
		}
		if !strings.Contains(res, "search default.svc.cluster.local") {
			t.Errorf("expected search directive to be preserved, got:\n%s", res)
		}
	})

	t.Run("RealNameserver_FallbackAbsent", func(t *testing.T) {
		dir := t.TempDir()
		resolvFile := filepath.Join(dir, "resolv.conf")
		content := "nameserver 192.168.1.1\nsearch example.com\n"
		if err := os.WriteFile(resolvFile, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write fixture: %v", err)
		}

		res := getHostResolvConf(kubeDNS, resolvFile)
		if strings.Contains(res, fallbackIP) {
			t.Errorf("expected fallback nameserver %s to be absent, got:\n%s", fallbackIP, res)
		}
		if !strings.Contains(res, "nameserver 192.168.1.1") {
			t.Errorf("expected real nameserver 192.168.1.1 in resolv.conf output, got:\n%s", res)
		}
		if !strings.Contains(res, "search example.com") {
			t.Errorf("expected search directive to be preserved, got:\n%s", res)
		}
	})

	t.Run("KubeDNSIP_FilteredOut", func(t *testing.T) {
		dir := t.TempDir()
		resolvFile := filepath.Join(dir, "resolv.conf")
		content := fmt.Sprintf("nameserver %s\n", kubeDNS)
		if err := os.WriteFile(resolvFile, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write fixture: %v", err)
		}

		res := getHostResolvConf(kubeDNS, resolvFile)
		if !strings.Contains(res, "nameserver "+fallbackIP) {
			t.Errorf("expected fallback nameserver when only kubeDNS IP is present, got:\n%s", res)
		}
	})

	t.Run("NonExistentFile_FallbackAppears", func(t *testing.T) {
		res := getHostResolvConf(kubeDNS, filepath.Join(t.TempDir(), "nonexistent.conf"))
		if !strings.Contains(res, "nameserver "+fallbackIP) {
			t.Errorf("expected fallback nameserver for nonexistent file, got:\n%s", res)
		}
	})
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

	t.Run("PodSpec", func(t *testing.T) {
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

func TestApptainerEnvFile_ShellEvaluation(t *testing.T) {
	testEnvs := []v1.EnvVar{
		{Name: "SIMPLE", Value: "hello"},
		{Name: "BASE64_PAD", Value: "first-line\nQUJDREVGRw=="},
		{Name: "MULTILINE_CERT", Value: "-----BEGIN CERTIFICATE-----\nMIIF...\n-----END CERTIFICATE-----"},
		{Name: "KUBERNETES_SERVICE_HOST", Value: "10.0.0.1"},
		{Name: "KUBERNETES_SERVICE_PORT", Value: "6443"},
		{Name: "QUOTED", Value: "foo'bar\"baz"},
	}

	var b strings.Builder
	for _, e := range testEnvs {
		fmt.Fprintf(&b, "%s=%s\n", e.Name, podhandler.EscapeSingleQuote(e.Value))
	}

	tmpFile := filepath.Join(t.TempDir(), "container.env")
	if err := os.WriteFile(tmpFile, []byte(b.String()), 0644); err != nil {
		t.Fatalf("failed to write tmp env file: %v", err)
	}

	// Evaluate the file with bash (representing apptainer's embedded shell interpreter)
	// and print each variable separated by NUL.
	script := fmt.Sprintf(`. %s; printf 'SIMPLE=%%s\0BASE64_PAD=%%s\0MULTILINE_CERT=%%s\0KUBERNETES_SERVICE_HOST=%%s\0KUBERNETES_SERVICE_PORT=%%s\0QUOTED=%%s\0' "$SIMPLE" "$BASE64_PAD" "$MULTILINE_CERT" "$KUBERNETES_SERVICE_HOST" "$KUBERNETES_SERVICE_PORT" "$QUOTED"`, tmpFile)

	output, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to evaluate env file with bash: %v, output: %s", err, output)
	}

	evalEnvs := parseEnvVars(output)
	if len(evalEnvs) != len(testEnvs) {
		t.Fatalf("expected %d evaluated env vars, got %d", len(testEnvs), len(evalEnvs))
	}

	for i, expected := range testEnvs {
		if evalEnvs[i].Name != expected.Name || evalEnvs[i].Value != expected.Value {
			t.Errorf("env[%d] mismatch: got name=%q val=%q, expected name=%q val=%q",
				i, evalEnvs[i].Name, evalEnvs[i].Value, expected.Name, expected.Value)
		}
	}
}
