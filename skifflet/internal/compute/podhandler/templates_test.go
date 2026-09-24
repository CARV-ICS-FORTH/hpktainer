package podhandler

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"skifflet/internal/compute/endpoint"
)

func TestEscapeSingleQuote_ShellRoundTrip(t *testing.T) {
	testCases := []struct {
		name  string
		input string
	}{
		{"simple", "simple"},
		{"spaces", "hello world"},
		{"single_quote", "foo'bar"},
		{"double_quote", `foo"bar`},
		{"dollar_sign", "foo$bar"},
		{"env_expansion_attempt", "$PATH ${VAR}"},
		{"backticks", "foo`whoami`bar"},
		{"backslashes", `foo\bar\baz`},
		{"newlines", "line1\nline2\nline3"},
		{"tabs", "col1\tcol2"},
		{"semicolon_and_pipes", "foo; bar | baz && qux || exit 1"},
		{"glob_chars", "* ? [a-z] {1..5}"},
		{"mixed_all", `echo "hello '$USER'" && \` + "\n" + `\` + "`whoami`" + ` $PATH`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			escaped := EscapeSingleQuote(tc.input)

			// Execute a real shell command that outputs the escaped argument via printf '%s'
			cmd := exec.Command("sh", "-c", "printf '%s' "+escaped)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("shell execution failed for input %q (escaped %q): %v", tc.input, escaped, err)
			}

			if string(out) != tc.input {
				t.Errorf("Round-trip mismatch for %s:\ninput:   %q\nescaped: %q\ngot:     %q", tc.name, tc.input, escaped, string(out))
			}
		})
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
