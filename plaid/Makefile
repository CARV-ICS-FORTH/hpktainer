.PHONY: all build build-linux test clean

BIN_DIR := bin

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/plaidd ./cmd/plaidd
	go build -o $(BIN_DIR)/plaid ./cmd/plaid
	go build -o $(BIN_DIR)/plaidctl ./cmd/plaidctl
	go build -o $(BIN_DIR)/plaidtainer ./cmd/plaidtainer

build-linux:
	@mkdir -p $(BIN_DIR)
	GOOS=linux go build -o $(BIN_DIR)/plaidd-linux ./cmd/plaidd
	GOOS=linux go build -o $(BIN_DIR)/plaid-linux ./cmd/plaid
	GOOS=linux go build -o $(BIN_DIR)/plaidctl-linux ./cmd/plaidctl
	GOOS=linux go build -o $(BIN_DIR)/plaidtainer-linux ./cmd/plaidtainer

test:
	go test -v ./pkg/... ./cmd/...

clean:
	rm -rf $(BIN_DIR)
