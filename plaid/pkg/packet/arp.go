package packet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
)

const (
	ARPOperationRequest = 1
	ARPOperationReply   = 2

	ARPHardwareEthernet = 1
	ARPProtocolIPv4     = 0x0800

	ARPHeaderLen = 28
)

var (
	ErrARPPacketTooShort = errors.New("arp packet too short")
	ErrARPNotIPv4Ether   = errors.New("unsupported ARP hardware/protocol format")
)

// ARPPacket represents an IPv4-over-Ethernet ARP message.
type ARPPacket struct {
	Operation uint16
	SenderMAC net.HardwareAddr
	SenderIP  net.IP
	TargetMAC net.HardwareAddr
	TargetIP  net.IP
}

// ParseARP parses the payload of an Ethernet frame as an ARP packet.
func ParseARP(data []byte) (*ARPPacket, error) {
	if len(data) < ARPHeaderLen {
		return nil, ErrARPPacketTooShort
	}

	hwType := binary.BigEndian.Uint16(data[0:2])
	protoType := binary.BigEndian.Uint16(data[2:4])
	hwLen := data[4]
	protoLen := data[5]

	if hwType != ARPHardwareEthernet || protoType != ARPProtocolIPv4 || hwLen != 6 || protoLen != 4 {
		return nil, ErrARPNotIPv4Ether
	}

	return &ARPPacket{
		Operation: binary.BigEndian.Uint16(data[6:8]),
		SenderMAC: net.HardwareAddr(append([]byte(nil), data[8:14]...)),
		SenderIP:  net.IP(append([]byte(nil), data[14:18]...)),
		TargetMAC: net.HardwareAddr(append([]byte(nil), data[18:24]...)),
		TargetIP:  net.IP(append([]byte(nil), data[24:28]...)),
	}, nil
}

// Marshal serializes an ARPPacket into 28 raw bytes.
func (p *ARPPacket) Marshal() ([]byte, error) {
	if len(p.SenderMAC) != 6 || len(p.TargetMAC) != 6 {
		return nil, ErrInvalidMAC
	}
	sIP := p.SenderIP.To4()
	tIP := p.TargetIP.To4()
	if sIP == nil || tIP == nil {
		return nil, errors.New("invalid IPv4 address")
	}

	buf := make([]byte, ARPHeaderLen)
	binary.BigEndian.PutUint16(buf[0:2], ARPHardwareEthernet)
	binary.BigEndian.PutUint16(buf[2:4], ARPProtocolIPv4)
	buf[4] = 6
	buf[5] = 4
	binary.BigEndian.PutUint16(buf[6:8], p.Operation)
	copy(buf[8:14], p.SenderMAC)
	copy(buf[14:18], sIP)
	copy(buf[18:24], p.TargetMAC)
	copy(buf[24:28], tIP)

	return buf, nil
}

// NewARPReply creates an ARP Reply Ethernet frame answering an ARP Request.
func NewARPReply(req *ARPPacket, myMAC net.HardwareAddr, myIP net.IP) (*EthernetFrame, error) {
	arpReply := &ARPPacket{
		Operation: ARPOperationReply,
		SenderMAC: myMAC,
		SenderIP:  myIP,
		TargetMAC: req.SenderMAC,
		TargetIP:  req.SenderIP,
	}

	payload, err := arpReply.Marshal()
	if err != nil {
		return nil, err
	}

	frame := &EthernetFrame{
		DstMAC:    req.SenderMAC,
		SrcMAC:    myMAC,
		EtherType: EtherTypeARP,
		Payload:   payload,
	}

	return frame, nil
}

func (p *ARPPacket) String() string {
	op := "Unknown"
	switch p.Operation {
	case ARPOperationRequest:
		op = "Request"
	case ARPOperationReply:
		op = "Reply"
	}
	return fmt.Sprintf("ARP(op=%s, sender=%s/%s, target=%s/%s)",
		op, p.SenderMAC, p.SenderIP, p.TargetMAC, p.TargetIP)
}
