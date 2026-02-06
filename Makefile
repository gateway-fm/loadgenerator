.PHONY: build test bench test-contract test-e2e docker docker-push run clean help

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

# Run locally (requires block-builder at localhost:13000)
run: build
	BUILDER_RPC_URL=http://localhost:13000 \
	L2_RPC_URL=http://localhost:13000 \
	PRECONF_WS_URL=ws://localhost:13002/ws/preconfirmations \
	LISTEN_ADDR=:13001 \
	DATABASE_PATH=./loadgen-dev.db \
	./loadgen

# Clean build artifacts
clean:
	rm -f loadgen
	rm -f *.db *.db-shm *.db-wal

# Show help
help:
	@echo "Available commands:"
	@echo ""
	@echo "  Development:"
	@echo "    make build        - Build binary"
	@echo "    make test         - Run all tests with race detector"
	@echo "    make bench        - Run benchmarks"
	@echo "    make run          - Run locally (needs block-builder at localhost:13000)"
	@echo "    make clean        - Remove build artifacts"
	@echo ""
	@echo "  Testing:"
	@echo "    make test-contract - API contract tests (no stack needed)"
	@echo "    make test-e2e      - E2E tests (requires running stack)"
	@echo ""
	@echo "  Docker:"
	@echo "    make docker       - Build Docker image"
	@echo "    make docker-push  - Push to DockerHub"
	@echo ""
	@echo "  Configuration:"
	@echo "    DOCKER_IMAGE      - Docker image name (default: gatewayfm/loadgenerator)"
	@echo "    DOCKER_TAG        - Docker image tag (default: latest)"
