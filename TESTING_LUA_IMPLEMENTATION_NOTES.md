# Lua Scripting Engine - Implementation Notes

## Overview

This document tracks the implementation progress of the Lua Scripting Engine for HOWK webhook delivery system. The implementation follows the plan outlined in `/home/renato/.claude/plans/memoized-pondering-bird.md`.

## Implementation Status

### ✅ Phase 1: Foundation (COMPLETED)

**Files Modified:**
- `internal/domain/types.go` - Added `ScriptHash string` field to Webhook struct (line 37)
- `internal/domain/types.go` - Added 8 new DLQ reason constants for script errors
- `internal/script/domain.go` - Created ScriptConfig, ScriptError, TransformResult types
- `internal/config/config.go` - Added LuaConfig struct and integration
- `docker-compose.yml` - Added `howk.scripts` Kafka topic with compaction
- `.env.example` - Documented Lua configuration environment variables
- `config.example.yaml` - Added Lua configuration section
- `CLAUDE.md` - Updated with Lua scripting documentation
- `.gitignore` - Added patterns for stray binaries (api, worker, scheduler, reconciler)

**Key Types Added:**
```go
type ScriptConfig struct {
    ConfigID  domain.ConfigID `json:"config_id"`
    LuaCode   string          `json:"lua_code"`
    Hash      string          `json:"hash"`        // SHA256
    Version   string          `json:"version"`
    CreatedAt time.Time       `json:"created_at"`
    UpdatedAt time.Time       `json:"updated_at"`
}

type LuaConfig struct {
    Enabled       bool              `mapstructure:"enabled"`
    Timeout       time.Duration     `mapstructure:"timeout"`
    MemoryLimitMB int               `mapstructure:"memory_limit_mb"`
    AllowedHosts  []string          `mapstructure:"allowed_hosts"`
    CryptoKeys    map[string]string `mapstructure:"crypto_keys"`
    HTTPTimeout   time.Duration     `mapstructure:"http_timeout"`
    KVTTLDefault  time.Duration     `mapstructure:"kv_ttl_default"`
}
```

**DLQ Reasons Added:**
- `DLQReasonScriptDisabled`
- `DLQReasonScriptNotFound`
- `DLQReasonScriptSyntaxError`
- `DLQReasonScriptRuntimeError`
- `DLQReasonScriptTimeout`
- `DLQReasonScriptMemoryLimit`
- `DLQReasonCryptoKeyNotFound`
- `DLQReasonScriptInvalidOutput`

### ✅ Phase 2: Script Management API (COMPLETED)

**Dependencies Installed:**
- `github.com/yuin/gopher-lua v1.1.1` - Lua 5.1 VM implementation

**Files Created:**
- `internal/script/validator.go` - Lua syntax validation
- `internal/script/publisher.go` - Kafka integration for script publishing
- `internal/api/h_script.go` - Script management HTTP endpoints

**Files Modified:**
- `internal/hotstate/hotstate.go` - Added HotState interface methods for scripts
- `internal/hotstate/redis.go` - Implemented GetScript, SetScript, DeleteScript
- `internal/broker/kafka.go` - Updated Publish method signature to variadic
- `internal/api/server.go` - Registered script routes and initialized components
- `cmd/api/main.go` - Added script validator and publisher initialization

**API Endpoints Implemented:**
1. `PUT /config/:config_id/script` - Upload/update script
2. `GET /config/:config_id/script` - Retrieve script
3. `DELETE /config/:config_id/script` - Delete script
4. `POST /config/:config_id/script/test` - Test script execution (implemented in Phase 7)

**Redis Keys:**
- `script:{config_id}` - Stores ScriptConfig JSON, TTL 7 days

**Kafka Topic:**
- `howk.scripts` - Compacted topic, key=config_id, value=ScriptConfig JSON

### ✅ Phase 3: Lua Engine Core (COMPLETED)

