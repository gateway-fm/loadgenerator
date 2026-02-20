# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.25-alpine AS builder

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

# Build with cache mounts for Go modules and build cache
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod tidy && go build -ldflags="-s -w" -o load-generator ./cmd/loadgen

# Runtime stage
FROM alpine:latest@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

RUN apk upgrade --no-cache && apk add --no-cache ca-certificates curl

WORKDIR /app

COPY --from=builder /app/load-generator .

EXPOSE 3001

# Health check - load-generator exposes /health endpoint
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -sf http://localhost:3001/health || exit 1

# Default to server mode on port 3001
CMD ["./load-generator"]
