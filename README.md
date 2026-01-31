[![codecov](https://codecov.io/gh/renatocron/howk/graph/badge.svg?token=UXH5PUHYH0)](https://codecov.io/gh/renatocron/howk)

# HOWK - High Opinionated Webhook Kit

A high-throughput, fault-tolerant webhook delivery system built on Kafka + Redis.

## Philosophy

- **Kafka is the source of truth** — every webhook and delivery result is a Kafka record
- **Redis is rebuildable hot state** — if Redis dies, replay from Kafka
- **Circuit breakers protect endpoints** — failing endpoints don't burn your retry budget
- **At-least-once delivery** — we never lose a webhook, duplicates are the receiver's problem

## Architecture

```
                    ┌─────────────────────────────────────┐
                    │           API Gateway               │
                    │  POST /webhooks/:tenant/enqueue     │
                    │  validate → batch produce           │
                    │  → 202 Accepted                     │
                    └───────────────┬─────────────────────┘
                                    │
                          Kafka Produce (batched)
                                    │
                                    ▼
                 ┌──────────────────────────────────────────┐
                 │            Kafka Cluster                 │
                 │                                          │
                 │  howk.pending    → webhooks to deliver   │
                 │  howk.results    → delivery outcomes     │
                 │  howk.deadletter → exhausted retries     │
                 │                                          │
                 │  retention: 7 days                       │
                 └────┬─────────────────────────┬──────────┘
                      │                         │
            ┌─────────┘                         └──────────┐
            ▼                                              ▼
  ┌──────────────────────┐                    ┌───────────────────────┐
  │   Worker Pool        │                    │   Results Consumer    │
  │   (N consumers)      │                    │                       │
  │                      │                    │   • Update Redis      │
  │   • Read pending     │                    │     status/stats      │
  │   • Check circuit    │                    │   • Feed ClickHouse   │
  │   • Fire HTTP        │                    │     (optional)        │
  │   • Produce result   │                    │                       │
  │   • Schedule retry   │                    └───────────────────────┘
  │     if needed        │
  └──────────┬───────────┘
             │
             ▼
  ┌──────────────────────────────────────────────────────────────────┐
  │                           Redis                                   │
  │                                                                   │
  │  Circuit Breaker (per endpoint):                                  │
  │    HSET circuit:{endpoint_hash} state=OPEN failures=5 last=...   │
  │                                                                   │
  │  Retry Scheduling:                                                │
  │    ZADD retries <next_at_unix> <webhook_json>                    │
  │                                                                   │
  │  Status (per webhook):                                            │
  │    HSET status:{webhook_id} state=delivered attempt=1 ...        │
  │                                                                   │
  │  Stats (hourly buckets):                                          │
  │    INCR stats:delivered:2026013015                               │
  │    PFADD stats:hll:endpoints:2026013015 {endpoint}               │
  │                                                                   │
  │  ══════════════════════════════════════════════════════════════  │
  │  ALL OF THIS IS REBUILDABLE FROM KAFKA REPLAY                    │
  └──────────────────────────────────────────────────────────────────┘
```

## Circuit Breaker Design

Per-endpoint circuit breaker with three states:

```
    ┌─────────────────────────────────────────────────────────────┐
    │                                                             │
    ▼                                                             │
┌────────┐   failure_threshold    ┌────────┐   recovery_timeout  ┌───────────┐
│ CLOSED │ ────────────────────▶  │  OPEN  │ ─────────────────▶  │ HALF_OPEN │
│        │      exceeded          │        │     expired         │           │
└────────┘                        └────────┘                     └───────────┘
    ▲                                  ▲                              │
    │                                  │                              │
    │         success                  │        probe fails           │
    └──────────────────────────────────┴──────────────────────────────┘
                                       │
                                       │ probe succeeds
                                       └────────────────────▶ CLOSED
```

**When circuit is OPEN:**
- Don't attempt delivery (save resources)
- Schedule retry far in the future (respect the endpoint)
- Periodically allow ONE probe request (HALF_OPEN)

**Circuit state is per-endpoint, stored in Redis, rebuildable from Kafka results.**

## Retry Strategy

Exponential backoff with circuit-aware delays:

```
Base delay: 10s
Max delay: 24h
Max attempts: 20
Jitter: ±20%

Circuit CLOSED:  delay = base * (2 ^ min(attempt, 10)) + jitter
Circuit OPEN:    delay = recovery_timeout (e.g., 5 minutes)
Circuit HALF_OPEN: immediate (it's a probe)
```

## Components

| Binary | Purpose |
|--------|---------|
| `howk-api` | HTTP API for enqueueing webhooks |
| `howk-worker` | Consumes pending, delivers, produces results |
| `howk-scheduler` | Pops due retries from Redis, re-enqueues to Kafka |
| `howk-reconciler` | Rebuilds Redis state from Kafka replay |

## Quick Start

```bash
# Start infrastructure
docker-compose up -d

# Run all components
make run-api
make run-worker
make run-scheduler

# Enqueue a webhook
curl -X POST http://localhost:8080/webhooks/tenant123/enqueue \
  -H "Content-Type: application/json" \
  -d '{
    "endpoint": "https://example.com/webhook",
    "payload": {"event": "user.created", "data": {"id": 123}},
    "idempotency_key": "user-created-123"
  }'
```

## Configuration

```yaml
# config.yaml
kafka:
  brokers: ["localhost:9092"]
  topics:
    pending: howk.pending
    results: howk.results
    deadletter: howk.deadletter
  consumer_group: howk-workers
  retention: 168h  # 7 days

redis:
  addr: "localhost:6379"

delivery:
  timeout: 30s
  max_idle_conns: 100
  max_conns_per_host: 10

retry:
  base_delay: 10s
  max_delay: 24h
  max_attempts: 20
  jitter: 0.2

circuit_breaker:
  failure_threshold: 5        # failures before OPEN
  failure_window: 60s         # window for counting failures
  recovery_timeout: 300s      # time before HALF_OPEN
  probe_interval: 60s         # time between probes in HALF_OPEN
  success_threshold: 2        # successes in HALF_OPEN before CLOSED
```

## API

### Enqueue Webhook

```
POST /webhooks/:tenant/enqueue
```

Request:
```json
{
  "endpoint": "https://customer.com/webhook",
  "payload": {"event": "order.completed"},
  "headers": {"X-Custom": "value"},
  "idempotency_key": "order-123-completed",
  "signing_secret": "whsec_..."
}
```

Response: `202 Accepted`
```json
{
  "webhook_id": "wh_01HQXYZ...",
  "status": "pending"
}
```

### Get Status

```
GET /webhooks/:webhook_id/status
```

Response:
```json
{
  "webhook_id": "wh_01HQXYZ...",
  "state": "delivered",
  "attempts": 2,
  "last_attempt_at": "2026-01-30T10:00:00Z",
  "last_status_code": 200,
  "next_retry_at": null
}
```

### Get Stats

```
GET /stats
```

Response:
```json
{
  "last_1h": {
    "enqueued": 7200,
    "delivered": 7150,
    "failed": 50,
    "unique_endpoints": 1200
  },
  "last_24h": {
    "enqueued": 172800,
    "delivered": 170000,
    "failed": 2800,
    "unique_endpoints": 45000
  }
}
```

## Recovery

### Redis Dies

1. Redis comes back (empty or stale)
2. Run reconciler: `howk-reconciler --from-beginning`
3. Reconciler replays `howk.pending` and `howk.results` topics
4. Rebuilds: circuit breaker states, retry queue, status hashes, stats
5. Normal operation resumes

**During rebuild:** Workers keep delivering (Kafka is the queue). Status queries return "rebuilding".

### Kafka Broker Dies

Kafka handles this internally via replication. If you lose all replicas... you have bigger problems.

## License

MIT
