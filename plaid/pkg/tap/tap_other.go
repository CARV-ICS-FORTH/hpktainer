//go:build !linux

package tap

import (
	"errors"
	"net"
	"os"
)

// TapConfig holds network configuration parameters for the TAP interface.
type TapConfig struct {
	Name    string
	IP      net.IP
	Mask    net.IPMask
	Gateway net.IP
	MAC     net.HardwareAddr
	MTU     int
}

// CreateAndConfigureTap is a stub on non-Linux platforms.
func CreateAndConfigureTap(cfg TapConfig) (*os.File, error) {
	return nil, errors.New("TAP device configuration is only supported on Linux")
}
