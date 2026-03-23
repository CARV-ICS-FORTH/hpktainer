#!/bin/bash -x

BUBBLE_ID=${1:-1}

NAME=bubble$BUBBLE_ID

CIDR=10.0.$((BUBBLE_ID + 1)).0
CIDR_PREFIX=$(echo "$CIDR" | awk -F. '{print $1"."$2"."$3}')
DNS_ADDR=$CIDR_PREFIX.3
NS_ADDR=$CIDR_PREFIX.100

# Detect Host IP (first non-loopback)
HOST_IP_DETECTED=$(ip route get 1 | awk '{print $7; exit}')
# Use provided CONTROLLER_IP or default to detected host IP
CONTROLLER_IP=${CONTROLLER_IP:-$HOST_IP_DETECTED}
API_RELAY_PID=""

cleanup() {
	echo "Cleaning up..."
    if [[ -n $API_RELAY_PID ]]; then
        kill $API_RELAY_PID 2>/dev/null
        wait $API_RELAY_PID 2>/dev/null
    fi
    pkill -f "HPK_API_RELAY_${NAME}" 2>/dev/null || true
	if [[ -n $SLIRP_PID ]]; then
		kill $SLIRP_PID 2>/dev/null
		wait $SLIRP_PID 2>/dev/null
	fi
	apptainer instance stop $NAME
	[ -e $NAME-slirp4netns.sock ] && rm -f $NAME-slirp4netns.sock
    [ -e resolv.conf.$NAME ] && rm -f resolv.conf.$NAME
}

trap cleanup INT TERM

# Namespace
RESOLV_CONF=resolv.conf.$NAME
echo "nameserver $DNS_ADDR" > $RESOLV_CONF

mkdir -p $HOME/.hpk
echo "Starting Bubble $NAME..."
echo "  CIDR: $CIDR"
echo "  Host IP: $HOST_IP_DETECTED"
echo "  Controller IP: $CONTROLLER_IP"

# Determine image source
if [ "${HPK_DEV:-0}" = "1" ]; then
    echo "  Development mode: using local images"
    IMAGE_DIR="$HOME/.hpk/images"
    IMAGE_TAR="$IMAGE_DIR/hpk-bubble.tar"
    IMAGE_SIF="$IMAGE_DIR/hpk-bubble.sif"
    
    # Convert tar to sif if not already done
    if [ ! -f "$IMAGE_SIF" ] && [ -f "$IMAGE_TAR" ]; then
        echo "  Converting $IMAGE_TAR to $IMAGE_SIF..."
        apptainer build "$IMAGE_SIF" "docker-archive://$IMAGE_TAR"
    fi
    
    if [ ! -f "$IMAGE_SIF" ]; then
        echo "Error: $IMAGE_SIF not found. Run 'make develop' first."
        exit 1
    fi
    
    BUBBLE_IMAGE="$IMAGE_SIF"

    # Also handle hpk-pause image
    PAUSE_IMAGE_TAR="$IMAGE_DIR/hpk-pause.tar"
    PAUSE_IMAGE_SIF="$IMAGE_DIR/hpk-pause.sif"

    if [ ! -f "$PAUSE_IMAGE_SIF" ] && [ -f "$PAUSE_IMAGE_TAR" ]; then
        echo "  Converting $PAUSE_IMAGE_TAR to $PAUSE_IMAGE_SIF..."
        apptainer build "$PAUSE_IMAGE_SIF" "docker-archive://$PAUSE_IMAGE_TAR"
    fi
else
    BUBBLE_IMAGE="docker://docker.io/chazapis/hpk-bubble:latest"
fi

# Pass IPs as env variables
apptainer instance run \
	--fakeroot \
	--no-mount home \
	--no-mount cwd \
	--no-mount hostfs \
	--writable-tmpfs \
	--network=none \
	--bind $RESOLV_CONF:/etc/resolv.conf \
	--bind $HOME/.hpk:/var/lib/hpk \
    --bind $HOME/.hpk:/root/.hpk \
    --env APPTAINER_CACHEDIR=/root/.hpk/.apptainer/cache \
    --env APPTAINER_TMPDIR=/root/.hpk/.apptainer/tmp \
    --env SINGULARITY_CACHEDIR=/root/.hpk/.apptainer/cache \
    --env SINGULARITY_TMPDIR=/root/.hpk/.apptainer/tmp \
    --env TMPDIR=/root/.hpk/.apptainer/tmp \
	--env HOST_IP=$HOST_IP_DETECTED \
	--env CONTROLLER_IP=$CONTROLLER_IP \
	--env HPK_DEV=${HPK_DEV:-0} \
	--env NUM_NODES=${NUM_NODES:-1} \
	$BUBBLE_IMAGE \
	$NAME
