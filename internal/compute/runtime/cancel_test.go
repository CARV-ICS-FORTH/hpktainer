package runtime

import (
	"errors"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

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
	// Process that traps and ignores SIGTERM
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 10")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start trap process: %v", err)
	}

	pidStr := strconv.Itoa(cmd.Process.Pid)
	// Use short timeout so test runs fast
	_, err := KillProcessByPIDWithTimeout(pidStr, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error killing process with escalation: %v", err)
	}

	_ = cmd.Wait()
}
