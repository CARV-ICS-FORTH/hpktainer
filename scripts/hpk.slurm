#!/bin/bash
#SBATCH --job-name=hpk-cluster
#SBATCH --output=%x-%j.out
#SBATCH --nodes=1
#SBATCH --ntasks-per-node=1
#SBATCH --mem=1500M

# Usage: sbatch hpk.slurm
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
echo "Cleaning up stale configuration in ~/.hpk..."
rm -rf $HOME/.hpk/kubeconfig $HOME/.hpk/node-token $HOME/.hpk/kubelet.crt $HOME/.hpk/kubelet.key $HOME/.hpk/tls $HOME/.hpk/.certs

# Pre-process images
echo "Pre-processing images..."
IMAGE_DIR="$HOME/.hpk/images"
mkdir -p "$IMAGE_DIR"

if [ "${HPK_DEV:-0}" = "1" ]; then
    echo "Development mode: ensuring local .tar images are converted to .sif..."
    for img in hpk-bubble hpk-pause; do
        IMAGE_TAR="$IMAGE_DIR/${img}.tar"
        IMAGE_SIF="$IMAGE_DIR/${img}.sif"
        if [ -f "$IMAGE_TAR" ]; then
            echo "Converting $IMAGE_TAR to $IMAGE_SIF..."
            apptainer build --force "$IMAGE_SIF" "docker-archive://$IMAGE_TAR"
        else
            echo "Warning: $IMAGE_TAR not found!"
        fi
    done
    
    echo "Development mode: pre-warming third-party images (calico-node)..."
    # Using 'exec /bin/true' forces Apptainer to fully build and cache the SIF file. 
    # 'inspect' sometimes skips the full SIF generation in the cache.
    apptainer exec docker://docker.io/calico/node:v3.28.0 /bin/true 2>/dev/null || true
else
    echo "Production mode: pre-warming Apptainer cache..."
    apptainer exec docker://docker.io/chazapis/hpk-bubble:latest /bin/true 2>/dev/null || true
    apptainer exec docker://docker.io/chazapis/hpk-pause:latest /bin/true 2>/dev/null || true
    apptainer exec docker://docker.io/calico/node:v3.28.0 /bin/true 2>/dev/null || true
fi

# Start Controller Bubble
echo "Starting Controller on $CONTROLLER_NODE..."
export HPK_ROLE="controller"
export CONTROLLER_IP=$CONTROLLER_IP
export HPK_DEV=${HPK_DEV:-0}
export NUM_NODES=$NUM_NODES
srun --nodes=1 -w $CONTROLLER_NODE --exclusive ./hpk-bubble.sh 1 &

# Wait for Controller initialization
echo "Waiting for Controller initialization..."
sleep 15

# Start Node (Worker) Bubbles
BUBBLE_ID=2
for ((i=1; i<NUM_NODES; i++)); do
    NODE=${NODES[$i]}
    echo "Starting Node on $NODE (Bubble ID $BUBBLE_ID)..."
    
    (
        export HPK_ROLE="node"
        export CONTROLLER_IP=$CONTROLLER_IP
        export HPK_DEV=${HPK_DEV:-0}
        export NUM_NODES=$NUM_NODES
        srun --nodes=1 -w $NODE --exclusive ./hpk-bubble.sh $BUBBLE_ID
    ) &
    
    ((BUBBLE_ID++))
done

# Wait for all background jobs
wait
