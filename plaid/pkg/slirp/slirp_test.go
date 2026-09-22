package slirp

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"plaid/pkg/packet"
)

type mockInjector struct {
	mu     sync.Mutex
	frames []*packet.EthernetFrame
	notify chan struct{}
}

func newMockInjector() *mockInjector {
	return &mockInjector{
		notify: make(chan struct{}, 10),
	}
}

func (m *mockInjector) InjectFrame(frame *packet.EthernetFrame) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frames = append(m.frames, frame)
	select {
	case m.notify <- struct{}{}:
	default:
	}
	return nil
}

func (m *mockInjector) LastFrame() *packet.EthernetFrame {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.frames) == 0 {
		return nil
	}
	return m.frames[len(m.frames)-1]
}

func TestSlirpManagerConduit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	injector := newMockInjector()
	sm := NewSlirpManager(SlirpConfig{
		CIDR:       "10.244.1.0/24",
		MTU:        1500,
		DisableDNS: true,
	}, injector)

	// Use in-memory pipe to simulate slirp socket
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	sm.AttachConn(ctx, clientConn)

	// 1. Test outbound frame: Plaid -> Slirp
	srcMAC, _ := net.ParseMAC("02:00:00:00:01:02")
	dstMAC, _ := net.ParseMAC("02:00:00:00:00:01")
	outFrame := &packet.EthernetFrame{
		DstMAC:    dstMAC,
		SrcMAC:    srcMAC,
		EtherType: packet.EtherTypeIPv4,
		Payload:   []byte("outbound http request data"),
	}

	go func() {
		_ = sm.WriteFrame(outFrame)
	}()

	buf := make([]byte, 1024)
	n, err := serverConn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read on serverConn: %v", err)
	}

	receivedAtSlirp, err := packet.ParseEthernet(buf[:n])
	if err != nil {
		t.Fatalf("Failed to parse ethernet on serverConn: %v", err)
	}
	if !bytes.Equal(receivedAtSlirp.Payload, outFrame.Payload) {
		t.Errorf("Payload mismatch at slirp: %s vs %s", receivedAtSlirp.Payload, outFrame.Payload)
	}

	// 2. Test inbound return frame: Slirp -> Plaid
	retPayload := []byte("inbound http response data")
	inFrame := &packet.EthernetFrame{
		DstMAC:    srcMAC,
		SrcMAC:    dstMAC,
		EtherType: packet.EtherTypeIPv4,
		Payload:   retPayload,
	}
	inBytes, err := inFrame.Marshal()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	_, err = serverConn.Write(inBytes)
	if err != nil {
		t.Fatalf("serverConn write error: %v", err)
	}

	select {
	case <-injector.notify:
	case <-time.After(2 * time.Second):
		t.Fatalf("Timed out waiting for injector to receive frame")
	}

	injected := injector.LastFrame()
	if injected == nil {
		t.Fatalf("Injector received nil frame")
	}
	if !bytes.Equal(injected.Payload, retPayload) {
		t.Errorf("Injected payload mismatch: %s vs %s", injected.Payload, retPayload)
	}
	if !bytes.Equal(injected.DstMAC, srcMAC) {
		t.Errorf("Injected DstMAC mismatch")
	}
}
