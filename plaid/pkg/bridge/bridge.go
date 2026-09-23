package bridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"plaid/pkg/packet"
)

var (
	ErrEndpointNotFound = errors.New("endpoint not found")
)

func ipAdd(ip net.IP, n uint32) net.IP {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil
	}
	val := binary.BigEndian.Uint32(ip4) + n
	res := make(net.IP, 4)
	binary.BigEndian.PutUint32(res, val)
	return res
}

// MakeSlirpMAC returns the libslirp emulated host MAC for an IP address: 52:55:IP0:IP1:IP2:IP3
func MakeSlirpMAC(ip net.IP) net.HardwareAddr {
	ip4 := ip.To4()
	if ip4 == nil {
		return net.HardwareAddr{0x52, 0x55, 0x00, 0x00, 0x00, 0x00}
	}
	return net.HardwareAddr{0x52, 0x55, ip4[0], ip4[1], ip4[2], ip4[3]}
}

// Endpoint represents any entity connected to the user-space bridge.
type Endpoint interface {
	ID() string
	Name() string
	IP() net.IP
	MAC() net.HardwareAddr
	Write(frame []byte) error
	Close() error
}

// OverlayHandler is invoked when a packet needs cross-host overlay encapsulation (e.g. VXLAN).
type OverlayHandler func(frame *packet.EthernetFrame, dstIP net.IP) error

// OutboundHandler is invoked when a packet needs outbound NAT (e.g. slirp4netns).
type OutboundHandler func(frame *packet.EthernetFrame) error

// FilterHandler is a hook evaluated on incoming frames (user-space br_netfilter).
// Returning false causes the packet to be dropped.
type FilterHandler func(frame *packet.EthernetFrame, srcEP Endpoint) bool

// BridgeConfig configures the user-space bridge parameters.
type BridgeConfig struct {
	GatewayIP   net.IP
	GatewayMAC  net.HardwareAddr
	NodeCIDR    *net.IPNet
	ClusterCIDR *net.IPNet
	FDBTTL      time.Duration
}

// Bridge is the user-space L2/L3 switch.
type Bridge struct {
	mu          sync.RWMutex
	endpoints   map[string]Endpoint
	ipToEp      map[string]Endpoint
	fdb         *FDB
	gatewayIP   net.IP
	gatewayMAC  net.HardwareAddr
	nodeCIDR    *net.IPNet
	clusterCIDR *net.IPNet

	overlayHandler  OverlayHandler
	outboundHandler OutboundHandler
	filterHandler   FilterHandler
}

// NewBridge creates and initializes a Bridge.
func NewBridge(cfg BridgeConfig) *Bridge {
	fdbTTL := cfg.FDBTTL
	if fdbTTL <= 0 {
		fdbTTL = 5 * time.Minute
	}

	b := &Bridge{
		endpoints:   make(map[string]Endpoint),
		ipToEp:      make(map[string]Endpoint),
		fdb:         NewFDB(fdbTTL),
		gatewayIP:   cfg.GatewayIP,
		gatewayMAC:  cfg.GatewayMAC,
		nodeCIDR:    cfg.NodeCIDR,
		clusterCIDR: cfg.ClusterCIDR,
	}

	return b
}

// SetOverlayHandler sets the handler for cross-host overlay packets.
func (b *Bridge) SetOverlayHandler(h OverlayHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.overlayHandler = h
}

// SetOutboundHandler sets the handler for external/internet egress packets.
func (b *Bridge) SetOutboundHandler(h OutboundHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.outboundHandler = h
}

// SetFilterHandler sets the packet filtering hook.
func (b *Bridge) SetFilterHandler(h FilterHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.filterHandler = h
}

// AddEndpoint registers a new pod or virtual endpoint with the bridge.
func (b *Bridge) AddEndpoint(ep Endpoint) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := ep.ID()
	if old, exists := b.endpoints[id]; exists {
		_ = old.Close()
		delete(b.endpoints, id)
		if old.IP() != nil {
			delete(b.ipToEp, old.IP().String())
		}
		if old.MAC() != nil {
			b.fdb.Delete(old.MAC())
		}
		b.fdb.DeleteByEndpoint(id)
	}

	b.endpoints[id] = ep
	if ep.IP() != nil {
		b.ipToEp[ep.IP().String()] = ep
	}
	if ep.MAC() != nil {
		b.fdb.SetStatic(ep.MAC(), ep)
	}

	return nil
}

