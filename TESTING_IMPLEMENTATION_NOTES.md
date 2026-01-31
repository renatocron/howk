# Testing Implementation Notes

Quick reference for implementing the test suite.

## Key Code Characteristics

### Domain Package (`internal/domain/types.go`)
- **Pure functions**: `HashEndpoint()`, `IsRetryable()`, `IsSuccess()`
- `HashEndpoint()` uses SHA256, takes first 16 bytes (32 hex chars)
- `IsRetryable()` returns true for: 5xx, 408 (timeout), 429 (rate limit)
- `IsSuccess()` returns true for: 200-299 status codes

### Retry Strategy (`internal/retry/strategy.go`)
- Uses `math/rand` for jitter - **test bounds, not exact values**
- Formula: `delay = baseDelay * 2^min(attempt, 10)` with jitter ±20%
- Circuit states affect delays:
  - CLOSED: normal exponential backoff
  - OPEN: 5 minutes fixed
  - HALF_OPEN: 30 seconds fixed
- Max delay cap: 24 hours
- `ShouldRetry()` checks: attempt < maxAttempts, network errors always retry, status code retryable

### Circuit Breaker (`internal/circuit/breaker.go`)
- Uses Redis with optimistic locking (WATCH/MULTI/EXEC)
- Key format: `circuit:{endpoint_hash}`
- States: CLOSED, OPEN, HALF_OPEN
- Transitions:
  - CLOSED → OPEN: failures >= threshold (5) in window (60s)
  - OPEN → HALF_OPEN: after recovery timeout (5min)
  - HALF_OPEN → CLOSED: successes >= threshold (2)
  - HALF_OPEN → OPEN: any failure
- TTL: 24h if open/half-open, 2×recovery_timeout if closed with 0 failures
- **Important**: Use `miniredis` for unit tests, real Redis for integration tests

### Worker (`internal/worker/worker.go`)
- Idempotency check: `CheckAndSetProcessed(webhookID, attempt, 24h)`
- Processing flow:
  1. Check idempotency
  2. Check circuit breaker
  3. Update status to "delivering"
  4. Deliver webhook
  5. Record success/failure with circuit breaker
  6. Schedule retry if needed OR send to dead letter
  7. Publish result to Kafka
  8. Record stats

### Delivery Client (`internal/delivery/client.go`)
- **Signature generation**: HMAC-SHA256 of payload with signing secret
- Headers set:
  - `Content-Type: application/json`
  - `X-Webhook-ID: {webhook.ID}`
  - `X-Webhook-Timestamp: {timestamp}`
  - `X-Webhook-Signature: {hmac_sha256}`
  - Custom headers from `webhook.Headers`
- HTTP client configured in `config.DeliveryConfig`

### Hot State (`internal/hotstate/redis.go`)
- **Status**: `HSET status:{webhook_id} ...`
- **Retry queue**: `ZADD retries {next_at_unix} {webhook_json}` (sorted set by time)
- **Stats**: `INCR stats:{metric}:{YYYYMMDDHH}` (hourly buckets)
- **HLL**: `PFADD stats:hll:endpoints:{YYYYMMDDHH} {endpoint_hash}`
- **Idempotency**: `SETNX processed:{webhook_id}:{attempt} 1 EX {ttl}`
- **Lua script**: `PopDueRetries` atomically pops webhooks with score <= now

### Broker (`internal/broker/kafka.go`)
- Producer: sync, batched, snappy compression
- Consumer: consumer group, round-robin rebalancing
- Topics: `howk.pending`, `howk.results`, `howk.deadletter`
- **Testing challenge**: Async consumption - need to wait for consumer group join (2s warmup)

## Testing Utilities Structure

```
internal/testutil/
├── testutil.go          # General helpers (NewTestWebhook, WaitFor)
├── fixtures.go          # Test data constants
├── redis.go             # Redis setup (integration tag)
└── kafka.go             # Kafka setup (integration tag)
```

### testutil.go Example
```go
package testutil

import (
    "testing"
    "time"
    "github.com/oklog/ulid/v2"
    "github.com/howk/howk/internal/domain"
)

func NewTestWebhook(endpoint string) *domain.Webhook {
    return &domain.Webhook{
        ID:           domain.WebhookID("wh_test_" + ulid.Make().String()),
        ConfigID:     "test-tenant",
        Endpoint:     endpoint,
        EndpointHash: domain.HashEndpoint(endpoint),
        Payload:      []byte(`{"test":true}`),
        Attempt:      0,
        MaxAttempts:  3,
        CreatedAt:    time.Now(),
        ScheduledAt:  time.Now(),
    }
}

func WaitFor(t *testing.T, timeout time.Duration, condition func() bool) {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if condition() {
            return
        }
        time.Sleep(50 * time.Millisecond)
    }
    t.Fatal("timeout waiting for condition")
}
```

