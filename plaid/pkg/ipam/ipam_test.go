package ipam

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGetIPAMConfig_File(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ipam-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ipamJSON := `{
  "cniVersion": "0.4.0",
  "name": "test-ipam",
  "ipam": {
    "type": "host-local",
    "subnet": "10.244.10.0/24"
  }
}`
	ipamFile := filepath.Join(tmpDir, "ipam.json")
	if err := os.WriteFile(ipamFile, []byte(ipamJSON), 0644); err != nil {
		t.Fatalf("failed to write ipam file: %v", err)
	}

	socketPath := filepath.Join(tmpDir, "plaidd.sock")
	data, err := GetIPAMConfig(socketPath)
	if err != nil {
		t.Fatalf("GetIPAMConfig failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to parse returned IPAM config: %v", err)
	}

	if parsed["name"] != "test-ipam" {
		t.Errorf("expected name 'test-ipam', got %v", parsed["name"])
	}
}
