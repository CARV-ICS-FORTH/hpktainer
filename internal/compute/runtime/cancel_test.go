package runtime

import (
	"errors"
	"testing"
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
