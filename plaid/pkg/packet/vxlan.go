package packet

import (
	"errors"
	"fmt"
)

const (
	VXLANHeaderLen   = 8
	VXLANFlags       = 0x08 // 'I' flag indicating valid VNI
	DefaultVXLANPort = 8472
	DefaultVNI       = 1
)

var (
	ErrVXLANPacketTooShort = errors.New("vxlan packet too short")
	ErrInvalidVXLANFlags   = errors.New("invalid vxlan flags (missing I-flag)")
)

// VXLANHeader represents the 8-byte VXLAN encapsulation header.
type VXLANHeader struct {
	Flags uint8
	VNI   uint32 // 24-bit VNI
}

// VXLANPacket represents an inner Ethernet frame encapsulated with a VXLAN header.
type VXLANPacket struct {
	Header     VXLANHeader
	InnerFrame *EthernetFrame
}

// ParseVXLAN parses the UDP payload as a VXLAN packet containing an inner Ethernet frame.
func ParseVXLAN(data []byte) (*VXLANPacket, error) {
	if len(data) < VXLANHeaderLen+EthernetHeaderLen {
		return nil, ErrVXLANPacketTooShort
	}

	flags := data[0]
	if (flags & VXLANFlags) == 0 {
		return nil, ErrInvalidVXLANFlags
	}

	// 24-bit VNI in bytes 4, 5, 6
	vni := uint32(data[4])<<16 | uint32(data[5])<<8 | uint32(data[6])

	innerFrame, err := ParseEthernet(data[VXLANHeaderLen:])
	if err != nil {
		return nil, fmt.Errorf("failed to parse inner frame: %w", err)
	}

	return &VXLANPacket{
		Header: VXLANHeader{
			Flags: flags,
			VNI:   vni,
		},
		InnerFrame: innerFrame,
	}, nil
}

// MarshalVXLAN encapsulates an inner Ethernet frame with a VXLAN header.
func MarshalVXLAN(vni uint32, innerFrameBytes []byte) []byte {
	buf := make([]byte, VXLANHeaderLen+len(innerFrameBytes))
	buf[0] = VXLANFlags
	// bytes 1, 2, 3 reserved (0)
	buf[4] = byte(vni >> 16)
	buf[5] = byte(vni >> 8)
	buf[6] = byte(vni)
	// byte 7 reserved (0)
	copy(buf[VXLANHeaderLen:], innerFrameBytes)
	return buf
}

// Marshal encodes a VXLANPacket into raw bytes.
func (p *VXLANPacket) Marshal() ([]byte, error) {
	innerBytes, err := p.InnerFrame.Marshal()
	if err != nil {
		return nil, err
	}
	return MarshalVXLAN(p.Header.VNI, innerBytes), nil
}

func (p *VXLANPacket) String() string {
	return fmt.Sprintf("VXLAN(vni=%d, inner=%s)", p.Header.VNI, p.InnerFrame)
}
