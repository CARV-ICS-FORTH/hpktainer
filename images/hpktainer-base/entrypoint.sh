#!/bin/sh
set -e

# Default values or env vars checks?
if [ -z "$HPK_IP" ] || [ -z "$HPK_GATEWAY_IP" ] || [ -z "$HPK_SOCKET_PATH" ]; then
    echo "Error: HPK environment variables missing."
    echo "HPK_IP=$HPK_IP"
    echo "HPK_GATEWAY_IP=$HPK_GATEWAY_IP"
    echo "HPK_SOCKET_PATH=$HPK_SOCKET_PATH"
    # Fallback or exit? If user runs without hpktainer wrapper, this fails.
    # We might just exec "$@" if specific vars are missing to allow non-hpk usage?
    # But this image is specifically for hpktainer.
    exit 1
fi

echo "Starting hpk-net-daemon..."
HPK_MTU=${HPK_MTU:-1500}
# Run in background. Daemon will create tap0 and connect to socket.
hpk-net-daemon -mode client -socket "$HPK_SOCKET_PATH" -tap tap0 -create-tap -mtu "$HPK_MTU" &
DAEMON_PID=$!

# Wait for tap0 to be created by daemon
TRIES=0
while ! ip link show tap0 >/dev/null 2>&1; do
    sleep 0.1
    TRIES=$((TRIES+1))
    if [ $TRIES -gt 50 ]; then
        echo "Timeout waiting for tap0 creation"
        exit 1
    fi
done

echo "Configuring network..."
# HPK_IP is expected to be CIDR (e.g. 10.244.0.2/24)
ip addr add "$HPK_IP" dev tap0
ip link set tap0 mtu "$HPK_MTU" up
ip route add "$HPK_GATEWAY_IP" dev tap0 scope link
ip route add default via "$HPK_GATEWAY_IP" dev tap0

# Set up trap to clean up the client-side hpk-net-daemon when we exit
cleanup() {
    echo "Stopping hpk-net-daemon..."
    kill "$DAEMON_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "Network ready. Executing command: $@"
"$@"

