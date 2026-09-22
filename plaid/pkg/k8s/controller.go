package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// Config configures the Kubernetes controller.
type Config struct {
	Kubeconfig string
	NodeName   string
}

// RouteAddFunc is invoked when a remote node's pod CIDR route is discovered or updated.
type RouteAddFunc func(nodeName string, subnet *net.IPNet, hostIP net.IP)

// RouteDeleteFunc is invoked when a remote node's pod CIDR route is removed.
type RouteDeleteFunc func(nodeName string, subnet *net.IPNet)

// Controller watches Kubernetes Node resources and coordinates overlay routing.
type Controller struct {
	client   kubernetes.Interface
	nodeName string
}

// NewController creates a Controller instance with a provided kubernetes.Interface.
func NewController(client kubernetes.Interface, nodeName string) *Controller {
	return &Controller{
		client:   client,
		nodeName: nodeName,
	}
}

// NewControllerFromConfig creates a Controller instance from Config using clientcmd.
func NewControllerFromConfig(cfg Config) (*Controller, error) {
	nodeName := cfg.NodeName
	if nodeName == "" {
		nodeName = os.Getenv("NODE_NAME")
	}
	if nodeName == "" {
		nodeName = os.Getenv("HOSTNAME")
	}
	if nodeName == "" {
		h, err := os.Hostname()
		if err == nil {
			nodeName = h
		}
	}
	if nodeName == "" {
		return nil, errors.New("unable to determine local node name (specify --node-name or set NODE_NAME)")
	}

	restConfig, err := clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	return NewController(clientset, nodeName), nil
}

// NodeName returns the local node name.
func (c *Controller) NodeName() string {
	return c.nodeName
}

// Client returns the underlying kubernetes.Interface.
func (c *Controller) Client() kubernetes.Interface {
	return c.client
}

// GetLocalNode waits for and retrieves the local node's Pod CIDR and internal IP.
func (c *Controller) GetLocalNode(ctx context.Context) (*v1.Node, *net.IPNet, net.IP, error) {
	var node *v1.Node
	var podNet *net.IPNet
	var hostIP net.IP

	pollInterval := 1 * time.Second
	pollTimeout := 3 * time.Minute

	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		n, err := c.client.CoreV1().Nodes().Get(ctx, c.nodeName, metav1.GetOptions{})
		if err != nil {
			return false, nil // retry
		}

		cidrStr := n.Spec.PodCIDR
		if cidrStr == "" && len(n.Spec.PodCIDRs) > 0 {
			cidrStr = n.Spec.PodCIDRs[0]
		}
		if cidrStr == "" {
			// PodCIDR not yet assigned by kube-controller-manager; retry
			return false, nil
		}

		_, parsedNet, err := net.ParseCIDR(cidrStr)
		if err != nil {
			return false, fmt.Errorf("invalid podCIDR %q on node %s: %w", cidrStr, c.nodeName, err)
		}

		parsedHostIP := ExtractNodeIP(n)
		if parsedHostIP == nil {
			return false, nil // wait for node IP
		}

		node = n
		podNet = parsedNet
		hostIP = parsedHostIP
		return true, nil
	})

	if err != nil {
		return nil, nil, nil, fmt.Errorf("timed out waiting for node %q to have PodCIDR assigned: %w", c.nodeName, err)
	}

	return node, podNet, hostIP, nil
}

// SetNodeNetworkReady updates the node's NodeNetworkUnavailable condition to False.
func (c *Controller) SetNodeNetworkReady(ctx context.Context) error {
	condition := v1.NodeCondition{
		Type:               v1.NodeNetworkUnavailable,
		Status:             v1.ConditionFalse,
		Reason:             "PlaidIsUp",
		Message:            "Plaid user-space networking is operational",
		LastTransitionTime: metav1.Now(),
		LastHeartbeatTime:  metav1.Now(),
	}

	raw, err := json.Marshal([]v1.NodeCondition{condition})
	if err != nil {
		return fmt.Errorf("failed to marshal condition: %w", err)
	}

	patch := []byte(fmt.Sprintf(`{"status":{"conditions":%s}}`, raw))
	_, err = c.client.CoreV1().Nodes().PatchStatus(ctx, c.nodeName, patch)
	if err != nil {
		// Fallback to standard patch if PatchStatus fails
		_, err = c.client.CoreV1().Nodes().Patch(ctx, c.nodeName, types.StrategicMergePatchType, patch, metav1.PatchOptions{}, "status")
	}
	return err
}

// Run starts the Informer to watch peer nodes and calls route callbacks.
func (c *Controller) Run(ctx context.Context, onAdd RouteAddFunc, onDelete RouteDeleteFunc) {
	lw := cache.NewListWatchFromClient(
		c.client.CoreV1().RESTClient(),
		"nodes",
		metav1.NamespaceAll,
		fields.Everything(),
	)

	informer := cache.NewSharedIndexInformer(
		lw,
		&v1.Node{},
		5*time.Minute,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)

	_, _ = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node, ok := obj.(*v1.Node)
			if !ok || node == nil {
				return
			}
			c.handleNodeAddOrUpdate(node, onAdd)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			node, ok := newObj.(*v1.Node)
			if !ok || node == nil {
				return
			}
			c.handleNodeAddOrUpdate(node, onAdd)
		},
		DeleteFunc: func(obj interface{}) {
			node, ok := obj.(*v1.Node)
			if !ok {
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					return
				}
				node, ok = tombstone.Obj.(*v1.Node)
				if !ok || node == nil {
					return
				}
			}
			c.handleNodeDelete(node, onDelete)
		},
	})

	informer.Run(ctx.Done())
}

func (c *Controller) handleNodeAddOrUpdate(node *v1.Node, onAdd RouteAddFunc) {
	if node.Name == c.nodeName {
		return
	}

	cidrStr := node.Spec.PodCIDR
	if cidrStr == "" && len(node.Spec.PodCIDRs) > 0 {
		cidrStr = node.Spec.PodCIDRs[0]
	}
	if cidrStr == "" {
		return
	}

	_, sn, err := net.ParseCIDR(cidrStr)
	if err != nil {
		return
	}

	hostIP := ExtractNodeIP(node)
	if hostIP == nil {
		return
	}

	if onAdd != nil {
		onAdd(node.Name, sn, hostIP)
	}
}

func (c *Controller) handleNodeDelete(node *v1.Node, onDelete RouteDeleteFunc) {
	if node.Name == c.nodeName {
		return
	}

	cidrStr := node.Spec.PodCIDR
	if cidrStr == "" && len(node.Spec.PodCIDRs) > 0 {
		cidrStr = node.Spec.PodCIDRs[0]
	}
	if cidrStr == "" {
		return
	}

	_, sn, err := net.ParseCIDR(cidrStr)
	if err != nil {
		return
	}

	if onDelete != nil {
		onDelete(node.Name, sn)
	}
}

// ExtractNodeIP finds the preferred IPv4 address for a Node.
func ExtractNodeIP(node *v1.Node) net.IP {
	var fallbackIP net.IP

	for _, addr := range node.Status.Addresses {
		ip := net.ParseIP(strings.TrimSpace(addr.Address))
		if ip == nil || ip.To4() == nil {
			continue
		}

		if addr.Type == v1.NodeInternalIP {
			return ip.To4()
		}

		if fallbackIP == nil && (addr.Type == v1.NodeExternalIP || addr.Type == v1.NodeHostName) {
			fallbackIP = ip.To4()
		}
	}

	return fallbackIP
}
