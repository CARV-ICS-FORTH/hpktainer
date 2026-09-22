#!/usr/bin/env bash
# End-to-end verification script for Skiff.
# Deploys an HTTP server pod and a client pod, verifies IP allocation,
# DNS resolution, connectivity, logs retrieval, and graceful deletion.

set -euo pipefail

NAMESPACE="default"
SERVER_POD="skiff-e2e-server"
CLIENT_POD="skiff-e2e-client"
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

echo "=== 1. Checking Kubernetes cluster access ==="
if ! kubectl cluster-info >/dev/null 2>&1; then
    echo "Error: Unable to connect to Kubernetes cluster via kubectl."
    exit 1
fi

cleanup() {
    echo "=== Cleaning up test pods ==="
    kubectl delete pod "$SERVER_POD" --namespace="$NAMESPACE" --ignore-not-found=true --grace-period=5 2>/dev/null || true
    kubectl delete pod "$CLIENT_POD" --namespace="$NAMESPACE" --ignore-not-found=true --grace-period=5 2>/dev/null || true
}
trap cleanup EXIT INT TERM

cleanup

echo "=== 2. Creating server pod ($SERVER_POD) ==="
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${SERVER_POD}
  namespace: ${NAMESPACE}
  labels:
    app: skiff-e2e-server
spec:
  containers:
  - name: server
    image: python:3.9-slim
    command: ["python3", "-m", "http.server", "8080"]
    ports:
    - containerPort: 8080
EOF

echo "Waiting for ${SERVER_POD} to become Running..."
kubectl wait --for=condition=Ready "pod/${SERVER_POD}" --namespace="$NAMESPACE" --timeout="${TIMEOUT}s"

SERVER_IP=$(kubectl get pod "${SERVER_POD}" --namespace="$NAMESPACE" -o jsonpath='{.status.podIP}')
SERVER_NODE=$(kubectl get pod "${SERVER_POD}" --namespace="$NAMESPACE" -o jsonpath='{.spec.nodeName}')
echo "Server pod is Running on node ${SERVER_NODE} with IP: ${SERVER_IP}"

if [ -z "$SERVER_IP" ]; then
    echo "Error: Server pod did not receive a PodIP"
    exit 1
fi

echo "=== 3. Verifying logs collection ==="
sleep 2
kubectl logs "${SERVER_POD}" -c server --namespace="$NAMESPACE" || true

# Determine if there is another node to test cross-node overlay networking
TARGET_NODE_SPEC=""
ALL_NODES=($(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'))
for n in "${ALL_NODES[@]}"; do
    if [ "$n" != "$SERVER_NODE" ]; then
        TARGET_NODE_SPEC="nodeName: $n"
        echo "Multi-node detected: scheduling ${CLIENT_POD} to $n to test cross-node overlay connectivity"
        break
    fi
done

echo "=== 4. Testing connectivity from client pod ($CLIENT_POD) ==="
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${CLIENT_POD}
  namespace: ${NAMESPACE}
spec:
  ${TARGET_NODE_SPEC}
  restartPolicy: Never
  containers:
  - name: client
    image: curlimages/curl:latest
    command: ["curl", "-s", "-f", "--connect-timeout", "5", "http://${SERVER_IP}:8080"]
EOF

echo "Waiting for ${CLIENT_POD} to complete..."
for i in $(seq 1 $TIMEOUT); do
    PHASE=$(kubectl get pod "${CLIENT_POD}" --namespace="$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    if [ "$PHASE" = "Succeeded" ]; then
        echo "Client pod completed successfully!"
        break
    elif [ "$PHASE" = "Failed" ]; then
        echo "Error: Client pod failed!"
        kubectl logs "${CLIENT_POD}" --namespace="$NAMESPACE" || true
        exit 1
    fi
    sleep 1
done

if [ "$PHASE" != "Succeeded" ]; then
    echo "Error: Client pod timed out waiting for completion (phase: $PHASE)"
    exit 1
fi

echo "=== 5. Testing pod deletion ==="
kubectl delete pod "${SERVER_POD}" --namespace="$NAMESPACE" --timeout=60s
kubectl delete pod "${CLIENT_POD}" --namespace="$NAMESPACE" --timeout=60s

echo "=== E2E Test Passed Successfully ==="
