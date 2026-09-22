package bridge

import (
	"bytes"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"plaid/pkg/packet"
)

// mockEndpoint is an in-memory test endpoint.
type mockEndpoint struct {
	mu      sync.Mutex
	id      string
	name    string
	ip      net.IP
	mac     net.HardwareAddr
	packets [][]byte
	closed  bool
}

func newMockEndpoint(id, name string, ipStr, macStr string) *mockEndpoint {
	ip := net.ParseIP(ipStr)
	mac, _ := net.ParseMAC(macStr)
	return &mockEndpoint{
		id:   id,
		name: name,
		ip:   ip,
		mac:  mac,
	}
}

func (m *mockEndpoint) ID() string            { return m.id }
func (m *mockEndpoint) Name() string          { return m.name }
func (m *mockEndpoint) IP() net.IP            { return m.ip }
func (m *mockEndpoint) MAC() net.HardwareAddr { return m.mac }

func (m *mockEndpoint) Write(frame []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := append([]byte(nil), frame...)
	m.packets = append(m.packets, cp)
	return nil
}

func (m *mockEndpoint) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockEndpoint) ReceivedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.packets)
}

func (m *mockEndpoint) LastPacket() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.packets) == 0 {
		return nil
	}
	return m.packets[len(m.packets)-1]
}

func setupTestBridge() (*Bridge, *mockEndpoint, *mockEndpoint) {
	gwIP := net.ParseIP("10.244.1.1")
	gwMAC, _ := net.ParseMAC("02:00:00:00:00:01")
	_, nodeCIDR, _ := net.ParseCIDR("10.244.1.0/24")
	_, clusterCIDR, _ := net.ParseCIDR("10.244.0.0/16")

	b := NewBridge(BridgeConfig{
		GatewayIP:   gwIP,
		GatewayMAC:  gwMAC,
		NodeCIDR:    nodeCIDR,
		ClusterCIDR: clusterCIDR,
		FDBTTL:      time.Minute,
	})

	pod1 := newMockEndpoint("pod1", "tap1", "10.244.1.2", "02:00:00:00:00:02")
	pod2 := newMockEndpoint("pod2", "tap2", "10.244.1.3", "02:00:00:00:00:03")

	_ = b.AddEndpoint(pod1)
	_ = b.AddEndpoint(pod2)

	return b, pod1, pod2
}

func TestBridgeLocalUnicast(t *testing.T) {
	b, pod1, pod2 := setupTestBridge()

	// Pod1 sends frame to Pod2
	frame := &packet.EthernetFrame{
		DstMAC:    pod2.MAC(),
		SrcMAC:    pod1.MAC(),
		EtherType: packet.EtherTypeIPv4,
		Payload:   []byte("test unicast packet"),
	}

	raw, err := frame.Marshal()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	err = b.ProcessFrame(pod1, raw)
	if err != nil {
		t.Fatalf("ProcessFrame error: %v", err)
	}

	if pod2.ReceivedCount() != 1 {
		t.Fatalf("Expected pod2 to receive 1 packet, got %d", pod2.ReceivedCount())
	}
	if pod1.ReceivedCount() != 0 {
		t.Fatalf("Expected pod1 to receive 0 packets, got %d", pod1.ReceivedCount())
	}

	received, _ := packet.ParseEthernet(pod2.LastPacket())
	if !bytes.Equal(received.Payload, frame.Payload) {
		t.Errorf("Payload mismatch: %s vs %s", received.Payload, frame.Payload)
	}
}

func TestBridgeGatewayARP(t *testing.T) {
	b, pod1, _ := setupTestBridge()

	// Pod1 broadcasts ARP request for Gateway IP 10.244.1.1
	bcastMAC, _ := net.ParseMAC("ff:ff:ff:ff:ff:ff")
	arpReq := &packet.ARPPacket{
		Operation: packet.ARPOperationRequest,
		SenderMAC: pod1.MAC(),
		SenderIP:  pod1.IP(),
		TargetMAC: net.HardwareAddr{0, 0, 0, 0, 0, 0},
		TargetIP:  net.ParseIP("10.244.1.1"),
	}
	arpBytes, err := arpReq.Marshal()
	if err != nil {
		t.Fatalf("ARP marshal error: %v", err)
	}

	frame := &packet.EthernetFrame{
		DstMAC:    bcastMAC,
		SrcMAC:    pod1.MAC(),
		EtherType: packet.EtherTypeARP,
		Payload:   arpBytes,
	}
	raw, _ := frame.Marshal()

	err = b.ProcessFrame(pod1, raw)
	if err != nil {
		t.Fatalf("ProcessFrame error: %v", err)
	}

	// Pod1 should have received an ARP Reply from Gateway
	if pod1.ReceivedCount() != 1 {
		t.Fatalf("Expected pod1 to receive ARP reply, got %d", pod1.ReceivedCount())
	}

	replyFrame, err := packet.ParseEthernet(pod1.LastPacket())
	if err != nil {
		t.Fatalf("Failed to parse reply frame: %v", err)
	}

	if !bytes.Equal(replyFrame.DstMAC, pod1.MAC()) {
		t.Errorf("Reply DstMAC mismatch: %s", replyFrame.DstMAC)
	}
	if !bytes.Equal(replyFrame.SrcMAC, b.gatewayMAC) {
		t.Errorf("Reply SrcMAC mismatch: %s", replyFrame.SrcMAC)
	}

	replyARP, err := packet.ParseARP(replyFrame.Payload)
	if err != nil {
		t.Fatalf("Failed to parse ARP payload: %v", err)
	}
	if replyARP.Operation != packet.ARPOperationReply {
		t.Errorf("Expected ARP Reply, got %d", replyARP.Operation)
	}
	if !bytes.Equal(replyARP.SenderMAC, b.gatewayMAC) {
		t.Errorf("SenderMAC should be gateway MAC")
	}
	if !replyARP.SenderIP.Equal(net.ParseIP("10.244.1.1")) {
		t.Errorf("SenderIP should be gateway IP")
	}
}

