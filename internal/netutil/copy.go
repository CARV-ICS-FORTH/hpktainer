package netutil

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"github.com/songgao/water"
)

// CopyFromTapToSocket reads packets from the TAP interface and writes them to the socket
// with a 4-byte length prefix in a SINGLE system call to prevent queue overflow.
func CopyFromTapToSocket(tap *water.Interface, conn net.Conn) error {
	// Allocate a 64k buffer. We reserve the first 4 bytes for the length header.
	buf := make([]byte, 65536)

	for {
		// Read from TAP directly into the buffer, offset by exactly 4 bytes
		n, err := tap.Read(buf[4:])
		if err != nil {
			return fmt.Errorf("read from tap error: %w", err)
		}

		// Write the 4-byte length prefix at the very beginning of the buffer
		binary.BigEndian.PutUint32(buf[:4], uint32(n))

		// Write the header AND the payload to the UNIX socket in ONE shot
		if _, err := conn.Write(buf[:4+n]); err != nil {
			return fmt.Errorf("write to socket error: %w", err)
		}
	}
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