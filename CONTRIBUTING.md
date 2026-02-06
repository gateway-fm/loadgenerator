# Contributing to GasStorm Load Generator

## Prerequisites

- **Go 1.25+** (check with `go version`)
- **Docker** (for building images and running the full stack)
- **make** (standard build tool)

## Development Setup

```bash
# Clone the repo
git clone https://github.com/gateway-fm/loadgenerator.git
cd loadgenerator

# Build the binary
make build

# Run tests to verify your setup
make test
```

## Test Commands

| Command | Description | Requirements |
|---------|-------------|--------------|
| `make test` | All unit tests with race detector (`go test -race -v ./...`) | None |
| `make bench` | Benchmarks with memory allocation stats (`go test -bench=. -benchmem ./...`) | None |
| `make test-contract` | API contract tests (validates JSON field names, response shapes) | None |
| `make test-e2e` | End-to-end integration tests | Running GasStorm stack |

Always run `make test` before submitting a pull request. All tests must pass with the `-race` flag.

## Adding a New Transaction Type

1. Define the type constant in `pkg/types/types.go`:

   ```go
   const TxTypeMyNewType TransactionType = "my-new-type"
   ```

2. Create a builder in `internal/txbuilder/`:

   Create a new file `internal/txbuilder/mynewtype.go` implementing the transaction building logic. Follow the pattern in existing builders (e.g., `eth_transfer.go`, `storage_write.go`).

3. Register in the valid types map in `internal/transport/http.go`:

   ```go
   var validTxTypes = map[types.TransactionType]bool{
       // ... existing types
       types.TxTypeMyNewType: true,
   }
   ```

4. Add to the realistic test tracking in `internal/metrics/collector.go` (`InitRealisticMetrics`).

5. Update the `TxTypeRatio` struct in `pkg/types/types.go` if it should be available in realistic/mixed mode.

6. Add the gas estimate to the README table.

7. Add the type to the OpenAPI spec in `api/openapi.yaml` under `TransactionType` enum.

## Adding a New Load Pattern

1. Define the pattern constant in `pkg/types/types.go`:

   ```go
   const PatternMyPattern LoadPattern = "my-pattern"
   ```

2. Add pattern-specific fields to `StartTestRequest` in `pkg/types/types.go`.

3. Register in the valid patterns map in `internal/transport/http.go`:

   ```go
   var validPatterns = map[types.LoadPattern]bool{
       // ... existing patterns
       types.PatternMyPattern: true,
   }
   ```

4. Add validation logic in `validateStartRequest()` in `internal/transport/http.go`.

5. Implement the pattern logic in `cmd/loadgen/main.go` (in the rate control goroutine).

6. Add to the OpenAPI spec in `api/openapi.yaml` under `LoadPattern` enum.

7. Document the pattern with a JSON example in the README.

## Adding a New Execution Layer

1. Add a capability function in `internal/execnode/registry.go`:

   ```go
   func NewMyNodeCapabilities() *ExecutionLayerCapabilities {
       return &ExecutionLayerCapabilities{
           Name:                     "my-node",
           HasExternalBlockBuilder:  false,
           SupportsPreconfirmations: false,
           SupportsBuilderStatusAPI: false,
           SupportsBlockMetricsWS:   false,
       }
   }
   ```

2. Register in `DefaultRegistry()` in the same file.

3. Create `docker-compose-my-node.yaml` in the GasStorm repo if needed.

4. No changes needed to load-generator logic, dashboard code, or API handlers -- the capability system handles routing automatically.

## API Contract Notes

- All JSON responses use **camelCase** field names, not PascalCase.
- Every Go struct exposed via the API **must** have `json:"fieldName"` tags.
- TypeScript types in the dashboard define the API contract.
- The `make test-contract` target validates response shapes without a running stack.
- The OpenAPI spec in `api/openapi.yaml` is the canonical reference.

## Code Style

- Follow idiomatic Go conventions.
- Explicit error handling; no `panic` except in `init()`.
- Use `context.Context` propagation for cancellation.
- Structured logging with `log/slog`.
- Table-driven tests for all new code.
- Max 300 lines per file, max 50 lines per function (split if larger).

## Git Workflow

When committing, use `--no-gpg-sign` to avoid GPG timeout issues:

```bash
git commit --no-gpg-sign -m "commit message"
```

## Docker

```bash
make docker            # Build image (gatewayfm/loadgenerator:latest)
make docker-push       # Push to DockerHub
```

Override the image name or tag:

```bash
DOCKER_IMAGE=myregistry/loadgen DOCKER_TAG=v1.2.3 make docker
```
