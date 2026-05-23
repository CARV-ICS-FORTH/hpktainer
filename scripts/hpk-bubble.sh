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

cleanup() {
	echo "Cleaning up..."
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
    IMAGE_SIF="$IMAGE_DIR/hpk-bubble.sif"
    
    if [ ! -f "$IMAGE_SIF" ]; then
        echo "Error: $IMAGE_SIF not found. Run 'make develop' first."
        exit 1
    fi
    
    BUBBLE_IMAGE="$IMAGE_SIF"
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
	--bind $HOME/.apptainer/cache:/root/.apptainer/cache \
	--env HOST_IP=$HOST_IP_DETECTED \
	--env CONTROLLER_IP=$CONTROLLER_IP \
	--env HPK_DEV=${HPK_DEV:-0} \
	--env BUBBLE_ID=$BUBBLE_ID \
	$BUBBLE_IMAGE \
	$NAME
PID=$(apptainer instance list -j $NAME | jq -r '.instances[] | .pid')

# Userlevel networking
slirp4netns --configure --cidr=$CIDR/24 --mtu=65520 --api-socket $NAME-slirp4netns.sock $PID tap0 &
SLIRP_PID=$!

# Forward ports based on Role
# 17900: Calico BGP (TCP) - All
# 4789: Calico VXLAN (UDP) - All
# 6443: K3s API (TCP) - Controller
# 2379: Etcd (TCP) - Controller

# Construct JSON for hostfwd
# Always forward 17900 TCP and 4789 UDP to the container's Host IP address (which Calico listens on)
FWD_JSON_BGP='{"execute": "add_hostfwd", "arguments": {"proto": "tcp", "host_addr": "0.0.0.0", "host_port": 17900, "guest_addr": "'$HOST_IP_DETECTED'", "guest_port": 17900}}'
FWD_JSON_VXLAN='{"execute": "add_hostfwd", "arguments": {"proto": "udp", "host_addr": "0.0.0.0", "host_port": 4789, "guest_addr": "'$HOST_IP_DETECTED'", "guest_port": 4789}}'

HPK_ROLE=${HPK_ROLE:-controller}

if [ "$HPK_ROLE" = "controller" ]; then
    # Add K3s 6443
    FWD_JSON_K3S='{"execute": "add_hostfwd", "arguments": {"proto": "tcp", "host_addr": "0.0.0.0", "host_port": 6443, "guest_addr": "'$HOST_IP_DETECTED'", "guest_port": 6443}}'
    
    # Add Etcd 2379
    FWD_JSON_ETCD='{"execute": "add_hostfwd", "arguments": {"proto": "tcp", "host_addr": "0.0.0.0", "host_port": 2379, "guest_addr": "'$HOST_IP_DETECTED'", "guest_port": 2379}}'
fi

while [ ! -e $NAME-slirp4netns.sock ]; do
    sleep 1
done

echo -n "$FWD_JSON_BGP" | nc -U $NAME-slirp4netns.sock
sleep 0.1
echo -n "$FWD_JSON_VXLAN" | nc -U $NAME-slirp4netns.sock
if [ "$HPK_ROLE" = "controller" ]; then
    sleep 0.1
    echo -n "$FWD_JSON_K3S" | nc -U $NAME-slirp4netns.sock
    sleep 0.1
    echo -n "$FWD_JSON_ETCD" | nc -U $NAME-slirp4netns.sock
fi

echo "Bubble started. Press Ctrl+C to stop."
wait $SLIRP_PID