// RemoveEndpoint unregisters an endpoint and cleans up its FDB entries.
func (b *Bridge) RemoveEndpoint(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	ep, exists := b.endpoints[id]
	if !exists {
		return ErrEndpointNotFound
	}

	delete(b.endpoints, id)
	if ep.IP() != nil {
		delete(b.ipToEp, ep.IP().String())
	}
	if ep.MAC() != nil {
		b.fdb.Delete(ep.MAC())
	}
	b.fdb.DeleteByEndpoint(id)

	return ep.Close()
}

// GetEndpoint returns an endpoint by its ID.
func (b *Bridge) GetEndpoint(id string) (Endpoint, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ep, ok := b.endpoints[id]
	return ep, ok
}

// GetEndpointByIP returns an endpoint matching an IP address.
func (b *Bridge) GetEndpointByIP(ip net.IP) (Endpoint, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ep, ok := b.ipToEp[ip.String()]
	return ep, ok
}

// Endpoints returns a slice of all currently registered endpoints.
func (b *Bridge) Endpoints() []Endpoint {
	b.mu.RLock()
	defer b.mu.RUnlock()
	eps := make([]Endpoint, 0, len(b.endpoints))
	for _, ep := range b.endpoints {
		eps = append(eps, ep)
	}
	return eps
}

// ProcessFrame receives a raw Ethernet frame from an endpoint and forwards it.
func (b *Bridge) ProcessFrame(srcEP Endpoint, raw []byte) error {
	frame, err := packet.ParseEthernet(raw)
	if err != nil {
		return fmt.Errorf("failed to parse ethernet frame: %w", err)
	}

	// Filter hook (user-space br_netfilter)
	b.mu.RLock()
	filter := b.filterHandler
	b.mu.RUnlock()
	if filter != nil && !filter(frame, srcEP) {
		// Dropped by policy
		return nil
	}

	// Dynamic MAC learning
	if srcEP != nil && frame.SrcMAC != nil {
		b.fdb.Learn(frame.SrcMAC, srcEP)
	}

	// Handle Broadcast / Multicast
	if frame.IsBroadcast() || frame.IsMulticast() {
		return b.handleBroadcast(srcEP, frame, raw)
	}

	// Handle Unicast
	return b.handleUnicast(srcEP, frame, raw)
}

func (b *Bridge) handleBroadcast(srcEP Endpoint, frame *packet.EthernetFrame, raw []byte) error {
	// If it's an ARP request, check if it's asking for known local endpoints or bridge/gateway/slirp
	if frame.EtherType == packet.EtherTypeARP {
		arpPkt, err := packet.ParseARP(frame.Payload)
		if err == nil && arpPkt.Operation == packet.ARPOperationRequest {
			// 1. Check if target IP belongs to another known local pod
			b.mu.RLock()
			targetEP, exists := b.ipToEp[arpPkt.TargetIP.String()]
			b.mu.RUnlock()
			if exists && targetEP != nil && targetEP.MAC() != nil {
				// Proxy ARP response for local pod
				replyFrame, err := packet.NewARPReply(arpPkt, targetEP.MAC(), arpPkt.TargetIP)
				if err == nil {
					replyBytes, err := replyFrame.Marshal()
					if err == nil && srcEP != nil {
						return srcEP.Write(replyBytes)
					}
				}
			}

			// 2. Check if asking for Gateway IP, slirp host/DNS alias, or remote overlay pod
			if b.gatewayMAC != nil {
				// Gateway ARP
				if b.gatewayIP != nil && arpPkt.TargetIP.Equal(b.gatewayIP) {
					replyFrame, err := packet.NewARPReply(arpPkt, b.gatewayMAC, b.gatewayIP)
					if err == nil {
						replyBytes, err := replyFrame.Marshal()
						if err == nil && srcEP != nil {
							return srcEP.Write(replyBytes)
						}
					}
				}

				// Slirp Host / DNS ARP (52:55:IP:IP:IP:IP)
				if b.nodeCIDR != nil {
					baseIP := b.nodeCIDR.IP.Mask(b.nodeCIDR.Mask)
					hostIP := ipAdd(baseIP, 2)
					dnsIP := ipAdd(baseIP, 3)
					if (hostIP != nil && arpPkt.TargetIP.Equal(hostIP)) ||
						(dnsIP != nil && arpPkt.TargetIP.Equal(dnsIP)) {
						slirpMAC := MakeSlirpMAC(arpPkt.TargetIP)
						replyFrame, err := packet.NewARPReply(arpPkt, slirpMAC, arpPkt.TargetIP)
						if err == nil {
							replyBytes, err := replyFrame.Marshal()
							if err == nil && srcEP != nil {
								return srcEP.Write(replyBytes)
							}
						}
					}
				}

				// Overlay remote pod ARP
				if b.clusterCIDR != nil && b.clusterCIDR.Contains(arpPkt.TargetIP) &&
					(b.nodeCIDR == nil || !b.nodeCIDR.Contains(arpPkt.TargetIP)) {
					replyFrame, err := packet.NewARPReply(arpPkt, b.gatewayMAC, arpPkt.TargetIP)
					if err == nil {
						replyBytes, err := replyFrame.Marshal()
						if err == nil && srcEP != nil {
							return srcEP.Write(replyBytes)
						}
					}
				}
			}
		}
	}

	// Flood to all other local endpoints
	return b.flood(srcEP, raw)
}

