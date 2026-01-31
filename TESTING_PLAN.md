# HOWK Testing Plan

## Overview

Create a comprehensive test suite for the HOWK webhook delivery system, achieving 85%+ code coverage with a phased approach: unit tests → integration tests → E2E tests → CI/CD automation.

**Current State:**
- 0% test coverage, no test files exist
- `make test` runs but has nothing to test
- Go 1.25.5 available via asdf
- Infrastructure: docker-compose.yml with Redpanda (Kafka), Redis
- Dependencies: testify v1.8.4, leaktest v1.3.0 already in go.sum

**Strategy:**
- Unit tests: Use mocks (miniredis, httptest), no external dependencies
- Integration tests: Use docker-compose with `//go:build integration` tags
- E2E tests: Full system with all components running

---

## Phase 1: Foundation - Unit Tests (60% Coverage)

### Goal
Get `make test` passing with tests for pure logic packages. Fast, no external dependencies.

### Files to Create

#### 1. `/home/renato/projetos/gl/howk/internal/domain/types_test.go`
**Test Cases:**
- `TestHashEndpoint` - Verify deterministic SHA256 hashing
- `TestIsRetryable` - Table-driven tests for all status codes (408, 429, 500-599, 200-299, 400-499)
- `TestIsSuccess` - Table-driven tests for 2xx vs others

**Pattern:** Simple table-driven tests, 100% coverage achievable in <100 LOC

#### 2. `/home/renato/projetos/gl/howk/internal/retry/strategy_test.go`
**Test Cases:**
- `TestShouldRetry_Exhausted` - Attempt >= MaxAttempts returns false
- `TestShouldRetry_NetworkError` - Network errors always retryable
- `TestShouldRetry_StatusCodes` - Table-driven for retryable vs non-retryable codes
- `TestNextDelay_CircuitClosed` - Exponential backoff formula (2^attempt * baseDelay)
- `TestNextDelay_CircuitOpen` - Returns 5 minutes fixed
- `TestNextDelay_CircuitHalfOpen` - Returns 30 seconds fixed
- `TestNextDelay_MaxCap` - Verify capping at 24h max delay
- `TestAddJitter_Bounds` - Verify jitter is within ±20% of base delay
- `TestIsExhausted` - Boundary conditions
- `TestRetrySchedule` - Verify human-readable schedule generation

**Challenge:** `addJitter()` uses `math/rand` - test bounds, not exact values

#### 3. `/home/renato/projetos/gl/howk/internal/delivery/client_test.go`
**Test Cases:**
- `TestDeliver_Success` - Mock HTTP server returns 200
- `TestDeliver_Timeout` - Simulate request timeout
- `TestDeliver_NetworkError` - Server unreachable
- `TestDeliver_SignatureGeneration` - Verify HMAC-SHA256 format in X-Webhook-Signature header
- `TestDeliver_Headers` - Verify X-Webhook-ID, X-Webhook-Timestamp, Content-Type
- `TestDeliver_CustomHeaders` - Verify custom headers passed through
- `TestDeliver_PayloadSent` - Verify JSON payload in request body

**Approach:** Use `httptest.NewServer()` for mock endpoints

#### 4. `/home/renato/projetos/gl/howk/internal/circuit/breaker_test.go`
**Test Cases:**
- `TestGet_NoState` - Returns CLOSED by default when no Redis key exists
- `TestShouldAllow_Closed` - Always returns true
- `TestShouldAllow_Open_BeforeRecovery` - Returns false before recovery timeout
- `TestShouldAllow_Open_AfterRecovery` - Transitions to HALF_OPEN, returns true (probe)
- `TestShouldAllow_HalfOpen_ProbeInterval` - Allows probe, rejects between probes
- `TestRecordSuccess_Closed` - Resets failure counter
- `TestRecordSuccess_HalfOpen_BelowThreshold` - Increments success counter
- `TestRecordSuccess_HalfOpen_ReachThreshold` - Transitions to CLOSED
- `TestRecordFailure_Closed_BelowThreshold` - Increments failure counter
- `TestRecordFailure_Closed_ReachThreshold` - Transitions to OPEN
- `TestRecordFailure_HalfOpen` - Transitions back to OPEN
- `TestRecordFailure_WindowExpiry` - Resets failure count outside window

**Approach:** Use `github.com/alicebob/miniredis/v2` for in-memory Redis mock

#### 5. `/home/renato/projetos/gl/howk/internal/config/config_test.go`
**Test Cases:**
- `TestDefaultConfig` - Smoke test to verify all defaults are set