**Dependencies Installed:**
- `layeh.com/gopher-json v0.0.0-20201124131017-552bb3c4c3bf` - JSON module for Lua
- `github.com/cjoudrey/gluahttp v0.0.0-20201111170219-25003d9adfa9` - HTTP module (not yet used)

**Files Created:**
- `internal/script/engine.go` - Core Lua execution engine with VM pooling
- `internal/script/loader.go` - Script caching and loading
- `internal/script/modules/base64.go` - Base64 encode/decode module
- `internal/script/engine_test.go` - Comprehensive engine tests (8 tests, all passing)

**Key Features:**
- **VM Pooling**: sync.Pool for Lua state reuse
- **Sandboxing**: Disabled io, os, debug, package modules; removed dofile, loadfile, load
- **Timeout Enforcement**: 500ms CPU timeout via goroutine + context
- **Memory Limits**: 50MB per execution (configured)
- **Script Compilation**: Pre-compilation and caching support

**Lua Modules Available:**
- `json` - encode/decode JSON (layeh.com/gopher-json)
- `base64` - encode/decode Base64 (custom implementation)
- Standard libraries: string, table, math

**Input Globals:**
```lua
payload           -- string: raw JSON payload
headers           -- table: HTTP headers map
metadata          -- table: {webhook_id, config_id, attempt, max_attempts, created_at}
previous_error    -- nil (future: retry error info)
```

**Output Globals:**
```lua
request.body      -- string: override payload (can be binary)
request.headers   -- table: additional/override headers
config.opt_out_default_headers  -- bool: skip X-Webhook-* headers
```

**Test Results:**
- Simple header transform: ✅
- JSON transformation: ✅
- Base64 encode/decode: ✅
- Metadata access: ✅
- Script not found error: ✅
- Disabled engine error: ✅
- Timeout enforcement: ✅ (infinite loop caught)
- Runtime error handling: ✅

### ✅ Phase 7: Worker Integration (COMPLETED)

**Files Modified:**
- `internal/worker/worker.go`:
  - Added `scriptEngine *script.Engine` field to Worker struct
  - Updated NewWorker to accept script.Engine parameter
  - Integrated script execution at lines 107-133 (after status update, before HTTP delivery)
  - Added `handleScriptError()` method for error classification
  - Added `sendToDLQForScriptDisabled()` method
  - Added `sendToDLQ()` helper method
  - Implemented lazy-loading of scripts from Redis if not in cache
- `cmd/worker/main.go`:
  - Initialize script.Loader
  - Create script.Engine with config
  - Pass to worker.NewWorker
  - Added defer scriptEngine.Close()
- `internal/worker/worker_test.go`:
  - Added script package import
  - Updated setupWorkerTest to create test script engine
  - Updated TestNewWorker to pass script engine
  - All tests passing ✅

**Script Execution Flow:**
1. Check if webhook has ScriptHash set
2. If HOWK_LUA_ENABLED=false but ScriptHash set → Send to DLQ (safety mechanism)
3. Try to load script from Redis if not in worker's cache (lazy-load)
4. Execute script transformation via engine.Execute()
5. On error: Classify as retryable (Redis/HTTP failures) or non-retryable (syntax, timeout, etc.)
6. Apply transformation to webhook
7. Continue with HTTP delivery

**Error Handling:**
- **Non-retryable** → DLQ: ScriptDisabled, NotFound, Syntax, Runtime, Timeout, MemoryLimit, InvalidOutput
- **Retryable** → Schedule retry: Redis unavailable, HTTP fetch failed (future KV/HTTP modules)

### ✅ Phase 9: API Integration (PARTIALLY COMPLETED)

**Files Modified:**
- `internal/api/server.go`:
  - Modified `buildWebhook()` method to auto-apply ScriptHash (lines 205-235)
  - Checks Redis for script when enqueueing webhook
  - Sets webhook.ScriptHash if script exists
  - Logs script hash application

