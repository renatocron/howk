# Incoming Webhook Transformers

Transformers are API-side Lua scripts that receive incoming HTTP requests and fan them out into multiple webhooks. This enables powerful use cases like:

- **Event routing**: Split a single incoming event to multiple destinations
- **Payload transformation**: Modify, filter, or enrich webhook payloads
- **Protocol bridging**: Transform between different webhook formats
- **Fan-out patterns**: One incoming request → N outgoing webhooks

## Quick Start

### 1. Create a Transformer Script

Create a Lua file in your configured script directory:

```lua
-- /etc/howk/transformers/echo.lua
-- Simple echo transformer: forwards incoming payload to a single endpoint

local data = json.decode(incoming)

-- Create outgoing webhook
howk.post("https://httpbin.org/post", data)
```

### 2. Test It

```bash
curl -X POST http://localhost:8080/incoming/echo \
  -H "Content-Type: application/json" \
  -d '{"message": "hello", "user": "alice"}'
```

Response:
```json
{
  "webhooks": [
    {"id": "wh_01JHX...", "endpoint": "https://httpbin.org/post"}
  ],
  "count": 1
}
```

## Configuration

Enable transformers in your configuration:

```yaml
# config.yaml
transformer:
  enabled: true
  script_dirs:
    - "/etc/howk/transformers"
  passwd_dirs: []  # Optional: separate dir for .passwd files
  timeout: 500ms  # Script execution timeout
  memory_limit_mb: 50  # Lua memory limit
```

Or via environment variables:

```bash
export HOWK_TRANSFORMER_ENABLED=true
export HOWK_TRANSFORMER_SCRIPT_DIRS=/etc/howk/transformers
export HOWK_TRANSFORMER_TIMEOUT=500ms
export HOWK_TRANSFORMER_MEMORY_LIMIT_MB=50
```

## Kubernetes Deployment (Helm)

When deploying with Helm, configure transformers in `values.yaml`:

```yaml
config:
  transformer:
    enabled: true
    scriptDirs:
      - "/etc/howk/transformers"
    timeout: "500ms"
    memoryLimitMb: 50

# Define transformer scripts inline
transformerScripts:
  stripe-router: |
    local ok, event = pcall(json.decode, incoming)
    if not ok then return end
    
    if event.type == "charge.succeeded" then
      howk.post("https://billing.internal/webhook", event)
    end
    howk.post("https://analytics.internal/track", event)

# Optional: JSON configs per script  
transformerConfigs:
  stripe-router: |
    {"allowed_domains": ["billing.internal", "analytics.internal"]}

# Optional: Basic Auth
transformerAuth:
  stripe-router: |
    admin:$2b$12$xxxxxxxx...
```

The Helm chart creates ConfigMaps for scripts/configs/auth and mounts them into the API pods.

## Script File Layout

Each transformer consists of 1-3 files:

```
/etc/howk/transformers/
├── my-transformer.lua          # Required: Lua script
├── my-transformer.json         # Optional: Configuration
└── my-transformer.passwd       # Optional: Basic Auth credentials
```

### Script Naming

Script names must match the pattern: `^[a-z0-9][a-z0-9_-]*$`

- Start with alphanumeric
- Contain only: lowercase letters, numbers, underscores, hyphens
- Examples: `stripe-webhook`, `billing_v2`, `alert-handler`

## Lua API Reference

### Globals (Read-Only)

| Global | Type | Description |
|--------|------|-------------|
| `incoming` | string | Raw request body (bytes as string) |
| `headers` | table | HTTP request headers |
| `config` | table | Contents of `[script].json` (empty if no file) |

### howk Module

#### `howk.post(endpoint, payload [, options])`

Creates and queues a webhook for delivery.

**Parameters:**
- `endpoint` (string, required): Target URL (must match `allowed_domains` if configured)
- `payload` (table, required): Data to send (automatically JSON-encoded)
- `options` (table, optional): 
  - `headers` - Additional HTTP headers (table)
  - `signing_secret` - HMAC-SHA256 secret for signature
  - `config_id` - Override default config_id (default: script name)
  - `max_attempts` - Max retry attempts (default: 20)

**Returns:** Webhook ID string (e.g., `wh_01JHX...`)

**Example:**
```lua
local id = howk.post("https://api.example.com/webhook", {
    event = "user.created",
    user_id = "123"
}, {
    headers = {["X-Custom"] = "value"},
    signing_secret = "my-secret",
    max_attempts = 10
})

log.info("Created webhook: " .. id)
```

### Built-in Modules

