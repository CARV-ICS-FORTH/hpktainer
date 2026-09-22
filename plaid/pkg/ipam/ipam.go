package ipam

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"plaid/pkg/api"
)

type ipamResult struct {
	CNIVersion string `json:"cniVersion"`
	IPs        []struct {
		Version string `json:"version"`
		Address string `json:"address"`
		Gateway string `json:"gateway"`
	} `json:"ips"`
}

// FindHostLocalBinary searches for the host-local CNI IPAM plugin.
func FindHostLocalBinary() (string, error) {
	candidates := []string{
		"/opt/cni/bin/host-local",
		"/usr/libexec/cni/host-local",
		"/usr/lib/cni/host-local",
		"/usr/local/bin/host-local",
	}

	if cniPath := os.Getenv("CNI_PATH"); cniPath != "" {
		for _, dir := range filepath.SplitList(cniPath) {
			candidates = append([]string{filepath.Join(dir, "host-local")}, candidates...)
		}
	}

	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
			return p, nil
		}
	}

	if p, err := exec.LookPath("host-local"); err == nil {
		return p, nil
	}

	return "", fmt.Errorf("host-local CNI plugin not found in standard paths or CNI_PATH")
}

// GetIPAMConfig returns the IPAM JSON configuration, either by reading /run/plaid/ipam.json
// or dynamically synthesizing it from plaidd status.
func GetIPAMConfig(socketPath string) ([]byte, error) {
	socketDir := filepath.Dir(socketPath)
	ipamFile := filepath.Join(socketDir, "ipam.json")

	if data, err := os.ReadFile(ipamFile); err == nil && len(data) > 0 {
		return data, nil
	}

	// Query plaidd
	client := api.NewClient(socketPath)
	status, err := client.GetStatus()
	if err != nil {
		return nil, fmt.Errorf("failed to query plaidd status: %w", err)
	}

	if status.NodeCIDR == "" {
		return nil, fmt.Errorf("plaidd status has empty NodeCIDR")
	}

	_, ipNet, err := net.ParseCIDR(status.NodeCIDR)
	if err != nil || ipNet.IP.To4() == nil {
		return nil, fmt.Errorf("invalid NodeCIDR %q: %w", status.NodeCIDR, err)
	}

	ip4 := ipNet.IP.To4()
	rangeStart := net.IPv4(ip4[0], ip4[1], ip4[2], 4).String()
	rangeEnd := net.IPv4(ip4[0], ip4[1], ip4[2], 254).String()
	gwIP := status.GatewayIP
	if gwIP == "" {
		gwIP = net.IPv4(ip4[0], ip4[1], ip4[2], 1).String()
	}

	ipamDir := filepath.Join(socketDir, "ipam")
	_ = os.MkdirAll(ipamDir, 0777)

	conf := fmt.Sprintf(`{
  "cniVersion": "0.4.0",
  "name": "plaid-ipam",
  "ipam": {
    "type": "host-local",
    "dataDir": %q,
    "ranges": [
      [
        {
          "subnet": %q,
          "rangeStart": %q,
          "rangeEnd": %q,
          "gateway": %q
        }
      ]
    ]
  }
}`, ipamDir, status.NodeCIDR, rangeStart, rangeEnd, gwIP)

	return []byte(conf), nil
}

// AllocateIP allocates an IP for containerID using host-local.
func AllocateIP(containerID, socketPath string) (string, string, error) {
	binPath, err := FindHostLocalBinary()
	if err != nil {
		return "", "", err
	}

	ipamJSON, err := GetIPAMConfig(socketPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to get IPAM config: %w", err)
	}

	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(),
		"CNI_COMMAND=ADD",
		"CNI_CONTAINERID="+containerID,
		"CNI_NETNS=/dev/null",
		"CNI_IFNAME=eth0",
		"CNI_PATH=/opt/cni/bin",
	)
	cmd.Stdin = bytes.NewReader(ipamJSON)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("host-local ADD failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	var res ipamResult
	if err := json.Unmarshal(out, &res); err != nil {
		return "", "", fmt.Errorf("failed to parse host-local output: %w (output: %s)", err, string(out))
	}

	if len(res.IPs) == 0 {
		return "", "", fmt.Errorf("host-local returned no IP addresses")
	}

	return res.IPs[0].Address, res.IPs[0].Gateway, nil
}

// ReleaseIP releases the allocated IP for containerID using host-local.
func ReleaseIP(containerID, socketPath string) error {
	binPath, err := FindHostLocalBinary()
	if err != nil {
		return nil
	}

	ipamJSON, err := GetIPAMConfig(socketPath)
	if err != nil {
		return nil
	}

	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(),
		"CNI_COMMAND=DEL",
		"CNI_CONTAINERID="+containerID,
		"CNI_NETNS=/dev/null",
		"CNI_IFNAME=eth0",
		"CNI_PATH=/opt/cni/bin",
	)
	cmd.Stdin = bytes.NewReader(ipamJSON)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("host-local DEL failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	return nil
}
