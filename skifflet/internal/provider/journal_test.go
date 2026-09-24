package provider

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"skifflet/internal/compute/runtime"
)

func TestInstanceLock_MutualExclusion(t *testing.T) {
	tmpDir := t.TempDir()

	lock1, err := AcquireInstanceLock(tmpDir)
	if err != nil {
		t.Fatalf("first AcquireInstanceLock failed: %v", err)
	}
	defer lock1.Release()

	// Second acquire on the same directory must fail with ErrInstanceLocked
	_, err2 := AcquireInstanceLock(tmpDir)
	if !errors.Is(err2, ErrInstanceLocked) {
		t.Fatalf("expected ErrInstanceLocked on second acquire, got: %v", err2)
	}

	// After release, re-acquire succeeds
	lock1.Release()

	lock3, err3 := AcquireInstanceLock(tmpDir)
	if err3 != nil {
		t.Fatalf("AcquireInstanceLock failed after release: %v", err3)
	}
	defer lock3.Release()
}

func TestReconcileSurvivingWorkloads_ReapsOrphanProcess(t *testing.T) {
	tmpDir := t.TempDir()

	// Start a dummy sleeper process to simulate an orphan surviving workload
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleeper: %v", err)
	}
	pid := cmd.Process.Pid
	st, _ := runtime.GetProcessStartTime(pid)

	rec := PodJournalRecord{
		UID:            types.UID("orphan-uid-123"),
		Generation:     1,
		Namespace:      "default",
		Name:           "orphan-pod",
		PodDir:         filepath.Join(tmpDir, "orphan-pod"),
		PausePID:       pid,
		PauseStartTime: st,
	}

	if err := WriteJournalRecord(tmpDir, rec); err != nil {
		t.Fatalf("WriteJournalRecord failed: %v", err)
	}

	// Reconcile should terminate the process and remove the journal record
	if err := ReconcileSurvivingWorkloads(tmpDir); err != nil {
		t.Fatalf("ReconcileSurvivingWorkloads failed: %v", err)
	}

	// Verify process was killed
	time.Sleep(100 * time.Millisecond)
	_ = cmd.Wait()
	if !runtime.IsProcessDead(pid) {
		t.Errorf("expected orphan process %d to be dead after reconcile", pid)
	}

	// Verify journal entry was removed
	journalPath := filepath.Join(JournalDir(tmpDir), "orphan-uid-123.json")
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Errorf("expected journal file %s to be removed, but still exists", journalPath)
	}
}
