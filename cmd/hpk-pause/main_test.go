package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestPauseSignalHandling(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	done := make(chan struct{})

	go func() {
		sig := <-sigChan
		if sig != syscall.SIGTERM {
			t.Errorf("expected SIGTERM, got %v", sig)
		}
		close(done)
	}()

	sigChan <- syscall.SIGTERM

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for signal")
	}
}
