# HOWK Failure Modes & Recovery

## Infrastructure Failures

### 1. Redis Unavailable

**Symptoms:**
- Workers continue processing but lose idempotency protection
- Duplicate deliveries possible
- No circuit breaker protection (endpoints not protected from overload)
- Status queries fail

**Behavior:**
- Workers **fail open**: Continue delivering webhooks
- Circuit breaker checks fail → allow all requests
- Idempotency checks fail → log warning and proceed
- Stats not recorded

**Recovery:**
1. Bring Redis back online (empty or from backup)
2. Run reconciler to rebuild state:
```bash
   ./bin/howk-reconciler --from-beginning
```
3. Reconciler replays `howk.results` topic to rebuild:
   - Circuit breaker states
   - Retry queue
   - Status hashes
   - Statistics

**Prevention:**
- Redis Sentinel for HA
- Redis Cluster for horizontal scaling
- Regular backups (though state is rebuildable from Kafka)

---

### 2. Kafka Broker Unavailable

**Symptoms:**
- API returns 500 on webhook enqueue
- Workers can't consume new webhooks
- Results can't be published

**Behavior:**
- API: Kafka publish fails → returns 500 to caller
- Workers: Consumer blocks waiting for Kafka
- Scheduler: Can't re-enqueue retries

**Recovery:**
- Kafka handles internally via replication
- If leader fails → new leader elected
- Consumers rebalance automatically
- **No manual intervention needed** (unless all replicas lost)

**Prevention:**
- Kafka replication factor ≥ 3
- Min in-sync replicas ≥ 2
- Monitor Kafka cluster health

---

### 3. Both Redis AND Kafka Unavailable

**Symptoms:**
- Complete system outage

**Behavior:**
- API: Can't enqueue webhooks
- Workers: Can't consume or deliver
- Scheduler: Can't run

**Recovery:**
1. Bring Kafka back online first (most critical)
2. Bring Redis back
3. Run reconciler
4. Normal operation resumes

**Data Loss:**
- **Kafka**: If all replicas lost → PERMANENT DATA LOSS (webhooks lost)
- **Redis**: No data loss (rebuildable from Kafka)

---

## Application Failures

### 4. Worker Crashes Mid-Processing

**Scenario:** Worker crashes after delivering webhook but before committing Kafka offset

**Behavior:**
- Kafka message not marked as consumed
- After consumer group rebalance, message redelivered to another worker
- **Idempotency check prevents duplicate delivery**
- Status may show "delivering" until next attempt

**Recovery:**
- Automatic: Kubernetes restarts pod
- Another worker picks up message
- Idempotency key prevents double-send

---

### 5. Scheduler Crashes

**Scenario:** Scheduler crashes while processing retry batch

**Behavior:**
- Retries remain in Redis sorted set
- Next scheduler poll picks them up
- May cause delay in retry processing (max = poll_interval)

**Recovery:**
- Automatic restart
- No data loss (retries persist in Redis)
- Retries delayed by at most `poll_interval` (default 1s)

---

### 6. API Crashes During Enqueue

**Scenario:** API crashes after publishing to Kafka but before returning 202

**Behavior:**
- Webhook successfully enqueued in Kafka
- Client receives 500/connection reset
- Client may retry → creates duplicate with different webhook_id
- **No idempotency** at enqueue level

**Prevention:**
- Client should use `idempotency_key` field
- Future: Add idempotency check in API before publishing

---

## Network Failures

### 7. Webhook Endpoint Unreachable

**Symptoms:**
- Network timeout errors
- Connection refused

**Behavior:**
- Marked as retryable failure
- Circuit breaker records failure
- Exponential backoff retry
- After threshold failures → circuit opens

**Recovery:**
- Automatic retries (up to `max_attempts`)
- Circuit breaker protects endpoint from overload
- After `recovery_timeout` → probe request sent
- If endpoint recovers → circuit closes

---

### 8. Webhook Endpoint Slow (>30s)

**Symptoms:**
- Context deadline exceeded errors

**Behavior:**
- Delivery times out after 30s (configurable)
- Treated as retryable failure
- Retry scheduled with exponential backoff