func (b *Bridge) handleUnicast(srcEP Endpoint, frame *packet.EthernetFrame, raw []byte) error {
	// 1. Direct local delivery: Lookup destination MAC in FDB
	targetEP := b.fdb.Lookup(frame.DstMAC)
	if targetEP != nil {
		if srcEP != nil && targetEP.ID() == srcEP.ID() {
			// Hairpin or self-destination
			return nil
		}
		return targetEP.Write(raw)
	}

	// 2. If destination is a slirp emulated MAC (52:55:xx:xx:xx:xx), forward to outbound handler
	if len(frame.DstMAC) == 6 && frame.DstMAC[0] == 0x52 && frame.DstMAC[1] == 0x55 {
		b.mu.RLock()
		outbound := b.outboundHandler
		b.mu.RUnlock()
		if outbound != nil {
			return outbound(frame)
		}
	}

	// 3. If destination is the Gateway MAC, inspect IP header
	if b.gatewayMAC != nil && bytes.Equal(frame.DstMAC, b.gatewayMAC) {
		return b.routeGatewayTraffic(srcEP, frame, raw)
	}

	// 3. If destination MAC is unknown and it's IPv4:
	if frame.EtherType == packet.EtherTypeIPv4 {
		ipPkt, err := packet.ParseIPv4(frame.Payload)
		if err == nil {
			dstIP := ipPkt.Header.DstIP
			// Is it a local pod by IP?
			b.mu.RLock()
			localTarget, ok := b.ipToEp[dstIP.String()]
			b.mu.RUnlock()
			if ok && localTarget != nil {
				return localTarget.Write(raw)
			}

			// Is it an overlay remote node destination?
			if b.clusterCIDR != nil && b.clusterCIDR.Contains(dstIP) &&
				(b.nodeCIDR == nil || !b.nodeCIDR.Contains(dstIP)) {
				b.mu.RLock()
				overlay := b.overlayHandler
				b.mu.RUnlock()
				if overlay != nil {
					return overlay(frame, dstIP)
				}
			}

			// Outbound internet / non-cluster destination
			b.mu.RLock()
			outbound := b.outboundHandler
			b.mu.RUnlock()
			if outbound != nil {
				return outbound(frame)
			}
		}
	}

	// Unknown unicast: flood to all other endpoints
	return b.flood(srcEP, raw)
}

