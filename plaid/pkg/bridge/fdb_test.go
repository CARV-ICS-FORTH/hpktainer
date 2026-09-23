package bridge

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestFDBEvictExpired(t *testing.T) {
	ttl := 50 * time.Millisecond
	fdb := NewFDB(ttl)

	mac1, _ := net.ParseMAC("02:00:00:00:00:01")
	mac2, _ := net.ParseMAC("02:00:00:00:00:02")
	staticMAC, _ := net.ParseMAC("02:00:00:00:00:03")

	ep1 := newMockEndpoint("ep1", "tap1", "10.244.1.2", mac1.String())
	ep2 := newMockEndpoint("ep2", "tap2", "10.244.1.3", mac2.String())
	epStatic := newMockEndpoint("ep3", "tap3", "10.244.1.4", staticMAC.String())

	fdb.Learn(mac1, ep1)
	fdb.SetStatic(staticMAC, epStatic)

	// Immediate lookup succeeds
	if ep := fdb.Lookup(mac1); ep == nil || ep.ID() != "ep1" {
		t.Fatalf("expected to find ep1 immediately")
	}
	if ep := fdb.Lookup(staticMAC); ep == nil || ep.ID() != "ep3" {
		t.Fatalf("expected to find static ep3 immediately")
	}

	time.Sleep(70 * time.Millisecond)

	// Learn mac2 after sleep
	fdb.Learn(mac2, ep2)

	// Evict expired entries: mac1 should be evicted, mac2 and staticMAC should remain
	evicted := fdb.EvictExpired()
	if evicted != 1 {
		t.Errorf("expected 1 entry to be evicted, got %d", evicted)
	}

	if ep := fdb.Lookup(mac1); ep != nil {
		t.Errorf("expected mac1 to be evicted, but lookup returned %v", ep)
	}
	if ep := fdb.Lookup(mac2); ep == nil || ep.ID() != "ep2" {
		t.Errorf("expected mac2 to be found, got %v", ep)
	}
	if ep := fdb.Lookup(staticMAC); ep == nil || ep.ID() != "ep3" {
		t.Errorf("expected staticMAC to remain after TTL expiry")
	}
}

func TestFDBMaxEntriesBound(t *testing.T) {
	fdb := NewFDB(time.Hour)
	fdb.SetMaxEntries(3)

	for i := 1; i <= 5; i++ {
		mac, _ := net.ParseMAC(fmt.Sprintf("02:00:00:00:00:0%d", i))
		ep := newMockEndpoint(fmt.Sprintf("ep%d", i), fmt.Sprintf("tap%d", i), fmt.Sprintf("10.244.1.%d", i+1), mac.String())
		fdb.Learn(mac, ep)
		time.Sleep(2 * time.Millisecond)
	}

	if fdb.Len() > 3 {
		t.Errorf("expected FDB length to be bounded to 3, got %d", fdb.Len())
	}

	// Oldest entries (1 and 2) should have been evicted to respect maxEntries
	mac1, _ := net.ParseMAC("02:00:00:00:00:01")
	if ep := fdb.Lookup(mac1); ep != nil {
		t.Errorf("expected oldest entry mac1 to be evicted")
	}

	mac5, _ := net.ParseMAC("02:00:00:00:00:05")
	if ep := fdb.Lookup(mac5); ep == nil {
		t.Errorf("expected newest entry mac5 to be present")
	}
}
