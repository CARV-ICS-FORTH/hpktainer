# Skifflet

`skifflet` is a Kubernetes [Virtual Kubelet](https://github.com/virtual-kubelet/virtual-kubelet) implementation tailored for High-Performance Computing (HPC) environments running [Apptainer](https://apptainer.org/) (formerly Singularity) and [Plaid](https://github.com/forth-ics/plaid) user-space networking.

## Overview

Rather than running standard rootful container runtimes (like `containerd` or `dockerd`), `skifflet`:
- Registers as a standard Kubernetes Node with custom capacity and attributes.
- Schedules Pods directly as unprivileged Apptainer containers on HPC nodes.
- Uses the standard Kubernetes pause container (`registry.k8s.io/pause:3.10`) to hold the shared pod network namespace, while Skifflet supervises application processes and lifecycle.
- Interacts with Plaid's execution wrapper (`plaidtainer`) to give each Pod its own IP on the cluster overlay network without requiring root or host network reconfiguration.
- Supports volume mounts (ConfigMaps, Secrets, EmptyDir, HostPath, DownwardAPI, Projected volumes) directly inside unprivileged Apptainer user namespaces.

## Building

```bash
make build
```

This compiles the `skifflet` binary into `bin/skifflet`.

To cross-compile for Linux:

```bash
make build-linux
```

## Running Tests

```bash
make test
```

## Command Line Flags

| Flag | Default | Description |
|---|---|---|
| `--nodename` | `skifflet` | Kubernetes node name advertised to the API server |
| `--kubelet-addr` | `$VKUBELET_ADDRESS` | Address to advertise for Kubelet API |
| `--apptainer` | `plaidtainer` | Path or binary name for Apptainer execution wrapper |
| `--pause-image` | `registry.k8s.io/pause:3.10` | OCI image for the Pod pause container |
| `--working-dir` | `$HOME` | Base working directory for Skiff runtime files (`~/.skiff`) |
| `--pods-dir` | `/tmp/.skiff/.pods` | Ephemeral directory for pod job state and volume mounts |
| `--disable-taint` | `false` | Whether to disable the default `virtual-kubelet.io/provider=skiff` taint |
| `--certificate` | `$APISERVER_CERT_LOCATION` | Server TLS certificate for Kubelet HTTPS serving endpoint (logs/exec) |
| `--key` | `$APISERVER_KEY_LOCATION` | Server TLS private key for Kubelet HTTPS serving endpoint (logs/exec) |
