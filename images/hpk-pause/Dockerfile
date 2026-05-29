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

# Install Apptainer (required for hpk-pause agent to spawn containers)
RUN apt-get update && \
    apt-get install -y software-properties-common && \
    add-apt-repository -y ppa:apptainer/ppa && \
    apt-get install -y apptainer && \
    rm -rf /var/lib/apt/lists/*

# Copy hpk-pause binary
COPY --from=builder /bin/hpk-pause /usr/local/bin/hpk-pause

CMD ["/usr/local/bin/hpk-pause"]
