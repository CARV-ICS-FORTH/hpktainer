package tap

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"

	"plaid/pkg/bridge"
)

// TapEndpoint implements bridge.Endpoint for a pod's TAP interface file descriptor.
type TapEndpoint struct {
	epID       string
	epName     string
	epIP       net.IP
	epMAC      net.HardwareAddr
	file       *os.File
	closeOnce  sync.Once
	closedChan chan struct{}
}

// NewTapEndpoint creates an Endpoint backed by an open TAP file descriptor.
func NewTapEndpoint(id, name string, ip net.IP, mac net.HardwareAddr, file *os.File) *TapEndpoint {
	return &TapEndpoint{
		epID:       id,
		epName:     name,
		epIP:       ip,
		epMAC:      mac,
		file:       file,
		closedChan: make(chan struct{}),
	}
}

func (t *TapEndpoint) ID() string            { return t.epID }
func (t *TapEndpoint) Name() string          { return t.epName }
func (t *TapEndpoint) IP() net.IP            { return t.epIP }
func (t *TapEndpoint) MAC() net.HardwareAddr { return t.epMAC }
func (t *TapEndpoint) File() *os.File        { return t.file }

// Write sends an Ethernet frame into the TAP device (received inside the pod netns).
func (t *TapEndpoint) Write(frame []byte) error {
	select {
	case <-t.closedChan:
		return fmt.Errorf("endpoint %s is closed", t.epID)
	default:
	}

	_, err := t.file.Write(frame)
	return err
}

// StartReadLoop starts a goroutine reading Ethernet frames from the TAP device and processing them through the Bridge.
func (t *TapEndpoint) StartReadLoop(ctx context.Context, b *bridge.Bridge) {
	go func() {
		buf := make([]byte, 65535)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.closedChan:
				return
			default:
			}

			n, err := t.file.Read(buf)
			if err != nil {
				select {
				case <-t.closedChan:
					return
				case <-ctx.Done():
					return
				default:
					return
				}
			}

			if n > 0 {
				_ = b.ProcessFrame(t, buf[:n])
			}
		}
	}()
}

// Close closes the TAP device file.
func (t *TapEndpoint) Close() error {
	var err error
	t.closeOnce.Do(func() {
		close(t.closedChan)
		if t.file != nil {
			err = t.file.Close()
		}
	})
	return err
}
