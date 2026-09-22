package endpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkiffPath_WalkPodDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	podsDir := filepath.Join(tmpDir, ".skiff", ".pods")
	skiff := SkiffWithPods(tmpDir, podsDir)

	if skiff.PodsDir() != podsDir {
		t.Errorf("skiff.PodsDir() = %s, want %s", skiff.PodsDir(), podsDir)
	}

	// Create structure:
	// .skiff/.pods/default/mypod/job
	// .skiff/.pods/.hidden/hiddenpod/job
	podDir := filepath.Join(skiff.PodsDir(), "default", "mypod", "job")
	if err := os.MkdirAll(podDir, 0755); err != nil {
		t.Fatalf("failed to create pod dir: %v", err)
	}

	hiddenDir := filepath.Join(skiff.PodsDir(), ".hidden", "hiddenpod", "job")
	if err := os.MkdirAll(hiddenDir, 0755); err != nil {
		t.Fatalf("failed to create hidden dir: %v", err)
	}

	var visited []string
	err := skiff.WalkPodDirectories(func(path PodPath) error {
		visited = append(visited, path.String())
		return nil
	})
	if err != nil {
		t.Fatalf("WalkPodDirectories failed: %v", err)
	}

	expectedPodPath := filepath.Join(skiff.PodsDir(), "default", "mypod")
	if len(visited) != 1 || visited[0] != expectedPodPath {
		t.Errorf("WalkPodDirectories visited %v, expected [%s]", visited, expectedPodPath)
	}
}

func TestSkiffPath_Defaults(t *testing.T) {
	skiff := Skiff("/home/testuser")
	if skiff.PodsDir() != DefaultPodsDir {
		t.Errorf("expected default pods dir %s, got %s", DefaultPodsDir, skiff.PodsDir())
	}
	if skiff.String() != filepath.Clean("/home/testuser/.skiff") {
		t.Errorf("expected root dir %s, got %s", filepath.Clean("/home/testuser/.skiff"), skiff.String())
	}
}
