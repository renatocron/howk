# Changelog

All notable changes to HOWK are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.4.9] - 2026-06-10

### Added
- **`require_script` enqueue flag (fail-fast on missing transformation)**: per-enqueue boolean (`require_script: true` in the webhook body). When set and no Lua script resolves for the `config_id` (memory-first loader, then Redis fallback), the API rejects the enqueue with **422** and publishes nothing — for batches it's all-or-nothing. Lets producers whose payload is meaningless without transformation (e.g. credential-injecting scripts) surface a missing/expired script *immediately at the source* instead of silently delivering a raw payload. HOWK enforces only script *presence*; it has no knowledge of what the script does (the credential-marker header is customer Lua convention, not a HOWK feature).

### Fixed
- **Script configs no longer silently disappear (Redis TTL + OffsetNewest); enqueue is now memory-first**: two compounding bugs could silently disable all Lua transformation, and the fix was a near-miss in its first form. (1) The Redis `script:*` key carried a TTL that nothing refreshed, and the API's enqueue-time `ScriptHash` tagging consulted **only** Redis — after expiry, webhooks shipped with an empty `ScriptHash` and the worker skipped transformation entirely, delivering raw payloads (with unexchanged credential markers) to endpoints. (2) The script consumer used a consumer **group** with `Offsets.Initial=OffsetNewest`, so a fresh group never replayed the compacted scripts topic, and multiple replicas split partitions — the durable Kafka copy was unreachable exactly when Redis was empty.
  - New `Broker.Replay(ctx, topic, handler)`: consumes **all** partitions from the earliest offset with no consumer group and keeps tailing (KTable pattern). Implemented for Kafka, dev-mode `MemBroker` (with per-topic history retention), and test mocks.
  - `script.Consumer` now uses `Replay` — every worker **and** API instance materializes the full script state in memory on startup, and the write-through self-heals Redis on every boot.
  - The API now runs its own script consumer + in-memory loader, and `buildWebhook` is **memory-first**: it resolves `ScriptHash` from the in-process loader (O(1), namespace fallback included) with **no Redis round-trip on the enqueue hot path**. Redis is consulted only as a cold-start fallback (the brief window before the first replay, or deployments wiring no loader); a fallback hit populates the loader so subsequent enqueues stay in memory. `handleUploadScript` now also write-throughs to the local loader for immediate read-after-write. This removes Redis as both a correctness dependency *and* a per-enqueue latency cost.
  - The Redis script mirror is now written with **no expiry** (`scriptCacheTTL = 0`, and `script.Consumer` TTL `0`). A TTL on a mirror whose source of truth is a compacted topic only re-arms the original failure on a delay; deletes are already propagated by tombstones, so the mirror never accumulates orphans and has nothing to expire. The consumer's write-through still rebuilds the mirror from Kafka on boot, so a Redis flush self-heals.

## [0.4.8] - 2026-06-10

### Added
- **Outgoing request debug dump (`delivery.dump_requests`)**: New opt-in flag that logs the exact outgoing HTTP request — request line, all headers, and body — immediately before it hits the wire, i.e. **after** Lua script transformation, delivery-time overrides, and HMAC signing have been applied. Closes the visibility gap where the final request could not be observed once a script rewrote the payload/headers.
  - Implemented in `delivery.Client` via `httputil.DumpRequestOut`, which serializes the real transport bytes and transparently restores `req.Body` so the actual send is unaffected.
  - Emitted through the structured logger (stdout / configured sink) at info level, tagged with `webhook_id`, `endpoint`, and `attempt` for easy grepping — no Kafka topic involved.
  - Default `false`. The dump is intentionally **unredacted** and may expose payloads and secret-bearing headers, so keep it disabled in production. (DLQ-bound records remain governed by the separate `dlq` redaction policy.)
  - Env var: `HOWK_DELIVERY_DUMP_REQUESTS`.

## [0.4.7] - 2026-05-11

### Added
- **Configurable DLQ payload**: New `dlq` config section controls what is persisted to `howk.deadletter`, so 4xx failures can be diagnosed without re-running the request.
  - `dlq.include_response_body` (default `true`): attaches the first 1KB of the endpoint response body (already captured by `delivery.Client`) to DLQ records via the new `DeadLetter.ResponseBody` field. Set to `false` if receiver responses can carry PII in your deployment.
  - `dlq.redact_headers` (default unset → built-in safe list): full-override of the redacted-header list. Built-in defaults — `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-API-Key`, `X-Auth-Token` — are exposed as `domain.DefaultSensitiveHeaders` and applied case-insensitively. Setting `redact_headers` *replaces* the defaults (include `Authorization` yourself).
  - `dlq.disable_redaction` (default `false`): escape hatch for trusted-internal-only deployments wanting full request fidelity.
  - New `domain.RedactHeaders(headers, names)` helper and `(*Webhook).CloneForDLQ(names)` keep the original webhook in memory untouched — only the DLQ-bound copy is redacted.
  - Env vars: `HOWK_DLQ_REDACT_HEADERS`, `HOWK_DLQ_DISABLE_REDACTION`, `HOWK_DLQ_INCLUDE_RESPONSE_BODY`.

