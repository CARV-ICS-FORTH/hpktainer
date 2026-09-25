package podhandler

import (
	"context"
	"fmt"
	"net"
	"time"
)

// The daemon opens its API socket only after the bubble TAP, routes, and rules
// are ready. This gate lets Node registration happen before Plaid startup.
func waitForPodNetwork(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for {
		conn, err := net.DialTimeout("unix", "/run/plaid/plaidd.sock", time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for Plaid pod network: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}
