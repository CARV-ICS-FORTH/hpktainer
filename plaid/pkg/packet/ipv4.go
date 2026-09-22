package packet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
)

const (
	IPv4HeaderMinLen = 20

	IPProtocolICMP = 1
	IPProtocolTCP  = 6
	IPProtocolUDP  = 17
)

var (
	ErrIPv4PacketTooShort = errors.New("ipv4 packet too short")
	ErrInvalidIPv4Version = errors.New("invalid IPv4 version")
)

// IPv4Header contains parsed IPv4 header fields.
type IPv4Header struct {
	Version  uint8
	IHL      uint8
	TOS      uint8
	TotalLen uint16
	ID       uint16
	Flags    uint8
	FragOff  uint16
	TTL      uint8
	Protocol uint8
	Checksum uint16
	SrcIP    net.IP
	DstIP    net.IP
	Options  []byte
}

// IPv4Packet represents an IPv4 packet with header and payload.
type IPv4Packet struct {
	Header  IPv4Header
	Payload []byte
}

// ParseIPv4 parses the payload of an Ethernet frame as an IPv4 packet.
func ParseIPv4(data []byte) (*IPv4Packet, error) {
	if len(data) < IPv4HeaderMinLen {
		return nil, ErrIPv4PacketTooShort
	}

	verIHL := data[0]
	version := verIHL >> 4
	if version != 4 {
		return nil, ErrInvalidIPv4Version
	}
	ihl := verIHL & 0x0f
	headerLen := int(ihl) * 4
	if headerLen < IPv4HeaderMinLen || len(data) < headerLen {
		return nil, ErrIPv4PacketTooShort
	}

	totalLen := binary.BigEndian.Uint16(data[2:4])
	if int(totalLen) > len(data) || totalLen < uint16(headerLen) {
		// Truncated, bad length field, or zero length; cap/fallback to actual data length
		totalLen = uint16(len(data))
	}

	flagsFrag := binary.BigEndian.Uint16(data[6:8])

	hdr := IPv4Header{
		Version:  version,
		IHL:      ihl,
		TOS:      data[1],
		TotalLen: totalLen,
		ID:       binary.BigEndian.Uint16(data[4:6]),
		Flags:    uint8(flagsFrag >> 13),
		FragOff:  flagsFrag & 0x1fff,
		TTL:      data[8],
		Protocol: data[9],
		Checksum: binary.BigEndian.Uint16(data[10:12]),
		SrcIP:    net.IP(append([]byte(nil), data[12:16]...)),
		DstIP:    net.IP(append([]byte(nil), data[16:20]...)),
	}

	if headerLen > IPv4HeaderMinLen {
		hdr.Options = append([]byte(nil), data[IPv4HeaderMinLen:headerLen]...)
	}

	payload := data[headerLen:totalLen]

	return &IPv4Packet{
		Header:  hdr,
		Payload: payload,
	}, nil
}

func (p *IPv4Packet) String() string {
	return fmt.Sprintf("IPv4(src=%s, dst=%s, proto=%d, len=%d)",
		p.Header.SrcIP, p.Header.DstIP, p.Header.Protocol, p.Header.TotalLen)
}

// CreateICMPEchoReply creates a raw IPv4 packet containing an ICMP Echo Reply in response to an Echo Request.
func CreateICMPEchoReply(ipPkt *IPv4Packet) []byte {
	if ipPkt.Header.Protocol != IPProtocolICMP || len(ipPkt.Payload) < 8 {
		return nil
	}
	if ipPkt.Payload[0] != 8 { // Type 8 = Echo Request
		return nil
	}

	icmpLen := len(ipPkt.Payload)
	ipTotalLen := IPv4HeaderMinLen + icmpLen
	out := make([]byte, ipTotalLen)

	// IPv4 Header (20 bytes)
	out[0] = 0x45 // Version 4, IHL 5
	out[1] = 0    // TOS
	binary.BigEndian.PutUint16(out[2:4], uint16(ipTotalLen))
	binary.BigEndian.PutUint16(out[4:6], ipPkt.Header.ID+1)
	binary.BigEndian.PutUint16(out[6:8], 0) // Flags / Frag
	out[8] = 64                             // TTL
	out[9] = IPProtocolICMP                 // Protocol 1
	// Checksum at [10:12] initialized to 0
	copy(out[12:16], ipPkt.Header.DstIP.To4()) // SrcIP = Gateway
	copy(out[16:20], ipPkt.Header.SrcIP.To4()) // DstIP = Pod

	// Compute IPv4 header checksum
	var sum uint32
	for i := 0; i < IPv4HeaderMinLen; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(out[i : i+2]))
	}
	for sum > 0xffff {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	binary.BigEndian.PutUint16(out[10:12], ^uint16(sum))

	// ICMP Payload
	copy(out[IPv4HeaderMinLen:], ipPkt.Payload)
	icmpData := out[IPv4HeaderMinLen:]
	icmpData[0] = 0 // Type 0 = Echo Reply
	icmpData[1] = 0 // Code 0
	icmpData[2] = 0 // Checksum reset
	icmpData[3] = 0

	// Compute ICMP checksum
	var icmpSum uint32
	for i := 0; i < len(icmpData)-1; i += 2 {
		icmpSum += uint32(binary.BigEndian.Uint16(icmpData[i : i+2]))
	}
	if len(icmpData)%2 != 0 {
		icmpSum += uint32(icmpData[len(icmpData)-1]) << 8
	}
	for icmpSum > 0xffff {
		icmpSum = (icmpSum & 0xffff) + (icmpSum >> 16)
	}
	binary.BigEndian.PutUint16(icmpData[2:4], ^uint16(icmpSum))

	return out
}
