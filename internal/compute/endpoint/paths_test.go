package endpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHPKPath_WalkPodDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	podsDir := filepath.Join(tmpDir, ".hpk", ".pods")
	hpk := HPKWithPods(tmpDir, podsDir)

	if hpk.PodsDir() != podsDir {
		t.Errorf("hpk.PodsDir() = %s, want %s", hpk.PodsDir(), podsDir)
	}

	// Create structure:
	// .hpk/.pods/default/mypod/job
	// .hpk/.pods/.hidden/hiddenpod/job
	podDir := filepath.Join(hpk.PodsDir(), "default", "mypod", "job")
	if err := os.MkdirAll(podDir, 0755); err != nil {
		t.Fatalf("failed to create pod dir: %v", err)
	}

	hiddenDir := filepath.Join(hpk.PodsDir(), ".hidden", "hiddenpod", "job")
	if err := os.MkdirAll(hiddenDir, 0755); err != nil {
		t.Fatalf("failed to create hidden dir: %v", err)
	}

	var visited []string
	err := hpk.WalkPodDirectories(func(path PodPath) error {
		visited = append(visited, path.String())
		return nil
	})
	if err != nil {
		t.Fatalf("WalkPodDirectories failed: %v", err)
	}

	expectedPodPath := filepath.Join(hpk.PodsDir(), "default", "mypod")
	if len(visited) != 1 || visited[0] != expectedPodPath {
		t.Errorf("WalkPodDirectories visited %v, expected [%s]", visited, expectedPodPath)
	}
}

func TestHPKPath_Defaults(t *testing.T) {
	hpk := HPK("/home/testuser")
	if hpk.PodsDir() != DefaultPodsDir {
		t.Errorf("expected default pods dir %s, got %s", DefaultPodsDir, hpk.PodsDir())
	}
	if hpk.String() != filepath.Clean("/home/testuser/.hpk") {
		t.Errorf("expected root dir %s, got %s", filepath.Clean("/home/testuser/.hpk"), hpk.String())
	}
}
