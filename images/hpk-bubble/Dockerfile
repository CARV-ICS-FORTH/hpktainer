# Builder stage for hpk-net-daemon
ARG REGISTRY=docker.io/chazapis
ARG BASE_IMAGE=ubuntu:24.04
FROM ${REGISTRY}/hpk-builder:latest AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/hpk-net-daemon ./cmd/hpk-net-daemon
RUN CGO_ENABLED=0 go build -o /bin/hpktainer ./cmd/hpktainer
RUN CGO_ENABLED=0 go build -o /bin/hpk-kubelet ./cmd/hpk-kubelet

FROM ${BASE_IMAGE}
WORKDIR /

# Basic utilities
RUN apt-get update && \
    apt-get install -y --no-install-recommends vim-tiny curl iproute2 net-tools iputils-ping nano socat && \
    rm -rf /var/lib/apt/lists/*

# Apptainer
RUN apt-get update && \
    apt-get install -y --no-install-recommends gpg ca-certificates && \
    mkdir -p /etc/apt/keyrings && \
    curl -fsSL "https://keyserver.ubuntu.com/pks/lookup?op=get&search=0xAFE36534FC6218AE" | gpg --dearmor -o /etc/apt/keyrings/apptainer.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/apptainer.gpg] https://ppa.launchpadcontent.net/apptainer/ppa/ubuntu noble main" > /etc/apt/sources.list.d/apptainer-ubuntu-ppa-noble.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends apptainer && \
    rm -rf /var/lib/apt/lists/*

# CNI plugins (only host-local IPAM plugin is required by hpktainer)
ARG TARGETARCH
RUN curl -LO https://github.com/containernetworking/plugins/releases/download/v1.9.0/cni-plugins-linux-${TARGETARCH}-v1.9.0.tgz && \
    mkdir -p /opt/cni/bin && \
    (cd /opt/cni/bin && tar -zxvf /cni-plugins-linux-${TARGETARCH}-v1.9.0.tgz ./host-local) && \
    rm /cni-plugins-linux-${TARGETARCH}-v1.9.0.tgz

# Etcd and iptables dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends iptables nscd etcd-server etcd-client && \
    rm -rf /var/lib/apt/lists/*

# Calicoctl
ARG CALICO_VERSION=v3.28.0
RUN curl -L -o /usr/local/bin/calicoctl https://github.com/projectcalico/calico/releases/download/${CALICO_VERSION}/calicoctl-linux-${TARGETARCH} && \
    chmod +x /usr/local/bin/calicoctl

# K3s
ARG K3S_VERSION=v1.35.0+k3s1
RUN if [ "${TARGETARCH}" = "amd64" ]; then K3S_ARCH=""; else K3S_ARCH="-${TARGETARCH}"; fi; \
    curl -sfL "https://github.com/k3s-io/k3s/releases/download/${K3S_VERSION}/k3s${K3S_ARCH}" -o /usr/local/bin/k3s && \
    chmod +x /usr/local/bin/k3s
RUN mkdir -p /var/lib/rancher/k3s /etc/rancher/k3s

# kube-proxy
ARG KUBE_VERSION=v1.35.0
RUN curl -L "https://dl.k8s.io/release/${KUBE_VERSION}/bin/linux/${TARGETARCH}/kube-proxy" -o /usr/local/bin/kube-proxy && \
    chmod +x /usr/local/bin/kube-proxy

COPY --from=builder /bin/hpk-net-daemon /usr/bin/hpk-net-daemon
COPY --from=builder /bin/hpktainer /usr/bin/hpktainer
COPY --from=builder /bin/hpk-kubelet /usr/bin/hpk-kubelet
COPY images/hpk-bubble/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
