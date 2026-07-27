package podhandler

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestGenerateEnvTemplate_NULSeparated(t *testing.T) {
	tmpl, err := ParseTemplate(GenerateEnvTemplate)
	if err != nil {
		t.Fatalf("failed to parse template: %v", err)
	}

	fields := GenerateEnvFields{
		Variables: []corev1.EnvVar{
			{Name: "SIMPLE", Value: "hello"},
			{Name: "BASE64_PAD", Value: "first-line\nQUJDREVGRw=="},
			{Name: "MULTILINE_CERT", Value: "-----BEGIN CERTIFICATE-----\nMIIF...\n-----END CERTIFICATE-----"},
			{Name: "KUBERNETES_SERVICE_HOST", Value: "10.0.0.1"},
		},
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, fields); err != nil {
		t.Fatalf("failed to execute template: %v", err)
	}

	cmd := exec.Command("bash", "-c", buf.String())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script execution failed: %v, output:\n%s", err, output)
	}

	entries := bytes.Split(output, []byte{0})
	// Expect 4 entries plus trailing empty slice from NUL ending
	var parsed []corev1.EnvVar
	for _, entry := range entries {
		if len(entry) == 0 {
			continue
		}
		parts := strings.SplitN(string(entry), "=", 2)
		if len(parts) == 2 {
			parsed = append(parsed, corev1.EnvVar{Name: parts[0], Value: parts[1]})
		}
	}

	if len(parsed) != 4 {
		t.Fatalf("expected 4 parsed env vars, got %d", len(parsed))
	}

	if parsed[0].Name != "SIMPLE" || parsed[0].Value != "hello" {
		t.Errorf("unexpected parsed[0]: %+v", parsed[0])
	}

	if parsed[1].Name != "BASE64_PAD" || parsed[1].Value != "first-line\nQUJDREVGRw==" {
		t.Errorf("unexpected parsed[1]: %+v", parsed[1])
	}

	if parsed[2].Name != "MULTILINE_CERT" || parsed[2].Value != "-----BEGIN CERTIFICATE-----\nMIIF...\n-----END CERTIFICATE-----" {
		t.Errorf("unexpected parsed[2]: %+v", parsed[2])
	}

	if parsed[3].Name != "KUBERNETES_SERVICE_HOST" || parsed[3].Value != "10.0.0.1" {
		t.Errorf("unexpected parsed[3]: %+v", parsed[3])
	}
}
