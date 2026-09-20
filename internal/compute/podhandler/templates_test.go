package podhandler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hpk/internal/compute/endpoint"
)

func TestEscapeSingleQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"foo'bar", "'foo'\\''bar'"},
		{"hello world", "'hello world'"},
	}

	for _, tt := range tests {
		got := EscapeSingleQuote(tt.input)
		if got != tt.want {
			t.Errorf("EscapeSingleQuote(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildApptainerArgs(t *testing.T) {
	tmpDir := t.TempDir()
	podDir := endpoint.PodPath(filepath.Join(tmpDir, "pod"))
	_ = os.MkdirAll(podDir.JobDir(), 0755)
	_ = os.WriteFile(filepath.Join(podDir.JobDir(), "resolv.conf"), []byte("nameserver 1.1.1.1\n"), 0644)
	_ = os.WriteFile(filepath.Join(podDir.JobDir(), "hosts"), []byte("127.0.0.1 localhost\n"), 0644)

	c := &Container{
		ExecutionMode: "exec",
		ImageFilePath: "/images/alpine.sif",
		Command:       []string{"/bin/sh"},
		Args:          []string{"-c", "echo hi"},
		RunAsUser:     1000,
		RunAsGroup:    1000,
		Binds:         []string{"/host/data:/data:rw"},
	}

	args := c.BuildApptainerArgs(12345, podDir)

	joined := strings.Join(args, " ")
	if !strings.HasPrefix(joined, "--host-networking exec") {
		t.Errorf("expected --host-networking exec prefix, got: %s", joined)
	}
	if !strings.Contains(joined, "--netns-path /proc/12345/ns/net") {
		t.Errorf("expected netns-path in args, got: %s", joined)
	}
	if !strings.Contains(joined, "resolv.conf:/etc/resolv.conf") {
		t.Errorf("expected resolv.conf bind in args, got: %s", joined)
	}
	if !strings.Contains(joined, "--security uid:1000,gid:1000") {
		t.Errorf("expected security uid/gid in args, got: %s", joined)
	}
}
