# Makefile for Skiff project

REGISTRY ?= docker.io/chazapis
VERSION ?= $(shell cat VERSION 2>/dev/null || echo "0.1.0")

# Binary output directory
BIN_DIR = bin
export GOFLAGS ?= -buildvcs=false

.PHONY: all build build-skifflet build-plaid test clean builder images develop fmt fmt-check check-shell

all: build

build: build-skifflet build-plaid

build-skifflet:
	@echo "Building skifflet..."
	$(MAKE) -C skifflet build

build-plaid:
	@echo "Building plaid..."
	$(MAKE) -C plaid build

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
		shellcheck --severity=error $$(find scripts test -name "*.sh" -not -path "*/.*"); \
	else \
		echo "shellcheck not installed, skipping"; \
	fi

builder:
	@echo "Building and pushing skiff-builder image..."
	docker buildx build --platform linux/amd64,linux/arm64 \
		-t $(REGISTRY)/skiff-builder:$(VERSION) \
		-t $(REGISTRY)/skiff-builder:latest \
		--push \
		-f images/skiff-builder/Dockerfile images/skiff-builder

images:
	@echo "Building and pushing skiff-bubble image..."
	docker buildx build --platform linux/amd64,linux/arm64 \
		--build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/skiff-bubble:$(VERSION) \
		-t $(REGISTRY)/skiff-bubble:latest \
		--push \
		-f images/skiff-bubble/Dockerfile .

develop:
	@echo "Building images for local development..."
	
	# Build skiff-builder
	docker build --build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/skiff-builder:latest \
		-f images/skiff-builder/Dockerfile images/skiff-builder

	# Build skiff-bubble
	docker build --build-arg REGISTRY=$(REGISTRY) \
		-t $(REGISTRY)/skiff-bubble:latest \
		-f images/skiff-bubble/Dockerfile .

	@echo "Exporting images to tar files..."
	@mkdir -p /tmp/skiff-images
	docker save -o /tmp/skiff-images/skiff-bubble.tar $(REGISTRY)/skiff-bubble:latest

	@echo "Copying images to VMs via Vagrant..."
	cd test/vagrant && vagrant ssh controller -c "mkdir -p ~/.skiff/images && rm -f ~/.skiff/images/*.sif"
	cd test/vagrant && vagrant upload /tmp/skiff-images/skiff-bubble.tar /home/vagrant/.skiff/images/skiff-bubble.tar controller

	@echo "Copying scripts and tests to controller..."
	cd test/vagrant && vagrant ssh controller -c "mkdir -p ~/skiff/scripts ~/skiff/test"
	for f in scripts/*; do \
		(cd test/vagrant && vagrant upload ../../$$f /home/vagrant/skiff/scripts/$$(basename $$f) controller); \
	done
	for f in test/*.sh; do \
		(cd test/vagrant && vagrant upload ../../$$f /home/vagrant/skiff/test/$$(basename $$f) controller); \
	done
	cd test/vagrant && vagrant ssh controller -c "chmod +x ~/skiff/scripts/*.sh ~/skiff/test/*.sh"

	@echo "Development images deployed successfully!"
	@echo "Set SKIFF_DEV=1 in scripts/skiff.slurm to use local images."

clean:
	$(MAKE) -C skifflet clean
	$(MAKE) -C plaid clean
	rm -rf $(BIN_DIR) /tmp/skiff-images
