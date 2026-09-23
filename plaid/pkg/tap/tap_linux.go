//go:build linux

package tap

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"unsafe"
)

const (
	tunDevice = "/dev/net/tun"

	IFF_TAP   = 0x0002
	IFF_NO_PI = 0x1000

	TUNSETIFF = 0x400454ca

	SIOCSIFFLAGS   = 0x8914
	SIOCSIFADDR    = 0x8916
	SIOCSIFNETMASK = 0x891c
	SIOCSIFMTU     = 0x8922
	SIOCSIFHWADDR  = 0x8924
	SIOCADDRT      = 0x890b

	IFF_UP      = 0x1
	IFF_RUNNING = 0x40

	RTF_UP       = 0x0001
	RTF_GATEWAY  = 0x0002
	ARPHRD_ETHER = 1
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

type ifreq struct {
	name  [16]byte
	flags uint16
	pad   [22]byte
}

type ifreqInt struct {
	name [16]byte
	val  int32
	pad  [20]byte
}

type ifreqSockaddr struct {
	name [16]byte
	addr syscall.RawSockaddrInet4
	pad  [8]byte
}

type ifreqHWAddr struct {
	name [16]byte
	addr struct {
		family uint16
		data   [14]byte
	}
	pad [8]byte
}

type rtentry struct {
	pad1    uint64
	dst     syscall.RawSockaddrInet4
	gateway syscall.RawSockaddrInet4
	genmask syscall.RawSockaddrInet4
	flags   uint16
	pad2    int16
	pad3    uint64
	pad4    uint64
	metric  int16
	pad5    [6]byte
	dev     *byte
	pad6    [24]byte
}

func nameTo16(name string) [16]byte {
	var b [16]byte
	copy(b[:], []byte(name))
	return b
}

// CreateAndConfigureTap opens /dev/net/tun, allocates a TAP interface, and configures IP/routes inside netns.
func CreateAndConfigureTap(cfg TapConfig) (*os.File, error) {
	fd, err := syscall.Open(tunDevice, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", tunDevice, err)
	}

	var req ifreq
	req.flags = IFF_TAP | IFF_NO_PI
	if cfg.Name != "" {
		req.name = nameTo16(cfg.Name)
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(TUNSETIFF), uintptr(unsafe.Pointer(&req)))
	if errno != 0 {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("ioctl TUNSETIFF failed: %w", errno)
	}

	actualName := string(req.name[:])
	for i, c := range req.name {
		if c == 0 {
			actualName = string(req.name[:i])
			break
		}
	}

	// Open socket for network configuration ioctls
	sock, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("failed to create config socket: %w", err)
	}
	defer syscall.Close(sock)

	// 1. Set MTU
	if cfg.MTU > 0 {
		var reqMTU ifreqInt
		reqMTU.name = nameTo16(actualName)
		reqMTU.val = int32(cfg.MTU)
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(sock), uintptr(SIOCSIFMTU), uintptr(unsafe.Pointer(&reqMTU)))
		if errno != 0 {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("ioctl SIOCSIFMTU (%d) failed on %s: %w", cfg.MTU, actualName, errno)
		}
	}

	// 2. Set MAC
	if len(cfg.MAC) == 6 {
		var reqMAC ifreqHWAddr
		reqMAC.name = nameTo16(actualName)
		reqMAC.addr.family = ARPHRD_ETHER
		copy(reqMAC.addr.data[0:6], cfg.MAC)
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(sock), uintptr(SIOCSIFHWADDR), uintptr(unsafe.Pointer(&reqMAC)))
		if errno != 0 {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("ioctl SIOCSIFHWADDR (%s) failed on %s: %w", cfg.MAC, actualName, errno)
		}
	}

	// 3. Set IP
	if cfg.IP != nil && cfg.IP.To4() != nil {
		var reqAddr ifreqSockaddr
		reqAddr.name = nameTo16(actualName)
		reqAddr.addr.Family = syscall.AF_INET
		copy(reqAddr.addr.Addr[:], cfg.IP.To4())
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(sock), uintptr(SIOCSIFADDR), uintptr(unsafe.Pointer(&reqAddr)))
		if errno != 0 {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("ioctl SIOCSIFADDR (%s) failed on %s: %w", cfg.IP, actualName, errno)
		}
	}

	// 4. Set Netmask
	if len(cfg.Mask) == 4 {
		var reqMask ifreqSockaddr
		reqMask.name = nameTo16(actualName)
		reqMask.addr.Family = syscall.AF_INET
		copy(reqMask.addr.Addr[:], cfg.Mask)
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(sock), uintptr(SIOCSIFNETMASK), uintptr(unsafe.Pointer(&reqMask)))
		if errno != 0 {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("ioctl SIOCSIFNETMASK failed on %s: %w", actualName, errno)
		}
	}

	// 5. Bring TAP interface UP
	var reqUp ifreq
	reqUp.name = nameTo16(actualName)
	reqUp.flags = IFF_UP | IFF_RUNNING
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(sock), uintptr(SIOCSIFFLAGS), uintptr(unsafe.Pointer(&reqUp)))
	if errno != 0 {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("ioctl SIOCSIFFLAGS UP failed on %s: %w", actualName, errno)
	}

	// 6. Bring loopback up
	var reqLo ifreq
	reqLo.name = nameTo16("lo")
	reqLo.flags = IFF_UP | IFF_RUNNING
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(sock), uintptr(SIOCSIFFLAGS), uintptr(unsafe.Pointer(&reqLo)))
	if errno != 0 {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("ioctl SIOCSIFFLAGS UP failed on lo: %w", errno)
	}

	// 7. Add default route
	if cfg.Gateway != nil && cfg.Gateway.To4() != nil {
		var rt rtentry
		rt.flags = RTF_UP | RTF_GATEWAY
		devBytes := append([]byte(actualName), 0)
		rt.dev = &devBytes[0]
		rt.dst.Family = syscall.AF_INET
		rt.genmask.Family = syscall.AF_INET
		rt.gateway.Family = syscall.AF_INET
		copy(rt.gateway.Addr[:], cfg.Gateway.To4())
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(sock), uintptr(SIOCADDRT), uintptr(unsafe.Pointer(&rt)))
		if errno != 0 && errno != syscall.EEXIST {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("ioctl SIOCADDRT default route via %s dev %s failed: %w", cfg.Gateway, actualName, errno)
		}
	}

	return os.NewFile(uintptr(fd), actualName), nil
}
