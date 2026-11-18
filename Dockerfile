# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.26.11-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS builder

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
FROM alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f

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
