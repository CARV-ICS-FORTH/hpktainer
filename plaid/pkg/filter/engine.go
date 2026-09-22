package filter

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"

	"plaid/pkg/bridge"
	"plaid/pkg/packet"
)

type Action string

const (
	ActionAccept Action = "ACCEPT"
	ActionDrop   Action = "DROP"
)

// Rule defines an L3/L4 filtering rule (user-space br_netfilter).
type Rule struct {
	ID       string     `json:"id"`
	SrcCIDR  *net.IPNet `json:"src_cidr,omitempty"`
	DstCIDR  *net.IPNet `json:"dst_cidr,omitempty"`
	Protocol uint8      `json:"protocol,omitempty"` // 6 = TCP, 17 = UDP, 1 = ICMP, 0 = ANY
	SrcPort  uint16     `json:"src_port,omitempty"` // 0 = ANY
	DstPort  uint16     `json:"dst_port,omitempty"` // 0 = ANY
	Action   Action     `json:"action"`             // ACCEPT or DROP
}

// Engine evaluates filtering rules on incoming Ethernet frames.
type Engine struct {
	mu            sync.RWMutex
	rules         []*Rule
	defaultAction Action
}

// NewEngine creates a new Rule Engine with the specified default action (usually ACCEPT).
func NewEngine(defaultAction Action) *Engine {
	if defaultAction == "" {
		defaultAction = ActionAccept
	}
	return &Engine{
		rules:         make([]*Rule, 0),
		defaultAction: defaultAction,
	}
}

// AddRule appends a rule to the engine.
func (e *Engine) AddRule(r *Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for i, existing := range e.rules {
		if existing.ID == r.ID {
			e.rules[i] = r
			return
		}
	}
	e.rules = append(e.rules, r)
}

// RemoveRule deletes a rule by its ID.
func (e *Engine) RemoveRule(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	for i, r := range e.rules {
		if r.ID == id {
			e.rules = append(e.rules[:i], e.rules[i+1:]...)
			return true
		}
	}
	return false
}

// ClearRules removes all active rules.
func (e *Engine) ClearRules() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = make([]*Rule, 0)
}

// Rules returns a snapshot of current rules.
func (e *Engine) Rules() []*Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()

	res := make([]*Rule, len(e.rules))
	copy(res, e.rules)
	return res
}

// Filter matches the frame against the rules and returns true for ACCEPT, false for DROP.
func (e *Engine) Filter(frame *packet.EthernetFrame, srcEP bridge.Endpoint) bool {
	if frame.EtherType != packet.EtherTypeIPv4 {
		// Non-IPv4 (e.g. ARP) is accepted by default
		return true
	}

	ipPkt, err := packet.ParseIPv4(frame.Payload)
	if err != nil {
		return false
	}

	srcIP := ipPkt.Header.SrcIP
	dstIP := ipPkt.Header.DstIP
	proto := ipPkt.Header.Protocol

	var srcPort, dstPort uint16
	if (proto == packet.IPProtocolTCP || proto == packet.IPProtocolUDP) && len(ipPkt.Payload) >= 4 {
		srcPort = binary.BigEndian.Uint16(ipPkt.Payload[0:2])
		dstPort = binary.BigEndian.Uint16(ipPkt.Payload[2:4])
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, r := range e.rules {
		if e.matches(r, srcIP, dstIP, proto, srcPort, dstPort) {
			return r.Action == ActionAccept
		}
	}

	return e.defaultAction == ActionAccept
}

func (e *Engine) matches(r *Rule, srcIP, dstIP net.IP, proto uint8, srcPort, dstPort uint16) bool {
	if r.SrcCIDR != nil && !r.SrcCIDR.Contains(srcIP) {
		return false
	}
	if r.DstCIDR != nil && !r.DstCIDR.Contains(dstIP) {
		return false
	}
	if r.Protocol != 0 && r.Protocol != proto {
		return false
	}
	if r.SrcPort != 0 && r.SrcPort != srcPort {
		return false
	}
	if r.DstPort != 0 && r.DstPort != dstPort {
		return false
	}
	return true
}

func (r *Rule) String() string {
	return fmt.Sprintf("Rule(%s: src=%v, dst=%v, proto=%d, srcPort=%d, dstPort=%d -> %s)",
		r.ID, r.SrcCIDR, r.DstCIDR, r.Protocol, r.SrcPort, r.DstPort, r.Action)
}
