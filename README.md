[![codecov](https://codecov.io/gh/renatocron/howk/graph/badge.svg?token=UXH5PUHYH0)](https://codecov.io/gh/renatocron/howk)

# HOWK - High Opinionated Webhook Kit

A high-throughput, fault-tolerant webhook delivery system built on Kafka + Redis.

## Philosophy

- **Kafka is the source of truth** — every webhook and delivery result is a Kafka record
- **Redis is rebuildable hot state** — if Redis dies, replay from Kafka
- **Circuit breakers protect endpoints** — failing endpoints don't burn your retry budget
- **At-least-once delivery** — we never lose a webhook, duplicates are the receiver's problem

## Architecture

### High-Level Overview

```
                    ┌─────────────────────────────────────┐
                    │           API Gateway               │
                    │  POST /webhooks/:config/enqueue     │
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
  │    ZADD retries <next_at_unix> <webhook_id:attempt>              │
  │    SET retry_data:{id} <compressed_webhook>                      │
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

### System Data Flow

```mermaid
flowchart TB
    subgraph Kafka["Kafka Topics"]
        PENDING[("howk.pending")]
        RESULTS[("howk.results")]
        DLQ[("howk.deadletter")]
    end

    subgraph Redis["Redis Keys"]
        DATA[("retry_data:{webhook_id}<br/>Compressed JSON<br/>TTL: 7 days")]
        ZSET[("retries ZSET<br/>score: unix_timestamp<br/>member: webhook_id:attempt")]
        META[("retry_meta:{id}:{attempt}<br/>reason, scheduled_at")]
    end

    subgraph Worker["Worker Process"]
        W_CONSUME["1. Consume from<br/>howk.pending"]
        W_DELIVER["2. Deliver HTTP"]
        W_SUCCESS{"Success?"}
        W_RETRY{"Should<br/>Retry?"}
        W_STORE["StoreRetryData()<br/>SET retry_data:{id}"]
        W_SCHEDULE["ScheduleRetry()<br/>ZADD + SET meta"]
        W_CLEANUP["DeleteRetryData()<br/>DEL retry_data:{id}"]
        W_PUBLISH_OK["PublishResult()"]
        W_PUBLISH_DLQ["PublishDeadLetter()"]
    end

    subgraph Scheduler["Scheduler Process"]
        S_POLL["1. Poll every 1s"]
        S_POP["2. PopAndLockRetries()<br/>Lua: ZRANGEBYSCORE + ZADD"]
        S_PARSE["3. Parse reference"]
        S_FETCH["4. GetRetryData()"]
        S_PUBLISH["5. Publish to Kafka"]
        S_ACK["6. AckRetry()<br/>ZREM + DEL meta"]
    end

    PENDING --> W_CONSUME
    W_CONSUME --> W_DELIVER
    W_DELIVER --> W_SUCCESS
    
    W_SUCCESS -->|Yes| W_CLEANUP
    W_CLEANUP --> W_PUBLISH_OK
    W_PUBLISH_OK --> RESULTS
    
    W_SUCCESS -->|No| W_RETRY
    W_RETRY -->|Yes| W_STORE
    W_STORE --> DATA
    W_STORE --> W_SCHEDULE
    W_SCHEDULE --> ZSET
    W_SCHEDULE --> META
    
    W_RETRY -->|No/Exhausted| W_CLEANUP
    W_CLEANUP --> W_PUBLISH_DLQ
    W_PUBLISH_DLQ --> DLQ

    S_POLL --> S_POP
    S_POP --> ZSET
    ZSET --> S_PARSE
    S_PARSE --> S_FETCH
    S_FETCH --> DATA
    S_FETCH --> S_PUBLISH
    S_PUBLISH --> PENDING
    S_PUBLISH --> S_ACK
    S_ACK --> ZSET
    S_ACK --> META

    classDef kafka fill:#ff9800,stroke:#e65100,color:#000
    classDef redis fill:#dc382d,stroke:#a41e11,color:#fff
    classDef worker fill:#2196f3,stroke:#1565c0,color:#fff
    classDef scheduler fill:#4caf50,stroke:#2e7d32,color:#fff
    classDef decision fill:#ffeb3b,stroke:#f9a825,color:#000

    class PENDING,RESULTS,DLQ kafka
    class DATA,ZSET,META redis
    class W_CONSUME,W_DELIVER,W_STORE,W_SCHEDULE,W_CLEANUP,W_PUBLISH_OK,W_PUBLISH_DLQ worker
    class S_POLL,S_POP,S_PARSE,S_FETCH,S_PUBLISH,S_ACK scheduler
    class W_SUCCESS,W_RETRY decision
```

### Retry Lifecycle Sequence

```mermaid
sequenceDiagram
    autonumber
    participant K as Kafka<br/>howk.pending
    participant W as Worker
    participant R as Redis
    participant E as Endpoint
    participant S as Scheduler

    Note over K,S: INITIAL DELIVERY (Attempt 0)
    
    K->>W: Consume webhook (attempt=0)
    W->>E: HTTP POST
    E-->>W: 503 Service Unavailable
    
    Note over W,R: Store data & schedule retry
    
    W->>R: SET retry_data:wh_123 [compressed]
    W->>R: ZADD retries score "wh_123:1"
    W->>R: SET retry_meta:wh_123:1

    Note over K,S: SCHEDULER PICKS UP
    
    rect rgb(220, 240, 220)
        Note over S,R: Visibility Timeout (Pop & Lock)
        S->>R: Lua: ZRANGEBYSCORE + ZADD future
        R-->>S: ["wh_123:1"]
    end
    
    S->>R: GET retry_data:wh_123
    S->>K: Publish (attempt=1)
    S->>R: ZREM + DEL meta (NOT data)

    Note over K,S: SECOND DELIVERY (Attempt 1)
    
    K->>W: Consume (attempt=1)
    W->>E: HTTP POST
    E-->>W: 503 Again
    
    W->>R: SET retry_data:wh_123 (overwrite)
    W->>R: ZADD retries "wh_123:2"

    Note over K,S: ... Scheduler cycle ...

    Note over K,S: FINAL DELIVERY - SUCCESS
    
    K->>W: Consume (attempt=2)
    W->>E: HTTP POST
    E-->>W: 200 OK
    
    rect rgb(200, 255, 200)
        Note over W,R: Terminal State - Cleanup
        W->>R: DEL retry_data:wh_123
    end
```

### Redis Key Structure

```mermaid
flowchart LR
    subgraph DataKeys["Data Keys (One Per Webhook)"]
        D1["<b>retry_data:wh_123</b><br/>━━━━━━━━━━━━━━━<br/>Value: gzip(webhook JSON)<br/>TTL: 7 days<br/>Overwritten each retry"]
        D2["<b>retry_data:wh_456</b><br/>━━━━━━━━━━━━━━━<br/>Different webhook"]
    end
    
    subgraph ZSet["Sorted Set (References)"]
        Z["<b>retries</b><br/>━━━━━━━━━━━━━━━<br/>1706745600 → wh_123:1<br/>1706752800 → wh_123:2<br/>1706760000 → wh_456:1"]
    end
    
    subgraph MetaKeys["Metadata (Per Attempt)"]
        M1["<b>retry_meta:wh_123:1</b><br/>━━━━━━━━━━━━━━━<br/>{reason, scheduled_at}"]
        M2["<b>retry_meta:wh_123:2</b>"]
        M3["<b>retry_meta:wh_456:1</b>"]
    end

    Z -->|"parse id"| D1
    Z -->|"parse id"| D2
    Z -.->|"same ref"| M1
    Z -.->|"same ref"| M2
    Z -.->|"same ref"| M3

    classDef data fill:#e3f2fd,stroke:#1565c0,color:#000
    classDef zset fill:#fff3e0,stroke:#e65100,color:#000
    classDef meta fill:#f3e5f5,stroke:#7b1fa2,color:#000

    class D1,D2 data
    class Z zset
    class M1,M2,M3 meta
```

### Webhook State Machine

```mermaid
stateDiagram-v2
    [*] --> Pending: API Enqueue
    
    Pending --> Delivering: Worker Consume
    
    Delivering --> Delivered: HTTP 2xx
    Delivering --> Failed: HTTP 5xx/408/429
    Delivering --> Exhausted: HTTP 4xx
    
    Failed --> Pending: Scheduler Re-enqueue
    Failed --> Exhausted: Max Attempts
    
    Delivered --> [*]: ✓ Success
    Exhausted --> [*]: ✗ DLQ

    note right of Failed
        Redis State:
        • retry_data:{id} = compressed
        • retries ZSET = scheduled
        • retry_meta:{ref} = metadata
    end note
    
    note right of Delivered
        Cleanup:
        DEL retry_data:{id}
    end note
    
    note right of Exhausted
        Cleanup:
        DEL retry_data:{id}
        Publish to DLQ
    end note
```

### Operations Summary

| Operation | Component | Redis Commands | When Called |
|-----------|-----------|----------------|-------------|
| `StoreRetryData()` | Worker | `SET retry_data:{id} compressed EX 604800` | Before scheduling retry |
| `ScheduleRetry()` | Worker | `ZADD retries score member`<br>`SET retry_meta:{ref}` | After storing data |
| `PopAndLockRetries()` | Scheduler | `Lua: ZRANGEBYSCORE + ZADD future` | Poll loop (every 1s) |
| `GetRetryData()` | Scheduler | `GET retry_data:{id}` | After parsing reference |
| `AckRetry()` | Scheduler | `ZREM retries member`<br>`DEL retry_meta:{ref}` | After Kafka publish |
| `DeleteRetryData()` | Worker | `DEL retry_data:{id}` | Terminal state (success/DLQ) |

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

HOWK supports flexible configuration through:

1. **Environment Variables** (highest priority) - `HOWK_*` prefixed
2. **Config File** (YAML format) - specified via `--config` flag or auto-discovered
3. **Defaults** (lowest priority) - sensible built-in defaults

### Environment Variables

Override any configuration setting using environment variables with the `HOWK_` prefix:

```bash
export HOWK_API_PORT=9090
export HOWK_KAFKA_BROKERS=kafka1:9092,kafka2:9092
export HOWK_REDIS_ADDR=redis.example.com:6379
export HOWK_REDIS_PASSWORD=secret
export HOWK_TTL_STATUS_TTL=72h
bin/howk-api
```

See `.env.example` for a complete list of environment variables.

### Config File

Use a YAML config file for complex configurations:

```bash
bin/howk-api --config=/etc/howk/config.yaml
```

Example `config.yaml`:

```yaml
api:
  port: 8080
  read_timeout: 10s
  write_timeout: 10s

kafka:
  brokers:
    - localhost:19092
  topics:
    pending: howk.pending
    results: howk.results
    deadletter: howk.deadletter
  consumer_group: howk-workers
  retention: 168h

redis:
  addr: "localhost:6379"
  password: ""
  pool_size: 100

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
  failure_threshold: 5
  failure_window: 60s
  recovery_timeout: 5m
  probe_interval: 60s
  success_threshold: 2

scheduler:
  poll_interval: 1s
  batch_size: 500

ttl:
  circuit_state_ttl: 24h
  status_ttl: 168h
  stats_ttl: 48h
  idempotency_ttl: 24h
```

### Configuration Precedence

Environment variables override config file settings, which override defaults:

```bash
# config.yaml has: api.port: 7070
# Environment variable overrides it:
export HOWK_API_PORT=9090
bin/howk-api --config=config.yaml
# Result: API listens on port 9090
```

## API

### Enqueue Webhook

```
POST /webhooks/:config/enqueue
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
