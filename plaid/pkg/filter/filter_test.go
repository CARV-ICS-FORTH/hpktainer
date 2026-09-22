package filter

import (
	"net"
	"testing"

	"plaid/pkg/packet"
)

func makeTestIPv4Frame(srcIP, dstIP string, proto uint8, srcPort, dstPort uint16) *packet.EthernetFrame {
	sIP := net.ParseIP(srcIP).To4()
	dIP := net.ParseIP(dstIP).To4()

	// 20 byte IP header + 4 bytes TCP/UDP header
	ipData := make([]byte, 24)
	ipData[0] = 0x45 // Version=4, IHL=5
	ipData[2] = 0x00
	ipData[3] = 24 // TotalLen=24
	ipData[8] = 64 // TTL=64
	ipData[9] = proto
	copy(ipData[12:16], sIP)
	copy(ipData[16:20], dIP)

	// Ports
	ipData[20] = byte(srcPort >> 8)
	ipData[21] = byte(srcPort)
	ipData[22] = byte(dstPort >> 8)
	ipData[23] = byte(dstPort)

	return &packet.EthernetFrame{
		SrcMAC:    net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01},
		DstMAC:    net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02},
		EtherType: packet.EtherTypeIPv4,
		Payload:   ipData,
	}
}

func TestFilterEngine(t *testing.T) {
	engine := NewEngine(ActionAccept)

	f1 := makeTestIPv4Frame("10.244.1.5", "10.244.1.10", packet.IPProtocolTCP, 1234, 80)
	f2 := makeTestIPv4Frame("10.244.1.5", "10.244.1.10", packet.IPProtocolTCP, 1234, 8080)
	f3 := makeTestIPv4Frame("10.244.1.5", "10.244.1.10", packet.IPProtocolUDP, 1234, 53)

	// 1. Without rules, all accepted
	if !engine.Filter(f1, nil) || !engine.Filter(f2, nil) || !engine.Filter(f3, nil) {
		t.Errorf("Expected all frames accepted by default")
	}

	// 2. Add rule: Drop TCP port 8080
	engine.AddRule(&Rule{
		ID:       "block-8080",
		Protocol: packet.IPProtocolTCP,
		DstPort:  8080,
		Action:   ActionDrop,
	})

	if !engine.Filter(f1, nil) {
		t.Errorf("Port 80 should be accepted")
	}
	if engine.Filter(f2, nil) {
		t.Errorf("Port 8080 should be dropped")
	}

	// 3. Add rule: Drop all UDP
	engine.AddRule(&Rule{
		ID:       "block-udp",
		Protocol: packet.IPProtocolUDP,
		Action:   ActionDrop,
	})

	if engine.Filter(f3, nil) {
		t.Errorf("UDP should be dropped")
	}

	// 4. Remove rule
	engine.RemoveRule("block-8080")
	if !engine.Filter(f2, nil) {
		t.Errorf("Port 8080 should be accepted after rule removal")
	}

	// 5. Test CIDR rule
	_, blockSubnet, _ := net.ParseCIDR("10.244.99.0/24")
	engine.AddRule(&Rule{
		ID:      "block-subnet",
		DstCIDR: blockSubnet,
		Action:  ActionDrop,
	})

	fBlocked := makeTestIPv4Frame("10.244.1.5", "10.244.99.10", packet.IPProtocolTCP, 100, 80)
	if engine.Filter(fBlocked, nil) {
		t.Errorf("Traffic to 10.244.99.10 should be dropped")
	}
}
