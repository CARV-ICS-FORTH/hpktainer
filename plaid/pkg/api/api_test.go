package api

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type mockHandlers struct {
	lastAddReq   *Request
	lastDelReq   *Request
	lastAddRoute *Request
	lastDelRoute *Request
	receivedFD   int
}

func (m *mockHandlers) HandleAddEndpoint(req *Request, tapFD int) error {
	m.lastAddReq = req
	m.receivedFD = tapFD
	return nil
}

func (m *mockHandlers) HandleRemoveEndpoint(req *Request) error {
	m.lastDelReq = req
	return nil
}

func (m *mockHandlers) HandleAddRoute(req *Request) error {
	m.lastAddRoute = req
	return nil
}

func (m *mockHandlers) HandleRemoveRoute(req *Request) error {
	m.lastDelRoute = req
	return nil
}

func (m *mockHandlers) HandleGetStatus() (*Response, error) {
	return &Response{
		EndpointsCount: 42,
		RoutesCount:    3,
		NodeCIDR:       "10.244.1.0/24",
	}, nil
}

func TestAPIServerAndClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sockPath := filepath.Join(t.TempDir(), "test_plaidd.sock")
	handlers := &mockHandlers{}

	srv := NewServer(sockPath, handlers, handlers, handlers)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer srv.Close()

	client := NewClient(sockPath)

	// 1. Test GetStatus
	status, err := client.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus error: %v", err)
	}
	if !status.Success || status.EndpointsCount != 42 || status.NodeCIDR != "10.244.1.0/24" {
		t.Errorf("Unexpected status response: %+v", status)
	}

	// 2. Test AddRoute & RemoveRoute
	err = client.AddRoute(&Request{
		Subnet:       "10.244.2.0/24",
		RemoteHostIP: "192.168.1.10",
		VNI:          1,
	})
	if err != nil {
		t.Fatalf("AddRoute error: %v", err)
	}
	if handlers.lastAddRoute == nil || handlers.lastAddRoute.Subnet != "10.244.2.0/24" {
		t.Errorf("AddRoute handler did not receive expected params")
	}

	err = client.RemoveRoute("10.244.2.0/24")
	if err != nil {
		t.Fatalf("RemoveRoute error: %v", err)
	}
	if handlers.lastDelRoute == nil || handlers.lastDelRoute.Subnet != "10.244.2.0/24" {
		t.Errorf("RemoveRoute handler did not receive expected params")
	}

	// 3. Test AddEndpoint with SCM_RIGHTS FD passing
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe error: %v", err)
	}
	defer rPipe.Close()
	defer wPipe.Close()

	err = client.AddEndpoint(&Request{
		PodID:        "pod-123",
		PodName:      "nginx",
		PodNamespace: "default",
		IP:           "10.244.1.15",
		MAC:          "02:00:00:00:01:15",
	}, int(rPipe.Fd()))
	if err != nil {
		t.Fatalf("AddEndpoint with FD error: %v", err)
	}

	if handlers.lastAddReq == nil || handlers.lastAddReq.PodID != "pod-123" {
		t.Fatalf("AddEndpoint handler did not receive expected request")
	}

	if handlers.receivedFD < 0 {
		t.Fatalf("Server did not receive FD over SCM_RIGHTS (got %d)", handlers.receivedFD)
	}

	// Test communicating through the passed file descriptor
	receivedFile := os.NewFile(uintptr(handlers.receivedFD), "passed-tap-fd")
	defer receivedFile.Close()

	testMsg := "hello via passed fd"
	go func() {
		_, _ = wPipe.Write([]byte(testMsg))
		_ = wPipe.Close()
	}()

	readBuf := make([]byte, 64)
	n, err := receivedFile.Read(readBuf)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read from passed fd: %v", err)
	}
	if string(readBuf[:n]) != testMsg {
		t.Errorf("Expected msg %q, got %q", testMsg, string(readBuf[:n]))
	}

	// 4. Test RemoveEndpoint
	err = client.RemoveEndpoint("pod-123", "container-456")
	if err != nil {
		t.Fatalf("RemoveEndpoint error: %v", err)
	}
	if handlers.lastDelReq == nil || handlers.lastDelReq.PodID != "pod-123" {
		t.Errorf("RemoveEndpoint handler did not receive expected request")
	}
}
