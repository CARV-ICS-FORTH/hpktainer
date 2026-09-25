package podhandler

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestReadinessControlsPodCondition(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "server"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "server",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}},
			}},
		},
	}
	condition := func() corev1.ConditionStatus {
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady {
				return c.Status
			}
		}
		return corev1.ConditionUnknown
	}
	UpdateStatusFromRuntime(pod)
	if pod.Status.Phase != corev1.PodRunning || condition() != corev1.ConditionFalse {
		t.Fatalf("running but unready container must leave Pod Ready=False: %+v", pod.Status)
	}
	pod.Status.ContainerStatuses[0].Ready = true
	UpdateStatusFromRuntime(pod)
	if condition() != corev1.ConditionTrue {
		t.Fatalf("ready container did not make Pod Ready=True: %+v", pod.Status.Conditions)
	}
}

func TestHTTPReadinessUsesPodIP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	host, portString, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	probe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromInt(port)}}}
	if !evaluateReadinessProbe(probe, host, "", time.Second) {
		t.Fatal("HTTP readiness probe could not reach the PodIP")
	}
}