**Files Created:**
- `internal/script/engine.go` - Added `GetLoader()` method to expose loader

**Auto-Apply Flow:**
1. User enqueues webhook via `POST /webhooks/:config_id/enqueue`
2. API calls `buildWebhook(configID, req)`
3. buildWebhook checks Redis: `hotstate.GetScript(ctx, configID)`
4. If script exists: Parse JSON, extract hash, set `webhook.ScriptHash`
5. Webhook published to Kafka with ScriptHash field
6. Worker receives webhook, sees ScriptHash, executes transformation

**What's Complete:**
- ✅ Auto-apply ScriptHash on enqueue
- ✅ Worker lazy-loads scripts from Redis
- ✅ Script test endpoint fully implemented

**What's Missing:**
- ⏸️ Script hot reload from Kafka topic (worker doesn't subscribe to howk.scripts)
- ⏸️ API script cache hot reload (API doesn't subscribe to howk.scripts)

### ✅ Test Endpoint Implementation (BONUS - COMPLETED)

**File Modified:**
- `internal/api/h_script.go` - Implemented `handleTestScript()` (was stub in Phase 2)

**Implementation:**
- Creates temporary webhook with test data
- Creates temporary script loader and engine
- Executes script in sandboxed environment
- Returns transformed payload, headers, and execution time
- **Performance**: ~0.54ms execution time

**Example Usage:**
```bash
curl -X POST http://localhost:8080/config/test_config/script/test \
  -H "Content-Type: application/json" \
  -d '{
    "lua_code": "headers[\"X-Test\"] = \"value\"",
    "payload": {"foo": "bar"},
    "headers": {"Content-Type": "application/json"}
  }'
```

## Not Yet Implemented

### ⏸️ Phase 4: KV Module (Redis-backed storage)
- File: `internal/script/modules/kv.go`
- Namespace isolation: `lua:kv:{config_id}:{key}`
- Methods: kv.get(), kv.set(), kv.del()

### ⏸️ Phase 5: HTTP Module (GET with allowlist)
- File: `internal/script/modules/http.go`
- Singleflight deduplication
- Hostname allowlist enforcement
- Methods: http.get(url, headers)

### ⏸️ Phase 6: Crypto Module (RSA-OAEP + AES-GCM)
- File: `internal/script/modules/crypto.go`
- RSA private key loading
- Two-stage decryption
- Methods: crypto.decrypt_credential(key_name, symmetric_key_b64, encrypted_data_b64)

### ⏸️ Phase 8: Binary Data & Header Control
- Support binary request.body
- Implement config.opt_out_default_headers
- Modify delivery client to handle binary payloads

### ⏸️ Phase 10: Testing & Documentation
- Integration tests for full lifecycle
- Performance benchmarks
- Example scripts library
- Migration guide
- Security documentation

## Known Issues & Limitations

1. **Script Hot Reload**: Worker doesn't subscribe to `howk.scripts` topic, so script updates require worker restart or cache miss to load from Redis
2. **API Script Cache**: API doesn't maintain in-memory script cache, queries Redis on every enqueue
3. **Script Metrics**: No Prometheus metrics yet for script execution
4. **Script Debugging**: No structured logging within Lua scripts
5. **Memory Limits**: Configured but not enforced at VM level (Lua limitation)

## Configuration

**Environment Variables:**
```bash
HOWK_LUA_ENABLED=true              # Must be explicitly enabled
HOWK_LUA_TIMEOUT=500ms             # CPU timeout per execution
HOWK_LUA_MEMORY_LIMIT_MB=50        # Memory limit (not enforced yet)
HOWK_LUA_ALLOWED_HOSTS=*           # HTTP allowlist (not used yet)
HOWK_LUA_HTTP_TIMEOUT=5s           # HTTP module timeout (not used yet)
HOWK_LUA_KV_TTL_DEFAULT=24h        # KV default TTL (not used yet)
```

**Kafka Topic:**
```yaml
howk.scripts:
  partitions: 1
  replication: 1
  cleanup.policy: compact
  retention.ms: -1  # Indefinite
```

## Testing Results

### Unit Tests
- ✅ All existing tests pass
- ✅ Script engine tests: 8/8 passing
- ✅ Worker tests updated and passing

### Integration Test
- ✅ Script upload → Kafka → Redis
- ✅ Script retrieval from API
- ✅ Script test endpoint execution
- ✅ Webhook enqueue with auto-applied ScriptHash
- ✅ Worker lazy-load from Redis
- ✅ Script transformation execution
- ✅ HTTP delivery with transformed headers
- ✅ Webhook status: delivered successfully

### Performance
- Script test endpoint: ~0.54ms
- Worker delivery with script: ~3.11ms total (including HTTP)
- Overhead: < 10ms (well within target)

## Security Considerations

**Implemented:**
- ✅ Sandboxing: io, os, debug, package modules disabled
- ✅ Timeout: 500ms CPU limit enforced
- ✅ Feature flag: HOWK_LUA_ENABLED must be explicitly set
- ✅ DLQ safety: Scripts disabled + ScriptHash set = DLQ to prevent data leaks

**Not Yet Implemented:**
- ⏸️ Memory limits (Lua VM doesn't support runtime limits)
- ⏸️ Network allowlist (HTTP module not implemented)
- ⏸️ Namespace isolation for KV (module not implemented)
- ⏸️ Credential encryption (Crypto module not implemented)

## Example Script

Current working example:
```lua
-- Add custom headers
headers["X-Transformed"] = "by-lua-script"
headers["X-Original-Payload"] = payload

-- Access metadata
headers["X-Config"] = metadata.config_id

-- JSON transformation (example)
local json = require("json")
local data = json.decode(payload)
data.enriched = true
request.body = json.encode(data)
```

## Next Steps

**Priority 1 (Core Features):**
1. Implement Phase 4: KV module for token caching use cases
2. Implement Phase 5: HTTP module for payload enrichment
3. Add script hot reload (Kafka subscription in worker/API)

**Priority 2 (Production Readiness):**
4. Add Prometheus metrics for script execution
5. Implement Phase 10: Integration tests and documentation
6. Add structured logging for script execution steps

**Priority 3 (Advanced Features):**
7. Implement Phase 6: Crypto module for credential decryption
8. Implement Phase 8: Binary data support
9. Performance optimization and benchmarking

## Files Added/Modified Summary

**New Files (14):**
- `internal/script/domain.go`
- `internal/script/validator.go`
- `internal/script/publisher.go`
- `internal/script/loader.go`
- `internal/script/engine.go`
- `internal/script/engine_test.go`
- `internal/script/modules/base64.go`
- `internal/api/h_script.go`
- `TESTING_IMPLEMENTATION_NOTES.md` (this file)
- `TESTING_SUMMARY.md`

**Modified Files (15):**
- `internal/domain/types.go`
- `internal/config/config.go`
- `internal/broker/kafka.go`
- `internal/hotstate/hotstate.go`
- `internal/hotstate/redis.go`
- `internal/worker/worker.go`
- `internal/worker/worker_test.go`
- `internal/api/server.go`
- `cmd/api/main.go`
- `cmd/worker/main.go`
- `docker-compose.yml`
- `.env.example`
- `config.example.yaml`
- `CLAUDE.md`
- `.gitignore`

**Total Lines of Code Added:** ~2,000+ lines

## Changelog

**2026-02-01:**
- ✅ Phase 1: Foundation complete
- ✅ Phase 2: Script Management API complete
- ✅ Phase 3: Lua Engine Core complete
- ✅ Phase 7: Worker Integration complete
- ✅ Phase 9: Auto-apply ScriptHash (partial)
- ✅ Test endpoint fully implemented
- ✅ End-to-end test successful
- ✅ All unit tests passing

---
*Last Updated: 2026-02-01 03:00 AM*
