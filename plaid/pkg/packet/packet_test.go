package packet

import (
	"bytes"
	"net"
	"testing"
)

func TestEthernetFrame(t *testing.T) {
	srcMAC, _ := net.ParseMAC("02:42:0a:f4:01:02")
	dstMAC, _ := net.ParseMAC("02:42:0a:f4:01:03")
	payload := []byte("hello network")

	frame := &EthernetFrame{
		DstMAC:    dstMAC,
		SrcMAC:    srcMAC,
		EtherType: EtherTypeIPv4,
		Payload:   payload,
	}

	marshaled, err := frame.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	if len(marshaled) != EthernetHeaderLen+len(payload) {
		t.Errorf("Expected len %d, got %d", EthernetHeaderLen+len(payload), len(marshaled))
	}

	parsed, err := ParseEthernet(marshaled)
	if err != nil {
		t.Fatalf("ParseEthernet failed: %v", err)
	}

	if !bytes.Equal(parsed.SrcMAC, srcMAC) {
		t.Errorf("Expected src %s, got %s", srcMAC, parsed.SrcMAC)
	}
	if !bytes.Equal(parsed.DstMAC, dstMAC) {
		t.Errorf("Expected dst %s, got %s", dstMAC, parsed.DstMAC)
	}
	if parsed.EtherType != EtherTypeIPv4 {
		t.Errorf("Expected EtherType 0x%04x, got 0x%04x", EtherTypeIPv4, parsed.EtherType)
	}
	if !bytes.Equal(parsed.Payload, payload) {
		t.Errorf("Payload mismatch: %s vs %s", parsed.Payload, payload)
	}

	bcastMAC, _ := net.ParseMAC("ff:ff:ff:ff:ff:ff")
	bcastFrame := &EthernetFrame{DstMAC: bcastMAC}
	if !bcastFrame.IsBroadcast() {
		t.Errorf("Expected IsBroadcast to be true")
	}

	mcastMAC, _ := net.ParseMAC("01:00:5e:00:00:01")
	mcastFrame := &EthernetFrame{DstMAC: mcastMAC}
	if !mcastFrame.IsMulticast() {
		t.Errorf("Expected IsMulticast to be true")
	}
}

func TestARPPacket(t *testing.T) {
	senderMAC, _ := net.ParseMAC("02:42:0a:f4:01:02")
	targetMAC, _ := net.ParseMAC("00:00:00:00:00:00")
	senderIP := net.ParseIP("10.244.1.2")
	targetIP := net.ParseIP("10.244.1.1")

	req := &ARPPacket{
		Operation: ARPOperationRequest,
		SenderMAC: senderMAC,
		SenderIP:  senderIP,
		TargetMAC: targetMAC,
		TargetIP:  targetIP,
	}

	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal ARP failed: %v", err)
	}

	parsed, err := ParseARP(data)
	if err != nil {
		t.Fatalf("ParseARP failed: %v", err)
	}

	if parsed.Operation != ARPOperationRequest {
		t.Errorf("Expected operation Request, got %d", parsed.Operation)
	}
	if !bytes.Equal(parsed.SenderMAC, senderMAC) {
		t.Errorf("SenderMAC mismatch")
	}
	if !parsed.SenderIP.Equal(senderIP) {
		t.Errorf("SenderIP mismatch")
	}
	if !parsed.TargetIP.Equal(targetIP) {
		t.Errorf("TargetIP mismatch")
	}

	gwMAC, _ := net.ParseMAC("02:42:0a:f4:01:01")
	replyFrame, err := NewARPReply(parsed, gwMAC, targetIP)
	if err != nil {
		t.Fatalf("NewARPReply failed: %v", err)
	}

	if !bytes.Equal(replyFrame.DstMAC, senderMAC) {
		t.Errorf("Reply DstMAC mismatch")
	}
	if !bytes.Equal(replyFrame.SrcMAC, gwMAC) {
		t.Errorf("Reply SrcMAC mismatch")
	}

	replyARP, err := ParseARP(replyFrame.Payload)
	if err != nil {
		t.Fatalf("ParseARP on reply payload failed: %v", err)
	}
	if replyARP.Operation != ARPOperationReply {
		t.Errorf("Expected reply op, got %d", replyARP.Operation)
	}
	if !bytes.Equal(replyARP.SenderMAC, gwMAC) {
		t.Errorf("Reply SenderMAC mismatch")
	}
	if !replyARP.SenderIP.Equal(targetIP) {
		t.Errorf("Reply SenderIP mismatch")
	}
}