**Simple:** ~20 LOC verification

#### 6. `/home/renato/projetos/gl/howk/internal/testutil/testutil.go`
**Utilities:**
```go
// NewTestWebhook creates webhook with sensible defaults
func NewTestWebhook(endpoint string) *domain.Webhook

// NewTestWebhookWithOpts creates webhook with custom options
func NewTestWebhookWithOpts(opts WebhookOpts) *domain.Webhook

// WaitFor polls condition until true or timeout
func WaitFor(t *testing.T, timeout time.Duration, condition func() bool)
```

#### 7. `/home/renato/projetos/gl/howk/internal/testutil/fixtures.go`
**Constants:**
```go
var (
    ValidStatusCodes = []int{200, 201, 202, 204}
    RetryableStatusCodes = []int{408, 429, 500, 502, 503, 504}
    NonRetryableStatusCodes = []int{400, 401, 403, 404, 422}
)
```

### Dependencies to Add
```bash
go get github.com/alicebob/miniredis/v2
```

### Verification
```bash
# Run unit tests
make test-unit

# Should show passing tests for domain, retry, delivery, circuit, config packages
# Coverage should be ~60%
```

---

## Phase 2: Integration Tests (75% Coverage)

### Goal
Test external dependencies (Redis, Kafka) with real infrastructure using docker-compose.

### Files to Create

#### 8. `/home/renato/projetos/gl/howk/internal/hotstate/redis_integration_test.go`
**Build Tag:** `//go:build integration`

**Test Cases:**
- `TestSetStatus_GetStatus` - Round-trip status storage
- `TestScheduleRetry_PopDueRetries` - Lua script atomicity
- `TestPopDueRetries_OnlyDue` - Time-based filtering (only pop past due)
- `TestPopDueRetries_BatchSize` - Respects batch size limit
- `TestIncrStats_GetStats` - Hourly bucket counters
- `TestAddToHLL_GetStats` - HyperLogLog unique endpoint counting
- `TestCheckAndSetProcessed_First` - Returns true (not processed)
- `TestCheckAndSetProcessed_Duplicate` - Returns false (already processed)
- `TestCheckAndSetProcessed_TTL` - Expires after TTL

**Setup:** Connect to `localhost:6379`, flush test keys in setup/teardown

#### 9. `/home/renato/projetos/gl/howk/internal/broker/kafka_integration_test.go`
**Build Tag:** `//go:build integration`

**Test Cases:**
- `TestPublish_Subscribe_RoundTrip` - Publish message, consume via handler
- `TestPublishWebhook_Serialization` - Webhook → Kafka → deserialize
- `TestPublishResult_Headers` - Verify headers (tenant_id, endpoint_hash, success)
- `TestConsumerGroup_MultipleConsumers` - Load balancing across consumers

**Setup:** Connect to `localhost:19092`, use unique topic names per test

**Challenge:** Async consumption - use channels with 5s timeout

#### 10. `/home/renato/projetos/gl/howk/internal/worker/worker_integration_test.go`
**Build Tag:** `//go:build integration`

**Test Cases:**
- `TestWorker_SuccessfulDelivery` - Full lifecycle: consume → deliver → publish result
- `TestWorker_RetryAfterFailure` - Verify retry scheduled in Redis
- `TestWorker_CircuitOpens` - 5 failures → circuit opens
- `TestWorker_Idempotency` - Duplicate messages handled correctly

**Setup:** Requires Redis, Kafka, mock HTTP server (`httptest.NewServer`)

#### 11. `/home/renato/projetos/gl/howk/internal/scheduler/scheduler_integration_test.go`
**Build Tag:** `//go:build integration`

**Test Cases:**
- `TestScheduler_PopDueRetries` - Pop webhooks due for retry
- `TestScheduler_ReEnqueueToKafka` - Verify republished to pending topic
- `TestScheduler_NotDueYet` - Don't pop future retries

**Setup:** Redis + Kafka required

#### 12. `/home/renato/projetos/gl/howk/internal/testutil/redis.go`
**Build Tag:** `//go:build integration`

```go
// SetupRedis connects to localhost:6379, flushes DB on cleanup
func SetupRedis(t *testing.T) *hotstate.RedisHotState

// SetupRedisWithConfig allows custom config
func SetupRedisWithConfig(t *testing.T, cfg config.RedisConfig) *hotstate.RedisHotState
```

#### 13. `/home/renato/projetos/gl/howk/internal/testutil/kafka.go`
**Build Tag:** `//go:build integration`

