#!/bin/bash

# Redirect all stdout and stderr to /var/log/entrypoint.log while keeping terminal output
mkdir -p /var/log /tmp/.hpk-apptainer/tmp /root/.hpk/.apptainer/cache
exec > >(tee -a /var/log/entrypoint.log) 2>&1

# Default values if not provided
HOST_IP=${HOST_IP:-$(ip route get 1 | awk '{print $7; exit}')}
CONTROLLER_IP=${CONTROLLER_IP:-$HOST_IP}

echo "Starting Calico..."
echo "  Etcd Endpoint: http://${CONTROLLER_IP}:2379"
echo "  Public IP:     ${HOST_IP}"
echo "  Interface:     tap0"
echo "  Role:          ${HPK_ROLE}"

# Enable IP forwarding
sysctl -w net.ipv4.ip_forward=1

# Wait for tap0 interface to appear (created by slirp4netns)
echo "Waiting for tap0 interface..."
while ! ip link show tap0 >/dev/null 2>&1; do
    sleep 0.1
done
# Ensure it is up
ip link set tap0 up

# Add Host IP as secondary address to tap0
# This is required for Calico VXLAN to use it as a source IP
ip addr add ${HOST_IP}/32 dev tap0 2>/dev/null || true

# Start Etcd if Controller
if [ "$HPK_ROLE" = "controller" ]; then
    echo "Starting Etcd..."
    # Config for single node etcd
    etcd --name default \
         --listen-client-urls http://0.0.0.0:2379 \
         --advertise-client-urls http://${HOST_IP}:2379 \
         --listen-peer-urls http://0.0.0.0:2380 \
         --initial-advertise-peer-urls http://${HOST_IP}:2380 \
         --initial-cluster default=http://${HOST_IP}:2380 \
         --initial-cluster-token etcd-cluster-1 \
         --initial-cluster-state new \
         --data-dir /var/lib/etcd \
         >> /var/log/etcd.log 2>&1 &
    
    # Wait for etcd to accept connections
    echo "Waiting for Etcd to accept connections on port 2379..."
    while ! (echo > /dev/tcp/127.0.0.1/2379) 2>/dev/null; do
        sleep 1
    done
    
    # Initialize Calico config in Etcd
    echo "Initializing Calico config in Etcd..."
    export DATASTORE_TYPE=etcdv3
    export ETCD_ENDPOINTS=http://127.0.0.1:2379
    calicoctl apply -f - <<EOF
apiVersion: projectcalico.org/v3
kind: IPPool
metadata:
  name: default-ipv4-ippool
spec:
  cidr: 10.244.0.0/16
  ipipMode: Never
  vxlanMode: Always
  natOutgoing: true
  nodeSelector: all()
---
apiVersion: projectcalico.org/v3
kind: BGPConfiguration
metadata:
  name: default
spec:
  logSeverityScreen: Info
  listenPort: 17900
EOF
fi

BUBBLE_ID_VAL=${BUBBLE_ID:-1}
NODE_NAME="$(hostname)"

# Start Calico Node
echo "Starting Calico Node for ${NODE_NAME}..."
mkdir -p /var/run/calico /var/lib/calico /var/log/calico

CALICO_ETCD="http://${CONTROLLER_IP}:2379"
if [ "$HPK_ROLE" = "controller" ]; then
    CALICO_ETCD="http://127.0.0.1:2379"
fi

export DATASTORE_TYPE=etcdv3
export ETCD_ENDPOINTS="${ETCD_ENDPOINTS:-$CALICO_ETCD}"

CALICO_IMAGE="docker://docker.io/calico/node:v3.28.0"

