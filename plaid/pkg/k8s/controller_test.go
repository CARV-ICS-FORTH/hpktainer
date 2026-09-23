package k8s

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetLocalNode(t *testing.T) {
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
		},
		Spec: v1.NodeSpec{
			PodCIDR: "10.244.1.0/24",
		},
		Status: v1.NodeStatus{
			Addresses: []v1.NodeAddress{
				{Type: v1.NodeInternalIP, Address: "192.168.64.10"},
				{Type: v1.NodeHostName, Address: "node-1.local"},
			},
		},
	}

	client := fake.NewSimpleClientset(node)
	ctrl := NewController(client, "node-1")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	n, podNet, hostIP, err := ctrl.GetLocalNode(ctx)
	if err != nil {
		t.Fatalf("GetLocalNode failed: %v", err)
	}

	if n.Name != "node-1" {
		t.Errorf("expected node-1, got %s", n.Name)
	}
	if podNet.String() != "10.244.1.0/24" {
		t.Errorf("expected 10.244.1.0/24, got %s", podNet.String())
	}
	if hostIP.String() != "192.168.64.10" {
		t.Errorf("expected 192.168.64.10, got %s", hostIP.String())
	}
}

func TestSetNodeNetworkReady(t *testing.T) {
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
		},
	}

	client := fake.NewSimpleClientset(node)
	ctrl := NewController(client, "node-1")

	ctx := context.Background()
	if err := ctrl.SetNodeNetworkReady(ctx); err != nil {
		t.Fatalf("SetNodeNetworkReady failed: %v", err)
	}

	updated, err := client.CoreV1().Nodes().Get(ctx, "node-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to fetch updated node: %v", err)
	}

	found := false
	for _, cond := range updated.Status.Conditions {
		if cond.Type == v1.NodeNetworkUnavailable {
			found = true
			if cond.Status != v1.ConditionFalse {
				t.Errorf("expected ConditionFalse, got %s", cond.Status)
			}
			if cond.Reason != "PlaidIsUp" {
				t.Errorf("expected PlaidIsUp, got %s", cond.Reason)
			}
		}
	}

	if !found {
		t.Errorf("NodeNetworkUnavailable condition not found in node status")
	}
}

func TestNodeEventHandlers(t *testing.T) {
	ctrl := NewController(fake.NewSimpleClientset(), "local-node")

	var mu sync.Mutex
	addedRoutes := make(map[string]string)
	deletedRoutes := make(map[string]string)

	onAdd := func(name string, sn *net.IPNet, hostIP net.IP) {
		mu.Lock()
		defer mu.Unlock()
		addedRoutes[name] = sn.String() + "=" + hostIP.String()
	}

	onDelete := func(name string, sn *net.IPNet) {
		mu.Lock()
		defer mu.Unlock()
		deletedRoutes[name] = sn.String()
	}

	// 1. Peer node added
	peerNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "remote-node-1",
		},
		Spec: v1.NodeSpec{
			PodCIDR: "10.244.2.0/24",
		},
		Status: v1.NodeStatus{
			Addresses: []v1.NodeAddress{
				{Type: v1.NodeInternalIP, Address: "192.168.64.20"},
			},
		},
	}

	ctrl.handleNodeAddOrUpdate(peerNode, onAdd)

	mu.Lock()
	if addedRoutes["remote-node-1"] != "10.244.2.0/24=192.168.64.20" {
		t.Errorf("unexpected added route: %v", addedRoutes["remote-node-1"])
	}
	mu.Unlock()

	// 2. Local node event should be ignored
	localNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "local-node",
		},
		Spec: v1.NodeSpec{
			PodCIDR: "10.244.1.0/24",
		},
		Status: v1.NodeStatus{
			Addresses: []v1.NodeAddress{
				{Type: v1.NodeInternalIP, Address: "192.168.64.10"},
			},
		},
	}
	ctrl.handleNodeAddOrUpdate(localNode, onAdd)

	mu.Lock()
	if _, exists := addedRoutes["local-node"]; exists {
		t.Errorf("local-node route should not be added")
	}
	mu.Unlock()

	// 3. Peer node deleted
	ctrl.handleNodeDelete(peerNode, onDelete)

	mu.Lock()
	if deletedRoutes["remote-node-1"] != "10.244.2.0/24" {
		t.Errorf("unexpected deleted route: %v", deletedRoutes["remote-node-1"])
	}
	mu.Unlock()

	// 4. Peer node CIDR changed (handleNodeUpdate)
	updatedPeerNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "remote-node-1",
		},
		Spec: v1.NodeSpec{
			PodCIDR: "10.244.3.0/24",
		},
		Status: v1.NodeStatus{
			Addresses: []v1.NodeAddress{
				{Type: v1.NodeInternalIP, Address: "192.168.64.20"},
			},
		},
	}
	delete(deletedRoutes, "remote-node-1")
	ctrl.handleNodeUpdate(peerNode, updatedPeerNode, onAdd, onDelete)

	mu.Lock()
	if deletedRoutes["remote-node-1"] != "10.244.2.0/24" {
		t.Errorf("expected old route 10.244.2.0/24 to be deleted on CIDR change, got %v", deletedRoutes["remote-node-1"])
	}
	if addedRoutes["remote-node-1"] != "10.244.3.0/24=192.168.64.20" {
		t.Errorf("expected updated route 10.244.3.0/24=192.168.64.20, got %v", addedRoutes["remote-node-1"])
	}
	mu.Unlock()
}
