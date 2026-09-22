# Skiff

Skiff allows HPC users to run their own private Kubernetes "mini Cloud" on a typical HPC cluster and issue commands using Kubernetes-native tools without requiring root privileges.

To deploy, copy the `scripts/` folder contents to your HPC account under `~/skiff/` and run:

```bash
cd skiff
sbatch --nodes=3 skiff.slurm
```

Then configure and use `kubectl`:

```bash
export KUBECONFIG=${HOME}/.skiff/kubeconfig
kubectl get nodes
```

## Architecture & Components

Skiff is organized as a unified monorepo containing two core sub-projects alongside top-level container packaging, scripts, and end-to-end tests:

```text
skiff/
├── Makefile                 # Top-level build orchestration & VM deployment
├── README.md                # Top-level overview and architecture
├── LICENSE                  # Apache 2.0 License
├── VERSION                  # Project version
│
├── skifflet/                # Dedicated Kubelet module
│   ├── cmd/skifflet/        # Main virtual-kubelet entrypoint
│   ├── internal/            # Compute, provider, and pod lifecycle engine
│   ├── pkg/                 # Helpers (volumes, crdtools, etc.)
│   ├── go.mod               # module skifflet
│   └── Makefile             # Local module build & unit tests
│
├── plaid/                   # First-class networking module
│   ├── cmd/                 # plaid, plaidd, plaidctl, plaidtainer
│   ├── pkg/                 # api, bridge, ipam, packet, slirp, tap, vxlan
│   ├── go.mod               # module github.com/forth-ics/plaid
│   └── Makefile             # Local module build & unit tests
│
├── images/                  # Container definitions
│   ├── skiff-bubble/        # Bubble container packaging (skifflet, plaid, k3s)
│   └── skiff-builder/       # Builder base image
│
├── scripts/                 # Cluster runtime scripts
│   ├── skiff-bubble.sh      # Spawns bubble instance with slirp4netns
│   └── skiff.slurm          # SLURM multi-node batch job template
│
├── test/                    # Tests and Vagrant cluster environment
│   ├── vagrant/             # 2-node Vagrant development environment
│   ├── test-skiff-e2e.sh    # End-to-end multi-node integration test
│   ├── test-plaid-all.sh    # Plaid test suite
│   ├── test-plaid-e2e.sh    # Plaid Apptainer e2e test
│   └── test-plaid-unprivileged.sh
│
└── benchmarks/              # Cluster benchmark workloads
    ├── analytics-spark/
    ├── bert-distr-training/
    └── dask-matrix/
```

### 1. Skifflet (`skifflet/`)
`skifflet` is a Kubernetes [Virtual Kubelet](https://github.com/virtual-kubelet/virtual-kubelet) provider tailored for HPC environments:
- Registers as a standard Kubernetes node in K3s.
- Materializes Pods in unprivileged user space using [Apptainer](https://apptainer.org/) and `plaidtainer`.
- Uses the standard Kubernetes pause container (`registry.k8s.io/pause:3.10`) to hold network namespaces and reap processes without requiring custom pause images.
- Mounts Kubernetes volumes (ConfigMaps, Secrets, EmptyDir, HostPath, DownwardAPI, Projected) directly into unprivileged container filesystems.

### 2. Plaid (`plaid/`)
`plaid` provides rootless user-space networking:
- **`plaidd`**: In-memory virtual Ethernet switch and cross-node VXLAN overlay router running over UDP port 8472.
- **`plaidtainer`**: Transparent Apptainer wrapper that configures TAP interfaces and attaches containers to the Plaid subnet.
- **`plaid`**: CNI-compatible plugin and container TAP initializer.
- **`plaidctl`**: CLI tool for inspecting endpoints, routes, and network stats.

### 3. Skiff Bubble (`images/skiff-bubble/`)
A rootless Apptainer container deployed per HPC node:
- One bubble acts as the Kubernetes control plane (running [K3s](https://k3s.io/)), while the others act as worker nodes.
- For external access, each bubble uses [slirp4netns](https://github.com/rootless-containers/slirp4netns).
- For internal pod-to-pod networking across nodes, bubbles forward UDP port 8472 to maintain Plaid VXLAN overlay tunnels.

---

## Building

Build both `skifflet` and `plaid` binaries locally:

```bash
make build
```

Or build components individually:

```bash
make build-skifflet
make build-plaid
```

Run unit tests across all modules:

```bash
make test
```

Build container images with Docker / Buildx:

```bash
make images
```

---

## Evaluating Locally with Vagrant

You can test the full multi-node cluster locally using the provided Vagrant environment in `test/vagrant/`:

### 1. Start the Environment

```bash
cd test/vagrant
vagrant up
vagrant reload
cd ../..
```

This boots a 2-node cluster (`controller.local`, `node.local`) running Ubuntu 24.04 with Slurm pre-installed.

### 2. Deploy and Run with Local Images

```bash
make develop
```

This builds the `skiff-bubble` image, uploads it directly to the Vagrant VMs at `~/.skiff/images/`, and syncs all scripts and test suites.

Then SSH into the controller and submit the job:

```bash
ssh -o StrictHostKeyChecking=no vagrant@controller.local
export SKIFF_DEV=1
cd ~/skiff/scripts
sbatch --nodes=2 skiff.slurm
```

### 3. Run End-to-End Tests

Once the cluster is running:

```bash
export KUBECONFIG=~/.skiff/kubeconfig
kubectl get nodes
bash ~/skiff/test/test-skiff-e2e.sh
```