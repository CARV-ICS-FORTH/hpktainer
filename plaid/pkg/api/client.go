package api

import (
	"encoding/json"
	"fmt"
	"net"
	"syscall"
	"time"
)

// Client is a client to communicate with the plaidd API server.
type Client struct {
	socketPath string
	timeout    time.Duration
}

// NewClient creates a new API client.
func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		timeout:    5 * time.Second,
	}
}

// Send sends a request to plaidd, optionally passing a file descriptor via SCM_RIGHTS.
func (c *Client) Send(req *Request, passFD int) (*Response, error) {
	conn, err := net.DialTimeout("unix", c.socketPath, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to plaidd API at %s: %w", c.socketPath, err)
	}
	defer conn.Close()

	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("connection is not a unix domain socket")
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	var oob []byte
	if passFD >= 0 {
		oob = syscall.UnixRights(passFD)
	}

	_, _, err = unixConn.WriteMsgUnix(data, oob, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to send request to plaidd: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(c.timeout))
	respBuf := make([]byte, 4096)
	n, err := conn.Read(respBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to read response from plaidd: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(respBuf[:n], &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

// AddEndpoint sends an AddEndpoint request along with the opened TAP file descriptor.
func (c *Client) AddEndpoint(req *Request, tapFD int) error {
	req.Action = ActionAddEndpoint
	resp, err := c.Send(req, tapFD)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("add endpoint failed: %s", resp.Error)
	}
	return nil
}

// RemoveEndpoint sends a RemoveEndpoint request.
func (c *Client) RemoveEndpoint(podID, containerID string) error {
	req := &Request{
		Action:      ActionRemoveEndpoint,
		PodID:       podID,
		ContainerID: containerID,
	}
	resp, err := c.Send(req, -1)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("remove endpoint failed: %s", resp.Error)
	}
	return nil
}

// AddRoute sends an AddRoute request.
func (c *Client) AddRoute(req *Request) error {
	req.Action = ActionAddRoute
	resp, err := c.Send(req, -1)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("add route failed: %s", resp.Error)
	}
	return nil
}

// RemoveRoute sends a RemoveRoute request.
func (c *Client) RemoveRoute(subnet string) error {
	req := &Request{
		Action: ActionRemoveRoute,
		Subnet: subnet,
	}
	resp, err := c.Send(req, -1)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("remove route failed: %s", resp.Error)
	}
	return nil
}

// GetStatus retrieves daemon status.
func (c *Client) GetStatus() (*Response, error) {
	req := &Request{
		Action: ActionGetStatus,
	}
	return c.Send(req, -1)
}

// AddFilterRule sends an AddFilterRule request.
func (c *Client) AddFilterRule(req *Request) error {
	req.Action = ActionAddFilterRule
	resp, err := c.Send(req, -1)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("add filter rule failed: %s", resp.Error)
	}
	return nil
}

// RemoveFilterRule sends a RemoveFilterRule request.
func (c *Client) RemoveFilterRule(ruleID string) error {
	req := &Request{
		Action: ActionRemoveFilterRule,
		RuleID: ruleID,
	}
	resp, err := c.Send(req, -1)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("remove filter rule failed: %s", resp.Error)
	}
	return nil
}

// ListFilterRules retrieves all active filter rules.
func (c *Client) ListFilterRules() ([]string, error) {
	req := &Request{
		Action: ActionListFilterRules,
	}
	resp, err := c.Send(req, -1)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("list filter rules failed: %s", resp.Error)
	}
	return resp.FilterRules, nil
}
