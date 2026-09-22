.PHONY: all build build-linux test clean

BIN_DIR := bin
BINARY := $(BIN_DIR)/skifflet
VERSION ?= $(shell cat ../VERSION 2>/dev/null || echo "0.1.0")
BUILD_TIME := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -X skifflet/cmd/skifflet/commands.BuildVersion=$(VERSION) -X skifflet/cmd/skifflet/commands.BuildTime=$(BUILD_TIME)

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/skifflet

build-linux:
	@mkdir -p $(BIN_DIR)
	GOOS=linux go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux ./cmd/skifflet

test:
	go test -v ./pkg/... ./internal/... ./cmd/...

clean:
	rm -rf $(BIN_DIR)
