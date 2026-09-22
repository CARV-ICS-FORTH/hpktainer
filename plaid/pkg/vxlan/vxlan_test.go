package vxlan

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

func TestRouteTable(t *testing.T) {
	rt := NewRouteTable()

	_, sn1, _ := net.ParseCIDR("10.244.2.0/24")
	_, sn2, _ := net.ParseCIDR("10.244.3.0/24")

	vtepMAC, _ := net.ParseMAC("02:00:00:00:02:01")

	rt.AddRoute(&RouteEntry{
		Subnet:       sn1,
		RemoteHostIP: net.ParseIP("192.168.1.10"),
		VtepMAC:      vtepMAC,
		VNI:          1,
		Port:         8472,
	})

	rt.AddRoute(&RouteEntry{
		Subnet:       sn2,
		RemoteHostIP: net.ParseIP("192.168.1.11"),
		VNI:          1,
	})

	r, err := rt.Lookup(net.ParseIP("10.244.2.45"))
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	if !r.RemoteHostIP.Equal(net.ParseIP("192.168.1.10")) {
		t.Errorf("RemoteHostIP mismatch: %s", r.RemoteHostIP)
	}

	r2, err := rt.Lookup(net.ParseIP("10.244.3.100"))
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	if !r2.RemoteHostIP.Equal(net.ParseIP("192.168.1.11")) {
		t.Errorf("RemoteHostIP mismatch")
	}

	_, err = rt.Lookup(net.ParseIP("10.244.99.1"))
	if err == nil {
		t.Errorf("Expected route lookup error for unmapped IP")
	}

	deleted := rt.RemoveRoute("10.244.2.0/24")
	if !deleted {
		t.Errorf("Expected route to be deleted")
	}
	_, err = rt.Lookup(net.ParseIP("10.244.2.45"))
	if err == nil {
		t.Errorf("Expected lookup error after deletion")
	}
}

func TestOverlayEngineEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Node A setup (port 0 selects random free port)
	routesA := NewRouteTable()
	injA := newMockInjector()
	engineA := NewOverlayEngine(OverlayConfig{BindAddress: "127.0.0.1", Port: 0, DefaultVNI: 1}, routesA, injA)
	if err := engineA.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine A: %v", err)
	}
	defer engineA.Close()

	// Node B setup (port 0 selects random free port)
	routesB := NewRouteTable()
	injB := newMockInjector()
	engineB := NewOverlayEngine(OverlayConfig{BindAddress: "127.0.0.1", Port: 0, DefaultVNI: 1}, routesB, injB)
	if err := engineB.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine B: %v", err)
	}
	defer engineB.Close()

	portA := engineA.LocalPort()
	portB := engineB.LocalPort()

	// Configure route from Node A to Node B's subnet 10.244.2.0/24
	_, snB, _ := net.ParseCIDR("10.244.2.0/24")
	vtepMAC_B, _ := net.ParseMAC("02:00:00:00:00:02")
	routesA.AddRoute(&RouteEntry{
		Subnet:       snB,
		RemoteHostIP: net.ParseIP("127.0.0.1"),
		VtepMAC:      vtepMAC_B,
		VNI:          1,
		Port:         portB,
	})

	_ = portA

	// Construct inner Ethernet frame on Node A
	srcMAC, _ := net.ParseMAC("02:00:00:00:01:05")
	dstMAC, _ := net.ParseMAC("02:00:00:00:02:10")
	innerFrame := &packet.EthernetFrame{
		DstMAC:    dstMAC,
		SrcMAC:    srcMAC,
		EtherType: packet.EtherTypeIPv4,
		Payload:   []byte("cross-node overlay test payload"),
	}

	// Send overlay frame from A to an IP in Node B's subnet
	targetIP := net.ParseIP("10.244.2.10")
	if err := engineA.Send(innerFrame, targetIP); err != nil {
		t.Fatalf("engineA.Send failed: %v", err)
	}

	// Wait for Node B to receive and decapsulate
	select {
	case <-injB.notify:
	case <-time.After(2 * time.Second):
		t.Fatalf("Timed out waiting for Node B to receive VXLAN packet")
	}

	receivedFrame := injB.LastFrame()
	if receivedFrame == nil {
		t.Fatalf("Node B received nil frame")
	}

	if !bytes.Equal(receivedFrame.SrcMAC, srcMAC) {
		t.Errorf("SrcMAC mismatch: %s vs %s", receivedFrame.SrcMAC, srcMAC)
	}
	if !bytes.Equal(receivedFrame.Payload, innerFrame.Payload) {
		t.Errorf("Payload mismatch: %s vs %s", receivedFrame.Payload, innerFrame.Payload)
	}
}