Same modules available as worker-side scripts:

| Module | Functions |
|--------|-----------|
| `json` | `json.encode(table)`, `json.decode(string)` |
| `base64` | `base64.encode(str)`, `base64.decode(str)` |
| `kv` | `kv.get(key)`, `kv.set(key, value [, ttl])`, `kv.del(key)` |
| `http` | `http.get(url [, headers [, options]])` |
| `log` | `log.info(msg [, fields])`, `log.warn()`, `log.error()`, `log.debug()` |
| `crypto` | `crypto.decrypt_credential(key_suffix, sym_key, data)` |

## Handling Different Content Types

The `/incoming/:script_name` endpoint accepts **any** content type. The raw body is passed to your script as the `incoming` string.

### JSON Payloads

```lua
-- Handle JSON with error checking
local ok, data = pcall(json.decode, incoming)
if not ok then
    log.error("Invalid JSON received", {error = data})
    -- Script ends, no webhooks created
    return
end

-- Process the data
howk.post("https://api.example.com/webhook", data)
```

### Form Data (application/x-www-form-urlencoded)

```lua
-- Parse form data manually
local function parse_formdata(body)
    local result = {}
    for pair in string.gmatch(body, "([^&]+)") do
        local key, value = string.match(pair, "([^=]+)=([^=]*)")
        if key then
            -- URL decode (basic)
            value = string.gsub(value, "%%(%x%x)", function(h)
                return string.char(tonumber(h, 16))
            end)
            result[key] = value
        end
    end
    return result
end

local data = parse_formdata(incoming)
howk.post("https://api.example.com/webhook", data)
```

### XML Payloads

```lua
-- For XML, you might extract specific fields with patterns
-- or use the raw body
local webhook_id = howk.post("https://api.example.com/xml-webhook", {
    raw_xml = incoming,
    content_type = headers["Content-Type"] or "application/xml"
})
```

### Plain Text

```lua
-- Simple text processing
local lines = {}
for line in string.gmatch(incoming, "[^\r\n]+") do
    table.insert(lines, line)
end

howk.post("https://api.example.com/log", {
    line_count = #lines,
    first_line = lines[1]
})
```

## Configuration File ([script].json)

The JSON file controls script behavior:

```json
{
  "allowed_domains": ["api.example.com", "hooks.slack.com"],
  "webhook_timeout": 30,
  "custom_setting": "value"
}
```

### `allowed_domains`

**Critical security feature!** If specified, `howk.post()` will reject endpoints not matching:

```json
{
  "allowed_domains": ["*.internal.company.com", "api.stripe.com"]
}
```

- Exact match: `"api.example.com"` matches `https://api.example.com/...`
- Wildcard subdomains: `"*.example.com"` matches `https://foo.example.com/...`
- **Blocks all other domains** - requests fail with error

If `allowed_domains` is not specified (or no JSON file), all domains are allowed.

### Accessing Config in Lua

```lua
-- stripe-ingest.json: {"stripe_version": "2024-01", "allowed_domains": [...]}
local version = config.stripe_version or "default"
log.info("Processing with Stripe version", {version = version})
```

## Authentication ([script].passwd)

Protect scripts with HTTP Basic Auth:

### Bcrypt (Recommended)

Generate hash:
```bash
# Using htpasswd (Apache)
htpasswd -bnBC 12 "" "my-secret-password" | tr -d ':\n' | sed 's/$2y/$2b/'
# Output: $2b$12$xxxxxxxx...
```

Create `.passwd` file:
```
# /etc/howk/transformers/stripe-ingest.passwd
admin:$2b$12$xxxxxxxx...
webhook:$2b$12$yyyyyyyy...
```

### Plaintext (Development Only)

```
# NOT recommended for production!
devuser:devpassword
```

### Test with Auth

```bash
curl -X POST http://localhost:8080/incoming/stripe-ingest \
  -u admin:my-secret-password \
  -H "Content-Type: application/json" \
  -d '{"type": "charge.succeeded"}'
```

## Error Handling

### In Lua Scripts

Always use `pcall` for operations that might fail:

```lua
-- Safe JSON parsing
local ok, data = pcall(json.decode, incoming)
if not ok then
    log.error("JSON parse failed", {error = tostring(data)})
    -- Return empty - no webhooks created
    return
end

-- Safe HTTP calls
local ok, response, err = pcall(http.get, "https://api.example.com/data")
if not ok or err then
    log.error("HTTP call failed", {error = err or tostring(response)})
    return
end
```

### HTTP Status Codes