apptainer instance run \
  --no-mount home \
  --no-mount cwd \
  --no-mount hostfs \
  --writable-tmpfs \
  --bind /var/run/calico:/var/run/calico \
  --bind /var/lib/calico:/var/lib/calico \
  --bind /var/log/calico:/var/log/calico \
  --env DATASTORE_TYPE=etcdv3 \
  --env ETCD_ENDPOINTS=$CALICO_ETCD \
  --env BGP_PORT=17900 \
  --env FELIX_DEFAULTENDPOINTTOHOSTACTION=ACCEPT \
  --env FELIX_INTERFACEPREFIX=cali \
  --env FELIX_IPTABLESBACKEND=NFT \
  --env FELIX_VXLANPORT=4789 \
  --env CALICO_NETWORKING_BACKEND=bird \
  --env NO_DEFAULT_POOLS=true \
  --env NODENAME="${NODE_NAME}" \
  --env FELIX_FELIXHOSTNAME="${NODE_NAME}" \
  --env IP=${HOST_IP} \
  --env KUBERNETES_SERVICE_HOST=${CONTROLLER_IP} \
  --env KUBERNETES_SERVICE_PORT=6443 \
  $CALICO_IMAGE \
  calico-node

# Wait for Calico Node to allocate a block affinity for this node dynamically (indicated by a blackhole route in the kernel)
echo "Waiting for Calico to allocate an IPAM block for ${NODE_NAME}..."
POD_SUBNET=""
for i in {1..30}; do
    POD_SUBNET=$(ip route | awk '/blackhole/ {print $2; exit}')
    if [ -n "$POD_SUBNET" ]; then
        break
    fi
    sleep 1
done

if [ -z "$POD_SUBNET" ]; then
    echo "Error: Calico did not allocate an IPAM block in time."
    exit 1
fi
echo "Calico dynamically allocated subnet: ${POD_SUBNET}"

# Generate dynamic subnet config for CNI IPAM using the Calico-leased block
mkdir -p /run/calico
echo "CALICO_SUBNET=${POD_SUBNET}" > /run/calico/subnet.env
echo "CALICO_MTU=1500" >> /run/calico/subnet.env

# Configure iptables rules dynamically for the Calico-allocated subnet
echo "Configuring iptables NAT and FORWARD rules for ${POD_SUBNET}..."
iptables -t nat -C POSTROUTING -s ${POD_SUBNET} -o tap0 -j MASQUERADE 2>/dev/null || iptables -t nat -A POSTROUTING -s ${POD_SUBNET} -o tap0 -j MASQUERADE
iptables -C FORWARD -s ${POD_SUBNET} -j ACCEPT 2>/dev/null || iptables -I FORWARD 1 -s ${POD_SUBNET} -j ACCEPT
iptables -C FORWARD -d ${POD_SUBNET} -j ACCEPT 2>/dev/null || iptables -I FORWARD 1 -d ${POD_SUBNET} -j ACCEPT

