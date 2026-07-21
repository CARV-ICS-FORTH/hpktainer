package netutil

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"sync"

	"github.com/songgao/water"
)

// ActiveConnHolder holds the active net.Conn in a thread-safe manner for TAP-to-socket packet forwarding across sessions.
type ActiveConnHolder struct {
	mu   sync.Mutex
	conn net.Conn
}

// NewActiveConnHolder creates a new ActiveConnHolder.
func NewActiveConnHolder() *ActiveConnHolder {
	return &ActiveConnHolder{}
}

// Set updates the current active connection.
func (h *ActiveConnHolder) Set(conn net.Conn) {
	h.mu.Lock()
	h.conn = conn
	h.mu.Unlock()
}

// Get returns the current active connection, or nil if none is set.
func (h *ActiveConnHolder) Get() net.Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn
}

// Clear sets the current active connection to nil only if it currently equals target.
func (h *ActiveConnHolder) Clear(target net.Conn) {
	h.mu.Lock()
	if h.conn == target {
		h.conn = nil
	}
	h.mu.Unlock()
}

// ForwardTapToSocket continuously reads packets from the TAP interface and writes them to the active socket connection.
// If no connection is active, packets are dropped. It exits when reading from tap fails (e.g. when TAP is closed).
func ForwardTapToSocket(tap *water.Interface, activeConn *ActiveConnHolder) error {
	buf := make([]byte, 65536)

	for {
		n, err := tap.Read(buf[4:])
		if err != nil {
			return fmt.Errorf("read from tap error: %w", err)
		}

		conn := activeConn.Get()
		if conn == nil {
			// No active session; drop stray TAP packet
			continue
		}

		binary.BigEndian.PutUint32(buf[:4], uint32(n))
		if _, err := conn.Write(buf[:4+n]); err != nil {
			// Socket write error (e.g., connection closed); close connection to wake up socket reader if needed.
			_ = conn.Close()
		}
	}
}

// CopyFromTapToSocket reads packets from the TAP interface and writes them to a specific socket connection.
func CopyFromTapToSocket(tap *water.Interface, conn net.Conn) error {
	holder := NewActiveConnHolder()
	holder.Set(conn)
	return ForwardTapToSocket(tap, holder)
}

// CopyFromSocketToTap reads length-prefixed packets from the socket and writes them to the TAP interface.
func CopyFromSocketToTap(conn net.Conn, tap *water.Interface) error {
	lenBuf := make([]byte, 4)
	buf := make([]byte, 65536)

	for {
		// Read exactly 4 bytes for the length header
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return fmt.Errorf("read length from socket error: %w", err)
		}
		length := binary.BigEndian.Uint32(lenBuf)

		if length > uint32(len(buf)) {
			return fmt.Errorf("packet too large: %d", length)
		}

		// Read payload
		if _, err := io.ReadFull(conn, buf[:length]); err != nil {
			return fmt.Errorf("read payload from socket error: %w", err)
		}

		// Write the raw Ethernet frame to the TAP interface
		if _, err := tap.Write(buf[:length]); err != nil {
			return fmt.Errorf("write to tap error: %w", err)
		}
	}
}
