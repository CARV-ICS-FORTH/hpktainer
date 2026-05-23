# Dev Envirnoment for hpk-bubble
ARG REGISTRY=docker.io/chazapis
FROM ${REGISTRY}/hpk-builder:latest

WORKDIR /

# Basic utilities
RUN apt-get update && \
    apt-get install -y vim-tiny curl iproute2 net-tools bridge-utils iputils-ping

# Apptainer
RUN apt-get install -y software-properties-common && \
    add-apt-repository -y ppa:apptainer/ppa && \
    apt-get install -y apptainer

# CNI plugins
ARG TARGETARCH
RUN curl -LO https://github.com/containernetworking/plugins/releases/download/v1.9.0/cni-plugins-linux-${TARGETARCH}-v1.9.0.tgz && \
    mkdir -p /opt/cni/bin && \
    (cd /opt/cni/bin && tar -zxvf /cni-plugins-linux-${TARGETARCH}-v1.9.0.tgz) && \
    rm /cni-plugins-linux-${TARGETARCH}-v1.9.0.tgz

# Etcd and iptables dependencies
RUN apt-get install -y iptables nscd etcd-server etcd-client

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

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Build binaries in-place (for dev container use)
RUN go build -o /usr/bin/hpk-net-daemon ./cmd/hpk-net-daemon
RUN go build -o /usr/bin/hpktainer ./cmd/hpktainer
RUN go build -o /usr/bin/hpk-kubelet ./cmd/hpk-kubelet

COPY images/hpk-bubble/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