func TestBridgeProxyARPLocalPod(t *testing.T) {
	b, pod1, pod2 := setupTestBridge()

	// Pod1 broadcasts ARP request for Pod2 (10.244.1.3)
	bcastMAC, _ := net.ParseMAC("ff:ff:ff:ff:ff:ff")
	arpReq := &packet.ARPPacket{
		Operation: packet.ARPOperationRequest,
		SenderMAC: pod1.MAC(),
		SenderIP:  pod1.IP(),
		TargetMAC: net.HardwareAddr{0, 0, 0, 0, 0, 0},
		TargetIP:  pod2.IP(),
	}
	arpBytes, _ := arpReq.Marshal()

	frame := &packet.EthernetFrame{
		DstMAC:    bcastMAC,
		SrcMAC:    pod1.MAC(),
		EtherType: packet.EtherTypeARP,
		Payload:   arpBytes,
	}
	raw, _ := frame.Marshal()

	err := b.ProcessFrame(pod1, raw)
	if err != nil {
		t.Fatalf("ProcessFrame error: %v", err)
	}

	// Pod1 should receive proxy ARP reply with Pod2's MAC
	if pod1.ReceivedCount() != 1 {
		t.Fatalf("Expected pod1 to receive proxy ARP reply, got %d", pod1.ReceivedCount())
	}

	replyFrame, _ := packet.ParseEthernet(pod1.LastPacket())
	replyARP, _ := packet.ParseARP(replyFrame.Payload)
	if !bytes.Equal(replyARP.SenderMAC, pod2.MAC()) {
		t.Errorf("Expected pod2 MAC in proxy ARP reply, got %s", replyARP.SenderMAC)
	}
}

func TestBridgeOverlayRouting(t *testing.T) {
	b, pod1, _ := setupTestBridge()

	var overlayCalled bool
	var overlayDst net.IP

	b.SetOverlayHandler(func(frame *packet.EthernetFrame, dstIP net.IP) error {
		overlayCalled = true
		overlayDst = dstIP
		return nil
	})

	// Create IP packet to remote cluster pod 10.244.2.5 (in 10.244.0.0/16, but outside 10.244.1.0/24)
	ipRaw := []byte{
		0x45, 0x00, 0x00, 0x18,
		0x11, 0x22, 0x40, 0x00,
		0x40, 0x11, 0x00, 0x00,
		0x0a, 0xf4, 0x01, 0x02, // 10.244.1.2
		0x0a, 0xf4, 0x02, 0x05, // 10.244.2.5
		0x01, 0x02, 0x03, 0x04,
	}

	// Sent to Gateway MAC
	frame := &packet.EthernetFrame{
		DstMAC:    b.gatewayMAC,
		SrcMAC:    pod1.MAC(),
		EtherType: packet.EtherTypeIPv4,
		Payload:   ipRaw,
	}
	raw, _ := frame.Marshal()

	err := b.ProcessFrame(pod1, raw)
	if err != nil {
		t.Fatalf("ProcessFrame error: %v", err)
	}

	if !overlayCalled {
		t.Fatalf("Expected overlayHandler to be called")
	}
	if !overlayDst.Equal(net.ParseIP("10.244.2.5")) {
		t.Errorf("Expected overlay destination 10.244.2.5, got %s", overlayDst)
	}
}

func TestBridgeOutboundRouting(t *testing.T) {
	b, pod1, _ := setupTestBridge()

	var outboundCalled bool
	b.SetOutboundHandler(func(frame *packet.EthernetFrame) error {
		outboundCalled = true
		return nil
	})

	// Packet destined for 1.1.1.1 (Internet)
	ipRaw := []byte{
		0x45, 0x00, 0x00, 0x18,
		0x11, 0x22, 0x40, 0x00,
		0x40, 0x11, 0x00, 0x00,
		0x0a, 0xf4, 0x01, 0x02, // 10.244.1.2
		0x01, 0x01, 0x01, 0x01, // 1.1.1.1
		0x01, 0x02, 0x03, 0x04,
	}

	frame := &packet.EthernetFrame{
		DstMAC:    b.gatewayMAC,
		SrcMAC:    pod1.MAC(),
		EtherType: packet.EtherTypeIPv4,
		Payload:   ipRaw,
	}
	raw, _ := frame.Marshal()

	err := b.ProcessFrame(pod1, raw)
	if err != nil {
		t.Fatalf("ProcessFrame error: %v", err)
	}

	if !outboundCalled {
		t.Fatalf("Expected outboundHandler to be called for external traffic")
	}
}

