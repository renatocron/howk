# HOWK - Agent Guide

This document provides essential information for AI coding agents working with the HOWK codebase.

## Project Overview

**HOWK** (High Opinionated Webhook Kit) is a high-throughput, fault-tolerant webhook delivery system built on Kafka + Redis. It follows a strict philosophy: **Kafka is the source of truth**, Redis is rebuildable hot state, and circuit breakers protect endpoints.

### Core Philosophy
- **Kafka is the source of truth** — every webhook and delivery result is a Kafka record
- **Redis is rebuildable hot state** — if Redis dies, replay from Kafka
- **Circuit breakers protect endpoints** — failing endpoints don't burn your retry budget
- **At-least-once delivery** — we never lose a webhook, duplicates are the receiver's problem

## Technology Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.24.0 |
| Message Queue | Kafka (Redpanda for local dev) |
| Hot State | Redis 7 |
| HTTP Framework | Gin |
| Logging | zerolog |
| Configuration | viper |
| Testing | testify, miniredis |
| Scripting | Lua (gopher-lua) |
| Build | Make |

## Project Structure

```
cmd/                          # Entry points for each binary
  api/main.go                 # HTTP API server
  worker/main.go              # Webhook delivery worker
  scheduler/main.go           # Retry scheduler
  reconciler/main.go          # State rebuilder from Kafka

internal/                     # Internal packages
  domain/                     # Core domain types
    types.go                  # Webhook, DeliveryResult, CircuitBreaker types
  config/                     # Configuration management
    config.go                 # Config structs and loading
  broker/                     # Kafka abstraction
    kafka.go                  # Kafka broker implementation
    broker.go                 # Publisher interface
  hotstate/                   # Redis hot state management
    redis.go                  # Redis client
    hotstate.go               # State operations (retries, status, stats)
    compression.go            # Gzip compression for retry data
  circuit/                    # Circuit breaker
    breaker.go                # Per-endpoint circuit breaker
  retry/                      # Retry strategy
    strategy.go               # Exponential backoff with jitter
  delivery/                   # HTTP delivery
    client.go                 # Webhook HTTP client with signing
  worker/                     # Worker implementation
    worker.go                 # Main worker loop
  scheduler/                  # Scheduler implementation
    scheduler.go              # Retry polling and re-enqueue
  api/                        # HTTP handlers
    server.go                 # Gin server setup
    h_webhook.go              # Webhook enqueue/status handlers
    h_script.go               # Lua script management handlers
    h_check_deps.go           # Health check handler
  script/                     # Lua scripting engine
    engine.go                 # Sandboxed Lua execution
    loader.go                 # Script loading/caching
    validator.go              # Script validation
    publisher.go              # Script publishing to Kafka
    modules/                  # Lua modules
      kv.go                   # Redis-backed key-value storage
      http.go                 # HTTP GET with allowlist
      crypto.go               # RSA credential decryption
      base64.go               # Base64 encoding
      log.go                  # Structured logging
  reconciler/                 # State recovery
    reconciler.go             # Kafka replay to rebuild Redis
  testutil/                   # Test utilities
    testutil.go               # Test helpers
    fixtures.go               # Test data
    redis.go                  # Redis test setup
    kafka.go                  # Kafka test setup

docs/                         # Documentation
  FAILURE_MODES.md            # Failure scenarios and recovery

.docker-compose.yml           # Local infrastructure (Kafka, Redis, echo server)
Makefile                      # Build and test commands
config.example.yaml           # Example configuration
.env.example                  # Example environment variables
```

## Build Commands

```bash
# Build all binaries
make build

# Build individual components
go build -o bin/howk-api ./cmd/api
go build -o bin/howk-worker ./cmd/worker
go build -o bin/howk-scheduler ./cmd/scheduler
go build -o bin/howk-reconciler ./cmd/reconciler
```

## Development Commands

```bash
# Start infrastructure (Kafka, Redis, echo server)
make infra

# Stop infrastructure
make infra-down

# Clean infrastructure (removes volumes)
make infra-clean

# Run components individually (for development)
make run-api        # API server on :8080
make run-worker     # Worker
make run-scheduler  # Scheduler

# Run all components together
make run-all
```

## Testing Commands

```bash
# Run unit tests only (fast, no infrastructure needed)
make test-unit

# Run integration tests (requires Docker infrastructure)
make test-integration

# Run all tests with coverage
make test-coverage

# CI test pipeline (unit + integration)
make test-ci

# Lint code
make lint

# Format code
make fmt
```

### Test Structure

- **Unit tests:** Fast, use `miniredis` for Redis mocking, no external dependencies
- **Integration tests:** Require Docker infrastructure, marked with `//go:build integration` tag
- **Test utilities:** Located in `internal/testutil/`

### Test Environment Variables

```bash
TEST_REDIS_ADDR=localhost:6380      # Default for Docker Redis
TEST_KAFKA_BROKERS=localhost:19092  # Default for Docker Kafka
```

