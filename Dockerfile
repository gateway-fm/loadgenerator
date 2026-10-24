# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.26.10-alpine@sha256:5cb67f19c5e7994dcb80c074f7cfca8aeeada45bbdca035f73df269cefdcce61 AS builder

WORKDIR /app

# Install build dependencies
RUN apk upgrade --no-cache && apk add --no-cache gcc musl-dev

# Copy go mod files
COPY go.mod go.sum* ./

# Download dependencies with cache mount
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download || true

# Copy source
COPY . .

# Build with cache mounts for Go modules (go-build cache cleared to ensure source changes are picked up).
# The go-build cache is keyed per target arch: with a shared mount, one platform's
# `go clean -cache` deletes entries the other platform is reading mid-build.
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build,id=go-build-${TARGETARCH} \
    go clean -cache && go mod tidy && go build -ldflags="-s -w" -o load-generator ./cmd/loadgen

# Runtime stage
FROM alpine:3.22@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f

RUN apk upgrade --no-cache && apk add --no-cache ca-certificates curl

RUN adduser -D -u 1000 appuser

WORKDIR /app

COPY --from=builder /app/load-generator .

RUN chown -R appuser:appuser /app

USER appuser

EXPOSE 3001

# Health check - load-generator exposes /health endpoint
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -sf http://localhost:3001/health || exit 1

# Default to server mode on port 3001
CMD ["./load-generator"]
