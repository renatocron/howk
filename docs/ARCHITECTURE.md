# HOWK — Architecture Reference

Detailed flow diagrams that don't fit comfortably in the README. For a high-level
component overview, see the README. For failure scenarios, see
[FAILURE_MODES.md](./FAILURE_MODES.md).

## System Data Flow

End-to-end view of how a webhook moves through the system, including the fast
lane, slow lane, retry path, and the Redis keys touched at each step.

```mermaid
flowchart TB
    subgraph Kafka["Kafka Topics"]
        PENDING[("howk.pending")]
        SLOW[("howk.slow<br/>Rate-limited lane")]
        RESULTS[("howk.results")]
        DLQ[("howk.deadletter")]
        STATE[("howk.state<br/>Compacted topic<br/>Active webhook state")]
    end

    subgraph Redis["Redis Keys"]
        DATA[("retry_data:{webhook_id}<br/>Compressed JSON<br/>TTL: 7 days")]
        ZSET[("retries ZSET<br/>score: unix_timestamp<br/>member: webhook_id:attempt")]
        META[("retry_meta:{id}:{attempt}<br/>reason, scheduled_at")]
        CONC[("concurrency:{endpoint_hash}<br/>In-flight counter<br/>TTL: 2min")]
    end

    subgraph Worker["Worker Process (Fast Lane)"]
        W_CONSUME["1. Consume from<br/>howk.pending"]
        W_CHECK_CB{"Circuit<br/>Allows?"}
        W_CHECK_CONC{"Inflight &lt;<br/>threshold?"}
        W_INCR["IncrInflight()<br/>INCR + EXPIRE"]
        W_DELIVER["2. Deliver HTTP"]
        W_DECR["DecrInflight()<br/>Lua: DECR ≥0"]
        W_SUCCESS{"Success?"}
        W_RETRY{"Should<br/>Retry?"}
        W_STORE["StoreRetryData()<br/>SET retry_data:{id}"]
        W_SCHEDULE["ScheduleRetry()<br/>ZADD + SET meta"]
        W_CLEANUP["DeleteRetryData()<br/>DEL retry_data:{id}"]
        W_PUBLISH_OK["PublishResult()"]
        W_PUBLISH_DLQ["PublishDeadLetter()"]
        W_PUBLISH_SLOW["PublishToSlow()<br/>Divert to slow lane"]
    end

    subgraph SlowWorker["Slow Worker Process"]
        SW_CONSUME["1. Consume from<br/>howk.slow"]
        SW_RATE["2. Rate limit<br/>(5/sec default)"]
        SW_REUSE["3. Same logic as<br/>fast lane"]
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
    W_CONSUME --> W_CHECK_CB
    W_CHECK_CB -->|No| W_SCHEDULE
    W_CHECK_CB -->|Yes| W_INCR
    W_INCR --> CONC
    W_INCR --> W_CHECK_CONC

    W_CHECK_CONC -->|Yes| W_DELIVER
    W_CHECK_CONC -->|No| W_PUBLISH_SLOW
    W_PUBLISH_SLOW --> SLOW
    W_PUBLISH_SLOW -.->|decr| CONC

    W_DELIVER --> W_SUCCESS
    W_DELIVER -.->|defer| W_DECR
    W_DECR --> CONC

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

    SLOW --> SW_CONSUME
    SW_CONSUME --> SW_RATE
    SW_RATE --> SW_REUSE
    SW_REUSE --> W_CHECK_CB

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
    classDef slowworker fill:#ffeb3b,stroke:#f9a825,color:#000
    classDef scheduler fill:#4caf50,stroke:#2e7d32,color:#fff
    classDef decision fill:#ff9800,stroke:#e65100,color:#000

    class PENDING,SLOW,RESULTS,DLQ kafka
    class DATA,ZSET,META,CONC redis
    class W_CONSUME,W_DELIVER,W_STORE,W_SCHEDULE,W_CLEANUP,W_PUBLISH_OK,W_PUBLISH_DLQ,W_INCR,W_DECR,W_PUBLISH_SLOW worker
    class SW_CONSUME,SW_RATE,SW_REUSE slowworker
    class S_POLL,S_POP,S_PARSE,S_FETCH,S_PUBLISH,S_ACK scheduler
    class W_SUCCESS,W_RETRY,W_CHECK_CB,W_CHECK_CONC decision
```

