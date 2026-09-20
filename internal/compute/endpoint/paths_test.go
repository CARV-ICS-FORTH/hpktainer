package endpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHPKPath_WalkPodDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	hpk := HPK(tmpDir)

	// Create structure:
	// .hpk/default/mypod/job/pod.crd
	// .hpk/.corrupted/hiddenpod/job/pod.crd
	podDir := filepath.Join(tmpDir, ".hpk", "default", "mypod", "job")
	if err := os.MkdirAll(podDir, 0755); err != nil {
		t.Fatalf("failed to create pod dir: %v", err)
	}

	corruptedDir := filepath.Join(tmpDir, ".hpk", ".corrupted", "hiddenpod", "job")
	if err := os.MkdirAll(corruptedDir, 0755); err != nil {
		t.Fatalf("failed to create corrupted dir: %v", err)
	}

	var visited []string
	err := hpk.WalkPodDirectories(func(path PodPath) error {
		visited = append(visited, path.String())
		return nil
	})
	if err != nil {
		t.Fatalf("WalkPodDirectories failed: %v", err)
	}

	expectedPodPath := filepath.Join(tmpDir, ".hpk", "default", "mypod")
	if len(visited) != 1 || visited[0] != expectedPodPath {
		t.Errorf("WalkPodDirectories visited %v, expected [%s]", visited, expectedPodPath)
	}
}