func TestBridgeFilter(t *testing.T) {
	b, pod1, pod2 := setupTestBridge()

	// Drop all frames from pod1
	b.SetFilterHandler(func(frame *packet.EthernetFrame, srcEP Endpoint) bool {
		if srcEP != nil && srcEP.ID() == "pod1" {
			return false // DROP
		}
		return true // ACCEPT
	})

	frame := &packet.EthernetFrame{
		DstMAC:    pod2.MAC(),
		SrcMAC:    pod1.MAC(),
		EtherType: packet.EtherTypeIPv4,
		Payload:   []byte("blocked packet"),
	}
	raw, _ := frame.Marshal()

	_ = b.ProcessFrame(pod1, raw)

	if pod2.ReceivedCount() != 0 {
		t.Fatalf("Expected pod2 to receive 0 packets due to filter, got %d", pod2.ReceivedCount())
	}
}

func TestBridgeInjectFrame(t *testing.T) {
	b, pod1, _ := setupTestBridge()

	// External frame arriving (e.g. from VXLAN decapsulation)
	frame := &packet.EthernetFrame{
		DstMAC:    pod1.MAC(),
		SrcMAC:    net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x99, 0x99},
		EtherType: packet.EtherTypeIPv4,
		Payload:   []byte("injected from overlay"),
	}

	err := b.InjectFrame(frame)
	if err != nil {
		t.Fatalf("InjectFrame error: %v", err)
	}

	if pod1.ReceivedCount() != 1 {
		t.Fatalf("Expected pod1 to receive 1 injected frame, got %d", pod1.ReceivedCount())
	}
}

func TestBridgeGatewayICMPPing(t *testing.T) {
	b, pod1, _ := setupTestBridge()

	// Pod1 sends ICMP Echo Request to Gateway IP (10.244.1.1)
	icmpPayload := []byte{
		0x08, 0x00, 0x00, 0x00, // Type 8 Echo Request
		0xaa, 0xbb, 0x00, 0x01, // ID, Seq
		0x01, 0x02, 0x03, 0x04,
	}

	ipRaw := make([]byte, 20+len(icmpPayload))
	ipRaw[0] = 0x45
	binary.BigEndian.PutUint16(ipRaw[2:4], uint16(len(ipRaw)))
	ipRaw[9] = packet.IPProtocolICMP
	copy(ipRaw[12:16], pod1.IP().To4())
	copy(ipRaw[16:20], net.ParseIP("10.244.1.1").To4())
	copy(ipRaw[20:], icmpPayload)

	frame := &packet.EthernetFrame{
		DstMAC:    b.gatewayMAC,
		SrcMAC:    pod1.MAC(),
		EtherType: packet.EtherTypeIPv4,
		Payload:   ipRaw,
	}
	raw, err := frame.Marshal()
	if err != nil {
		t.Fatalf("Failed to marshal frame: %v", err)
	}

	err = b.ProcessFrame(pod1, raw)
	if err != nil {
		t.Fatalf("ProcessFrame failed: %v", err)
	}

	if pod1.ReceivedCount() != 1 {
		t.Fatalf("Expected pod1 to receive 1 ICMP reply, got %d", pod1.ReceivedCount())
	}

	replyFrame, err := packet.ParseEthernet(pod1.LastPacket())
	if err != nil {
		t.Fatalf("ParseEthernet failed: %v", err)
	}
	if !bytes.Equal(replyFrame.DstMAC, pod1.MAC()) {
		t.Errorf("Reply DstMAC mismatch: %s", replyFrame.DstMAC)
	}
	if !bytes.Equal(replyFrame.SrcMAC, b.gatewayMAC) {
		t.Errorf("Reply SrcMAC mismatch: %s", replyFrame.SrcMAC)
	}

	replyIP, err := packet.ParseIPv4(replyFrame.Payload)
	if err != nil {
		t.Fatalf("ParseIPv4 failed: %v", err)
	}
	if !replyIP.Header.SrcIP.Equal(net.ParseIP("10.244.1.1")) {
		t.Errorf("Expected reply SrcIP 10.244.1.1, got %s", replyIP.Header.SrcIP)
	}
	if !replyIP.Header.DstIP.Equal(pod1.IP()) {
		t.Errorf("Expected reply DstIP %s, got %s", pod1.IP(), replyIP.Header.DstIP)
	}
	if replyIP.Header.Protocol != packet.IPProtocolICMP {
		t.Errorf("Expected protocol ICMP, got %d", replyIP.Header.Protocol)
	}
	if len(replyIP.Payload) < 8 || replyIP.Payload[0] != 0 {
		t.Errorf("Expected ICMP Echo Reply (Type 0), got: %v", replyIP.Payload)
	}
}
