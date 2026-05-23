package network

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type CalicoConfig struct {
	Subnet string
	MTU    string
}

// ParseCalicoConfig reads the Calico subnet environment file and extracts subnet and MTU configurations.
func ParseCalicoConfig(path string) (*CalicoConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open calico config: %w", err)
	}
	defer file.Close()

	config := &CalicoConfig{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		value := parts[1]

		switch key {
		case "CALICO_SUBNET":
			config.Subnet = value
		case "CALICO_MTU":
			config.MTU = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading calico config: %w", err)
	}

	if config.Subnet == "" {
		return nil, fmt.Errorf("CALICO_SUBNET not found in config")
	}

	return config, nil
}
