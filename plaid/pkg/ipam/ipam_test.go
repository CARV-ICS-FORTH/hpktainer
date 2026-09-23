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

func TestValidateNodeCIDR(t *testing.T) {
	// Valid /24
	if _, err := ValidateNodeCIDR("10.244.1.0/24"); err != nil {
		t.Errorf("expected 10.244.1.0/24 to be valid, got: %v", err)
	}

	// Reject empty
	if _, err := ValidateNodeCIDR(""); err == nil {
		t.Errorf("expected empty string to fail validation")
	}

	// Reject IPv6
	if _, err := ValidateNodeCIDR("fd00::/64"); err == nil {
		t.Errorf("expected IPv6 to fail validation")
	}

	// Reject non-/24 prefix (/25)
	if _, err := ValidateNodeCIDR("10.244.1.0/25"); err == nil {
		t.Errorf("expected /25 prefix to fail validation")
	}

	// Reject non-/24 prefix (/16)
	if _, err := ValidateNodeCIDR("10.244.0.0/16"); err == nil {
		t.Errorf("expected /16 prefix to fail validation")
	}
}

func TestBuildHostLocalIPAMConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Default gateway
	cfgData, err := BuildHostLocalIPAMConfig(tmpDir, "10.244.5.0/24", "")
	if err != nil {
		t.Fatalf("BuildHostLocalIPAMConfig failed: %v", err)
	}

	var parsed struct {
		IPAM struct {
			Ranges [][]struct {
				Subnet     string `json:"subnet"`
				RangeStart string `json:"rangeStart"`
				RangeEnd   string `json:"rangeEnd"`
				Gateway    string `json:"gateway"`
			} `json:"ranges"`
		} `json:"ipam"`
	}
	if err := json.Unmarshal(cfgData, &parsed); err != nil {
		t.Fatalf("failed to unmarshal generated IPAM config: %v", err)
	}

	r := parsed.IPAM.Ranges[0][0]
	if r.Subnet != "10.244.5.0/24" {
		t.Errorf("expected subnet 10.244.5.0/24, got %s", r.Subnet)
	}
	if r.RangeStart != "10.244.5.4" {
		t.Errorf("expected rangeStart 10.244.5.4, got %s", r.RangeStart)
	}
	if r.RangeEnd != "10.244.5.254" {
		t.Errorf("expected rangeEnd 10.244.5.254, got %s", r.RangeEnd)
	}
	if r.Gateway != "10.244.5.1" {
		t.Errorf("expected gateway 10.244.5.1, got %s", r.Gateway)
	}

	// Out of subnet gateway should fail
	if _, err := BuildHostLocalIPAMConfig(tmpDir, "10.244.5.0/24", "10.244.6.1"); err == nil {
		t.Errorf("expected out-of-subnet gateway to fail")
	}
}
