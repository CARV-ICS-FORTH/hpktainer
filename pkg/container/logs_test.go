package container

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGetTailLogChronologicalOrder(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")

	content := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test log: %v", err)
	}

	lines, err := GetTailLog(logPath, 3)
	if err != nil {
		t.Fatalf("GetTailLog failed: %v", err)
	}

	expected := []string{"line 3", "line 4", "line 5"}
	if !reflect.DeepEqual(lines, expected) {
		t.Errorf("expected %v, got %v", expected, lines)
	}
}