## Configuration

Configuration is loaded with priority (highest to lowest):
1. Environment variables (`HOWK_*` prefix)
2. Config file (YAML)
3. Built-in defaults

### Environment Variables

All config can be overridden via `HOWK_*` prefixed environment variables:

```bash
HOWK_API_PORT=9090
HOWK_KAFKA_BROKERS=localhost:19092,kafka2:9092
HOWK_REDIS_ADDR=redis.example.com:6379
HOWK_CIRCUIT_BREAKER_FAILURE_THRESHOLD=3
```

See `.env.example` for complete list.

### Config File Locations

Searched in order (if `--config` not specified):
1. Current directory (`./config.yaml`)
2. Home directory (`~/.howk/config.yaml`)
3. System config (`/etc/howk/config.yaml`)

### Key Configuration Sections

```yaml
api:                    # HTTP API settings
  port: 8080
  read_timeout: 10s
  write_timeout: 10s

kafka:                  # Kafka configuration
  brokers: [localhost:19092]
  topics:
    pending: howk.pending
    results: howk.results
    deadletter: howk.deadletter
    scripts: howk.scripts

redis:                  # Redis configuration
  addr: localhost:6379
  password: ""
  pool_size: 100

delivery:               # HTTP delivery settings
  timeout: 30s
  max_idle_conns: 100

retry:                  # Retry strategy
  base_delay: 10s
  max_delay: 24h
  max_attempts: 20
  jitter: 0.2

circuit_breaker:        # Circuit breaker settings
  failure_threshold: 5
  recovery_timeout: 5m

scheduler:              # Scheduler settings
  poll_interval: 1s
  batch_size: 500

ttl:                    # Redis key TTLs
  circuit_state_ttl: 24h
  status_ttl: 168h
  stats_ttl: 48h

lua:                    # Lua scripting (disabled by default)
  enabled: false
  timeout: 500ms
  memory_limit_mb: 50
  allowed_hosts: ["*"]
```

## Architecture Details

### Data Flow

1. **API** receives webhook → validates → batch produces to `howk.pending` → returns 202
2. **Worker** consumes `howk.pending` → checks circuit → [OPTIONAL: executes Lua script] → fires HTTP → produces `DeliveryResult` to `howk.results`
3. If retry needed: Worker schedules retry in Redis sorted set
4. **Scheduler** polls Redis sorted set → re-enqueues due webhooks to `howk.pending`
5. **Results consumer** (part of worker) updates Redis state from `howk.results`

### Kafka Topics

| Topic | Purpose | Partitions | Retention |
|-------|---------|------------|-----------|
| `howk.pending` | Webhooks to deliver | 6 | 7 days |
| `howk.results` | Delivery outcomes | 6 | 7 days |
| `howk.deadletter` | Exhausted retries | 3 | 7 days |
| `howk.scripts` | Lua script configs | 3 | compacted |

### Redis Key Structure

```
circuit:{endpoint_hash}     # Circuit breaker state (hash)
retries                     # Retry queue (sorted set by timestamp)
retry_data:{webhook_id}     # Compressed webhook data (string)
retry_meta:{id}:{attempt}   # Retry metadata (hash)
status:{webhook_id}         # Webhook delivery status (hash)
stats:{metric}:{hour}       # Hourly statistics (string)
```

### Circuit Breaker States

- **CLOSED**: Normal operation, failures counted in window
- **OPEN**: Endpoint is down, don't attempt delivery, schedule far future retry
- **HALF_OPEN**: Recovery timeout expired, allow ONE probe request

State transitions:
- CLOSED → OPEN: failures exceed threshold in window
- OPEN → HALF_OPEN: recovery timeout expires
- HALF_OPEN → CLOSED: probe succeeds
- HALF_OPEN → OPEN: probe fails

### Retry Strategy

Exponential backoff with circuit awareness:

```
Base delay: 10s
Max delay: 24h
Max attempts: 20
Jitter: ±20%

Circuit CLOSED:    delay = base * (2^min(attempt, 10)) + jitter
Circuit OPEN:      delay = recovery_timeout (5 minutes)
Circuit HALF_OPEN: immediate (it's a probe)
```

## Lua Scripting Engine

HOWK supports per-config payload transformation via sandboxed Lua scripts.

### Feature Flag

Scripts are **disabled by default**. Must explicitly enable:

```bash
HOWK_LUA_ENABLED=true
```

### Script Modules

- **kv**: Redis-backed storage (`kv.get()`, `kv.set()`, `kv.del()`)
- **http**: HTTP GET with allowlist (`http.get(url, headers)`)
- **crypto**: RSA-OAEP + AES-GCM decryption (`crypto.decrypt_credential()`)
- **base64**: Base64 encoding/decoding
- **json**: JSON encode/decode

### Script Input/Output

