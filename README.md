# HPK

HPK allows HPC users to run their own private Kubernetes "mini Cloud" on a typical HPC cluster and then issue commands to it using Kubernetes-native tools.

To deploy, copy the `scripts/` folder contents to your HPC account under `~/hpk/` and run:

```bash
cd hpk
sbatch --nodes=3 hpk.slurm
```

Then configure and use `kubectl`:

```bash
export KUBECONFIG=${HOME}/.hpk/kubeconfig
kubectl get nodes
```

## Implementation

### Overview

Users run a Slurm command to deploy one rootless container per cluster node, which we call **bubble** (using the `hpk-bubble` image). One bubble acts as the Kubernetes control plane, while the others act as worker nodes; together they form the Kubernetes cluster. Each bubble runs an instance of [K3s](https://k3s.io/), alongside the HPK-specific kubelet (`hpk-kubelet`), implemented using the [Virtual Kubelet](https://github.com/virtual-kubelet/virtual-kubelet) framework.

For external networking, bubbles use [slirp4netns](https://github.com/rootless-containers/slirp4netns), while for internal, overlay networking they run [Calico](https://github.com/projectcalico/calico) and communicate via BGP-routed paths (each host forwards TCP port 17900 to the bubble to enable peer-to-peer BGP mesh communication).

Inside the bubble, an [Apptainer](https://apptainer.org/) wrapper (`hpktainer`) is used to spawn "pods" (using the `hpk-pause` image, derived from `hpktainer-base`); these are containers that are given unique network addresses in the corresponding Calico subnet and host user application containers.

All pod containers are configured in a bridge-free, point-to-point L3 routing layout at the bubble level. With the proper routing rules, they route traffic directly to pods running in other bubbles (via Calico-routed interfaces over BGP) and the outside world.

The pod network stack is implemented in userspace using a pair of TAP interfaces; one in the nested container and one in the bubble. The pair is connected via two instances of the `hpk-net-daemon` that forward traffic over a UNIX socket created in a shared folder.

### Architecture

HPK implements a **4-level distributed architecture**.

1. **Level 1: Host Node (Slurm Worker)**
    * The physical node managed by Slurm.
    * Executes `hpk.slurm`, which launches the bubble.

2. **Level 2: Bubble (Node Overlay)**
    * Implemented in the `hpk-bubble` container.
    * An Apptainer instance acting as a virtual node.
    * Runs K3s (the base Kubernetes distribution) and Calico (L3 routing and BGP peering). The first bubble, which acts as the Kubernetes control plane, also runs [etcd](https://etcd.io/) for supporting Calico.
    * Runs the local `hpk-kubelet`, which registers itself as a node in the K3s cluster.
    * Connects to other bubbles via a BGP mesh network (Calico, BGP port 17900).

3. **Level 3: Pod**
    * Implemented in the `hpk-pause` container.
    * Spawned by `hpk-kubelet` via `hpktainer`.
    * Each Pod is an Apptainer container with its own network namespace connected to the Bubble's point-to-point routing interface.
    * The Pod's entrypoint is the `hpk-pause` binary, which acts as a "pause container" to hold the network namespace and capture application container signals.

4. **Level 4: Application Container**
    * User application containers spawned by `hpk-pause`.
    * These run within the **same network namespace** as the Level 3 Pod.
    * They share the Pod's IP address and can communicate over `localhost`.

### Security & Trust Model

HPK is designed to run in HPC environments under a **single-user trust model**:

1. **Single-User Job Trust Domain**: All container instances and services (`hpk-bubble`, `hpk-kubelet`, K3s, Calico, etcd) run rootless under the requesting user's UID within a dedicated Slurm job allocation. Security isolation between different users is enforced by Slurm and host OS user isolation.
2. **Credential & Certificate Protection**:
   - Cluster credentials (`kubeconfig`, `node-token`) and `hpk-kubelet` private keys (`kubelet.key`) stored in the shared NFS directory (`~/.hpk`) are restricted to owner-only access (`0600` permissions).
   - The K3s server Certificate Authority private key (`server-ca.key`) is retained exclusively in memory/local storage on the controller node and is **never exported** to shared NFS storage.
   - Node webhook certificates (`kubelet.crt`) are issued via Certificate Signing Requests (CSRs) submitted to `~/.hpk/.certs/<node>/kubelet.csr` and signed by a background signing loop running on the controller node. Under the single-user trust model, any CSR appearing in `~/.hpk/.certs/` is signed without additional verification, as write access to the user's NFS directory implies full access to all job credentials and node tokens.
3. **Internal Services & Network Listeners**:
   - Etcd (supporting Calico datastore on port 2379) and Calico BGP (port 17900) run unauthenticated and bind to network interfaces managed within the Slurm job allocation. Access to these ports relies on host user boundaries and Slurm job isolation.

## Building

All binaries are built and embedded in container images. The deployment script uses these images.

To build, run:

```bash
make
```

This uses `docker buildx` to build and push the images with multi-architecture support (amd64/arm64) to the configured registry (default: `docker.io/chazapis`). You can override the registry:

```bash
REGISTRY=myregistry.io/user make
```

*Note for developers: You can also build the binaries locally for testing purposes using `make binaries`. These will be placed in `bin/`.*

## Evaluating Locally

You can test the setup locally using the provided Vagrant environment, which simulates a multi-node cluster using VMs.

### 1. Start the Environment
This creates a 2-node cluster (`controller`, `node`) running Ubuntu 24.04 with Slurm pre-installed.

```bash
cd vagrant
vagrant up
vagrant reload # Required to apply security settings (AppArmor disable)
```

The VMs use mDNS for networking and are accessible as `controller.local` and `node.local`.

### 2. Deploy Scripts and Images

**Option A: For production testing (using published images)**

Upload the project scripts to the controller node:

```bash
# From the repository root on your host
ssh -o StrictHostKeyChecking=no vagrant@controller.local "mkdir -p ~/hpk" # Password is 'vagrant'
scp -r -o StrictHostKeyChecking=no scripts/* vagrant@controller.local:~/hpk/ # Password is 'vagrant'
```

**Option B: For development (using local images)**

For rapid iteration during development, build and deploy images directly to the VMs:

```bash
make develop
```

This will:
1. Build all images locally for your current architecture
2. Export them as `.tar` files
3. Copy them to both VMs at `~/.hpk/images/`
4. Copy the `scripts/` directory to the controller at `~/hpk/`
5. Remove old `.sif` files to ensure fresh builds are used

To use the local images, set `HPK_DEV=1` before running the cluster (see step 3).

### 3. Run the Cluster
Connect to the controller and submit the Slurm job:

```bash
ssh -o StrictHostKeyChecking=no vagrant@controller.local # Password is 'vagrant'

export HPK_DEV=1 # If using development mode/local images
cd ~/hpk
sbatch --nodes=2 hpk.slurm
```

This will launch one controller bubble and one node bubble on the Vagrant VMs.

### 4. Interact with the Bubble
Once running, you can connect to the controller bubble:

```bash
apptainer shell instance://bubble1
```

Inside the bubble, you can run nested containers:

```bash
hpktainer run docker://docker.io/chazapis/hpktainer-base:latest /bin/sh
```

And verify connectivity:
```bash
ip addr show tap0    # Should show Calico IP
ping 8.8.8.8         # External access
```