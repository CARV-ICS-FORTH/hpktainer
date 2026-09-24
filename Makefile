# Makefile for Skiff project

REGISTRY ?= docker.io/chazapis

BASE_VERSION ?= $(shell cat VERSION 2>/dev/null || echo "0.1.0")
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
GIT_COMMIT_SHORT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_TAG := $(shell git describe --tags --exact-match 2>/dev/null || echo "")

ifeq ($(GIT_TAG),$(BASE_VERSION))
    VERSION := $(BASE_VERSION)
else ifeq ($(GIT_TAG),v$(BASE_VERSION))
    VERSION := $(BASE_VERSION)
else
    VERSION := $(BASE_VERSION)+$(GIT_COMMIT_SHORT)
endif

BUILD_TIME := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')

# Binary output directory
BIN_DIR = bin
export GOFLAGS ?= -buildvcs=false

.PHONY: all build build-skifflet build-plaid binaries-linux-amd64 binaries-linux-arm64 vet test clean builder images publish-images develop fmt fmt-check check-shell

all: build

build: build-skifflet build-plaid

build-skifflet:
	@echo "Building skifflet..."
	$(MAKE) -C skifflet build

build-plaid:
	@echo "Building plaid..."
	$(MAKE) -C plaid build

binaries-linux-amd64:
	@echo "Building linux/amd64 binaries..."
	@mkdir -p $(BIN_DIR)
	$(MAKE) -C skifflet build-linux-amd64
	$(MAKE) -C plaid build-linux-amd64
	@cp -f skifflet/bin/*-linux-amd64 $(BIN_DIR)/ 2>/dev/null || true
	@cp -f plaid/bin/*-linux-amd64 $(BIN_DIR)/ 2>/dev/null || true

binaries-linux-arm64:
	@echo "Building linux/arm64 binaries..."
	@mkdir -p $(BIN_DIR)
	$(MAKE) -C skifflet build-linux-arm64
	$(MAKE) -C plaid build-linux-arm64
	@cp -f skifflet/bin/*-linux-arm64 $(BIN_DIR)/ 2>/dev/null || true
	@cp -f plaid/bin/*-linux-arm64 $(BIN_DIR)/ 2>/dev/null || true

vet:
	@echo "Running go vet on skifflet..."
	$(MAKE) -C skifflet vet
	@echo "Running go vet on plaid..."
	$(MAKE) -C plaid vet

test:
	@echo "Running skifflet tests..."
	$(MAKE) -C skifflet test
	@echo "Running plaid tests..."
	$(MAKE) -C plaid test

fmt:
	@echo "Formatting Go source files..."
	gofmt -w skifflet plaid

fmt-check:
	@test -z "$$(gofmt -l skifflet plaid)" || (echo "Unformatted Go files found:" && gofmt -l skifflet plaid && exit 1)

check-shell:
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck --severity=error $$(find scripts images test -type f \( -name "*.sh" -o -name "*.slurm" \) -not -path "*/.*"); \
	else \
		echo "shellcheck not installed, skipping"; \
	fi

builder:
	@echo "Building skiff-builder image locally..."
	docker build \
		-t $(REGISTRY)/skiff-builder:latest \
		-f images/skiff-builder/Dockerfile images/skiff-builder

images:
	@echo "Building skiff-bubble image locally..."
	docker build \
		--build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/skiff-bubble:$(VERSION) \
		-t $(REGISTRY)/skiff-bubble:latest \
		-f images/skiff-bubble/Dockerfile .

publish-images:
	@echo "Building and pushing multi-arch skiff-bubble release image..."
	docker buildx build --platform linux/amd64,linux/arm64 \
		--build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/skiff-bubble:$(VERSION) \
		-t $(REGISTRY)/skiff-bubble:latest \
		--push \
		-f images/skiff-bubble/Dockerfile .

develop:
	@echo "Building images for local development (Version: $(VERSION))..."
	
	# Build skiff-builder locally
	docker build --build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/skiff-builder:latest \
		-f images/skiff-builder/Dockerfile images/skiff-builder

	# Build skiff-bubble locally
	docker build --build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/skiff-bubble:latest \
		-f images/skiff-bubble/Dockerfile .

	@echo "Exporting images to tar files..."
	@mkdir -p /tmp/skiff-images
	docker save -o /tmp/skiff-images/skiff-bubble.tar $(REGISTRY)/skiff-bubble:latest

	@echo "Generating deployment manifest..."
	@TAR_SHA=$$(sha256sum /tmp/skiff-images/skiff-bubble.tar 2>/dev/null | awk '{print $$1}' || shasum -a 256 /tmp/skiff-images/skiff-bubble.tar | awk '{print $$1}'); \
	cat <<EOF > /tmp/skiff-images/manifest.json \
	{\
	  "version": "$(VERSION)",\
	  "git_commit": "$(GIT_COMMIT)",\
	  "build_time": "$(BUILD_TIME)",\
	  "tar_sha256": "$$TAR_SHA"\
	}\
	EOF

	@echo "Copying image and manifest to Vagrant VMs..."
	cd test/vagrant && vagrant ssh controller -c "mkdir -p ~/.skiff/images && rm -f ~/.skiff/images/skiff-bubble.sif"
	cd test/vagrant && vagrant upload /tmp/skiff-images/skiff-bubble.tar /home/vagrant/.skiff/images/skiff-bubble.tar controller
	cd test/vagrant && vagrant upload /tmp/skiff-images/manifest.json /home/vagrant/.skiff/images/manifest.json controller

	@echo "Converting tar to SIF on controller VM..."
	cd test/vagrant && vagrant ssh controller -c "apptainer build --force ~/.skiff/images/skiff-bubble.sif docker-archive:///home/vagrant/.skiff/images/skiff-bubble.tar"

	@echo "Copying scripts and tests to controller..."
	cd test/vagrant && vagrant ssh controller -c "mkdir -p ~/skiff/scripts ~/skiff/test"
	for f in scripts/*; do \
		(cd test/vagrant && vagrant upload ../../$$f /home/vagrant/skiff/scripts/$$(basename $$f) controller); \
	done
	for f in test/*.sh; do \
		(cd test/vagrant && vagrant upload ../../$$f /home/vagrant/skiff/test/$$(basename $$f) controller); \
	done
	cd test/vagrant && vagrant ssh controller -c "chmod +x ~/skiff/scripts/*.sh ~/skiff/test/*.sh"

	@echo "Development image and deployment manifest deployed successfully!"
	@echo "Set SKIFF_DEV=1 in scripts/skiff.slurm to use local image."

clean:
	$(MAKE) -C skifflet clean
	$(MAKE) -C plaid clean
	rm -rf $(BIN_DIR) /tmp/skiff-images
