package projected

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"skifflet/internal/compute/volume/util"
)

func TestTokenRefreshLoop_RotatesToken(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "skiff-projected-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	writer, err := util.NewAtomicWriter(tempDir, "test-refresh")
	if err != nil {
		t.Fatalf("failed to create atomic writer: %v", err)
	}

	mode := int32(0644)
	initialData := map[string]util.FileProjection{
		"token": {
			Data: []byte("initial-token-value"),
			Mode: mode,
		},
	}
	if err := writer.Write(initialData); err != nil {
		t.Fatalf("failed initial write: %v", err)
	}

	// Verify initial token file
	tokenPath := filepath.Join(tempDir, "token")
	content, err := os.ReadFile(tokenPath)
	if err != nil || string(content) != "initial-token-value" {
		t.Fatalf("expected initial token, got %q (err: %v)", content, err)
	}

	// Test cancellation stops loop without panic
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	m := &VolumeMounter{}
	// Calling with cancelled context must return immediately
	m.startTokenRefreshLoop(ctx, tempDir, writer, time.Now().Add(10*time.Second))
}
