package network

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
)

// CNIConfig represents the input to CNI plugin
type CNIConfig struct {
	CniVersion string `json:"cniVersion"`
	Name       string `json:"name"`
	Type       string `json:"type"` // "host-local"
	IPAM       struct {
		Type    string `json:"type"`
		Subnet  string `json:"subnet"`
		DataDir string `json:"dataDir,omitempty"`
	} `json:"ipam"`
}

// CNIResult represents (partial) output from host-local
type CNIResult struct {
	IPs []struct {
		Address string `json:"address"` // CIDR notation
		Gateway string `json:"gateway"`
	} `json:"ips"`
}

// AllocateIP calls host-local to get an IP.
// id: Container ID (unique)
// subnet: Subnet CIDR
// dataDir: path to store allocations (e.g. /var/lib/cni/networks/hpktainer)
func AllocateIP(id string, subnet string, dataDir string) (string, error) {
	// Construct config
	conf := CNIConfig{
		CniVersion: "0.3.1",
		Name:       "hpktainer",
		Type:       "host-local",
	}
	conf.IPAM.Type = "host-local"

	// Sanitation: Ensure subnet is a valid CIDR with no host bits set (network address)
	// host-local is strict about this.
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("invalid subnet cidr: %w", err)
	}
	conf.IPAM.Subnet = ipNet.String()

	conf.IPAM.DataDir = dataDir

	input, err := json.Marshal(conf)
	if err != nil {
		return "", fmt.Errorf("marshal cni config: %w", err)
	}

	// Prepare command
	// We assume host-local is in path or common CNI paths.
	// The user instructions implied invoking the binary.
	// We'll search in standard locations or PATH.
	binPath, err := exec.LookPath("host-local")
	if err != nil {
		// Try standard paths
		candidates := []string{"/opt/cni/bin/host-local", "/usr/libexec/cni/host-local"}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				binPath = c
				break
			}
		}
	}
	if binPath == "" {
		return "", fmt.Errorf("host-local binary not found")
	}

	cmd := exec.Command(binPath)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env,
		"CNI_COMMAND=ADD",
		"CNI_CONTAINERID="+id,
		"CNI_NETNS=/dev/null", // host-local doesn't strictly need netns but spec requires it
		"CNI_IFNAME=eth0",     // Dummy
		"CNI_PATH="+filepath.Dir(binPath),
	)
	cmd.Stdin = bytes.NewReader(input)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cni add failed: %s: %w", output, err)
	}

	// Parse output
	var res CNIResult
	if err := json.Unmarshal(output, &res); err != nil {
		return "", fmt.Errorf("cni output parse error: %w (output: %s)", err, output)
	}

	if len(res.IPs) == 0 {
		return "", fmt.Errorf("no IP allocated")
	}

	return res.IPs[0].Address, nil
}

// ReleaseIP calls host-local to release an IP.
func ReleaseIP(id string, subnet string, dataDir string) error {
	conf := CNIConfig{
		CniVersion: "0.3.1",
		Name:       "hpktainer",
		Type:       "host-local",
	}
	conf.IPAM.Type = "host-local"

	// Sanitation: Ensure subnet is a valid CIDR with no host bits set
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return fmt.Errorf("invalid subnet cidr: %w", err)
	}
	conf.IPAM.Subnet = ipNet.String()

	conf.IPAM.DataDir = dataDir

	input, err := json.Marshal(conf)
	if err != nil {
		return fmt.Errorf("marshal cni config: %w", err)
	}

	binPath, err := exec.LookPath("host-local")
	if err != nil {
		// Try standard paths
		candidates := []string{"/opt/cni/bin/host-local", "/usr/libexec/cni/host-local"}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				binPath = c
				break
			}
		}
	}
	if binPath == "" {
		return fmt.Errorf("host-local binary not found")
	}

	cmd := exec.Command(binPath)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env,
		"CNI_COMMAND=DEL",
		"CNI_CONTAINERID="+id,
		"CNI_NETNS=/dev/null",
		"CNI_IFNAME=eth0",
		"CNI_PATH="+filepath.Dir(binPath),
	)
	cmd.Stdin = bytes.NewReader(input)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cni del failed: %s: %w", out, err)
	}
	return nil
}
