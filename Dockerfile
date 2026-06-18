# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.25.11-alpine@sha256:89f71d90dff0d7f30316963b3c3b8bfe5fb96b94641b3258963ce0c7a21dedda AS builder

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

# Build with cache mounts for Go modules (go-build cache cleared to ensure source changes are picked up)
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go clean -cache && go mod tidy && go build -ldflags="-s -w" -o load-generator ./cmd/loadgen

# Runtime stage
FROM alpine:3.22@sha256:310c62b5e7ca5b08167e4384c68db0fd2905dd9c7493756d356e893909057601

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
