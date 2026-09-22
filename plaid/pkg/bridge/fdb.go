package bridge

import (
	"net"
	"sync"
	"time"
)

// FDBEntry represents an entry in the Forwarding Database.
type FDBEntry struct {
	MAC       net.HardwareAddr
	Endpoint  Endpoint
	UpdatedAt time.Time
	Static    bool
}

// FDB is a concurrent-safe MAC forwarding database.
type FDB struct {
	mu      sync.RWMutex
	entries map[string]*FDBEntry
	ttl     time.Duration
}

// NewFDB creates a new FDB with the given entry expiration TTL (e.g. 5 minutes).
func NewFDB(ttl time.Duration) *FDB {
	return &FDB{
		entries: make(map[string]*FDBEntry),
		ttl:     ttl,
	}
}

// Learn registers or refreshes a MAC address mapping to an endpoint.
func (f *FDB) Learn(mac net.HardwareAddr, ep Endpoint) {
	key := mac.String()
	f.mu.Lock()
	defer f.mu.Unlock()

	if entry, ok := f.entries[key]; ok {
		entry.Endpoint = ep
		entry.UpdatedAt = time.Now()
		return
	}

	f.entries[key] = &FDBEntry{
		MAC:       mac,
		Endpoint:  ep,
		UpdatedAt: time.Now(),
		Static:    false,
	}
}

// SetStatic sets a static (non-aging) MAC mapping.
func (f *FDB) SetStatic(mac net.HardwareAddr, ep Endpoint) {
	key := mac.String()
	f.mu.Lock()
	defer f.mu.Unlock()

	f.entries[key] = &FDBEntry{
		MAC:       mac,
		Endpoint:  ep,
		UpdatedAt: time.Now(),
		Static:    true,
	}
}

// Lookup finds the endpoint associated with a MAC address. Returns nil if not found or expired.
func (f *FDB) Lookup(mac net.HardwareAddr) Endpoint {
	key := mac.String()
	f.mu.RLock()
	defer f.mu.RUnlock()

	entry, ok := f.entries[key]
	if !ok {
		return nil
	}

	if !entry.Static && f.ttl > 0 && time.Since(entry.UpdatedAt) > f.ttl {
		return nil
	}

	return entry.Endpoint
}

// Delete removes a MAC entry from the FDB.
func (f *FDB) Delete(mac net.HardwareAddr) {
	key := mac.String()
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, key)
}

// DeleteByEndpoint removes all entries pointing to a given endpoint ID.
func (f *FDB) DeleteByEndpoint(epID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for k, v := range f.entries {
		if v.Endpoint != nil && v.Endpoint.ID() == epID {
			delete(f.entries, k)
		}
	}
}
