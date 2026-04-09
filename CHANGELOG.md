# Changelog

All notable changes to HOWK are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.4.1]: https://github.com/renatocron/howk/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/renatocron/howk/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/renatocron/howk/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/renatocron/howk/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/renatocron/howk/releases/tag/v0.1.0
