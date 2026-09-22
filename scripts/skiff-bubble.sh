#!/bin/bash
set -euo pipefail

BUBBLE_ID=${1:-1}

NAME="bubble$BUBBLE_ID"

CIDR="10.0.$((BUBBLE_ID + 1)).0"
CIDR_PREFIX=$(echo "$CIDR" | awk -F. '{print $1"."$2"."$3}')
DNS_ADDR="$CIDR_PREFIX.3"
NS_ADDR="$CIDR_PREFIX.100"

# Detect Host IP (first non-loopback)
HOST_IP_DETECTED=$(ip route get 1 | awk '{print $7; exit}')
# Use provided CONTROLLER_IP or default to detected host IP
CONTROLLER_IP=${CONTROLLER_IP:-$HOST_IP_DETECTED}

SOCAT_PID_6443=""
SOCAT_PID_10250=""
SLIRP_PID=""

start_socat_relays() {
    if ! command -v socat >/dev/null 2>&1; then
        echo "socat command not found; skipping host-ip relay setup"
        return 0
    fi

    socat TCP4-LISTEN:6443,bind="${HOST_IP_DETECTED}",reuseaddr,fork TCP4:127.0.0.1:6443 &
    SOCAT_PID_6443=$!

    socat TCP4-LISTEN:10250,bind="${HOST_IP_DETECTED}",reuseaddr,fork TCP4:127.0.0.1:10250 &
    SOCAT_PID_10250=$!

    echo "Started socat relays on ${HOST_IP_DETECTED} (6443/tcp, 10250/tcp)"
}

cleanup() {
    trap - EXIT INT TERM
    echo "Cleaning up..."
    if [[ -n ${SOCAT_PID_6443:-} ]]; then
        kill "$SOCAT_PID_6443" 2>/dev/null || true
        wait "$SOCAT_PID_6443" 2>/dev/null || true
    fi
    if [[ -n ${SOCAT_PID_10250:-} ]]; then
        kill "$SOCAT_PID_10250" 2>/dev/null || true
        wait "$SOCAT_PID_10250" 2>/dev/null || true
    fi

    if [[ -n ${SLIRP_PID:-} ]]; then
        kill "$SLIRP_PID" 2>/dev/null || true
        wait "$SLIRP_PID" 2>/dev/null || true
    fi
    apptainer instance stop "$NAME" 2>/dev/null || true
    [ -e "$NAME-slirp4netns.sock" ] && rm -f "$NAME-slirp4netns.sock" || true
}

trap cleanup EXIT INT TERM

mkdir -p "$HOME/.skiff"
mkdir -p "$HOME/.skiff/.apptainer/tmp"
mkdir -p "$HOME/.skiff/.apptainer/cache"
echo "Starting Bubble $NAME..."
echo "  CIDR: $CIDR"
echo "  Host IP: $HOST_IP_DETECTED"
echo "  Controller IP: $CONTROLLER_IP"

# Determine image source
if [ "${SKIFF_DEV:-0}" = "1" ]; then
    echo "  Development mode: using local images"
    IMAGE_DIR="$HOME/.skiff/images"
    IMAGE_SIF="$IMAGE_DIR/skiff-bubble.sif"
    
    if [ ! -f "$IMAGE_SIF" ]; then
        echo "Error: $IMAGE_SIF not found. Run 'make develop' first."
        exit 1
    fi
    
    BUBBLE_IMAGE="$IMAGE_SIF"
else
    BUBBLE_IMAGE="docker://docker.io/chazapis/skiff-bubble:latest"
fi

# Ensure required Apptainer cache and tmp directories exist
mkdir -p /tmp/.skiff/.apptainer/tmp "$HOME/.skiff/.apptainer/cache" "$HOME/.apptainer/cache"

BIN_BINDS=()
for b in skifflet plaidd plaid plaidctl plaidtainer; do
    if [ -f "$HOME/.skiff/bin/$b" ]; then
        BIN_BINDS+=(
            --bind "$HOME/.skiff/bin/$b:/usr/bin/$b"
            --bind "$HOME/.skiff/bin/$b:/usr/local/bin/$b"
        )
    fi
done
if [ -f "$HOME/skiff/scripts/entrypoint.sh" ]; then
    BIN_BINDS+=(
        --bind "$HOME/skiff/scripts/entrypoint.sh:/entrypoint.sh"
    )
