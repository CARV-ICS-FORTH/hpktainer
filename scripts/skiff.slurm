#!/bin/bash
#SBATCH --job-name=skiff-cluster
#SBATCH --output=%x-%j.out
#SBATCH --nodes=1
#SBATCH --ntasks-per-node=1
#SBATCH --mem=1500M

# Usage: sbatch skiff.slurm
# The number of nodes allocated (N) determines the cluster size:
# Node 0: Controller Bubble (ID 1)
# Node 1..N-1: Node (Worker) Bubbles (ID 2..N)

NODES=($(scontrol show hostnames $SLURM_JOB_NODELIST))
NUM_NODES=${#NODES[@]}

if [ "$NUM_NODES" -eq 0 ]; then
    echo "This script must be run via sbatch/srun."
    exit 1
fi

CONTROLLER_NODE=${NODES[0]}
CONTROLLER_IP=$(srun --nodes=1 -w $CONTROLLER_NODE ip route get 1 | awk '{print $7; exit}')

echo "Cluster Setup:"
echo "  Controller Node: $CONTROLLER_NODE ($CONTROLLER_IP)"
echo "  Nodes: ${NODES[@]}"

# Clean up stale config files (but keep images for dev mode)
echo "Cleaning up stale configuration in ~/.skiff..."
rm -rf $HOME/.skiff/kubeconfig $HOME/.skiff/node-token $HOME/.skiff/tls $HOME/.skiff/.certs

# Pre-process images
echo "Pre-processing images..."
IMAGE_DIR="$HOME/.skiff/images"
mkdir -p "$IMAGE_DIR"

if [ "${SKIFF_DEV:-0}" = "1" ]; then
    echo "Development mode: ensuring local .tar images are converted to .sif..."
    for img in skiff-bubble; do
        IMAGE_TAR="$IMAGE_DIR/${img}.tar"
        IMAGE_SIF="$IMAGE_DIR/${img}.sif"
        if [ -f "$IMAGE_TAR" ]; then
            if [ ! -f "$IMAGE_SIF" ] || [ "$IMAGE_TAR" -nt "$IMAGE_SIF" ]; then
                echo "Converting $IMAGE_TAR to $IMAGE_SIF..."
                apptainer build --force "$IMAGE_SIF" "docker-archive://$IMAGE_TAR"
            fi
        else
            echo "Warning: $IMAGE_TAR not found!"
        fi
    done
    
else
    echo "Production mode: pre-warming Apptainer cache..."
    apptainer exec docker://docker.io/chazapis/skiff-bubble:latest /bin/true 2>/dev/null || true
    apptainer exec docker://registry.k8s.io/pause:3.10 /bin/true 2>/dev/null || true
fi

SCRIPT_DIR="${SLURM_SUBMIT_DIR:-$HOME/skiff}"
if [ -f "$SCRIPT_DIR/scripts/skiff-bubble.sh" ]; then
    SCRIPT_DIR="$SCRIPT_DIR/scripts"
elif [ -f "$HOME/skiff/scripts/skiff-bubble.sh" ]; then
    SCRIPT_DIR="$HOME/skiff/scripts"
elif [ ! -f "$SCRIPT_DIR/skiff-bubble.sh" ]; then
    SCRIPT_DIR="$HOME/skiff"
fi

# Start Controller Bubble
echo "Starting Controller on $CONTROLLER_NODE..."
export SKIFF_ROLE="controller"
export CONTROLLER_IP=$CONTROLLER_IP
export SKIFF_DEV=${SKIFF_DEV:-0}
export NUM_NODES=$NUM_NODES
srun --nodes=1 -w $CONTROLLER_NODE --exclusive "$SCRIPT_DIR/skiff-bubble.sh" 1 &

# Wait for Controller initialization
echo "Waiting for Controller initialization..."
WAIT_TIME=0
MAX_WAIT=300
while [ ! -f "$HOME/.skiff/kubeconfig" ] || [ ! -f "$HOME/.skiff/node-token" ] || [ ! -f "$HOME/.skiff/tls/server-ca.crt" ]; do
    if [ $WAIT_TIME -ge $MAX_WAIT ]; then
        echo "Error: Controller failed to initialize within ${MAX_WAIT} seconds." >&2
        exit 1
    fi
    sleep 1
    WAIT_TIME=$((WAIT_TIME + 1))
done
echo "Controller ready after ${WAIT_TIME}s."

# Start Node (Worker) Bubbles
BUBBLE_ID=2
for ((i=1; i<NUM_NODES; i++)); do
    NODE=${NODES[$i]}
    echo "Starting Node on $NODE (Bubble ID $BUBBLE_ID)..."
    
    (
        export SKIFF_ROLE="node"
        export CONTROLLER_IP=$CONTROLLER_IP
        export SKIFF_DEV=${SKIFF_DEV:-0}
        export NUM_NODES=$NUM_NODES
        srun --nodes=1 -w $NODE --exclusive "$SCRIPT_DIR/skiff-bubble.sh" $BUBBLE_ID
    ) &
    
    ((BUBBLE_ID++))
done

# Wait for all background jobs
wait
