package runtime

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestHelperProcessIgnoreSIGTERM(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	time.Sleep(10 * time.Second)
	os.Exit(0)
}

func createHelperProcess(t *testing.T) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessIgnoreSIGTERM$")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start helper process: %v", err)
	}
	return cmd
}


func TestKillProcessByPID_InvalidInputs(t *testing.T) {
	_, err := KillProcessByPID("")
	if !errors.Is(err, ErrInvalidJob) {
		t.Errorf("expected ErrInvalidJob for empty pid, got %v", err)
	}

	_, err = KillProcessByPID("invalid_number")
	if !errors.Is(err, ErrInvalidJob) {
		t.Errorf("expected ErrInvalidJob for non-numeric pid, got %v", err)
	}
}

func TestKillProcessByPID_NonExistentPID(t *testing.T) {
	// A very high PID unlikely to exist
	_, err := KillProcessByPID("9999999")
	if !errors.Is(err, ErrInvalidJob) {
		t.Errorf("expected ErrInvalidJob (ESRCH) for non-existent process, got %v", err)
	}
}

func TestKillProcessByPIDWithTimeout_GracefulExit(t *testing.T) {
	cmd := exec.Command("sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep process: %v", err)
	}

	pidStr := strconv.Itoa(cmd.Process.Pid)
	_, err := KillProcessByPIDWithTimeout(pidStr, 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected error killing process: %v", err)
	}

	_ = cmd.Wait()
}

func TestKillProcessByPIDWithTimeout_SIGKILLEscalation(t *testing.T) {
	cmd := createHelperProcess(t)
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})

	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	pidStr := strconv.Itoa(cmd.Process.Pid)
	// Use short timeout so test runs fast
	_, err := KillProcessByPIDWithTimeout(pidStr, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error killing process with escalation: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("process did not exit within timeout after SIGKILL")
	}
}

func TestKillPodProcessesWithTimeout_GroupEscalation(t *testing.T) {
	primary := createHelperProcess(t)
	secondary := createHelperProcess(t)
	time.Sleep(100 * time.Millisecond)

	doneP := make(chan struct{})
	go func() {
		_ = primary.Wait()
		close(doneP)
	}()

	doneS := make(chan struct{})
	go func() {
		_ = secondary.Wait()
		close(doneS)
	}()

	primaryPIDStr := strconv.Itoa(primary.Process.Pid)
	secondaryPIDStr := strconv.Itoa(secondary.Process.Pid)

	// Short timeout so test runs fast
	_, err := KillPodProcessesWithTimeout(primaryPIDStr, []string{secondaryPIDStr}, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error killing processes with escalation: %v", err)
	}

	select {
	case <-doneP:
	case <-time.After(3 * time.Second):
		t.Fatal("primary process did not exit within timeout after SIGKILL")
	}

	select {
	case <-doneS:
	case <-time.After(3 * time.Second):
		t.Fatal("secondary process did not exit within timeout after SIGKILL")
	}
}









