package netutil

import (
	"net"
	"testing"
)

func TestActiveConnHolder(t *testing.T) {
	holder := NewActiveConnHolder()
	if c := holder.Get(); c != nil {
		t.Fatalf("expected nil active conn, got %v", c)
	}

	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	holder.Set(conn1)
	if c := holder.Get(); c != conn1 {
		t.Fatalf("expected conn1, got %v", c)
	}

	// Clearing with different conn should not clear conn1
	holder.Clear(conn2)
	if c := holder.Get(); c != conn1 {
		t.Fatalf("expected conn1 after clearing conn2, got %v", c)
	}

	// Clearing with conn1 should set to nil
	holder.Clear(conn1)
	if c := holder.Get(); c != nil {
		t.Fatalf("expected nil after clearing conn1, got %v", c)
	}
}
