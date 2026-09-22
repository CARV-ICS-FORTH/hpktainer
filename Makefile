# Makefile for HPK project

REGISTRY ?= docker.io/chazapis
VERSION ?= $(shell cat VERSION)

# Extract K8s version from go.mod and map v0.x.y to v1.x.y
K8S_LIB_VERSION := $(shell go list -m -f '{{.Version}}' k8s.io/api)
K8S_VERSION := $(subst v0.,v1.,$(K8S_LIB_VERSION))

# Binary output directory
BIN_DIR = bin
export GOFLAGS ?= -buildvcs=false

# Inject version and build time
LDFLAGS := -X 'hpk/pkg/version.Version=$(VERSION)' \
           -X 'hpk/pkg/version.BuildTime=$(shell date)' \
           -X 'hpk/pkg/version.K8sVersion=$(K8S_VERSION)'

.PHONY: all builder binaries binaries-linux-amd64 binaries-linux-arm64 images develop clean fmt fmt-check vet test check-shell ci

all: builder images

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "Unformatted Go files found:" && gofmt -l . && exit 1)

vet:
	go vet ./...

test:
	go test ./...

check-shell:
	@if command -v shellcheck >/dev/null 2>&1; then shellcheck --severity=error $$(find . -name "*.sh" -not -path "*/.*"); else echo "shellcheck not installed, skipping"; fi

ci: fmt-check vet binaries-linux-amd64 test check-shell

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
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/amd64/hpk-kubelet ./cmd/hpk-kubelet
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/amd64/hpk-pause ./cmd/hpk-pause

binaries-linux-arm64:
	@echo "Building binaries for linux/arm64..."
	@mkdir -p $(BIN_DIR)/linux/arm64
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/arm64/hpk-kubelet ./cmd/hpk-kubelet
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux/arm64/hpk-pause ./cmd/hpk-pause

images:
	@echo "Building and pushing images..."

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

	# Build hpk-bubble (dev)
	# docker build --build-arg REGISTRY=$(REGISTRY) \
	# 	--build-arg BASE_IMAGE=$(REGISTRY)/hpk-builder:latest \
	# 	-t $(REGISTRY)/hpk-bubble:latest \
	# 	-f images/hpk-bubble/Dockerfile .
	
	# Build hpk-bubble
	docker build --build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/hpk-bubble:latest \
		-f images/hpk-bubble/Dockerfile .
	
	# Build hpk-pause
	docker build --build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/hpk-pause:latest \
		-f images/hpk-pause/Dockerfile .
	
	@echo "Exporting images to tar files..."
	@mkdir -p /tmp/hpk-images
	docker save -o /tmp/hpk-images/hpk-bubble.tar $(REGISTRY)/hpk-bubble:latest
	docker save -o /tmp/hpk-images/hpk-pause.tar $(REGISTRY)/hpk-pause:latest
	
	@echo "Copying images to VMs via Vagrant..."
	cd vagrant && vagrant ssh controller -c "mkdir -p ~/.hpk/images && rm -f ~/.hpk/images/*.sif"
	cd vagrant && vagrant upload /tmp/hpk-images/hpk-bubble.tar /home/vagrant/.hpk/images/hpk-bubble.tar controller
	cd vagrant && vagrant upload /tmp/hpk-images/hpk-pause.tar /home/vagrant/.hpk/images/hpk-pause.tar controller
	
	@echo "Copying scripts to controller..."
	cd vagrant && vagrant ssh controller -c "mkdir -p ~/hpk"
	for f in scripts/*; do \
		(cd vagrant && vagrant upload ../$$f /home/vagrant/hpk/$$(basename $$f) controller); \
	done
	cd vagrant && vagrant ssh controller -c "chmod +x ~/hpk/*.sh"
	
	@echo "Development images deployed successfully!"
	@echo "Set HPK_DEV=1 in hpk.slurm to use local images."

clean:
	rm -rf $(BIN_DIR)
