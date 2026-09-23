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

const DefaultMaxFDBEntries = 1024

// FDB is a concurrent-safe MAC forwarding database.
type FDB struct {
	mu         sync.RWMutex
	entries    map[string]*FDBEntry
	ttl        time.Duration
	maxEntries int
}

// NewFDB creates a new FDB with the given entry expiration TTL (e.g. 5 minutes).
func NewFDB(ttl time.Duration) *FDB {
	return &FDB{
		entries:    make(map[string]*FDBEntry),
		ttl:        ttl,
		maxEntries: DefaultMaxFDBEntries,
	}
}

// SetMaxEntries configures the maximum number of entries allowed in the FDB.
func (f *FDB) SetMaxEntries(max int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maxEntries = max
}

// EvictExpired removes all non-static entries that have exceeded TTL. Returns number of evicted entries.
func (f *FDB) EvictExpired() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.evictExpiredLocked()
}

func (f *FDB) evictExpiredLocked() int {
	if f.ttl <= 0 {
		return 0
	}
	now := time.Now()
	evicted := 0
	for k, e := range f.entries {
		if !e.Static && now.Sub(e.UpdatedAt) > f.ttl {
			delete(f.entries, k)
			evicted++
		}
	}
	return evicted
}

// Len returns the current number of entries in the FDB.
func (f *FDB) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.entries)
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

	// If table reached max capacity, evict expired entries first
	if f.maxEntries > 0 && len(f.entries) >= f.maxEntries {
		f.evictExpiredLocked()
	}

	// If still at capacity, evict oldest non-static entry
	if f.maxEntries > 0 && len(f.entries) >= f.maxEntries {
		var oldestKey string
		var oldestTime time.Time
		for k, e := range f.entries {
			if !e.Static {
				if oldestKey == "" || e.UpdatedAt.Before(oldestTime) {
					oldestKey = k
					oldestTime = e.UpdatedAt
				}
			}
		}
		if oldestKey != "" {
			delete(f.entries, oldestKey)
		}
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