| Status | Meaning |
|--------|---------|
| 200 | Success - webhooks created (count may be 0) |
| 400 | Bad request (body too large) |
| 401 | Unauthorized (invalid/missing credentials) |
| 404 | Script not found or transformer feature disabled |
| 413 | Request entity too large |
| 500 | Script execution error |

### Empty Results (0 Webhooks)

A script may legitimately create 0 webhooks:

```lua
-- Event filter transformer
local data = json.decode(incoming)

if data.event_type ~= "critical_alert" then
    log.info("Ignoring non-critical event", {type = data.event_type})
    -- No howk.post() called - returns {"webhooks": [], "count": 0}
    return
end

howk.post("https://pagerduty.com/integration", data)
```

Response:
```json
{"webhooks": [], "count": 0}
```

## Examples

For copy-paste ready examples covering common use cases, see **[transformers-examples.md](transformers-examples.md)**.

Available examples:
- **Basic Echo** - Simple forwarding
- **Event Filtering** - Selective processing
- **Webhook Routing** - Route to different endpoints
- **Payload Transformation** - Modify/enrich data
- **Error Handling Patterns** - Safe JSON parsing, validation
- **Working with Headers** - Signature validation
- **KV Store Usage** - Deduplication, rate limiting, state
- **HTTP Calls** - External API integration
- **Multi-Destination Fan-Out** - One-to-many delivery

### Quick Example

```lua
-- Simple event router
local ok, data = pcall(json.decode, incoming)
if not ok then
    log.error("Invalid JSON")
    return
end

-- Route based on event type
if data.type == "user.created" then
    howk.post("https://crm.internal/webhook", data)
elseif data.type == "order.placed" then
    howk.post("https://fulfillment.internal/webhook", data)
end

-- Always send to analytics
howk.post("https://analytics.internal/track", data)
```

## Hot Reload

### Kubernetes (Helm Deployment)

When using the Helm chart, you have three options for reloading:

#### Option 1: Automatic Pod Restart (Recommended)

```yaml
configmapReload:
  enabled: true
  method: annotation
```

When you update `transformerScripts` in values and run `helm upgrade`, pods automatically restart with new scripts.

#### Option 2: Hot Reload with Sidecar (Zero Downtime)

```yaml
configmapReload:
  enabled: true
  method: signal
  sleepTime: 15
```

Uses a sidecar container that watches ConfigMaps and sends SIGHUP to reload scripts without pod restart.

#### Option 3: Manual Reload

```bash
# Send SIGHUP to all API pods
kubectl exec deploy/howk-api -- kill -HUP 1

# Or restart deployment
kubectl rollout restart deployment/howk-api
```

### Docker / Bare Metal

```bash
# Send SIGHUP to the API process
kill -HUP $(pgrep howk-api)

# Or if running in Docker
docker kill -s HUP <container>
```

Logs will show:
```
INFO Received SIGHUP, reloading transformer scripts
INFO Loaded transformer scripts count=5
```

## Debugging Tips

### 1. Use Log Module

```lua
log.debug("Received request", {
    content_type = headers["Content-Type"],
    body_size = #incoming,
    user_agent = headers["User-Agent"]
})
```

### 2. Test Scripts Locally

```bash
# Set up test directory
mkdir -p /tmp/howk-transformers
echo 'print("Hello from Lua")' > /tmp/howk-transformers/test.lua

# Run API with test scripts
HOWK_TRANSFORMER_ENABLED=true \
HOWK_TRANSFORMER_SCRIPT_DIRS=/tmp/howk-transformers \
HOWK_LOG_LEVEL=debug \
  go run ./cmd/api
```

### 3. Validate JSON Config

```bash
# Check your JSON syntax
jq . /etc/howk/transformers/my-script.json
```

### 4. Monitor Webhook Creation

The response shows exactly which webhooks were created:

```bash
curl -s -X POST http://localhost:8080/incoming/my-script \
  -d '{"test": true}' | jq .
```

## Security Best Practices

1. **Always use `allowed_domains`** in production to prevent SSRF attacks
2. **Use bcrypt passwords** in `.passwd` files
3. **Validate all input** with `pcall` before processing
4. **Don't log sensitive data** (passwords, tokens, PII)
5. **Set memory limits** to prevent resource exhaustion
6. **Use HTTPS endpoints only** in production

## Limitations

- Script timeout: Configurable, default 500ms
- Memory limit: Per-script, default 50MB
- Max request body: Same as API config (default 1MB)
- No filesystem access from Lua (`io` module disabled)
- No OS commands from Lua (`os` module disabled)
