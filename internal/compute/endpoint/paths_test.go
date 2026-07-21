package endpoint

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestHPKPath_ParseAbsPath(t *testing.T) {

	rootDir := "/home/fnikol/"

	p := HPK(rootDir)

	tests := []struct {
		name         string
		path         string
		wantPodKey   types.NamespacedName
		wantFileName string
		wantInvalid  bool
	}{
		{
			name:         "empty",
			path:         "",
			wantPodKey:   types.NamespacedName{},
			wantFileName: "",
			wantInvalid:  true,
		},

		{
			name:         "wrongpath",
			path:         "/home/fnikol/.hpk/default/volume-user/reviews.exitCode",
			wantPodKey:   types.NamespacedName{},
			wantFileName: "",
			wantInvalid:  true,
		},

		{
			name:         "wrongpath2",
			path:         "/home/fnikol/.hpk/default/volume-user/notexist/reviews.exitCode",
			wantPodKey:   types.NamespacedName{},
			wantFileName: "",
			wantInvalid:  true,
		},

		{
			name: "abspath",
			path: "/home/fnikol/.hpk/default/volume-user/controlfiles/reviews.exitCode",
			wantPodKey: types.NamespacedName{
				Namespace: "default",
				Name:      "volume-user",
			},
			wantFileName: "reviews.exitCode",
			wantInvalid:  false,
		},

		{
			name: "secret-abspath",
			path: "/home/fnikol/.hpk/opengadget3/grafana/controlfiles/.ip",
			wantPodKey: types.NamespacedName{
				Namespace: "opengadget3",
				Name:      "grafana",
			},
			wantFileName: ".ip",
			wantInvalid:  false,
		},

		{
			name:         "relpath",
			path:         "./hpk" + "/default/volume-user/controlfiles/reviews.exitCode",
			wantPodKey:   types.NamespacedName{},
			wantFileName: "",
			wantInvalid:  true, // only absolute path is support
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPodKey, gotFileName, gotInvalid := p.ParseControlFilePath(tt.path)
			if !reflect.DeepEqual(gotPodKey, tt.wantPodKey) {
				t.Errorf("ParseControlFilePath() got PodKey = %v, want %v", gotPodKey, tt.wantPodKey)
			}
			if gotFileName != tt.wantFileName {
				t.Errorf("ParseControlFilePath() got FileName = %v, want %v", gotFileName, tt.wantFileName)
			}
			if gotInvalid != tt.wantInvalid {
				t.Errorf("ParseControlFilePath() got Invalid = %v, want %v", gotInvalid, tt.wantInvalid)
			}
		})
	}
}

func TestHPKPath_WalkPodDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	hpk := HPK(tmpDir)

	// Create structure:
	// .hpk/default/mypod/controlfiles
	// .hpk/.corrupted/hiddenpod/controlfiles
	podDir := filepath.Join(tmpDir, ".hpk", "default", "mypod", "controlfiles")
	if err := os.MkdirAll(podDir, 0755); err != nil {
		t.Fatalf("failed to create pod dir: %v", err)
	}

	corruptedDir := filepath.Join(tmpDir, ".hpk", ".corrupted", "hiddenpod", "controlfiles")
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