## Retry Lifecycle Sequence

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

## Redis Key Structure

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

## Webhook State Machine

```mermaid
stateDiagram-v2
    [*] --> Pending: API Enqueue

    Pending --> Delivering: Worker Consume

    state Delivering {
        [*] --> CheckCircuit: Receive message
        CheckCircuit --> CircuitOpen: circuit open
        CheckCircuit --> CheckConcurrency: circuit allows

        CheckConcurrency --> FastLane: inflight < threshold
        CheckConcurrency --> DivertToSlow: inflight ≥ threshold

        FastLane --> HTTPDeliver: INCR concurrency
        DivertToSlow --> [*]: Publish to howk.slow

        HTTPDeliver --> Success: 2xx response
        HTTPDeliver --> Failure: error/timeout

        Success --> [*]: DECR + publish result
        Failure --> ScheduleRetry: retryable
        Failure --> DLQ: non-retryable
        ScheduleRetry --> [*]: DECR + schedule
        DLQ --> [*]: DECR + dead letter

        CircuitOpen --> ScheduleRetryCircuit: schedule far future
        ScheduleRetryCircuit --> [*]
    }

    state SlowLane {
        [*] --> RateLimitedConsume: Consume from howk.slow
        RateLimitedConsume --> ReCheck: Rate limited
        ReCheck --> RetryDeliver: Re-check concurrency
        RetryDeliver --> Delivering: Process normally
    }

    Delivering --> Delivered: HTTP 2xx
    Delivering --> Failed: HTTP 5xx/408/429
    Delivering --> Exhausted: HTTP 4xx

    Failed --> Pending: Scheduler Re-enqueue
    Failed --> Exhausted: Max Attempts

    Delivered --> [*]: ✓ Success
    Exhausted --> [*]: ✗ DLQ

    note right of Delivering
        Concurrency Control:
        • INCR concurrency:{hash}
        • Check against threshold
        • DECR on all exits
        • Floor at 0 (Lua script)
    end note

    note right of SlowLane
        Self-healing:
        • Rate limited (5/sec)
        • Re-checks concurrency
        • Returns to fast lane
          when endpoint recovers
    end note

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

## Operations Summary

| Operation | Component | Redis Commands | When Called |
|-----------|-----------|----------------|-------------|
| `IncrInflight()` | Worker | `INCR concurrency:{hash}`<br>`EXPIRE concurrency:{hash} {ttl}` | Before delivery attempt |
| `DecrInflight()` | Worker | `Lua: DECR if > 0` | After delivery (success/fail/DLQ) |
| `PublishToSlow()` | Worker | Kafka Produce to `howk.slow` | When inflight ≥ threshold |
| `StoreRetryData()` | Worker | `SET retry_data:{id} compressed EX 604800` | Before scheduling retry |
| `ScheduleRetry()` | Worker | `ZADD retries score member`<br>`SET retry_meta:{ref}` | After storing data |
| `PopAndLockRetries()` | Scheduler | `Lua: ZRANGEBYSCORE + ZADD future` | Poll loop (every 1s) |
| `GetRetryData()` | Scheduler | `GET retry_data:{id}` | After parsing reference |
| `AckRetry()` | Scheduler | `ZREM retries member`<br>`DEL retry_meta:{ref}` | After Kafka publish |
| `DeleteRetryData()` | Worker | `DEL retry_data:{id}` | Terminal state (success/DLQ) |