func TestIPv4Packet(t *testing.T) {
	// Build minimal IPv4 packet: 20-byte header + 4 byte payload
	raw := []byte{
		0x45, 0x00, 0x00, 0x18, // Ver=4, IHL=5, TOS=0, TotalLen=24
		0x12, 0x34, 0x40, 0x00, // ID=0x1234, Flags=DF, FragOff=0
		0x40, 0x11, 0x00, 0x00, // TTL=64, Proto=UDP(17), Checksum=0
		0x0a, 0xf4, 0x01, 0x02, // Src=10.244.1.2
		0x08, 0x08, 0x08, 0x08, // Dst=8.8.8.8
		0xde, 0xad, 0xbe, 0xef, // Payload
	}

	pkt, err := ParseIPv4(raw)
	if err != nil {
		t.Fatalf("ParseIPv4 failed: %v", err)
	}

	if pkt.Header.Version != 4 || pkt.Header.IHL != 5 {
		t.Errorf("Unexpected version/IHL: %d/%d", pkt.Header.Version, pkt.Header.IHL)
	}
	if !pkt.Header.SrcIP.Equal(net.ParseIP("10.244.1.2")) {
		t.Errorf("SrcIP mismatch: %s", pkt.Header.SrcIP)
	}
	if !pkt.Header.DstIP.Equal(net.ParseIP("8.8.8.8")) {
		t.Errorf("DstIP mismatch: %s", pkt.Header.DstIP)
	}
	if pkt.Header.Protocol != IPProtocolUDP {
		t.Errorf("Protocol mismatch: %d", pkt.Header.Protocol)
	}
	if !bytes.Equal(pkt.Payload, []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Errorf("Payload mismatch: %x", pkt.Payload)
	}
}

func TestICMPEchoReply(t *testing.T) {
	// Construct an ICMP Echo Request IPv4 packet
	icmpPayload := []byte{
		0x08, 0x00, 0x00, 0x00, // Type=8 (Echo Req), Code=0, Checksum=0
		0x12, 0x34, 0x00, 0x01, // Identifier, Sequence Number
		't', 'e', 's', 't', 'd', 'a', 't', 'a', // Payload
	}
	pkt := &IPv4Packet{
		Header: IPv4Header{
			Version:  4,
			IHL:      5,
			Protocol: IPProtocolICMP,
			SrcIP:    net.ParseIP("10.244.1.5"),
			DstIP:    net.ParseIP("10.244.1.1"),
			ID:       100,
		},
		Payload: icmpPayload,
	}

	replyBytes := CreateICMPEchoReply(pkt)
	if replyBytes == nil {
		t.Fatalf("CreateICMPEchoReply returned nil for valid Echo Request")
	}

	replyIP, err := ParseIPv4(replyBytes)
	if err != nil {
		t.Fatalf("Failed to parse IPv4 reply: %v", err)
	}

	if !replyIP.Header.SrcIP.Equal(net.ParseIP("10.244.1.1")) {
		t.Errorf("Expected SrcIP 10.244.1.1, got %s", replyIP.Header.SrcIP)
	}
	if !replyIP.Header.DstIP.Equal(net.ParseIP("10.244.1.5")) {
		t.Errorf("Expected DstIP 10.244.1.5, got %s", replyIP.Header.DstIP)
	}
	if replyIP.Header.Protocol != IPProtocolICMP {
		t.Errorf("Expected protocol ICMP, got %d", replyIP.Header.Protocol)
	}
	if len(replyIP.Payload) < 8 {
		t.Fatalf("ICMP reply payload too short: %d", len(replyIP.Payload))
	}
	if replyIP.Payload[0] != 0 || replyIP.Payload[1] != 0 {
		t.Errorf("Expected ICMP Type=0 Code=0, got Type=%d Code=%d", replyIP.Payload[0], replyIP.Payload[1])
	}
	if !bytes.Equal(replyIP.Payload[8:], []byte("testdata")) {
		t.Errorf("ICMP payload data corrupted")
	}

	// Verify non-ICMP returns nil
	nonICMP := &IPv4Packet{
		Header:  IPv4Header{Protocol: IPProtocolTCP},
		Payload: icmpPayload,
	}
	if CreateICMPEchoReply(nonICMP) != nil {
		t.Errorf("Expected nil for non-ICMP packet")
	}
}

func TestVXLANPacket(t *testing.T) {
	srcMAC, _ := net.ParseMAC("02:42:0a:f4:01:02")
	dstMAC, _ := net.ParseMAC("02:42:0a:f4:02:02")
	inner := &EthernetFrame{
		DstMAC:    dstMAC,
		SrcMAC:    srcMAC,
		EtherType: EtherTypeIPv4,
		Payload:   []byte("encapsulated data"),
	}

	vxlan := &VXLANPacket{
		Header: VXLANHeader{
			Flags: VXLANFlags,
			VNI:   1,
		},
		InnerFrame: inner,
	}

	encoded, err := vxlan.Marshal()
	if err != nil {
		t.Fatalf("VXLAN Marshal failed: %v", err)
	}

	parsed, err := ParseVXLAN(encoded)
	if err != nil {
		t.Fatalf("ParseVXLAN failed: %v", err)
	}

	if parsed.Header.VNI != 1 {
		t.Errorf("Expected VNI 1, got %d", parsed.Header.VNI)
	}
	if !bytes.Equal(parsed.InnerFrame.SrcMAC, srcMAC) {
		t.Errorf("Inner frame SrcMAC mismatch")
	}
	if !bytes.Equal(parsed.InnerFrame.DstMAC, dstMAC) {
		t.Errorf("Inner frame DstMAC mismatch")
	}
	if !bytes.Equal(parsed.InnerFrame.Payload, inner.Payload) {
		t.Errorf("Inner frame payload mismatch")
	}
}
