package slirp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"plaid/pkg/packet"
)

var (
	ErrSlirpNotRunning = errors.New("slirp4netns is not running")
)

// FrameInjector receives packets coming back from slirp4netns to deliver to pods.
type FrameInjector interface {
	InjectFrame(frame *packet.EthernetFrame) error
}

// SlirpConfig holds configuration options for launching and connecting to slirp4netns.
type SlirpConfig struct {
	BinaryPath          string // Path to slirp4netns binary (default "slirp4netns")
	SocketPath          string // UNIX domain socket path for BESS mode (e.g. "/run/plaid/slirp.sock")
	CIDR                string // Node pod CIDR, e.g. "10.244.1.0/24"
	MTU                 int    // e.g. 1500 (or 65520)
	DisableDNS          bool   // Disable slirp built-in DNS (for Kubernetes CoreDNS)
	DisableHostLoopback bool   // If false, enables access to host 127.0.0.1
}

// MakeSlirpMAC returns the libslirp emulated host MAC for an IP address: 52:55:IP0:IP1:IP2:IP3
func MakeSlirpMAC(ip net.IP) net.HardwareAddr {
	ip4 := ip.To4()
	if ip4 == nil {
		return net.HardwareAddr{0x52, 0x55, 0x00, 0x00, 0x00, 0x00}
	}
	return net.HardwareAddr{0x52, 0x55, ip4[0], ip4[1], ip4[2], ip4[3]}
}

// SlirpManager supervises slirp4netns and handles packet transit.
type SlirpManager struct {
	mu         sync.Mutex
	cfg        SlirpConfig
	nodeNet    *net.IPNet
	routerMAC  net.HardwareAddr
	cmd        *exec.Cmd
	conn       net.Conn
	injector   FrameInjector
	closedChan chan struct{}
	closeOnce  sync.Once
}

// NewSlirpManager creates a new SlirpManager instance.
func NewSlirpManager(cfg SlirpConfig, injector FrameInjector) *SlirpManager {
	if cfg.BinaryPath == "" {
		cfg.BinaryPath = "slirp4netns"
	}
	if cfg.MTU <= 0 {
		cfg.MTU = 1500
	}

	var nodeNet *net.IPNet
	var routerMAC net.HardwareAddr
	if cfg.CIDR != "" {
		_, parsedNet, err := net.ParseCIDR(cfg.CIDR)
		if err == nil && parsedNet != nil {
			nodeNet = parsedNet
			baseIP := nodeNet.IP.Mask(nodeNet.Mask).To4()
			if baseIP != nil {
				routerIP := make(net.IP, 4)
				copy(routerIP, baseIP)
				routerIP[3] += 2
				routerMAC = MakeSlirpMAC(routerIP)
			}
		}
	}
	if routerMAC == nil {
		routerMAC = net.HardwareAddr{0x52, 0x55, 0x0a, 0x00, 0x02, 0x02}
	}

	return &SlirpManager{
		cfg:        cfg,
		nodeNet:    nodeNet,
		routerMAC:  routerMAC,
		injector:   injector,
		closedChan: make(chan struct{}),
	}
}

// Start launches slirp4netns as a child process and connects to its BESS UNIX domain socket.
func (sm *SlirpManager) Start(ctx context.Context) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Clean up stale socket file if it exists
	if sm.cfg.SocketPath != "" {
		_ = os.Remove(sm.cfg.SocketPath)
	}

	args := []string{
		"--target-type=bess",
		fmt.Sprintf("--mtu=%d", sm.cfg.MTU),
	}

	if sm.cfg.CIDR != "" {
		args = append(args, fmt.Sprintf("--cidr=%s", sm.cfg.CIDR))
	}
	if sm.cfg.DisableDNS {
		args = append(args, "--disable-dns")
	}
	if sm.cfg.DisableHostLoopback {
		args = append(args, "--disable-host-loopback")
	}

	args = append(args, sm.cfg.SocketPath)

	cmd := exec.CommandContext(ctx, sm.cfg.BinaryPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to spawn slirp4netns: %w", err)
	}
	sm.cmd = cmd

	// Wait for socket to become available and connect
	conn, err := sm.waitForSocket(ctx, sm.cfg.SocketPath, 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("failed to connect to slirp4netns BESS socket: %w", err)
	}
	sm.conn = conn

	go sm.readLoop(ctx)
	return nil
}

