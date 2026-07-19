ARG REGISTRY=docker.io/chazapis
FROM ${REGISTRY}/hpk-builder:latest AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/hpk-net-daemon ./cmd/hpk-net-daemon

FROM ubuntu:24.04
RUN apt-get update && \
    apt-get install -y --no-install-recommends iproute2 && \
    rm -rf /var/lib/apt/lists/*
COPY --from=builder /bin/hpk-net-daemon /usr/local/bin/hpk-net-daemon
COPY images/hpktainer-base/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
