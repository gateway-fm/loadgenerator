.PHONY: build test bench test-contract test-e2e docker docker-push run run-local setup-hooks clean mcp-build mcp-run help

# Docker image
DOCKER_IMAGE ?= gatewayfm/loadgenerator
DOCKER_TAG ?= latest

# Build the load-generator binary
build:
	go build -ldflags="-s -w" -o loadgen ./cmd/loadgen

# Run all tests with race detector
test:
	go test -race -v ./...

# Run benchmarks
bench:
	go test -bench=. -benchmem ./...

# Run API contract tests (no stack needed)
test-contract:
	go test -v -race ./internal/contract/...

# Run E2E integration tests (requires running stack)
test-e2e:
	BUILDER_RPC_URL=http://localhost:13000 \
	LOADGEN_API_URL=http://localhost:13001 \
	L2_RPC_URL=http://localhost:13000 \
	PRECONF_WS_URL=ws://localhost:13002/ws/preconfirmations \
	go test -v -race -timeout 120s ./internal/integration/... -run "TestE2E"

# Build Docker image
docker:
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

# Push Docker image to DockerHub
docker-push: docker
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)

# Run in Docker (builds image and starts via docker-compose)
run:
	docker compose up --build

# Run locally without Docker (requires block-builder at localhost:13000)
run-local: build
	BUILDER_RPC_URL=http://localhost:13000 \
	L2_RPC_URL=http://localhost:13000 \
	PRECONF_WS_URL=ws://localhost:13002/ws/preconfirmations \
	LISTEN_ADDR=:13001 \
	DATABASE_PATH=./loadgen-dev.db \
	./loadgen

# Install git pre-commit hook that runs tests
setup-hooks:
	@mkdir -p .git/hooks
	@echo '#!/bin/sh' > .git/hooks/pre-commit
	@echo 'make test' >> .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "pre-commit hook installed (.git/hooks/pre-commit)"

# Clean build artifacts
clean:
	rm -f loadgen
	rm -f *.db *.db-shm *.db-wal

# Build MCP server binary
mcp-build:
	go build -o loadgen-mcp ./cmd/mcp

# Run MCP server (stdio transport)
mcp-run:
	LOADGEN_URL=http://localhost:13001 go run ./cmd/mcp

# Show help
help:
	@echo "Available commands:"
	@echo ""
	@echo "  Development:"
	@echo "    make build         - Build binary"
	@echo "    make test          - Run all tests with race detector"
	@echo "    make bench         - Run benchmarks"
	@echo "    make run           - Run in Docker (builds and starts via docker-compose)"
	@echo "    make run-local     - Run locally (needs block-builder at localhost:13000)"
	@echo "    make setup-hooks   - Install pre-commit hook that runs tests"
	@echo "    make clean         - Remove build artifacts"
	@echo ""
	@echo "  Testing:"
	@echo "    make test-contract - API contract tests (no stack needed)"
	@echo "    make test-e2e      - E2E tests (requires running stack)"
	@echo ""
	@echo "  Docker:"
	@echo "    make docker        - Build Docker image"
	@echo "    make docker-push   - Push to DockerHub"
	@echo ""
	@echo "  Configuration:"
	@echo "    DOCKER_IMAGE       - Docker image name (default: gatewayfm/loadgenerator)"
	@echo "    DOCKER_TAG         - Docker image tag (default: latest)"
	@echo ""
	@echo "  MCP:"
	@echo "    make mcp-build     - Build MCP server binary"
	@echo "    make mcp-run       - Run MCP server (stdio)"
