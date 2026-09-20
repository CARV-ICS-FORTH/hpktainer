# Builder stage
ARG REGISTRY=docker.io/chazapis
FROM ${REGISTRY}/hpk-builder:latest AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/hpk-pause ./cmd/hpk-pause

# Final stage
FROM ${REGISTRY}/hpktainer-base:latest

# Basic utilities
RUN apt-get update && \
    apt-get install -y --no-install-recommends vim-tiny curl iproute2 net-tools iputils-ping nano socat && \
    rm -rf /var/lib/apt/lists/*


# Copy hpk-pause binary
COPY --from=builder /bin/hpk-pause /usr/local/bin/hpk-pause

CMD ["/usr/local/bin/hpk-pause"]