# Start K3s if Controller
if [ "$HPK_ROLE" = "controller" ]; then
    echo "Starting K3s Server..."
    k3s server \
      --bind-address 0.0.0.0 \
      --advertise-address ${HOST_IP} \
      --tls-san ${HOST_IP} \
      --tls-san 0.0.0.0 \
      --tls-san 127.0.0.1 \
      --tls-san localhost \
      --disable-agent \
      --disable servicelb \
      --disable traefik \
      --disable local-storage \
      --disable metrics-server \
      --disable-cloud-controller \
      --kubelet-arg=resolv-conf=/etc/resolv.conf \
      --write-kubeconfig-mode 777 \
      --egress-selector-mode=disabled \
      --kube-apiserver-arg=kubelet-certificate-authority=/var/lib/rancher/k3s/server/tls/server-ca.crt \
      --kube-apiserver-arg=kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname \
      >> /var/log/k3s.log 2>&1 &
    
    # Wait for K3s to create kubeconfig and node-token
    echo "Waiting for K3s to initialize..."
    while [ ! -f /etc/rancher/k3s/k3s.yaml ]; do
        sleep 1
    done
    while [ ! -f /var/lib/rancher/k3s/server/node-token ]; do
        sleep 1
    done
    
    # Export server-ca to shared directory for node certificates FIRST
    echo "Copying server-ca to /var/lib/hpk/tls..."
    mkdir -p /var/lib/hpk/tls
    cp /var/lib/rancher/k3s/server/tls/server-ca.crt /var/lib/hpk/tls/server-ca.crt.tmp
    cp /var/lib/rancher/k3s/server/tls/server-ca.key /var/lib/hpk/tls/server-ca.key.tmp
    chmod 600 /var/lib/hpk/tls/server-ca.key.tmp
    mv /var/lib/hpk/tls/server-ca.key.tmp /var/lib/hpk/tls/server-ca.key
    mv /var/lib/hpk/tls/server-ca.crt.tmp /var/lib/hpk/tls/server-ca.crt

    # Copy kubeconfig and node-token to shared directory AFTER server-ca is ready
    echo "Copying kubeconfig and node-token to /var/lib/hpk..."
    cp /etc/rancher/k3s/k3s.yaml /var/lib/hpk/kubeconfig.tmp
    # Replace 0.0.0.0 or 127.0.0.1 in kubeconfig server URL with actual HOST_IP
    sed -i "s|https://0.0.0.0:6443|https://${HOST_IP}:6443|g" /var/lib/hpk/kubeconfig.tmp
    sed -i "s|https://127.0.0.1:6443|https://${HOST_IP}:6443|g" /var/lib/hpk/kubeconfig.tmp
    cp /var/lib/rancher/k3s/server/node-token /var/lib/hpk/node-token.tmp
    chmod 644 /var/lib/hpk/kubeconfig.tmp /var/lib/hpk/node-token.tmp
    mv /var/lib/hpk/node-token.tmp /var/lib/hpk/node-token
    mv /var/lib/hpk/kubeconfig.tmp /var/lib/hpk/kubeconfig

    # Make CoreDNS inherit the bubble resolver instead of the cluster DNS service IP.
    echo "Configuring CoreDNS to use the bubble resolver..."
    COREDNS_WAIT=0
    COREDNS_TIMEOUT=120
    while ! k3s kubectl -n kube-system get deployment coredns >/dev/null 2>&1; do
      if [ "$COREDNS_WAIT" -ge "$COREDNS_TIMEOUT" ]; then
        echo "ERROR: Timed out waiting for CoreDNS deployment" >&2
        break
      fi
      sleep 1
      COREDNS_WAIT=$((COREDNS_WAIT + 1))
    done
    while [ "$COREDNS_WAIT" -lt "$COREDNS_TIMEOUT" ] && ! k3s kubectl -n kube-system get configmap coredns >/dev/null 2>&1; do
      if [ "$COREDNS_WAIT" -ge "$COREDNS_TIMEOUT" ]; then
        echo "ERROR: Timed out waiting for CoreDNS configmap" >&2
        break
      fi
      sleep 1
      COREDNS_WAIT=$((COREDNS_WAIT + 1))
    done

    if k3s kubectl -n kube-system get deployment coredns >/dev/null 2>&1 && k3s kubectl -n kube-system get configmap coredns >/dev/null 2>&1; then
      k3s kubectl -n kube-system patch deployment coredns --type=merge -p '{"spec":{"template":{"spec":{"dnsPolicy":"Default"}}}}'
      CURRENT_COREFILE="$(k3s kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}')"
      UPDATED_COREFILE="$(printf '%s\n' "$CURRENT_COREFILE" | sed -E 's#forward \. (/etc/resolv\.conf|([0-9]{1,3}\.){3}[0-9]{1,3})#forward . /etc/resolv.conf#g')"
      if [ -n "$UPDATED_COREFILE" ] && [ "$CURRENT_COREFILE" != "$UPDATED_COREFILE" ]; then
        cat <<EOF | k3s kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns
  namespace: kube-system
data:
  Corefile: |
$(printf '%s\n' "$UPDATED_COREFILE" | sed 's/^/    /')
EOF
      fi
      k3s kubectl -n kube-system rollout restart deployment coredns
    else
      echo "Skipping CoreDNS reconfiguration due to missing deployment or configmap" >&2
    fi
fi

