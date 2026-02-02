# Lua Scripting Engine

The HOWK Lua Scripting Engine provides a sandboxed environment for transforming webhook payloads before delivery.

## Overview

The engine executes Lua scripts in isolated states with resource limits, allowing safe payload transformation without affecting system stability.

## Architecture

### Components

```
┌─────────────────────────────────────────────────────────────┐
│                     Script Engine                           │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │   Loader     │  │   Pool       │  │   Consumer   │      │
│  │  (Cache)     │  │ (Lua States) │  │   (Kafka)    │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
└─────────────────────────────────────────────────────────────┘
         │                    │                    │
         ▼                    ▼                    ▼
    Redis Cache         Script Exec         Kafka Scripts
```

### Key Features

- **Sandboxed Execution**: No filesystem, OS, or debug access
- **Resource Limits**: Memory and CPU timeout protection
- **Script Caching**: Local cache with Redis backup
- **Kafka Sync**: Scripts synchronized via compacted topic
- **Module System**: Extensible with safe modules

## Configuration

```yaml
lua:
  enabled: false              # Feature flag (default: false)
  timeout: 500ms              # Max execution time per script
  memory_limit_mb: 50         # Approximate memory limit per script
  allowed_hosts: ["*"]        # HTTP module allowlist
  http_timeout: 5s            # HTTP request timeout
  kv_ttl_default: 24h         # Default KV store TTL
  http_cache_enabled: true    # Enable HTTP response caching
  http_cache_ttl: 5m          # HTTP cache TTL
```

## Script Input/Output

### Input Globals (Read-Only)

| Variable | Type | Description |
|----------|------|-------------|
| `payload` | string | Raw request body (JSON or binary) |
| `headers` | table | HTTP headers from original request |
| `metadata` | table | Webhook metadata |
| `previous_error` | table\|nil | Error info on retry attempts |

#### Metadata Fields

```lua
metadata.webhook_id    -- string: ULID
metadata.config_id     -- string: Configuration ID
metadata.attempt       -- number: Current attempt (0-indexed)
metadata.max_attempts  -- number: Max retry attempts
metadata.created_at    -- string: ISO8601 timestamp
```

### Output Tables (Write)

#### `request` Table

```lua
request.body = "..."           -- string: Override outgoing body (supports binary)
request.headers = {}           -- table: Additional/override headers
```

#### `config` Table

```lua
config.opt_out_default_headers = true  -- bool: Skip X-Webhook-* headers
```

## Available Modules

### json

JSON encoding/decoding.

```lua
local json = require("json")

-- Decode
local data = json.decode(payload)
data.enriched = true

-- Encode
request.body = json.encode(data)
```

### base64

Base64 encoding/decoding (safe for binary data).

```lua
local base64 = require("base64")

-- Encode
local encoded = base64.encode(binary_data)

-- Decode (returns binary-safe string)
local decoded = base64.decode(encoded_string)
```

### http

HTTP GET requests with allowlist.

```lua
-- Simple GET
local response = http.get("https://api.example.com/token")

-- With headers
local response = http.get("https://api.example.com/data", {
    ["Authorization"] = "Bearer " .. token
})

-- Response structure:
-- response.status_code  -- number
-- response.body         -- string
-- response.headers      -- table
```

**Features:**
- Hostname allowlist enforcement
- Response caching (if enabled)
- Singleflight (deduplication for concurrent requests)

### kv

Redis-backed key-value storage.

```lua
-- Set value
kv.set("token", "abc123", 3600)  -- key, value, ttl_seconds

-- Get value
local token = kv.get("token")  -- returns nil if not found

-- Delete value
kv.del("token")
```

### crypto

RSA credential decryption.

```lua
-- Decrypt credential encrypted with RSA-OAEP + AES-GCM
local plaintext = crypto.decrypt_credential("key_name", encrypted_data)
```

### log

Structured logging.

```lua
log.info("Processing webhook")
log.warn("Rate limit approaching")
log.error("Failed to fetch token: " .. error_message)
```

## Binary Data Support