elif [ -f "$HOME/skiff/images/skiff-bubble/entrypoint.sh" ]; then
    BIN_BINDS+=(
        --bind "$HOME/skiff/images/skiff-bubble/entrypoint.sh:/entrypoint.sh"
    )
fi

# Pass IPs as env variables
apptainer instance run \
	--fakeroot \
	--no-mount home \
	--no-mount cwd \
	--no-mount hostfs \
	--writable-tmpfs \
	--network=none \
	--dns "$DNS_ADDR" \
	--bind "$HOME/.skiff:/var/lib/skiff" \
    --bind "$HOME/.skiff:/root/.skiff" \
    --bind "$HOME/.apptainer/cache:/root/.apptainer/cache" \
    "${BIN_BINDS[@]}" \
    --env APPTAINER_CACHEDIR=/root/.skiff/.apptainer/cache \
    --env APPTAINER_TMPDIR=/tmp/.skiff/.apptainer/tmp \
    --env SINGULARITY_CACHEDIR=/root/.skiff/.apptainer/cache \
    --env SINGULARITY_TMPDIR=/tmp/.skiff/.apptainer/tmp \
    --env TMPDIR=/tmp/.skiff/.apptainer/tmp \
	--env HOST_IP="$HOST_IP_DETECTED" \
	--env CONTROLLER_IP="$CONTROLLER_IP" \
	--env SKIFF_DEV="${SKIFF_DEV:-0}" \
    --env BUBBLE_ID="$BUBBLE_ID" \
	"$BUBBLE_IMAGE" \
	"$NAME"

PID=""
for i in {1..10}; do
    PID=$(apptainer instance list -j "$NAME" 2>/dev/null | jq -r '.instances[]? | select(.instance=="'"$NAME"'") | .pid // empty')
    if [ -n "$PID" ] && [ "$PID" != "null" ]; then
        break
    fi
    sleep 0.5
done

if [ -z "$PID" ] || [ "$PID" = "null" ]; then
    echo "Error: Failed to retrieve PID for Apptainer instance $NAME" >&2
    exit 1
fi

# Userlevel networking
slirp4netns --configure --cidr="$CIDR/24" --mtu=1500 --api-socket "$NAME-slirp4netns.sock" "$PID" tap0 &
SLIRP_PID=$!

# Forward ports based on Role
# 8472: Plaid VXLAN (UDP) - All
# 10250: Kubelet (TCP) - All
# 6443: K3s API (TCP) - Controller

# Construct JSON for hostfwd
# Always forward 8472 UDP to the container's Host IP address (which Plaid listens on)
FWD_JSON_VXLAN='{"execute": "add_hostfwd", "arguments": {"proto": "udp", "host_addr": "0.0.0.0", "host_port": 8472, "guest_addr": "'$HOST_IP_DETECTED'", "guest_port": 8472}}'

# Always forward 10250 TCP (kubelet)
FWD_JSON_KUBELET='{"execute": "add_hostfwd", "arguments": {"proto": "tcp", "host_addr": "127.0.0.1", "host_port": 10250, "guest_addr": "'$NS_ADDR'", "guest_port": 10250}}'

SKIFF_ROLE=${SKIFF_ROLE:-controller}

if [ "$SKIFF_ROLE" = "controller" ]; then
    # Add K3s 6443
    FWD_JSON_K3S='{"execute": "add_hostfwd", "arguments": {"proto": "tcp", "host_addr": "127.0.0.1", "host_port": 6443, "guest_addr": "'$NS_ADDR'", "guest_port": 6443}}'
fi

# Wait for slirp4netns socket to be ready and accept connections
while ! echo -n "$FWD_JSON_VXLAN" | nc -U "$NAME-slirp4netns.sock" 2>/dev/null; do
    sleep 0.5
done
sleep 0.1
echo -n "$FWD_JSON_KUBELET" | nc -U "$NAME-slirp4netns.sock"
if [ "$SKIFF_ROLE" = "controller" ]; then
    sleep 0.1
    echo -n "$FWD_JSON_K3S" | nc -U "$NAME-slirp4netns.sock"
fi

start_socat_relays

echo "Bubble started. Press Ctrl+C to stop."
wait "$SLIRP_PID"
