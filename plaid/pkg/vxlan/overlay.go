package vxlan

import (
	"context"
	"fmt"
	"net"
	"sync"

	"plaid/pkg/packet"
)

// FrameInjector receives decapsulated Ethernet frames to deliver locally.
type FrameInjector interface {
	InjectFrame(frame *packet.EthernetFrame) error
}

// OverlayConfig holds configuration for the VXLAN overlay engine.
type OverlayConfig struct {
	BindAddress string // e.g. "0.0.0.0"
	Port        int    // e.g. 8472
	DefaultVNI  uint32 // e.g. 1
}

// OverlayEngine manages sending and receiving VXLAN packets over UDP.
type OverlayEngine struct {
	cfg        OverlayConfig
	routes     *RouteTable
	injector   FrameInjector
	conn       *net.UDPConn
	localAddr  *net.UDPAddr
	closeOnce  sync.Once
	closedChan chan struct{}
}

// NewOverlayEngine creates an OverlayEngine instance.
func NewOverlayEngine(cfg OverlayConfig, routes *RouteTable, injector FrameInjector) *OverlayEngine {
	if cfg.DefaultVNI == 0 {
		cfg.DefaultVNI = packet.DefaultVNI
	}

	return &OverlayEngine{
		cfg:        cfg,
		routes:     routes,
		injector:   injector,
		closedChan: make(chan struct{}),
	}
}

// Start begins listening on the configured UDP port for incoming VXLAN frames.
func (oe *OverlayEngine) Start(ctx context.Context) error {
	addrStr := fmt.Sprintf("%s:%d", oe.cfg.BindAddress, oe.cfg.Port)
	laddr, err := net.ResolveUDPAddr("udp", addrStr)
	if err != nil {
		return fmt.Errorf("failed to resolve overlay UDP address %s: %w", addrStr, err)
	}

	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return fmt.Errorf("failed to listen on VXLAN port %d: %w", oe.cfg.Port, err)
	}

	oe.conn = conn
	oe.localAddr = conn.LocalAddr().(*net.UDPAddr)

	go oe.readLoop(ctx)
	return nil
}

// LocalPort returns the actual bound port (useful for tests using port 0).
func (oe *OverlayEngine) LocalPort() int {
	if oe.localAddr != nil {
		return oe.localAddr.Port
	}
	return oe.cfg.Port
}

// Send encapsulates an Ethernet frame into a VXLAN packet and sends it to the remote node.
func (oe *OverlayEngine) Send(frame *packet.EthernetFrame, dstIP net.IP) error {
	if oe.conn == nil {
		return fmt.Errorf("overlay engine not started")
	}

	route, err := oe.routes.Lookup(dstIP)
	if err != nil {
		return fmt.Errorf("no overlay route for destination IP %s: %w", dstIP, err)
	}

	// Rewrite destination MAC to remote VTEP MAC if route specifies it
	if len(route.VtepMAC) == 6 {
		frame.DstMAC = route.VtepMAC
	}

	frameBytes, err := frame.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal inner frame: %w", err)
	}

	vni := route.VNI
	if vni == 0 {
		vni = oe.cfg.DefaultVNI
	}

	vxlanPayload := packet.MarshalVXLAN(vni, frameBytes)

	remotePort := route.Port
	if remotePort <= 0 {
		remotePort = oe.cfg.Port
	}

	remoteAddr := &net.UDPAddr{
		IP:   route.RemoteHostIP,
		Port: remotePort,
	}

	_, err = oe.conn.WriteToUDP(vxlanPayload, remoteAddr)
	return err
}

func (oe *OverlayEngine) readLoop(ctx context.Context) {
	buf := make([]byte, 65535)

	for {
		select {
		case <-ctx.Done():
			return
		case <-oe.closedChan:
			return
		default:
		}

		n, _, err := oe.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-oe.closedChan:
				return
			case <-ctx.Done():
				return
			default:
				continue
			}
		}

		if n < packet.VXLANHeaderLen {
			continue
		}

		vxlanPkt, err := packet.ParseVXLAN(buf[:n])
		if err != nil {
			continue
		}

		if oe.injector != nil && vxlanPkt.InnerFrame != nil {
			_ = oe.injector.InjectFrame(vxlanPkt.InnerFrame)
		}
	}
}

// Close closes the UDP socket and terminates background readers.
func (oe *OverlayEngine) Close() error {
	var err error
	oe.closeOnce.Do(func() {
		close(oe.closedChan)
		if oe.conn != nil {
			err = oe.conn.Close()
		}
	})
	return err
}