# Wait for kubeconfig, node-token, and server-ca, ensuring server-ca matches kubeconfig's cluster CA
echo "Waiting for /var/lib/hpk/kubeconfig, /var/lib/hpk/node-token, and matching server-ca..."
while true; do
  if [ -f /var/lib/hpk/kubeconfig ] && [ -f /var/lib/hpk/node-token ] && \
     [ -f /var/lib/hpk/tls/server-ca.crt ] && [ -f /var/lib/hpk/tls/server-ca.key ]; then
    KUBECONFIG_CA_HASH=$(grep 'certificate-authority-data:' /var/lib/hpk/kubeconfig 2>/dev/null | awk '{print $2}' | base64 -d 2>/dev/null | sha256sum | awk '{print $1}')
    SERVER_CA_HASH=$(sha256sum /var/lib/hpk/tls/server-ca.crt 2>/dev/null | awk '{print $1}')
    if [ -n "$KUBECONFIG_CA_HASH" ] && [ -n "$SERVER_CA_HASH" ] && [ "$KUBECONFIG_CA_HASH" = "$SERVER_CA_HASH" ]; then
      break
    fi
  fi
  sleep 1
done


# Generate per-node webhook certificate for hpk-kubelet with node IP SAN
NODE_NAME="$(hostname)"
NODE_CERT_DIR="/var/lib/hpk/.certs/${NODE_NAME}"
mkdir -p "${NODE_CERT_DIR}"

cat > /tmp/kubelet.cnf <<EOF
[req]
req_extensions = v3_req
distinguished_name = req_distinguished_name

[req_distinguished_name]

[v3_req]
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth, clientAuth
subjectAltName = @alt_names

[alt_names]
IP.1 = 127.0.0.1
IP.2 = ${HOST_IP}
EOF

if [ ! -f "${NODE_CERT_DIR}/kubelet.key" ]; then
    openssl genrsa -out "${NODE_CERT_DIR}/kubelet.key" 2048
fi
openssl req -new -key "${NODE_CERT_DIR}/kubelet.key" -subj "/CN=hpk-kubelet" \
  -out /tmp/kubelet.csr -config /tmp/kubelet.cnf
openssl x509 -req -days 365 -set_serial 01 \
  -CA /var/lib/hpk/tls/server-ca.crt -CAkey /var/lib/hpk/tls/server-ca.key \
  -in /tmp/kubelet.csr -out "${NODE_CERT_DIR}/kubelet.crt" \
  -extfile /tmp/kubelet.cnf -extensions v3_req
chmod 644 "${NODE_CERT_DIR}/kubelet.crt" "${NODE_CERT_DIR}/kubelet.key"

# Wait for kube-dns service (Controller creates it via K3s, Nodes wait for it)
echo "Waiting for kube-dns service..."
export KUBECONFIG=/var/lib/hpk/kubeconfig
while ! k3s kubectl get service -n kube-system kube-dns >/dev/null 2>&1; do
  sleep 1
done

echo "Starting hpk-kubelet..."
# Using --apptainer=hpktainer to use our networking wrapper

# Set pause container path based on development mode
if [ "${HPK_DEV:-0}" = "1" ]; then
    PAUSE_IMAGE="/var/lib/hpk/images/hpk-pause.sif"
else
    PAUSE_IMAGE=""
fi

KUBECONFIG=/var/lib/hpk/kubeconfig \
APISERVER_KEY_LOCATION="${NODE_CERT_DIR}/kubelet.key" \
APISERVER_CERT_LOCATION="${NODE_CERT_DIR}/kubelet.crt" \
VKUBELET_ADDRESS=${HOST_IP} \
hpk-kubelet \
  --apptainer=hpktainer \
  --nodename=$(hostname) \
  --disable-taint=true \
  ${PAUSE_IMAGE:+--pause-image=$PAUSE_IMAGE} \
  >> /var/log/hpk-kubelet.log 2>&1 &

echo "Starting kube-proxy..."
kube-proxy \
  --kubeconfig /var/lib/hpk/kubeconfig \
  --proxy-mode iptables \
  --hostname-override $(hostname) \
  --conntrack-max-per-core=0 \
  --conntrack-tcp-timeout-established=0 \
  --conntrack-tcp-timeout-close-wait=0 \
  >> /var/log/kube-proxy.log 2>&1 &

# Keep the container running
if [ "$#" -eq 0 ]; then
    # Default to bash
    exec /bin/bash
else
    exec "$@"
fi