func (b *Bridge) routeGatewayTraffic(srcEP Endpoint, frame *packet.EthernetFrame, raw []byte) error {
	if frame.EtherType != packet.EtherTypeIPv4 {
		return nil
	}

	ipPkt, err := packet.ParseIPv4(frame.Payload)
	if err != nil {
		return err
	}

	dstIP := ipPkt.Header.DstIP

	// 0. If destination is the Gateway IP itself and it's an ICMP Echo Request, reply!
	if b.gatewayIP != nil && dstIP.Equal(b.gatewayIP) && ipPkt.Header.Protocol == packet.IPProtocolICMP {
		replyData := packet.CreateICMPEchoReply(ipPkt)
		if replyData != nil && srcEP != nil {
			replyEther := &packet.EthernetFrame{
				DstMAC:    frame.SrcMAC,
				SrcMAC:    b.gatewayMAC,
				EtherType: packet.EtherTypeIPv4,
				Payload:   replyData,
			}
			if replyRaw, err := replyEther.Marshal(); err == nil {
				return srcEP.Write(replyRaw)
			}
		}
		return nil
	}

	// 1. If destination is another local pod
	b.mu.RLock()
	localTarget, ok := b.ipToEp[dstIP.String()]
	b.mu.RUnlock()
	if ok && localTarget != nil {
		// Rewrite destination MAC to the pod's MAC
		frame.DstMAC = localTarget.MAC()
		newRaw, err := frame.Marshal()
		if err != nil {
			return err
		}
		return localTarget.Write(newRaw)
	}

	// 2. If destination is in the cluster overlay (remote node)
	if b.clusterCIDR != nil && b.clusterCIDR.Contains(dstIP) &&
		(b.nodeCIDR == nil || !b.nodeCIDR.Contains(dstIP)) {
		b.mu.RLock()
		overlay := b.overlayHandler
		b.mu.RUnlock()
		if overlay != nil {
			return overlay(frame, dstIP)
		}
	}

	// 3. External / default route: send to slirp4netns
	b.mu.RLock()
	outbound := b.outboundHandler
	b.mu.RUnlock()
	if outbound != nil {
		return outbound(frame)
	}

	return nil
}

// InjectFrame injects a frame into the bridge (e.g. from VXLAN or slirp4netns return traffic).
func (b *Bridge) InjectFrame(frame *packet.EthernetFrame) error {
	if frame.IsBroadcast() || frame.IsMulticast() {
		raw, err := frame.Marshal()
		if err != nil {
			return err
		}
		return b.flood(nil, raw)
	}

	// Try lookup by destination IP first
	var targetEP Endpoint
	if frame.EtherType == packet.EtherTypeIPv4 {
		ipPkt, err := packet.ParseIPv4(frame.Payload)
		if err == nil {
			b.mu.RLock()
			targetEP = b.ipToEp[ipPkt.Header.DstIP.String()]
			b.mu.RUnlock()
		}
	}

	// If not found by IP, try lookup by destination MAC in FDB
	if targetEP == nil {
		targetEP = b.fdb.Lookup(frame.DstMAC)
	}

	if targetEP != nil {
		frame.DstMAC = targetEP.MAC()

		// If packet is from an on-link slirp host (52:55... and inside nodeCIDR), preserve slirp's SrcMAC.
		// Otherwise (cross-host VXLAN or external Internet via gateway), rewrite SrcMAC to gatewayMAC.
		isSlirpLocal := false
		if frame.EtherType == packet.EtherTypeIPv4 && len(frame.SrcMAC) == 6 &&
			frame.SrcMAC[0] == 0x52 && frame.SrcMAC[1] == 0x55 && b.nodeCIDR != nil {
			ipPkt, err := packet.ParseIPv4(frame.Payload)
			if err == nil && b.nodeCIDR.Contains(ipPkt.Header.SrcIP) {
				isSlirpLocal = true
			}
		}

		if !isSlirpLocal && b.gatewayMAC != nil {
			frame.SrcMAC = b.gatewayMAC
		}

		newRaw, err := frame.Marshal()
		if err != nil {
			return err
		}
		return targetEP.Write(newRaw)
	}

	// Flood if still unknown
	raw, err := frame.Marshal()
	if err != nil {
		return err
	}
	return b.flood(nil, raw)
}

func (b *Bridge) flood(srcEP Endpoint, raw []byte) error {
	b.mu.RLock()
	eps := make([]Endpoint, 0, len(b.endpoints))
	for _, ep := range b.endpoints {
		if srcEP != nil && ep.ID() == srcEP.ID() {
			continue
		}
		eps = append(eps, ep)
	}
	b.mu.RUnlock()

	var firstErr error
	for _, ep := range eps {
		if err := ep.Write(raw); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