// AttachConn allows attaching an already open connection (useful for in-memory tests).
func (sm *SlirpManager) AttachConn(ctx context.Context, conn net.Conn) {
	sm.mu.Lock()
	sm.conn = conn
	sm.mu.Unlock()

	go sm.readLoop(ctx)
}

func (sm *SlirpManager) waitForSocket(ctx context.Context, path string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Try to dial SOCK_SEQPACKET or stream socket
		conn, err := net.Dial("unixpacket", path)
		if err == nil {
			return conn, nil
		}
		// Fallback to standard unix socket if unixpacket isn't supported on platform
		conn, err = net.Dial("unix", path)
		if err == nil {
			return conn, nil
		}

		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for socket %s", path)
}

// WriteFrame sends an outbound Ethernet frame into slirp4netns.
func (sm *SlirpManager) WriteFrame(frame *packet.EthernetFrame) error {
	sm.mu.Lock()
	conn := sm.conn
	sm.mu.Unlock()

	if conn == nil {
		return ErrSlirpNotRunning
	}

	// libslirp emulated hosts use MAC format 52:55:IP:IP:IP:IP.
	// If destination already has 52:55 prefix, keep it.
	// Otherwise, for IPv4 packets:
	// If destination IP is within local node CIDR, use 52:55:<DstIP>.
	// If destination IP is external (Internet), use the slirp router MAC 52:55:<RouterIP>.
	if len(frame.DstMAC) != 6 || frame.DstMAC[0] != 0x52 || frame.DstMAC[1] != 0x55 {
		if frame.EtherType == packet.EtherTypeIPv4 {
			ipPkt, err := packet.ParseIPv4(frame.Payload)
			if err == nil {
				if sm.nodeNet != nil && sm.nodeNet.Contains(ipPkt.Header.DstIP) {
					frame.DstMAC = MakeSlirpMAC(ipPkt.Header.DstIP)
				} else if sm.routerMAC != nil {
					frame.DstMAC = sm.routerMAC
				}
			}
		} else if sm.routerMAC != nil {
			frame.DstMAC = sm.routerMAC
		}
	}

	raw, err := frame.Marshal()
	if err != nil {
		return err
	}

	_, err = conn.Write(raw)
	return err
}

func (sm *SlirpManager) readLoop(ctx context.Context) {
	buf := make([]byte, 65535)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sm.closedChan:
			return
		default:
		}

		sm.mu.Lock()
		conn := sm.conn
		sm.mu.Unlock()

		if conn == nil {
			return
		}

		n, err := conn.Read(buf)
		if err != nil {
			select {
			case <-sm.closedChan:
				return
			case <-ctx.Done():
				return
			default:
				return
			}
		}

		if n < packet.EthernetHeaderLen {
			continue
		}

		frame, err := packet.ParseEthernet(buf[:n])
		if err != nil {
			continue
		}

		if sm.injector != nil {
			_ = sm.injector.InjectFrame(frame)
		}
	}
}

// Close terminates the slirp4netns process and closes the socket connection.
func (sm *SlirpManager) Close() error {
	var err error
	sm.closeOnce.Do(func() {
		close(sm.closedChan)

		sm.mu.Lock()
		defer sm.mu.Unlock()

		if sm.conn != nil {
			_ = sm.conn.Close()
			sm.conn = nil
		}

		if sm.cmd != nil && sm.cmd.Process != nil {
			_ = sm.cmd.Process.Kill()
			_ = sm.cmd.Wait()
			sm.cmd = nil
		}

		if sm.cfg.SocketPath != "" {
			_ = os.Remove(sm.cfg.SocketPath)
		}
	})
	return err
}