### Security
- Secret-bearing request headers (`Authorization`, etc.) are now redacted by default before webhooks are published to `howk.deadletter`. Existing deployments that relied on the previous behavior — full headers on the DLQ topic — must opt in via `dlq.disable_redaction: true` or a custom `dlq.redact_headers` list.

## [0.4.6] - 2026-05-11

### Added
- **Script-declared retryable statuses (`request.retry_on_status`)**: Worker-side Lua scripts may now set `request.retry_on_status = {401, 403, ...}` to extend the default retry classifier on a per-webhook basis. Designed for scripts that resolve dynamic credentials (OAuth tokens, signed URLs cached via `kv`) where a 401/403 should invalidate the cache and refetch on the next attempt instead of going straight to DLQ.
  - New `request.retry_on_status` Lua output, extracted in `internal/script/engine.go` and propagated through `internal/script/domain.go` `TransformResult.RetryOnStatus` onto the webhook.
  - New `Webhook.RetryOnStatus` and `WebhookStateSnapshot.RetryOnStatus` fields, so the override survives the retry round-trip (Kafka, Redis retry data, and Redis-loss reconciliation).
  - `internal/retry/strategy.go` `ShouldRetry` consults the override before falling back to `domain.IsRetryable`. The `MaxAttempts` gate still applies — the override widens the retry classifier, it does not bypass exhaustion.
  - See [docs/LUA_ENGINE.md](docs/LUA_ENGINE.md#requestretry_on_status--script-declared-retryable-statuses) for usage and tradeoffs (a permanently-broken credential now consumes `MaxAttempts × backoff` before DLQ).

## [0.4.5] - 2026-05-08

### Added
- **Delivery-time overrides for query params and headers**: Transformer `.json` files (and worker-side `script_config`) may now declare reserved `_delivery_query_params` and `_delivery_headers` maps holding **unresolved** `${VAR_NAME}` templates. The worker resolves them against its own process environment at HTTP-send time and merges them into the outbound URL / headers. The Webhook record on Kafka, retry data, and `DeliveryResult` continue to carry the bare endpoint — secrets never leave the worker.
  - New package `internal/delivery/overrides.go` with `ExtractTemplates`, `ResolveTemplates`, `ApplyOverrides`.
  - New `Webhook.DeliveryQueryParams` / `Webhook.DeliveryHeaders` fields and matching `WebhookStateSnapshot` fields, so retries and reconciliation re-apply overrides.
  - Two activation paths: templates attached to the Webhook by the API-side transformer (typical), or `script_config` published via `PUT /config/:config_id/script` (fallback).
  - Existing query params on the endpoint are preserved on merge; empty resolutions are dropped (no `?key=`); colliding header keys are overridden.
  - See [docs/transformers.md](docs/transformers.md#delivery-time-overrides-keep-secrets-out-of-kafka) and [docs/transformers-examples.md](docs/transformers-examples.md#delivery-time-secret-injection).

### Security
- Storage invariant: the persisted webhook records (Kafka topics `howk.pending`, `howk.results`, `howk.state`, Redis retry data) only see templates and the bare endpoint, never the resolved secret values. The same env-var trust boundary documented in 0.4.4 applies — anyone with write access to a transformer `.json` or `script_config` can reference any env var visible to the worker process.

## [0.4.4] - 2026-04-17

### Added
- **Env var substitution in transformer engine**: `${VAR_NAME}` syntax in `script_config` values is now resolved from the process environment by the transformer engine (`internal/transformer/engine.go`), matching the behavior of the Lua script engine (v0.4.3).

### Security
- Documented trust boundary around `script_config`: anyone with write access to a config can reference arbitrary process env vars via `${VAR_NAME}` and exfiltrate secrets. Until a per-namespace env var allowlist lands, restrict `PUT /config/:config_id/script` to trusted deployers (see TODOs in `internal/api/h_script.go` and `internal/script/engine.go`).

## [0.4.3] - 2026-04-09

### Added
- **Env var substitution in `script_config`**: String values using `${VAR_NAME}` syntax are resolved from environment variables at execution time. Secrets never reach Kafka or Redis — store them as env vars and reference them in config.

## [0.4.2] - 2026-04-09

### Added
- **`extraEnvFrom` Helm value**: All pod templates (api, worker, scheduler, reconciler) now support `extraEnvFrom` for mounting secrets/configmaps as environment variables.

## [0.4.1] - 2026-04-09

### Added
- **Header removal in Lua scripts**: Setting `headers["key"] = ""` now removes the header from the outgoing request instead of passing an empty value.

## [0.4.0] - 2026-04-09

### Fixed
- **Namespace fallback in Redis hotstate**: `GetScript("wh:1:1")` now falls back to `wh` in Redis (not just the in-memory Loader), fixing script execution for namespaced config IDs in production.
- **Dev mode companion JSON loading**: `.json` files without `lua_code` are now treated as companion config (accessible as `config.*` in Lua) rather than full script configs. Backward-compatible with existing full-config `.json` files.

### Added
- **ScriptConfig field**: `script.Config` now carries `script_config` (arbitrary key-value map) populated from companion JSON or API upload, accessible as `config.*` globals in Lua scripts.
- **Upload script_config via API**: `PUT /config/:config_id/script` accepts optional `script_config` field.

## [0.3.0] - 2026-04-08

### Added
- **Namespace fallback for Lua scripts**: `GetScript("wh:42:6")` now falls back to the `wh` namespace script when no exact match exists. Enables a single worker script to handle all config IDs sharing a namespace prefix.
- **Dev mode script loader**: Load Lua scripts and JSON configs from disk (`internal/devmode/scripts.go`) for local development without recompiling or publishing to Kafka.

### Changed
- Helm chart docs: added instructions for Redis Sentinel configuration, ArgoCD deployment, Kafka topic creation, and scalability details.

## [0.2.0] - 2026-04-01

### Added
- **Redis Sentinel support**: Configure failover via `sentinelAddrs` and `sentinelMasterName` for high-availability Redis.
- **Helm chart**: Full Kubernetes deployment with ArgoCD support, configurable replicas, resource limits, probes, and ConfigMap-based transformer scripts.
- Helm chart docs: Redis Sentinel setup, ArgoCD + Cilium notes, Kafka topic creation, reconciler usage.

### Changed
- Code quality improvements across 51 files (health check refactor, linting, consistency).
- Standardized `PORT` environment variable usage for dynamic port configuration.
- Excluded stress test directory from coverage reports.

## [0.1.0] - 2026-03-25

### Added
- Initial release of HOWK (High Opinionated Webhook Kit).
- **Core pipeline**: API → Kafka (`howk.pending`) → Worker → HTTP delivery → Results (`howk.results`).
- **Circuit breaker**: Per-endpoint with CLOSED/OPEN/HALF_OPEN states, optimistic locking, distributed probe lock.
- **Retry strategy**: Exponential backoff with jitter, circuit-aware delays, configurable max attempts (default 20).
- **Scheduler**: Redis sorted set polling, batch re-enqueue of due retries.
- **Reconciler**: Rebuild Redis hot state from Kafka compacted topic (`howk.state`).
- **Dead letter queue**: `howk.deadletter` topic for exhausted retries and permanent failures.
- **Lua scripting engine**: Sandboxed per-config payload transformation with modules: `kv`, `http`, `crypto`, `base64`, `json`, `log`.
- **Transformer (Path B)**: Filesystem-deployed `.lua` scripts at `POST /incoming/:script_name` with `howk.post()` fan-out.
- **Slow lane**: Separate topic + worker for rate-limited delivery with configurable throughput.
- **Webhook signing**: HMAC-SHA256 signatures via `X-Webhook-Signature` header.
- **Idempotency**: Optional `idempotency_key` to deduplicate enqueues within TTL window.
- **Domain-level concurrency**: Per-domain and per-endpoint inflight limits.
- **Prometheus metrics**: `/metrics` endpoint with enqueue, delivery, retry, and circuit breaker counters.
- **Configuration**: Viper-based with YAML files, `HOWK_*` environment variable overrides, and sensible defaults.
- **Test infrastructure**: Integration tests with isolated Kafka topics + Redis key prefixes (`testutil.NewIsolatedEnv`), unit tests with miniredis.
- **CI/CD**: GitHub Actions with Redis + Redpanda services, race detector, Codecov integration.

[0.4.2]: https://github.com/renatocron/howk/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/renatocron/howk/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/renatocron/howk/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/renatocron/howk/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/renatocron/howk/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/renatocron/howk/releases/tag/v0.1.0