The engine properly handles binary data throughout the pipeline:

### Input Binary Data

When receiving binary data directly:
```lua
-- Binary payload is passed through unchanged
request.body = payload
```

### Base64-Encoded Binary in JSON

For JSON APIs that need to send binary data:
```lua
local json = require("json")
local base64 = require("base64")

-- Parse JSON containing base64-encoded binary
local data = json.decode(payload)

-- Decode to binary
request.body = base64.decode(data.image_data)

-- Set appropriate content type
request.headers["Content-Type"] = "image/png"
config.opt_out_default_headers = true
```

### Binary Safety Guarantees

- All byte values 0-255 preserved
- Null bytes (0x00) handled correctly
- No UTF-8 encoding/decoding corruption
- Base64 round-trip preserves exact bytes

## Security Model

### Sandboxing

The following are **NOT** available:
- `io` module (filesystem access)
- `os` module (system commands)
- `debug` module (introspection)
- `dofile`, `loadfile`, `load` functions
- Network access except via `http` module

### Resource Limits

| Limit | Implementation | Behavior on Exceed |
|-------|---------------|-------------------|
| CPU Time | Context timeout | ScriptErrorTimeout |
| Memory | Registry size limit | ScriptErrorMemoryLimit |
| HTTP Requests | Configured timeout | ScriptErrorModuleHTTP |

### Script Validation

Scripts are validated before execution:
- Syntax checking
- Basic structure validation
- Hash verification (if ScriptHash provided)

## Error Handling

### ScriptError Types

| Type | Description | Retryable |
|------|-------------|-----------|
| `script_not_found` | Script missing from cache | No |
| `script_syntax_error` | Lua compilation failed | No |
| `script_runtime_error` | Execution panicked | No |
| `script_timeout` | Exceeded CPU limit | No |
| `script_memory_limit` | Exceeded memory limit | No |
| `script_module_kv` | Redis operation failed | Yes |
| `script_module_http` | HTTP request failed | Yes |
| `script_module_crypto` | Decryption failed | No* |
| `script_invalid_output` | Output validation failed | No |

*Except for transient crypto key issues

### Error Propagation

Non-retryable errors result in DLQ with appropriate reason:
- `script_disabled` - Scripts disabled but ScriptHash set
- `script_not_found` - Script missing
- `script_syntax_error` - Invalid Lua
- `script_timeout` - CPU limit exceeded
- `script_memory_limit` - Memory limit exceeded
- `script_runtime_error` - General execution failure

## Best Practices

### Performance

1. **Minimize HTTP calls** - Use caching for tokens
2. **Reuse KV lookups** - Store frequently accessed data
3. **Keep scripts small** - Large scripts increase memory pressure
4. **Avoid infinite loops** - Scripts are terminated on timeout

### Binary Data Handling

When sending binary data through JSON-only APIs:

```lua
-- 1. Client encodes binary as base64 in JSON
-- {"image_data": "iVBORw0KGgo..."}

-- 2. Script decodes and sets appropriate headers
local json = require("json")
local base64 = require("base64")

local data = json.decode(payload)
request.body = base64.decode(data.image_data)
request.headers["Content-Type"] = "image/png"
config.opt_out_default_headers = true
```

## Limitations

### CPU-Bound Infinite Loops

The current gopher-lua version (1.1.1) does not support instruction counting hooks. Pure arithmetic loops like:

```lua
while true do
    local x = 1 + 1
end
```

May not respond to context cancellation until they perform an operation that yields (like a function call). The timeout will eventually trigger, but the goroutine may briefly continue running.

**Workaround:** The registry size limit and context cancellation handle most practical cases. Avoid tight infinite loops in scripts.

### Memory Limit Precision

The memory limit is enforced via registry size, which is an approximation:
- Each registry entry ~8 bytes (pointer size)
- Actual memory usage includes Lua objects, strings, tables
- The limit prevents runaway growth but is not exact

### HTTP Module Limitations

- Only GET requests supported
- No request body in HTTP calls
- Response body size limited by memory limits