PID=$(apptainer instance list -j $NAME | jq -r '.instances[] | .pid')

# Userlevel networking
slirp4netns --configure --cidr=$CIDR/24 --mtu=1500 --api-socket $NAME-slirp4netns.sock $PID tap0 &
SLIRP_PID=$!

# Forward ports based on Role
# 8472: Flannel VXLAN (UDP) - All
# 10250: Kubelet (TCP) - All
# 6443: K3s API (TCP) - Controller
# 2379: Etcd (TCP) - Controller

# Construct JSON for hostfwd
# Always forward 8472 UDP
FWD_JSON_FLANNEL='{"execute": "add_hostfwd", "arguments": {"proto": "udp", "host_addr": "'$HOST_IP_DETECTED'", "host_port": 8472, "guest_addr": "'$NS_ADDR'", "guest_port": 8472}}'

# Always forward 10250 TCP (kubelet)
FWD_JSON_KUBELET='{"execute": "add_hostfwd", "arguments": {"proto": "tcp", "host_addr": "'$HOST_IP_DETECTED'", "host_port": 10250, "guest_addr": "'$NS_ADDR'", "guest_port": 10250}}'

HPK_ROLE=${HPK_ROLE:-controller}

if [ "$HPK_ROLE" = "controller" ]; then
    # Forward K3s API to localhost first; then relay HOST_IP:6443 -> 127.0.0.1:16443.
    FWD_JSON_K3S='{"execute": "add_hostfwd", "arguments": {"proto": "tcp", "host_addr": "127.0.0.1", "host_port": 16443, "guest_addr": "'$NS_ADDR'", "guest_port": 6443}}'
    
    # Add Etcd 2379
    FWD_JSON_ETCD='{"execute": "add_hostfwd", "arguments": {"proto": "tcp", "host_addr": "'$HOST_IP_DETECTED'", "host_port": 2379, "guest_addr": "'$NS_ADDR'", "guest_port": 2379}}'
fi

while [ ! -e $NAME-slirp4netns.sock ]; do
    sleep 1
done

echo -n "$FWD_JSON_FLANNEL" | nc -U $NAME-slirp4netns.sock
sleep 0.1
echo -n "$FWD_JSON_KUBELET" | nc -U $NAME-slirp4netns.sock
if [ "$HPK_ROLE" = "controller" ]; then
    sleep 0.1
    echo -n "$FWD_JSON_K3S" | nc -U $NAME-slirp4netns.sock
    sleep 0.1
    echo -n "$FWD_JSON_ETCD" | nc -U $NAME-slirp4netns.sock

    # Ensure no stale relay remains from previous runs.
    pkill -f "HPK_API_RELAY_${NAME}" 2>/dev/null || true

    # Rootless TCP relay so HOST_IP:6443 works from this host and other nodes.
    python3 - "HPK_API_RELAY_${NAME}" <<PY &
import socket
import threading

LISTEN_HOST = "${HOST_IP_DETECTED}"
LISTEN_PORT = 6443
TARGET_HOST = "127.0.0.1"
TARGET_PORT = 16443

def pump(src, dst):
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except Exception:
        pass
    finally:
        try:
            dst.shutdown(socket.SHUT_WR)
        except Exception:
            pass

def handle(client):
    try:
        upstream = socket.create_connection((TARGET_HOST, TARGET_PORT), timeout=30)
    except Exception:
        try:
            client.close()
        except Exception:
            pass
        return

    t1 = threading.Thread(target=pump, args=(client, upstream), daemon=True)
    t2 = threading.Thread(target=pump, args=(upstream, client), daemon=True)
    t1.start()
    t2.start()
    t1.join()
    t2.join()

    try:
        upstream.close()
    except Exception:
        pass
    try:
        client.close()
    except Exception:
        pass

server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
server.bind((LISTEN_HOST, LISTEN_PORT))
server.listen(128)

while True:
    client, _ = server.accept()
    threading.Thread(target=handle, args=(client,), daemon=True).start()
PY
    API_RELAY_PID=$!
    echo "Started API relay ${HOST_IP_DETECTED}:6443 -> 127.0.0.1:16443 (pid ${API_RELAY_PID})"
fi

echo "Bubble started. Press Ctrl+C to stop."
wait $SLIRP_PID
