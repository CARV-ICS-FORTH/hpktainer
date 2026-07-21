package podhandler

import (
	"context"
	"testing"

	"hpk/internal/compute"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFromServices_KubeMasterPortOverride(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	k8sSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubernetes",
			Namespace: metav1.NamespaceDefault,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 443,
				},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(k8sSvc).Build()

	oldClient := compute.K8SClient
	oldHost := compute.Environment.KubeMasterHost
	oldPort := compute.Environment.KubeMasterPort
	defer func() {
		compute.K8SClient = oldClient
		compute.Environment.KubeMasterHost = oldHost
		compute.Environment.KubeMasterPort = oldPort
	}()

	compute.K8SClient = client
	compute.Environment.KubeMasterHost = "10.0.0.1"
	compute.Environment.KubeMasterPort = "6443"

	envVars, err := FromServices(context.Background(), metav1.NamespaceDefault, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	expected := map[string]string{
		"KUBERNETES_SERVICE_HOST":        "10.0.0.1",
		"KUBERNETES_SERVICE_PORT":        "6443",
		"KUBERNETES_PORT":                "tcp://10.0.0.1:6443",
		"KUBERNETES_PORT_6443_TCP":       "tcp://10.0.0.1:6443",
		"KUBERNETES_PORT_6443_TCP_PORT":  "6443",
		"KUBERNETES_PORT_6443_TCP_ADDR":  "10.0.0.1",
		"KUBERNETES_PORT_6443_TCP_PROTO": "tcp",
	}

	for key, val := range expected {
		if got, ok := envMap[key]; !ok {
			t.Errorf("missing env var %s", key)
		} else if got != val {
			t.Errorf("env var %s = %q; want %q", key, got, val)
		}
	}

	// Ensure no stale 443 references exist
	unexpectedKeys := []string{
		"KUBERNETES_PORT_443_TCP",
		"KUBERNETES_PORT_443_TCP_PORT",
		"KUBERNETES_PORT_443_TCP_ADDR",
		"KUBERNETES_PORT_443_TCP_PROTO",
	}
	for _, key := range unexpectedKeys {
		if val, ok := envMap[key]; ok {
			t.Errorf("found unexpected 443 env var %s = %q", key, val)
		}
	}
}