```go
// SetupKafka connects to localhost:19092
func SetupKafka(t *testing.T) *broker.KafkaBroker

// CreateTestTopic creates unique topic for test
func CreateTestTopic(t *testing.T, broker *broker.KafkaBroker) string
```

### Verification
```bash
# Start infrastructure
make infra

# Wait for services
sleep 15

# Run integration tests
make test-integration

# Should show passing tests for hotstate, broker, worker, scheduler
# Coverage should be ~75%
```

---

## Phase 3: End-to-End Tests (85% Coverage)

### Goal
Full system behavior validation with all components running.

### Files to Create

#### 14. `/home/renato/projetos/gl/howk/internal/api/server_e2e_test.go`
**Build Tag:** `//go:build e2e`

**Test Cases:**
- `TestEnqueueWebhook_FullLifecycle` - POST → Kafka → worker → delivery → GET status
- `TestEnqueueWebhook_BatchConcurrent` - 100 concurrent requests
- `TestGetStatus_NotFound` - Returns 404 for unknown webhook ID
- `TestGetStats_Aggregation` - Verify hourly/daily stats calculation

**Setup:** Start API server, worker, scheduler in goroutines

#### 15. `/home/renato/projetos/gl/howk/e2e/recovery_test.go`
**Build Tag:** `//go:build e2e`

**Test Cases:**
- `TestCircuitRecovery` - Endpoint fails 5x → circuit opens → endpoint recovers → circuit closes
- `TestRetryUntilExhaustion` - Webhook fails 20x → dead letter queue
- `TestWorkerRestartResumesProcessing` - Stop worker, restart, processing continues

**Note:** These are advanced tests, can be deferred if time-constrained

### Verification
```bash
# Start infrastructure
make infra

# Run E2E tests
make test-e2e

# Coverage should be ~85%
```

---

## Phase 4: CI/CD Automation

### Makefile Enhancements

Add to `/home/renato/projetos/gl/howk/Makefile`:

```makefile
# Run unit tests only (no external deps)
test-unit:
	go test -v -race -short ./...

# Run integration tests (requires infra)
test-integration:
	go test -v -race -tags=integration ./...

# Run E2E tests (requires all services)
test-e2e:
	go test -v -race -tags=e2e ./...

# Run all tests
test-all: test-unit test-integration test-e2e

# CI test pipeline (unit + integration)
test-ci: infra
	@echo "Waiting for services..."
	@sleep 15
	go test -v -race -short ./...
	go test -v -race -tags=integration ./...

# Coverage with integration tests
test-coverage:
	go test -coverprofile=coverage.out -tags=integration ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
	go tool cover -func=coverage.out | grep total
```

Update existing `test` target:
```makefile
test: test-unit
```

### GitHub Actions Workflow

Create `.github/workflows/test.yml`:

```yaml
name: Tests

on:
  push:
    branches: [main]
  pull_request:

jobs:
  unit-tests:
    name: Unit Tests
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Install dependencies
        run: go mod download

      - name: Run unit tests
        run: make test-unit

  integration-tests:
    name: Integration Tests
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Start infrastructure
        run: docker-compose up -d

      - name: Wait for services
        run: sleep 20

      - name: Run integration tests
        run: make test-integration

      - name: Stop infrastructure
        if: always()
        run: docker-compose down -v

  coverage:
    name: Coverage Report
    runs-on: ubuntu-latest
    needs: [unit-tests, integration-tests]
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Start infrastructure
        run: docker-compose up -d

      - name: Wait for services
        run: sleep 20

      - name: Generate coverage
        run: make test-coverage

      - name: Upload coverage to Codecov
        uses: codecov/codecov-action@v3
        with:
          files: ./coverage.out

      - name: Stop infrastructure
        if: always()
        run: docker-compose down -v
```

### Verification
```bash
# Simulate CI locally
make clean
make deps
make test-ci

# Check coverage report
open coverage.html
```

---

## Implementation Order

1. **Day 1:** Create testutil package + domain tests + retry tests
2. **Day 2:** Delivery client tests + circuit breaker tests
3. **Day 3:** Verify Phase 1 complete (60% coverage)
4. **Day 4:** Redis integration tests + Kafka integration tests
5. **Day 5:** Worker integration tests
6. **Day 6:** Scheduler integration tests
7. **Day 7:** Verify Phase 2 complete (75% coverage)
8. **Day 8:** API E2E tests
9. **Day 9:** Recovery E2E tests (optional)
10. **Day 10:** Verify Phase 3 complete (85% coverage)
11. **Day 11:** Update Makefile + GitHub Actions workflow