### redis.go Example (integration tag)
```go
//go:build integration

package testutil

import (
    "context"
    "testing"
    "github.com/howk/howk/internal/config"
    "github.com/howk/howk/internal/hotstate"
)

func SetupRedis(t *testing.T) *hotstate.RedisHotState {
    if testing.Short() {
        t.Skip("skipping integration test in short mode")
    }

    cfg := config.RedisConfig{
        Addr: "localhost:6379",
        DB: 15, // Use test DB
    }

    hs, err := hotstate.NewRedisHotState(cfg)
    if err != nil {
        t.Fatalf("failed to connect to redis: %v", err)
    }

    // Flush test DB on cleanup
    t.Cleanup(func() {
        ctx := context.Background()
        hs.Client().FlushDB(ctx)
        hs.Close()
    })

    return hs
}
```

## Common Test Patterns

### 1. Table-Driven Test for Status Codes
```go
func TestIsRetryable(t *testing.T) {
    tests := []struct {
        name string
        code int
        want bool
    }{
        {"success 200", 200, false},
        {"success 204", 204, false},
        {"timeout 408", 408, true},
        {"rate limit 429", 429, true},
        {"server error 500", 500, true},
        {"bad request 400", 400, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := domain.IsRetryable(tt.code)
            require.Equal(t, tt.want, got)
        })
    }
}
```

### 2. Mock HTTP Server for Delivery Tests
```go
func TestDeliver_Success(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Verify headers
        assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
        assert.NotEmpty(t, r.Header.Get("X-Webhook-Signature"))

        // Verify body
        body, _ := io.ReadAll(r.Body)
        assert.JSONEq(t, `{"test":true}`, string(body))

        w.WriteHeader(200)
    }))
    defer server.Close()

    client := delivery.NewClient(config.DefaultConfig().Delivery)
    webhook := testutil.NewTestWebhook(server.URL)
    webhook.Payload = []byte(`{"test":true}`)
    webhook.SigningSecret = "secret123"

    result := client.Deliver(context.Background(), webhook)

    assert.NoError(t, result.Error)
    assert.Equal(t, 200, result.StatusCode)
    assert.Greater(t, result.Duration, time.Duration(0))
}
```

### 3. Circuit Breaker State Transitions with miniredis
```go
func TestCircuitBreaker_StateTransitions(t *testing.T) {
    mr, err := miniredis.Run()
    require.NoError(t, err)
    defer mr.Close()

    rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
    defer rdb.Close()

    cfg := config.CircuitBreakerConfig{
        FailureThreshold: 3,
        RecoveryTimeout: 100 * time.Millisecond,
        SuccessThreshold: 2,
    }

    breaker := circuit.NewBreaker(rdb, cfg)
    endpoint := domain.HashEndpoint("https://example.com")
    ctx := context.Background()

    // Initial state: CLOSED
    cb, _ := breaker.Get(ctx, endpoint)
    assert.Equal(t, domain.CircuitClosed, cb.State)

    // Record 3 failures → OPEN
    for i := 0; i < 3; i++ {
        breaker.RecordFailure(ctx, endpoint)
    }

    cb, _ = breaker.Get(ctx, endpoint)
    assert.Equal(t, domain.CircuitOpen, cb.State)

    // Wait for recovery timeout → HALF_OPEN
    time.Sleep(150 * time.Millisecond)
    allowed, isProbe, _ := breaker.ShouldAllow(ctx, endpoint)
    assert.True(t, allowed)
    assert.True(t, isProbe)

    cb, _ = breaker.Get(ctx, endpoint)
    assert.Equal(t, domain.CircuitHalfOpen, cb.State)

    // Record 2 successes → CLOSED
    breaker.RecordSuccess(ctx, endpoint)
    breaker.RecordSuccess(ctx, endpoint)

    cb, _ = breaker.Get(ctx, endpoint)
    assert.Equal(t, domain.CircuitClosed, cb.State)
}
```

