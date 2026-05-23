# Makefile for hpktainer project

REGISTRY ?= docker.io/chazapis
VERSION ?= $(shell cat VERSION)

# Extract K8s version from go.mod and map v0.x.y to v1.x.y
K8S_LIB_VERSION := $(shell go list -m -f '{{.Version}}' k8s.io/api)
K8S_VERSION := $(subst v0.,v1.,$(K8S_LIB_VERSION))

# Binary output directory
BIN_DIR = bin

# Inject version and build time
LDFLAGS := -X 'hpk/pkg/version.Version=$(VERSION)' \
           -X 'hpk/pkg/version.BuildTime=$(shell date)' \
           -X 'hpk/pkg/version.K8sVersion=$(K8S_VERSION)'

.PHONY: all builder binaries binaries-linux-amd64 binaries-linux-arm64 images develop clean

all: builder images

builder:
	@echo "Building and pushing hpk-builder image..."
	docker buildx build --platform linux/amd64,linux/arm64 \
		-t $(REGISTRY)/hpk-builder:$(VERSION) \
		-t $(REGISTRY)/hpk-builder:latest \
		--push \
		-f images/hpk-builder/Dockerfile images/hpk-builder

binaries: binaries-linux-amd64 binaries-linux-arm64

binaries-linux-amd64:
	@echo "Building binaries for linux/amd64..."
	@mkdir -p $(BIN_DIR)/linux/amd64
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/amd64/hpktainer ./cmd/hpktainer
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/amd64/hpk-net-daemon ./cmd/hpk-net-daemon
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/amd64/hpk-kubelet ./cmd/hpk-kubelet
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/amd64/hpk-pause ./cmd/hpk-pause

binaries-linux-arm64:
	@echo "Building binaries for linux/arm64..."
	@mkdir -p $(BIN_DIR)/linux/arm64
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/arm64/hpktainer ./cmd/hpktainer
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/arm64/hpk-net-daemon ./cmd/hpk-net-daemon
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/arm64/hpk-kubelet ./cmd/hpk-kubelet
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/arm64/hpk-pause ./cmd/hpk-pause

images:
	@echo "Building and pushing images..."
	
	# hpktainer-base
	docker buildx build --platform linux/amd64,linux/arm64 \
		--build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/hpktainer-base:$(VERSION) \
		-t $(REGISTRY)/hpktainer-base:latest \
		--push \
		-f images/hpktainer-base/Dockerfile .

	# hpk-bubble
	docker buildx build --platform linux/amd64,linux/arm64 \
		--build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/hpk-bubble:$(VERSION) \
		-t $(REGISTRY)/hpk-bubble:latest \
		--push \
		-f images/hpk-bubble/Dockerfile .

	# hpk-pause
	docker buildx build --platform linux/amd64,linux/arm64 \
		--build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/hpk-pause:$(VERSION) \
		-t $(REGISTRY)/hpk-pause:latest \
		--push \
		-f images/hpk-pause/Dockerfile .

develop:
	@echo "Building images for local development..."
	
	# Build hpk-builder
	docker build --build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/hpk-builder:latest \
		-f images/hpk-builder/Dockerfile images/hpk-builder
	
	# Build hpktainer-base
	docker build --build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/hpktainer-base:latest \
		-f images/hpktainer-base/Dockerfile .
	
	# Build hpk-bubble (dev)
	docker build --build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/hpk-bubble:latest \
		-f images/hpk-bubble-dev/Dockerfile .
	
	# Build hpk-pause
	docker build --build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/hpk-pause:latest \
		-f images/hpk-pause/Dockerfile .
	
	@echo "Exporting images to tar files..."
	@mkdir -p /tmp/hpk-images
	docker save -o /tmp/hpk-images/hpk-bubble.tar $(REGISTRY)/hpk-bubble:latest
	docker save -o /tmp/hpk-images/hpk-pause.tar $(REGISTRY)/hpk-pause:latest
	
	@echo "Copying images to VMs..."
	# Using sshpass if available to automate password entry
	$(eval SSHPASS := sshpass -p vagrant)
	@command -v sshpass >/dev/null 2>&1 || { echo "Error: sshpass is required for automated password entry. Install it or run commands manually."; exit 1; }
	
	$(SSHPASS) ssh -o StrictHostKeyChecking=no vagrant@controller.local "mkdir -p ~/.hpk/images && rm -f ~/.hpk/images/*.sif"
	$(SSHPASS) scp -o StrictHostKeyChecking=no /tmp/hpk-images/*.tar vagrant@controller.local:~/.hpk/images/
	
	@echo "Copying scripts to controller..."
	$(SSHPASS) ssh -o StrictHostKeyChecking=no vagrant@controller.local "mkdir -p ~/hpk"
	$(SSHPASS) scp -r -o StrictHostKeyChecking=no scripts/* vagrant@controller.local:~/hpk/
	
	@echo "Development images deployed successfully!"
	@echo "Set HPK_DEV=1 in hpk.slurm to use local images."

clean:
	rm -rf $(BIN_DIR)