---

## Critical Files to Modify/Create

### New Test Files (17 files)
1. `internal/domain/types_test.go`
2. `internal/retry/strategy_test.go`
3. `internal/delivery/client_test.go`
4. `internal/circuit/breaker_test.go`
5. `internal/config/config_test.go`
6. `internal/testutil/testutil.go`
7. `internal/testutil/fixtures.go`
8. `internal/testutil/redis.go` (integration tag)
9. `internal/testutil/kafka.go` (integration tag)
10. `internal/hotstate/redis_integration_test.go` (integration tag)
11. `internal/broker/kafka_integration_test.go` (integration tag)
12. `internal/worker/worker_integration_test.go` (integration tag)
13. `internal/scheduler/scheduler_integration_test.go` (integration tag)
14. `internal/api/server_e2e_test.go` (e2e tag)
15. `e2e/recovery_test.go` (e2e tag)

### Modified Files (2 files)
16. `Makefile` - Add test-unit, test-integration, test-e2e, test-ci targets
17. `.github/workflows/test.yml` - New CI workflow

### New Dependencies
```bash
go get github.com/alicebob/miniredis/v2
```

---

## Pre-Implementation Verification

Before writing any tests, verify the project builds and infrastructure works:

```bash
# Verify Go version
go version  # Should show go1.25.5 or compatible

# Verify dependencies
make deps

# Build all binaries
make build

# Start infrastructure
make infra

# Verify Kafka
docker exec howk-redpanda-1 rpk cluster info

# Verify Redis
redis-cli -h localhost -p 6379 ping

# Stop infrastructure
make infra-down
```

---

## Testing Patterns

### Table-Driven Tests
```go
func TestIsRetryable(t *testing.T) {
    tests := []struct {
        name       string
        statusCode int
        want       bool
    }{
        {"2xx success", 200, false},
        {"timeout", 408, true},
        {"rate limit", 429, true},
        {"server error", 500, true},
        {"client error", 400, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := domain.IsRetryable(tt.statusCode)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

### Integration Test Setup
```go
//go:build integration

func TestRedisIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test in short mode")
    }

    hs := testutil.SetupRedis(t)
    // Test uses real Redis at localhost:6379
    // Cleanup handled by t.Cleanup()
}
```

### Async Testing
```go
func TestKafkaConsume(t *testing.T) {
    received := make(chan *broker.Message, 1)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // Start consumer
    go broker.Subscribe(ctx, "test-topic", "test-group", func(ctx context.Context, msg *broker.Message) error {
        received <- msg
        return nil
    })

    // Wait for consumer to join group
    time.Sleep(2 * time.Second)

    // Publish message
    broker.Publish(ctx, "test-topic", testMsg)

    // Wait for message
    select {
    case msg := <-received:
        assert.Equal(t, testMsg.Key, msg.Key)
    case <-ctx.Done():
        t.Fatal("message not received within timeout")
    }
}
```

---

## Success Criteria

- [ ] `make test` passes without errors
- [ ] `make test-unit` runs in < 5 seconds
- [ ] `make test-integration` passes with infrastructure running
- [ ] Coverage > 85% (run `make test-coverage`)
- [ ] All packages have at least one test file
- [ ] CI workflow passes on GitHub Actions
- [ ] No race conditions detected (`-race` flag)

---

## Risk Mitigation

| Risk | Mitigation |
|------|-----------|
| Timing-dependent tests flaky | Use generous timeouts (5s), avoid `time.Sleep()` in unit tests |
| Kafka consumer lag in tests | Add 2s warmup after subscribing, use `WaitFor()` helper |
| Redis Lua script failures | Test with real Redis in integration tests |
| CI timeout (>10 min) | Split unit/integration jobs, run unit first for fast feedback |
| Test data pollution | Use unique prefixes (`wh_test_*`), cleanup in `t.Cleanup()` |

---

## Notes

- **Go version compatibility:** Tests use Go 1.23 features, compatible with 1.25.5
- **Docker Compose:** Existing `docker-compose.yml` uses Redpanda (Kafka-compatible) on port 19092
- **Test isolation:** Integration tests should use unique topic/key names to avoid conflicts
- **Short mode:** Unit tests should respect `-short` flag: `if testing.Short() { t.Skip() }`
- **Build tags:** Integration tests use `//go:build integration`, E2E tests use `//go:build e2e`
