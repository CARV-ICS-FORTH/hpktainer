#!/usr/bin/env bash
# End-to-end verification script for Skiff.
# Deploys an HTTP server pod and client pod, verifies IP allocation,
# DNS resolution, cross-node connectivity, logs retrieval, and resource cleanup.

set -euo pipefail

RAND_ID=$((RANDOM % 9000 + 1000))
NAMESPACE="skiff-e2e-${RAND_ID}"
SERVER_POD="skiff-e2e-server"
CLIENT_POD="skiff-e2e-client"
SERVICE_NAME="skiff-e2e-svc"
TIMEOUT=180

if ! command -v kubectl >/dev/null 2>&1 && ! command -v k3s >/dev/null 2>&1; then
    if command -v apptainer >/dev/null 2>&1 && apptainer instance list 2>/dev/null | grep -q 'bubble1'; then
        kubectl() { apptainer exec --pwd /var/lib/skiff instance://bubble1 k3s kubectl --kubeconfig /var/lib/skiff/kubeconfig "$@"; }
    fi
elif ! command -v kubectl >/dev/null 2>&1 && command -v k3s >/dev/null 2>&1; then
    kubectl() { k3s kubectl "$@"; }
fi

if [ -z "${KUBECONFIG:-}" ]; then
    if [ -f "/var/lib/skiff/kubeconfig" ]; then
        export KUBECONFIG="/var/lib/skiff/kubeconfig"
    elif [ -f "$HOME/.skiff/kubeconfig" ]; then
        export KUBECONFIG="$HOME/.skiff/kubeconfig"
    fi
fi

echo "=== 1. Checking Kubernetes cluster access and node requirements ==="
if ! kubectl cluster-info >/dev/null 2>&1; then
    echo "Error: Unable to connect to Kubernetes cluster via kubectl." >&2
    exit 1
fi

ALL_NODES=($(kubectl get nodes --no-headers -o jsonpath='{.items[*].metadata.name}'))
echo "Discovered nodes: ${ALL_NODES[*]}"
if [ "${#ALL_NODES[@]}" -lt 2 ]; then
    echo "ERROR: Full two-node Skiff acceptance requires at least 2 Ready nodes, but only ${#ALL_NODES[@]} was found." >&2
    echo "Failing multi-node qualification suite." >&2
    exit 1
fi

cleanup() {
    trap - EXIT INT TERM
    echo "=== Cleaning up test namespace and resources ==="
    kubectl delete namespace "$NAMESPACE" --timeout=60s 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "Creating isolated test namespace ${NAMESPACE}..."
kubectl create namespace "$NAMESPACE"

echo "=== 2. Creating server pod and service ($SERVER_POD) ==="
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${SERVER_POD}
  namespace: ${NAMESPACE}
  labels:
    app: skiff-e2e-server
spec:
  nodeName: ${ALL_NODES[0]}
  containers:
  - name: server
    image: python:3.9-slim
    command: ["python3", "-m", "http.server", "8080"]
    ports:
    - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: ${SERVICE_NAME}
  namespace: ${NAMESPACE}
spec:
  selector:
    app: skiff-e2e-server
  ports:
  - port: 8080
    targetPort: 8080
EOF

echo "Waiting for ${SERVER_POD} to become Ready..."
kubectl wait --for=condition=Ready "pod/${SERVER_POD}" --namespace="$NAMESPACE" --timeout="${TIMEOUT}s"

SERVER_IP=$(kubectl get pod "${SERVER_POD}" --namespace="$NAMESPACE" -o jsonpath='{.status.podIP}')
SERVER_NODE=$(kubectl get pod "${SERVER_POD}" --namespace="$NAMESPACE" -o jsonpath='{.spec.nodeName}')
echo "Server pod is Running on node ${SERVER_NODE} with PodIP: ${SERVER_IP}"

if [ -z "$SERVER_IP" ]; then
    echo "Error: Server pod did not receive a valid PodIP" >&2
    exit 1
fi

# Target client to the second node to mandate cross-node overlay communication
TARGET_NODE="${ALL_NODES[1]}"
echo "Scheduling ${CLIENT_POD} to remote worker node: ${TARGET_NODE}"

echo "=== 3. Testing cross-node PodIP connectivity & DNS resolution from ($CLIENT_POD) ==="
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${CLIENT_POD}
  namespace: ${NAMESPACE}
spec:
  nodeName: ${TARGET_NODE}
  restartPolicy: Never
  containers:
  - name: client
    image: curlimages/curl:latest
    command:
    - "/bin/sh"
    - "-c"
    - |
      set -e
      echo "Testing direct PodIP connectivity across nodes (${TARGET_NODE} -> ${SERVER_NODE}):"
      curl -s -f --connect-timeout 10 "http://${SERVER_IP}:8080"
      echo "Testing DNS resolution of internal cluster domain:"
      nslookup kubernetes.default.svc.cluster.local
      echo "All network assertions completed successfully."
EOF

echo "Waiting for ${CLIENT_POD} to complete..."
PHASE=""
for i in $(seq 1 $TIMEOUT); do
    PHASE=$(kubectl get pod "${CLIENT_POD}" --namespace="$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    if [ "$PHASE" = "Succeeded" ]; then
        echo "Client pod completed successfully!"
        break
    elif [ "$PHASE" = "Failed" ]; then
        echo "Error: Client pod failed!" >&2
        kubectl logs "${CLIENT_POD}" --namespace="$NAMESPACE" >&2 || true
        exit 1
    fi
    sleep 1
done

if [ "$PHASE" != "Succeeded" ]; then
    echo "Error: Client pod timed out waiting for completion (phase: $PHASE)" >&2
    kubectl describe pod "${CLIENT_POD}" --namespace="$NAMESPACE" >&2 || true
    exit 1
fi

echo "=== 4. Verifying server logs collection ==="
SERVER_LOGS=$(kubectl logs "${SERVER_POD}" -c server --namespace="$NAMESPACE")
if [ -z "$SERVER_LOGS" ]; then
    echo "Error: Failed to retrieve logs from ${SERVER_POD} or logs were empty" >&2
    exit 1
fi
echo "Verified server log output:"
echo "$SERVER_LOGS" | head -n 5

echo "=== 5. Testing pod deletion & resource reconciliation ==="
kubectl delete pod "${CLIENT_POD}" --namespace="$NAMESPACE" --timeout=60s
kubectl delete pod "${SERVER_POD}" --namespace="$NAMESPACE" --timeout=60s

# Verify pods are removed from Kubernetes API
REMAINING_PODS=$(kubectl get pods --namespace="$NAMESPACE" --no-headers 2>/dev/null || true)
if [ -n "$REMAINING_PODS" ]; then
    echo "Error: Pods still exist after deletion timeout: $REMAINING_PODS" >&2
    exit 1
fi

# Verify endpoint cleanup in Plaid if plaidctl is available
if command -v plaidctl >/dev/null 2>&1; then
    ACTIVE_ENDPOINTS=$(plaidctl status 2>/dev/null | grep -E "Endpoints:\s+[1-9]" || true)
    if [ -n "$ACTIVE_ENDPOINTS" ]; then
        echo "Warning/Check: Non-zero endpoints active after pod deletion: $ACTIVE_ENDPOINTS"
    fi
fi

echo "=== E2E Test Passed Successfully (Multi-Node Overlay, DNS, Logs & Cleanup) ==="
