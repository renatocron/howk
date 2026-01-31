# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

HOWK (High Opinionated Webhook Kit) is a high-throughput, fault-tolerant webhook delivery system built on Kafka + Redis. The system follows a strict philosophy: **Kafka is the source of truth**, Redis is rebuildable hot state, and circuit breakers protect endpoints.

## Project Structure

```
cmd/
  api/         - HTTP API for enqueueing webhooks
  worker/      - Consumes pending webhooks and delivers them
  scheduler/   - Pops due retries from Redis and re-enqueues to Kafka
  reconciler/  - Rebuilds Redis state from Kafka replay

internal/
  domain/      - Core types (Webhook, DeliveryResult, CircuitBreaker)
  config/      - Configuration structs with defaults
  broker/      - Kafka abstraction (KafkaBroker, WebhookPublisher)
  hotstate/    - Redis hot state management (circuit breakers, retries, status)
  circuit/     - Circuit breaker implementation (per-endpoint state machine)
  retry/       - Retry strategy (exponential backoff with jitter)
  delivery/    - HTTP client for webhook delivery
  worker/      - Worker loop (consume, check circuit, deliver, produce result)
  scheduler/   - Scheduler loop (poll Redis sorted set, re-enqueue)
  api/         - Gin HTTP server with enqueue and status endpoints
  reconciler/  - State rebuilder from Kafka replay
```

## Commands

### Infrastructure

```bash
# Start infrastructure (Redpanda/Kafka + Redis)
make infra

# Stop infrastructure
make infra-down

# Clean infrastructure (removes volumes)
make infra-clean
```

After `make infra`, services are available at:
- Kafka: `localhost:19092`
- Redis: `localhost:6379`
- Redpanda Console: `http://localhost:8888`
- Webhook echo server: `http://localhost:8090`

### Build

```bash
# Build all binaries
make build

# Binaries output to bin/howk-{api,worker,scheduler}
```

### Running Components

```bash
# Run individual components (development mode)
make run-api        # Start API server on :8080
make run-worker     # Start worker
make run-scheduler  # Start scheduler

# Or run everything together
make run-all
```

### Testing

```bash
# Run all tests
make test

# Run tests with coverage
make test-coverage

# Quick manual tests
make test-enqueue              # Enqueue a test webhook
make test-status ID=wh_xxx     # Check webhook status
make test-stats                # View system stats
```

### Development

```bash
# Format code
make fmt

# Lint code
make lint

# Update dependencies
make deps
```

## Architecture Principles

### Kafka as Source of Truth

- **Every webhook** is a record in `howk.pending` topic
- **Every delivery result** is a record in `howk.results` topic
- **Exhausted retries** go to `howk.deadletter` topic
- Retention: 7 days (configurable)
- If Redis dies, replay Kafka to rebuild state

### Redis as Hot State

Redis stores rebuildable, ephemeral state:

1. **Circuit Breaker State** (per endpoint hash):
   ```
   HSET circuit:{endpoint_hash} state=OPEN failures=5 last_failure_at=...
   ```

2. **Retry Queue** (sorted set by next retry time):
   ```
   ZADD retries <next_at_unix> <webhook_json>
   ```

3. **Webhook Status** (per webhook ID):
   ```
   HSET status:{webhook_id} state=delivered attempt=1 last_status_code=200 ...
   ```

4. **Statistics** (hourly buckets):
   ```
   INCR stats:delivered:2026013015
   PFADD stats:hll:endpoints:2026013015 {endpoint}
   ```

### Circuit Breaker

Per-endpoint circuit breaker with three states:
- **CLOSED**: Normal operation, failures counted in window
- **OPEN**: Endpoint is down, don't attempt delivery, schedule far future retry
- **HALF_OPEN**: Recovery timeout expired, allow ONE probe request

State transitions:
- CLOSED → OPEN: failures exceed threshold in window
- OPEN → HALF_OPEN: recovery timeout expires
- HALF_OPEN → CLOSED: probe succeeds
- HALF_OPEN → OPEN: probe fails

Circuit state is keyed by `EndpointHash` (SHA256 of endpoint URL first 16 bytes).

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

### Data Flow

1. **API** receives webhook → validates → batch produces to `howk.pending` → returns 202
2. **Worker** consumes `howk.pending` → checks circuit → fires HTTP → produces `DeliveryResult` to `howk.results`
3. If retry needed: Worker schedules retry in Redis sorted set
4. **Scheduler** polls Redis sorted set → re-enqueues due webhooks to `howk.pending`
5. **Results consumer** (part of worker) updates Redis state from `howk.results`

## Key Domain Types

### Webhook
- `ID`: ULID-based unique identifier
- `ConfigID`: Customer/tenant identifier
- `Endpoint`: Target URL
- `EndpointHash`: SHA256 hash of endpoint (for circuit breaker keys)
- `Payload`: JSON payload to deliver
- `Attempt`: Current attempt number (1-indexed)
- `MaxAttempts`: Maximum retry attempts (default 20)
- `ScheduledAt`: When this delivery should be attempted

### DeliveryResult
- `Success`: Whether delivery succeeded (2xx status)
- `StatusCode`: HTTP status code
- `ShouldRetry`: Whether to schedule retry
- `NextRetryAt`: When to retry (if applicable)
- `Webhook`: Original webhook (for retry scheduling)

### CircuitBreaker States
- `CircuitClosed`: Normal operation
- `CircuitOpen`: Endpoint is down
- `CircuitHalfOpen`: Probing for recovery

## Configuration

Configuration is loaded via `config.DefaultConfig()` with sensible defaults. Key settings:

- **Kafka**: Brokers, topics, consumer group, retention, compression (snappy)
- **Redis**: Address, pool size, timeouts
- **Delivery**: HTTP timeout (30s), connection pooling
- **Retry**: Base delay (10s), max delay (24h), max attempts (20), jitter (0.2)
- **Circuit Breaker**: Failure threshold (5), recovery timeout (5m), success threshold (2)
- **Scheduler**: Poll interval (1s), batch size (500)

## Recovery Scenarios

### Redis Dies

1. Redis comes back empty
2. Run reconciler: `go run ./cmd/reconciler --from-beginning`
3. Reconciler replays `howk.pending` and `howk.results` topics
4. Rebuilds: circuit breaker states, retry queue, status hashes, stats
5. Normal operation resumes

During rebuild, workers keep delivering (Kafka is the queue). Status queries may return stale data.

## Testing Strategy

- Unit tests for retry strategy, circuit breaker logic, domain helpers
- Integration tests require infrastructure (use `make infra` first)
- Manual testing: `make test-enqueue` sends webhook to echo server at `localhost:8090`

## Important Patterns

### Idempotency
- Webhooks can have an `idempotency_key` to prevent duplicates (application-level)
- System guarantees **at-least-once delivery** - receivers must handle duplicates

### Error Handling
- Retryable: 5xx, 408, 429
- Non-retryable: 4xx (except 408, 429)
- Circuit opens on consecutive failures in window, not just count

### Structured Logging
- Uses `zerolog` for structured logging
- Log level: Info (default), configurable via code
- Console output in development

### Graceful Shutdown
- All components listen for SIGINT/SIGTERM
- Context cancellation propagates through all goroutines
- Kafka consumers commit offsets before shutdown
