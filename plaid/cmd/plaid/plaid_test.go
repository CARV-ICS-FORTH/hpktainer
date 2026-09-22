package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"plaid/pkg/api"
)

type testAPIHandler struct {
	endpoints map[string]*api.Request
}

func (h *testAPIHandler) HandleAddEndpoint(req *api.Request, tapFD int) error {
	h.endpoints[req.PodID] = req
	if tapFD >= 0 {
		_ = os.NewFile(uintptr(tapFD), "tap").Close()
	}
	return nil
}

func (h *testAPIHandler) HandleRemoveEndpoint(req *api.Request) error {
	delete(h.endpoints, req.PodID)
	delete(h.endpoints, req.ContainerID)
	return nil
}

func (h *testAPIHandler) HandleAddRoute(req *api.Request) error {
	return nil
}

func (h *testAPIHandler) HandleRemoveRoute(req *api.Request) error {
	return nil
}

func (h *testAPIHandler) HandleGetStatus() (*api.Response, error) {
	return &api.Response{
		EndpointsCount: len(h.endpoints),
	}, nil
}

func TestCNIPlugin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "plaidd.sock")
	handler := &testAPIHandler{endpoints: make(map[string]*api.Request)}

	srv := api.NewServer(sockPath, handler, handler, handler)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Failed to start test API server: %v", err)
	}
	defer srv.Close()

	binPath := filepath.Join(tmpDir, "plaid")
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build plaid binary: %v, output: %s", err, out)
	}

	// 1. Test VERSION
	vCmd := exec.Command(binPath)
	vCmd.Env = append(os.Environ(), "CNI_COMMAND=VERSION")
	vOut, err := vCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CNI VERSION failed: %v, output: %s", err, vOut)
	}
	var vResp map[string]interface{}
	if err := json.Unmarshal(vOut, &vResp); err != nil {
		t.Fatalf("Failed to parse VERSION response: %v", err)
	}
	if vResp["cniVersion"] != "0.4.0" {
		t.Errorf("Unexpected cniVersion: %v", vResp["cniVersion"])
	}

	// 2. Test DEL (empty/non-existent endpoint should succeed without error)
	delConf := fmt.Sprintf(`{
		"cniVersion": "0.4.0",
		"name": "cbr0",
		"type": "plaid",
		"socketPath": %q
	}`, sockPath)

	dCmd := exec.Command(binPath)
	dCmd.Env = append(os.Environ(),
		"CNI_COMMAND=DEL",
		"CNI_CONTAINERID=cont-123",
		"CNI_IFNAME=eth0",
		"CNI_ARGS=K8S_POD_NAME=test-pod;K8S_POD_NAMESPACE=default",
	)
	dCmd.Stdin = bytes.NewReader([]byte(delConf))
	dOut, err := dCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CNI DEL failed: %v, output: %s", err, dOut)
	}

	// 3. Test API communication directly
	client := api.NewClient(sockPath)
	rPipe, _, _ := os.Pipe()
	defer rPipe.Close()

	err = client.AddEndpoint(&api.Request{
		PodID: "pod-direct",
		IP:    "10.244.1.5",
		MAC:   "02:00:00:00:00:05",
	}, int(rPipe.Fd()))
	if err != nil {
		t.Fatalf("Direct AddEndpoint failed: %v", err)
	}

	status, err := client.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if status.EndpointsCount != 1 {
		t.Errorf("Expected 1 endpoint, got %d", status.EndpointsCount)
	}

	err = client.RemoveEndpoint("pod-direct", "")
	if err != nil {
		t.Fatalf("Direct RemoveEndpoint failed: %v", err)
	}

	status, _ = client.GetStatus()
	if status.EndpointsCount != 0 {
		t.Errorf("Expected 0 endpoints after DEL, got %d", status.EndpointsCount)
	}
}

func init() {
	// Silence unused warnings for net and time
	_ = net.IPv4zero
	_ = time.Second
}