### 4. Integration Test with Real Redis
```go
//go:build integration

func TestRedisHotState_ScheduleAndPopRetries(t *testing.T) {
    hs := testutil.SetupRedis(t)
    ctx := context.Background()

    // Schedule retry for 100ms in the future
    webhook := testutil.NewTestWebhook("https://example.com")
    retryAt := time.Now().Add(100 * time.Millisecond)

    err := hs.ScheduleRetry(ctx, &hotstate.RetryMessage{
        Webhook: webhook,
        ScheduledAt: retryAt,
        Reason: "test",
    })
    require.NoError(t, err)

    // Pop immediately - should get nothing
    retries, err := hs.PopDueRetries(ctx, 10)
    require.NoError(t, err)
    assert.Empty(t, retries)

    // Wait for retry to be due
    time.Sleep(150 * time.Millisecond)

    // Pop again - should get the webhook
    retries, err = hs.PopDueRetries(ctx, 10)
    require.NoError(t, err)
    require.Len(t, retries, 1)
    assert.Equal(t, webhook.ID, retries[0].Webhook.ID)
}
```

### 5. Async Kafka Consumer Test
```go
//go:build integration

func TestKafka_PublishSubscribe(t *testing.T) {
    broker := testutil.SetupKafka(t)
    topic := "test-" + ulid.Make().String()

    received := make(chan *broker.Message, 1)
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    // Start consumer
    go func() {
        broker.Subscribe(ctx, topic, "test-group", func(ctx context.Context, msg *broker.Message) error {
            received <- msg
            return nil
        })
    }()

    // Wait for consumer group join
    time.Sleep(2 * time.Second)

    // Publish message
    testMsg := broker.Message{
        Key:   []byte("test-key"),
        Value: []byte("test-value"),
    }
    err := broker.Publish(ctx, topic, testMsg)
    require.NoError(t, err)

    // Wait for message
    select {
    case msg := <-received:
        assert.Equal(t, testMsg.Key, msg.Key)
        assert.Equal(t, testMsg.Value, msg.Value)
    case <-time.After(5 * time.Second):
        t.Fatal("message not received within timeout")
    }
}
```

## Configuration for Tests

### Default Test Config
```go
func testConfig() *config.Config {
    cfg := config.DefaultConfig()

    // Shorter timeouts for faster tests
    cfg.CircuitBreaker.RecoveryTimeout = 100 * time.Millisecond
    cfg.CircuitBreaker.ProbeInterval = 50 * time.Millisecond
    cfg.Delivery.Timeout = 1 * time.Second

    return cfg
}
```

### Redis Test DB
Use DB 15 for tests to avoid conflicts with development data:
```go
cfg := config.RedisConfig{
    Addr: "localhost:6379",
    DB: 15, // Test database
}
```

### Unique Kafka Topics
Generate unique topic names per test to avoid interference:
```go
topic := "test-pending-" + ulid.Make().String()
```

## Infrastructure Commands

```bash
# Start infrastructure
make infra

# Verify Redis
redis-cli -h localhost -p 6379 ping

# Verify Kafka
docker exec howk-redpanda-1 rpk cluster info

# List Kafka topics
docker exec howk-redpanda-1 rpk topic list

# Stop infrastructure
make infra-down

# Clean infrastructure (remove volumes)
make infra-clean
```

## Test Execution

```bash
# Unit tests (no infrastructure needed)
go test -v -race -short ./...
make test-unit

# Integration tests (requires infrastructure)
make infra
sleep 15
go test -v -race -tags=integration ./...
make test-integration

# E2E tests
make infra
go test -v -race -tags=e2e ./...
make test-e2e

# All tests
make test-all

# Coverage
make test-coverage
open coverage.html
```

## Common Pitfalls

1. **Jitter randomness**: Don't test exact delay values, test bounds
2. **Async consumers**: Always add 2s warmup after subscribing to Kafka
3. **Redis transactions**: Use miniredis for unit tests, real Redis for integration
4. **Time-dependent tests**: Use short timeouts in test configs (100ms instead of 5min)
5. **Test isolation**: Use unique IDs/topics/keys per test
6. **Build tags**: Don't forget `//go:build integration` at the top of integration test files
7. **Cleanup**: Always use `t.Cleanup()` for teardown
8. **Short mode**: Integration tests should skip with `if testing.Short() { t.Skip() }`

## Dependencies

```bash
# Required for unit tests
go get github.com/alicebob/miniredis/v2

# Already available
# - github.com/stretchr/testify v1.8.4
# - github.com/fortytw2/leaktest v1.3.0
```

## File Count Summary

- **Unit tests**: 7 files (domain, retry, delivery, circuit, config, testutil×2)
- **Integration tests**: 6 files (redis, kafka, worker, scheduler, testutil×2)
- **E2E tests**: 2 files (api, recovery)
- **Infrastructure**: 2 files (Makefile, GitHub Actions)
- **Total**: 17 files