```lua
-- Input globals (read-only)
payload           -- string: raw JSON payload
headers           -- table: HTTP headers
metadata          -- table: {attempt, config_id, webhook_id, created_at}
previous_error    -- table|nil: {status_code, error, attempt} on retries

-- Output (write to these)
request.body      -- string: override outgoing payload
request.headers   -- table: additional/override headers
config.opt_out_default_headers  -- bool: skip X-Webhook-* headers
```

### Security Constraints

- Sandboxed (no `io`, `os`, `debug` modules)
- CPU timeout: 500ms (default)
- Memory limit: 50MB per execution
- HTTP module enforces hostname allowlist

## Code Style Guidelines

### Package Structure
- Each package has a clear, single responsibility
- Domain types in `internal/domain/`
- Interfaces defined at consumer, implemented at provider
- Test utilities in `internal/testutil/`

### Naming Conventions
- Go standard naming: `PascalCase` for exported, `camelCase` for unexported
- Test files: `*_test.go` for unit tests, `*_integration_test.go` for integration
- Build tags: `//go:build integration` for integration tests

### Error Handling
- Use `fmt.Errorf()` with `%w` verb for error wrapping
- Structured logging with `zerolog`
- Return errors, don't log and swallow

### Logging
- Use `log.Info()`, `log.Error()`, etc. from `zerolog`
- Structured fields: `log.Info().Str("webhook_id", id).Msg("delivered")`
- Console output in development (configured in `main.go`)

## Testing Guidelines

### Unit Tests
- Use `miniredis` for Redis mocking
- No external dependencies
- Fast execution (< 5 seconds total)

```go
func TestSomething(t *testing.T) {
    // Use miniredis
    s := miniredis.RunT(&t)
    defer s.Close()

    // Test logic
}
```

### Integration Tests
- Marked with `//go:build integration`
- Require Docker infrastructure (`make infra`)
- Use real Redis and Kafka
- Clean up after tests

```go
//go:build integration

func TestIntegration(t *testing.T) {
    // Use real Redis/Kafka
    rdb := testutil.NewTestRedis(t)
    brokers := testutil.GetKafkaBrokers()

    // Test logic
}
```

### Test Helpers

```go
// Create test webhook
wh := testutil.NewTestWebhook("https://example.com/webhook")
wh := testutil.NewTestWebhookWithOpts(testutil.WebhookOpts{
    Endpoint: "https://api.example.com/webhook",
    ConfigID: "tenant-123",
})

// Wait for condition
testutil.WaitFor(t, 5*time.Second, func() bool {
    return someCondition()
})
```

## Security Considerations

### Webhook Signing
- Support for HMAC-SHA256 webhook signatures
- Signing secret per webhook configuration
- Header: `X-Webhook-Signature`

### Lua Script Security
- Sandboxed execution environment
- No filesystem or network access (except HTTP module with allowlist)
- CPU and memory limits enforced
- Disabled by default

### Circuit Breaker Protection
- Prevents overwhelming failing endpoints
- Per-endpoint isolation via endpoint hash
- Automatic recovery with probe requests

## Recovery Procedures

### Redis Dies

```bash
# 1. Redis comes back (empty or from backup)
# 2. Run reconciler to rebuild state
go run ./cmd/reconciler --from-beginning
# 3. Normal operation resumes
```

Reconciler replays `howk.pending` and `howk.results` topics to rebuild:
- Circuit breaker states
- Retry queue
- Status hashes
- Statistics

### Kafka Issues
- Kafka handles broker failures internally via replication
- No manual intervention needed (unless all replicas lost)
- Consumer groups rebalance automatically

## CI/CD

GitHub Actions workflow in `.github/workflows/test.yml`:
- Runs on push to `main` and PRs
- Starts Redis and Redpanda services
- Runs unit and integration tests with race detector
- Uploads coverage to Codecov

## Manual Testing

```bash
# Start infrastructure
make infra

# Run API
make run-api

# Enqueue test webhook (in another terminal)
make test-enqueue

# Check webhook status
make test-status ID=wh_xxx

# View system stats
make test-stats
```

## Key Files for Common Tasks

| Task | File(s) |
|------|---------|
| Add new config option | `internal/config/config.go` |
| Add new domain type | `internal/domain/types.go` |
| Modify retry logic | `internal/retry/strategy.go` |
| Modify circuit breaker | `internal/circuit/breaker.go` |
| Add Lua module | `internal/script/modules/*.go` |
| Add HTTP handler | `internal/api/h_*.go` |
| Add integration test | `internal/{pkg}/*_integration_test.go` |

## Common Gotchas

1. **Redis port**: Docker Redis runs on port 6380, not 6379 (to avoid conflicts)
2. **Redpanda upgrade**: Use `make infra-clean` when upgrading Redpanda versions
3. **Lua disabled**: Scripts are disabled by default, must set `HOWK_LUA_ENABLED=true`
4. **Consumer groups**: Tests use unique consumer groups to avoid cross-test interference
5. **Test isolation**: Always clean up Redis keys in integration tests
