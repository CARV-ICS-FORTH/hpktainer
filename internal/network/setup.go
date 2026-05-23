package network

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

const BridgeName = "hpk-bridge"

// EnsureBridge creates the bridge if it doesn't exist and assigns the gateway IP.
func EnsureBridge(subnetCIDR string) (string, error) {
	// Parse CIDR
	_, ipNet, err := net.ParseCIDR(subnetCIDR)
	if err != nil {
		return "", fmt.Errorf("invalid subnet CIDR: %w", err)
	}

	// Gateway is the first IP (.1)
	// ipNet.IP is the network address (e.g. 10.244.0.0)
	// We increment it to get .1
	gwIP := make(net.IP, len(ipNet.IP))
	copy(gwIP, ipNet.IP)
	inc(gwIP)

	// gwCIDR := fmt.Sprintf("%s/%d", gwIP.String(), 32) // Add address as /32 usually or with mask?
	// Usually bridges act as gateway for the whole subnet, so we should add it with the subnet mask.
	ones, _ := ipNet.Mask.Size()
	gwWithMask := fmt.Sprintf("%s/%d", gwIP.String(), ones)

	// Check if bridge exists
	l, err := netlink.LinkByName(BridgeName)
	var bridge *netlink.Bridge
	if err != nil {
		// Create bridge
		bridge = &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: BridgeName}}
		if err := netlink.LinkAdd(bridge); err != nil {
			return "", fmt.Errorf("failed to create bridge: %w", err)
		}
		l = bridge
	} else {
		var ok bool
		bridge, ok = l.(*netlink.Bridge)
		if !ok {
			return "", fmt.Errorf("%s exists but is not a bridge", BridgeName)
		}
	}

	// Set UP
	if err := netlink.LinkSetUp(l); err != nil {
		return "", fmt.Errorf("failed to set bridge up: %w", err)
	}

	// Check/Add Address
	addrs, err := netlink.AddrList(l, netlink.FAMILY_V4)
	if err != nil {
		return "", fmt.Errorf("failed to list addrs: %w", err)
	}

	found := false
	for _, addr := range addrs {
		if addr.IPNet.String() == gwWithMask {
			found = true
			break
		}
	}

	if !found {
		addr, err := netlink.ParseAddr(gwWithMask)
		if err != nil {
			return "", fmt.Errorf("failed to parse gw addr: %w", err)
		}
		if err := netlink.AddrAdd(l, addr); err != nil {
			return "", fmt.Errorf("failed to add addr to bridge: %w", err)
		}
	}

	// Enable forwarding? (Should be global usually, but good to check)
	// We'll leave global sysctl checks to main CLI for now or assume it's set.

	return gwIP.String(), nil
}



// Helper to increment IP
func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}
