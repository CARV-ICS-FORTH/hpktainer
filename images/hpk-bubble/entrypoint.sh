#!/bin/bash

# Default values if not provided
HOST_IP=${HOST_IP:-$(ip route get 1 | awk '{print $7; exit}')}
CONTROLLER_IP=${CONTROLLER_IP:-$HOST_IP}

echo "Starting Flannel..."
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
# This is required for Flannel VXLAN to use it as a source IP
ip addr add ${HOST_IP}/32 dev tap0

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
    
    # Wait for etcd
    sleep 5
    
    # Initialize Flannel config
    echo "Initializing Flannel config in Etcd..."
    etcdctl --endpoints=http://127.0.0.1:2379 put /coreos.com/network/config '{"Network": "10.244.0.0/16", "SubnetLen": 24, "Backend": {"Type": "vxlan"}}'
fi

# Start flanneld
# Connecting to CONTROLLER_IP (which is host IP of controller, or localhost if controller)
# Simplest: If Controller, use localhost for Flannel. If Node, use CONTROLLER_IP.

FLANNEL_ETCD="http://${CONTROLLER_IP}:2379"
if [ "$HPK_ROLE" = "controller" ]; then
    FLANNEL_ETCD="http://127.0.0.1:2379"
fi

flanneld \
  --etcd-endpoints=$FLANNEL_ETCD \
  -ip-masq \
  -iface tap0 \
  --public-ip=${HOST_IP} \
  >> /var/log/flannel.log 2>&1 &

FLANNEL_PID=$!
echo "Flannel started with PID $FLANNEL_PID"

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
    
    # Copy kubeconfig and node-token to shared directory
    echo "Copying kubeconfig and node-token to /var/lib/hpk..."
    cp /etc/rancher/k3s/k3s.yaml /var/lib/hpk/kubeconfig
    # Replace 0.0.0.0 or 127.0.0.1 in kubeconfig server URL with actual HOST_IP
    sed -i "s|https://0.0.0.0:6443|https://${HOST_IP}:6443|g" /var/lib/hpk/kubeconfig
    sed -i "s|https://127.0.0.1:6443|https://${HOST_IP}:6443|g" /var/lib/hpk/kubeconfig
    cp /var/lib/rancher/k3s/server/node-token /var/lib/hpk/node-token
    chmod 644 /var/lib/hpk/kubeconfig /var/lib/hpk/node-token
    
    # Generate webhook certificate for hpk-kubelet
    echo "Generating webhook certificate..."
    
    # Build alt_names section with all node IPs
    ALT_NAMES="IP.1 = 127.0.0.1
IP.2 = ${HOST_IP}"
    
    # Auto-generate node IPs based on NUM_NODES
    NUM_NODES=${NUM_NODES:-1}
    if [ "$NUM_NODES" -gt 1 ]; then
        # Extract the base IP and last octet from HOST_IP
        BASE_IP=$(echo $HOST_IP | sed 's/\.[0-9]*$/\./')
        LAST_OCTET=$(echo $HOST_IP | awk -F'.' '{print $NF}')
        
        counter=3
        for ((i=1; i<NUM_NODES; i++)); do
            NODE_IP="${BASE_IP}$((LAST_OCTET + i))"
            ALT_NAMES="${ALT_NAMES}
IP.${counter} = ${NODE_IP}"
            ((counter++))
        done
    fi
    
    cat >kubelet.cnf <<EOF
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
${ALT_NAMES}
EOF
    TLS_PATH=/var/lib/rancher/k3s/server/tls
    if [ ! -f kubelet.key ]; then openssl genrsa -out kubelet.key 2048; fi
    openssl req -new -key kubelet.key -subj "/CN=hpk-kubelet" \
      -out kubelet.csr -config kubelet.cnf
    openssl x509 -req -days 365 -set_serial 01 \
      -CA ${TLS_PATH}/server-ca.crt -CAkey ${TLS_PATH}/server-ca.key \
      -in kubelet.csr -out kubelet.crt \
      -extfile kubelet.cnf -extensions v3_req
    
    # Copy certificates to shared directory
    echo "Copying certificates to /var/lib/hpk..."
    cp kubelet.crt /var/lib/hpk/
    cp kubelet.key /var/lib/hpk/
    chmod 644 /var/lib/hpk/kubelet.crt /var/lib/hpk/kubelet.key
fi

# Wait for kubeconfig and node-token (Controller creates them, Nodes wait for them)
echo "Waiting for /var/lib/hpk/kubeconfig and /var/lib/hpk/node-token..."
while [ ! -f /var/lib/hpk/kubeconfig ] || [ ! -f /var/lib/hpk/node-token ]; do
  sleep 1
done

# Wait for certificates (Controller creates them, Nodes wait for them)
echo "Waiting for /var/lib/hpk/kubelet.crt and /var/lib/hpk/kubelet.key..."
while [ ! -f /var/lib/hpk/kubelet.crt ] || [ ! -f /var/lib/hpk/kubelet.key ]; do
  sleep 1
done

# Wait for kube-dns service (Controller creates it via K3s, Nodes wait for it)
echo "Waiting for kube-dns service..."
export KUBECONFIG=/var/lib/hpk/kubeconfig
while ! k3s kubectl get service -n kube-system kube-dns >/dev/null 2>&1; do
  sleep 1
done

echo "Starting hpk-kubelet..."
# Using --run-slurm=false to run locally
# Using --apptainer=hpktainer to use our networking wrapper

# Set pause container path based on development mode
if [ "${HPK_DEV:-0}" = "1" ]; then
    PAUSE_IMAGE="/var/lib/hpk/images/hpk-pause.sif"
else
    PAUSE_IMAGE=""
fi

KUBECONFIG=/var/lib/hpk/kubeconfig \
APISERVER_KEY_LOCATION=/var/lib/hpk/kubelet.key \
APISERVER_CERT_LOCATION=/var/lib/hpk/kubelet.crt \
VKUBELET_ADDRESS=${HOST_IP} \
hpk-kubelet \
  --run-slurm=false \
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