**Tuning:**
```bash
HOWK_DELIVERY_TIMEOUT=60s  # Increase if endpoints legitimately slow
```

---

### 9. Webhook Endpoint Returns 429 (Rate Limited)

**Behavior:**
- Marked as retryable (honors recipient's rate limit)
- Exponential backoff provides breathing room
- Circuit breaker may open if sustained

**Best Practice:**
- Endpoints should return `Retry-After` header
- Future: Honor `Retry-After` in retry delay calculation

---

### 10. Webhook Endpoint Returns 4xx (Client Error)

**Symptoms:**
- 400 Bad Request, 401 Unauthorized, 403 Forbidden, 404 Not Found, etc.

**Behavior:**
- Marked as **non-retryable** (except 408 Timeout, 429 Rate Limited)
- Sent directly to DLQ with `reason_type: unrecoverable`
- Does NOT consume retry attempts
- Circuit breaker NOT affected (not endpoint's fault)

**Example Scenario:**
```
Attempt 1: 404 Not Found
→ Immediate DLQ (unrecoverable)
→ webhook.Attempt = 0 (not incremented)
→ reason: "unrecoverable error: status=404"
```

**Recovery:**
- Requires manual intervention to fix configuration
- Cannot be auto-recovered by retries
- Review webhook endpoint URL or credentials
- May need to update tenant configuration

---

## Data Corruption

### 11. Malformed Messages in Kafka

**Symptoms:**
- JSON unmarshal errors in worker logs
- Scheduler finds nil webhooks in retry queue

**Behavior:**
- Worker: Log error, skip message (don't retry)
- Scheduler: Log CRITICAL error, skip (message lost)

**Detection:**
```bash
# Monitor for these log patterns
grep "Failed to unmarshal webhook" /var/log/howk/worker.log
grep "CRITICAL: Retry message has nil webhook" /var/log/howk/scheduler.log
```

**Root Causes:**
- Schema changes without migration
- Bugs in serialization code
- Kafka bit rot (extremely rare)

**Prevention:**
- Schema validation before publishing
- Comprehensive unit tests
- Kafka topic checksums enabled

---

## Circuit Breaker Edge Cases

### 12. Circuit Opens During High-Priority Delivery

**Scenario:** Circuit opens right before critical webhook delivery

**Behavior:**
- Webhook scheduled for retry in 5 minutes (recovery timeout)
- Not delivered immediately even if endpoint recovers

**Manual Override:**
```bash
# Reset circuit breaker via Redis
redis-cli DEL circuit:<endpoint_hash>
```

**Future Feature:** Admin API to manually close circuits

---

### 13. Flapping Circuit (Open/Close Oscillation)

**Symptoms:**
- Circuit repeatedly opens and closes
- Endpoint marginally healthy

**Tuning:**
```bash
# Increase failure threshold
HOWK_CIRCUIT_FAILURE_THRESHOLD=10

# Increase probe interval to reduce oscillation
HOWK_CIRCUIT_PROBE_INTERVAL=2m
```

---

## Reconciler Scenarios

### 14. Reconciler Started While Workers Running

**Behavior:**
- Safe: Workers and reconciler both write to Redis
- Potential race conditions on status updates
- Last write wins

**Best Practice:**
- Stop workers before running reconciler
- Or accept minor inconsistencies (rebuilding anyway)

---

### 15. Reconciler Fails Mid-Replay

**Behavior:**
- Partial state rebuilt
- Safe to re-run with `--from-beginning`
- Idempotent: Rebuilding same state multiple times is safe

**Recovery:**
```bash
# Just re-run
./bin/howk-reconciler --from-beginning
```

---

## Dead Letter Queue (DLQ) Classification

### 16. Understanding DLQ Reason Types

HOWK classifies webhooks sent to the dead letter queue (`howk.deadletter` topic) into two categories:

**1. Exhausted (`DLQReasonExhausted`)**

Webhooks that ran out of retry attempts after persistent failures.

**Characteristics:**
- `webhook.Attempt >= webhook.MaxAttempts` (default: 20 attempts)
- Endpoint may be down, slow, or returning retryable errors (5xx, 408, 429)
- Represents legitimate delivery attempts over time
- Example reason: `"exhausted after 20 attempts, last_status=503"`

**Common Causes:**
- Endpoint persistently down or unreachable
- Server continuously returning 500/502/503 errors
- Network issues preventing delivery
- Circuit breaker repeatedly opening

**Recovery Actions:**
- Investigate endpoint health
- Check if endpoint URL is correct
- Review circuit breaker state for endpoint
- Consider manual replay after endpoint recovers

**2. Unrecoverable (`DLQReasonUnrecoverable`)**

Webhooks that hit non-retryable errors, typically on first or early attempts.

**Characteristics:**
- `webhook.Attempt < webhook.MaxAttempts`
- HTTP 4xx errors (except 408 Request Timeout, 429 Too Many Requests)
- Non-retryable failures that won't improve with time
- Example reason: `"unrecoverable error: status=404"`

**Common Causes:**
- 400 Bad Request: Malformed payload or headers
- 401 Unauthorized: Invalid credentials or signature
- 403 Forbidden: Insufficient permissions
- 404 Not Found: Endpoint URL incorrect or removed
- 410 Gone: Endpoint permanently deleted

**Recovery Actions:**
- 400: Review webhook payload format
- 401/403: Check signing secret configuration
- 404/410: Verify endpoint URL, may need reconfiguration
- Generally requires fixing webhook configuration or payload

### Message Structure in DLQ

Dead letter messages include structured metadata:

```json
{
  "webhook": { ... },
  "reason": "exhausted after 20 attempts, last_status=503",
  "reason_type": "exhausted",
  "last_error": "context deadline exceeded",
  "status_code": 503,
  "time": "2026-01-31T10:30:00Z"
}
```

**Kafka Headers:**
- `config_id`: Configuration/tenant identifier
- `reason`: Human-readable reason string
- `reason_type`: Machine-readable classification (`exhausted` or `unrecoverable`)

### Filtering DLQ by Reason Type

**Consume only exhausted webhooks:**
```bash
# These may be retryable after endpoint recovers
kafka-console-consumer --topic howk.deadletter \
  --property print.headers=true | grep "reason_type:exhausted"
```

**Consume only unrecoverable errors:**
```bash
# These require configuration fixes
kafka-console-consumer --topic howk.deadletter \
  --property print.headers=true | grep "reason_type:unrecoverable"
```

### DLQ Replay Strategy

**Exhausted webhooks:**
- Wait for endpoint recovery
- Verify circuit breaker state is CLOSED
- Replay to `howk.pending` topic
- Monitor for successful delivery

**Unrecoverable webhooks:**
- Fix root cause first (URL, credentials, payload format)
- Optionally transform payload if schema changed
- Test with single webhook before bulk replay
- May need to discard if issue can't be fixed (e.g., 410 Gone)

---

## Monitoring Checklist

**Critical Alerts:**
- Kafka consumer lag > 1000 messages
- Redis down
- Worker error rate > 5%
- DLQ message count increasing rapidly
- DLQ unrecoverable errors spiking (indicates configuration issues)
- Circuit breaker stuck OPEN > 1 hour
- "CRITICAL: Retry message has nil webhook" logged

**Metrics to Track:**
- Webhooks enqueued/sec
- Webhooks delivered/sec
- Delivery latency p50/p95/p99
- Retry queue depth
- Circuit breaker state distribution
- DLQ total message count
- DLQ exhausted vs unrecoverable ratio
- DLQ messages by config_id (identify problematic tenants)

**Logs to Index:**
- All ERROR level logs
- "Already processed" (detect duplicate sends)
- "Circuit opened" (endpoint issues)
- "Retries exhausted" (persistent failures → DLQ exhausted)
- "Unrecoverable error" (config issues → DLQ unrecoverable)

**DLQ Monitoring Best Practices:**
- **Exhausted webhooks**: Spike indicates endpoint outages, review circuit breaker states
- **Unrecoverable webhooks**: Spike indicates bad configuration, review recent config changes
- Set up separate alerts for each DLQ type with different severity levels
- Create dashboards showing DLQ breakdown by reason_type and config_id