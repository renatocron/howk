# Transformer Examples Cookbook

Copy-paste ready examples for common use cases.

## Table of Contents

- [Basic Echo](#basic-echo)
- [Event Filtering](#event-filtering)
- [Webhook Routing](#webhook-routing)
- [Payload Transformation](#payload-transformation)
- [Error Handling Patterns](#error-handling-patterns)
- [Working with Headers](#working-with-headers)
- [KV Store Usage](#kv-store-usage)
- [HTTP Calls from Scripts](#http-calls-from-scripts)

---

## Basic Echo

Simply forward incoming JSON to another endpoint.

**File:** `/etc/howk/transformers/echo.lua`

```lua
-- Echo transformer - forwards payload unchanged
local data = json.decode(incoming)
howk.post("https://httpbin.org/post", data)
```

**Test:**
```bash
curl -X POST http://localhost:8080/incoming/echo \
  -H "Content-Type: application/json" \
  -d '{"hello": "world"}'
```

---

## Event Filtering

Only process specific event types.

**File:** `/etc/howk/transformers/filter.lua`

```lua
-- Only forward "critical" severity events
local ok, data = pcall(json.decode, incoming)
if not ok then
    return  -- Silently drop invalid JSON
end

if data.severity ~= "critical" then
    log.info("Skipping non-critical event", {severity = data.severity})
    return  -- 0 webhooks created
end

howk.post("https://pagerduty.com/webhook", data)
```

---

## Webhook Routing

Route different events to different endpoints.

**File:** `/etc/howk/transformers/router.lua`

```lua
-- Route events based on 'type' field
local ok, event = pcall(json.decode, incoming)
if not ok then
    log.error("Invalid JSON")
    return
end

-- Define routes
local routes = {
    user_created = "https://crm.internal/webhooks/user",
    order_placed = "https://fulfillment.internal/webhooks/order",
    payment_received = "https://accounting.internal/webhooks/payment"
}

local endpoint = routes[event.type]
if endpoint then
    howk.post(endpoint, event)
    log.info("Routed event", {type = event.type, endpoint = endpoint})
else
    log.warn("No route for event type", {type = event.type})
end
```

---

## Payload Transformation

Transform the payload before forwarding.

**File:** `/etc/howk/transformers/transform.lua`

```lua
-- Transform GitHub webhook to internal format
local ok, github = pcall(json.decode, incoming)
if not ok then
    log.error("Invalid GitHub payload")
    return
end

-- Build internal format
local internal = {
    source = "github",
    event_type = headers["X-GitHub-Event"],
    repository = github.repository and github.repository.full_name,
    actor = github.sender and github.sender.login,
    timestamp = github.repository and github.repository.updated_at,
    metadata = {
        ref = github.ref,
        commit = github.after,
        installation_id = github.installation and github.installation.id
    }
}

howk.post("https://platform.internal/events", internal)
```

---

## Error Handling Patterns

### Pattern 1: Graceful Degradation

```lua
local ok, data = pcall(json.decode, incoming)
if not ok then
    -- Log error but don't crash
    log.error("Invalid JSON, trying raw forward")
    
    -- Forward raw body anyway
    howk.post("https://logs.internal/raw", {
        raw_body = incoming:sub(1, 10000),  -- Limit size
        error = "json_parse_failed",
        received_at = os.date("!%Y-%m-%dT%H:%M:%SZ")
    })
    return
end

-- Normal processing...
```

### Pattern 2: Validation with Early Return

```lua
local ok, data = pcall(json.decode, incoming)
if not ok then
    log.error("JSON parse failed", {error = tostring(data)})
    return
end

-- Validate required fields
if not data.user_id then
    log.error("Missing user_id")
    return
end

if not data.action then
    log.error("Missing action")
    return
end

-- All validations passed, process...
howk.post("https://api.internal/process", data)
```

### Pattern 3: Try/Catch for HTTP Calls

```lua
local data = json.decode(incoming)

-- Try to enrich with user data
local ok, user_data, err = pcall(http.get, 
    "https://api.internal/users/" .. data.user_id)

if ok and not err then
    data.user = user_data
else
    log.warn("Failed to enrich user data", {user_id = data.user_id, error = err})
    -- Continue without enrichment
end

howk.post("https://webhook.internal/event", data)
```

---

## Working with Headers

### Extract Specific Headers

```lua
-- Get authentication info from headers
local auth_header = headers["Authorization"]
local event_type = headers["X-Webhook-Event"]
local signature = headers["X-Signature"]

log.info("Received webhook", {
    event = event_type,
    has_auth = auth_header ~= nil,
    has_signature = signature ~= nil
})

-- Include headers in forwarded payload
howk.post("https://internal.webhook/process", {
    original_body = json.decode(incoming),
    original_event_type = event_type,
    processed_at = os.time()
})
```

### Validate Webhook Signature (HMAC)

```lua
-- Simple HMAC validation (requires crypto module setup)
local function validate_signature(body, signature, secret)
    -- Note: This is a placeholder - actual HMAC requires specific crypto
    -- For production, implement proper HMAC-SHA256 comparison
    return true  -- Implement actual validation
end

local signature = headers["X-Hub-Signature-256"]
local secret = config.webhook_secret  -- From .json config

if not validate_signature(incoming, signature, secret) {
    log.error("Invalid signature")
    return
end

-- Signature valid, process webhook
local data = json.decode(incoming)
howk.post("https://internal.webhook/process", data)
```

---

## KV Store Usage

### Deduplication

```lua
-- Prevent duplicate webhook processing
local data = json.decode(incoming)
local dedup_key = "dedup:" .. (data.id or "")

-- Check if already processed
local existing = kv.get(dedup_key)
if existing then
    log.info("Duplicate webhook ignored", {id = data.id})
    return
end

-- Mark as processed (TTL: 24 hours)
kv.set(dedup_key, "1", 86400)

-- Process the webhook
howk.post("https://api.internal/webhook", data)
```

### Rate Limiting

```lua
-- Simple rate limiting per user
local data = json.decode(incoming)
local user_id = data.user_id

if not user_id then
    log.error("Missing user_id")
    return
end

local rate_key = "rate_limit:" .. user_id
local count = kv.get(rate_key) or "0"
count = tonumber(count)

if count >= 100 then
    log.warn("Rate limit exceeded", {user_id = user_id})
    return
end

-- Increment counter (expires in 1 hour)
kv.set(rate_key, tostring(count + 1), 3600)

-- Process webhook
howk.post("https://api.internal/webhook", data)
```

### State Persistence

```lua
-- Track sequence numbers for ordered processing
local data = json.decode(incoming)
local stream_id = data.stream_id
local sequence = data.sequence_number

-- Get last processed sequence
local last_seq_key = "seq:" .. stream_id
local last_seq = tonumber(kv.get(last_seq_key) or "0")

if sequence <= last_seq then
    log.info("Out of order event ignored", {
        stream = stream_id,
        received = sequence,
        last_processed = last_seq
    })
    return
end

-- Process event
howk.post("https://api.internal/ordered-events", data)

-- Update sequence
kv.set(last_seq_key, tostring(sequence), 86400)
```

---

## HTTP Calls from Scripts

### Enrich Payload with External Data

```lua
local data = json.decode(incoming)

-- Fetch additional user info
local ok, response = pcall(http.get, 
    "https://api.internal/users/" .. data.user_id,
    {["Authorization"] = "Bearer " .. config.internal_token}
)

if ok and response and response.status_code == 200 then
    local user_info = json.decode(response.body)
    data.user_email = user_info.email
    data.user_tier = user_info.subscription_tier
else
    log.warn("Failed to fetch user info", {user_id = data.user_id})
end

howk.post("https://analytics.internal/track", data)
```

### Conditional Processing Based on External State

```lua
local data = json.decode(incoming)

-- Check if feature flag is enabled
local ok, flag_response = pcall(http.get,
    "https://feature-flags.internal/check/" .. data.feature,
    {},
    {cache_ttl = 60}  -- Cache for 60 seconds
)

local enabled = true  -- Default
if ok and flag_response and flag_response.status_code == 200 then
    local flag = json.decode(flag_response.body)
    enabled = flag.enabled
end

if not enabled then
    log.info("Feature disabled, skipping", {feature = data.feature})
    return
end

-- Feature enabled, process
howk.post("https://api.internal/feature-events", data)
```

---

## Multi-Destination Fan-Out

Send one event to multiple destinations.

```lua
local data = json.decode(incoming)

-- Define destinations
local destinations = {
    {url = "https://analytics.internal/track", priority = "high"},
    {url = "https://audit.internal/log", priority = "high"},
    {url = "https://metrics.internal/count", priority = "low"},
    {url = "https://backup.internal/archive", priority = "low"}
}

-- Send to all destinations
for _, dest in ipairs(destinations) do
    -- Customize payload per destination
    local payload = {
        original_event = data,
        destination = dest.url,
        priority = dest.priority,
        forwarded_at = os.date("!%Y-%m-%dT%H:%M:%SZ")
    }
    
    -- Each call returns a webhook ID
    local id = howk.post(dest.url, payload)
    log.debug("Created webhook", {id = id, destination = dest.url})
end

-- All 4 webhooks created
```

---

## Configuration Examples

### Basic Config (all domains allowed)

**File:** `/etc/howk/transformers/simple.json`
```json
{}
```

### Production Config (restricted domains)

**File:** `/etc/howk/transformers/production.json`
```json
{
  "allowed_domains": [
    "api.internal.company.com",
    "*.internal.company.com",
    "hooks.slack.com"
  ],
  "webhook_secret": "${WEBHOOK_SECRET}",
  "internal_token": "${INTERNAL_API_TOKEN}"
}
```

### With Authentication

**File:** `/etc/howk/transformers/secure.passwd`
```
# Bcrypt hashed passwords
admin:$2b$12$xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
service:$2b$12$yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy
```

---

## Testing Your Scripts

### Quick Test

```bash
# Test with curl
curl -X POST http://localhost:8080/incoming/my-script \
  -H "Content-Type: application/json" \
  -d '{"test": "data"}' | jq .
```

### With Authentication

```bash
curl -X POST http://localhost:8080/incoming/secure-script \
  -u admin:password \
  -H "Content-Type: application/json" \
  -d '{"test": "data"}'
```

### Load Testing

```bash
# Using hey (https://github.com/rakyll/hey)
hey -n 1000 -c 10 \
  -m POST \
  -H "Content-Type: application/json" \
  -d '{"test": "data"}' \
  http://localhost:8080/incoming/my-script
```

---

## Common Pitfalls

### 1. Not Using pcall for json.decode

```lua
-- BAD - will crash on invalid JSON
local data = json.decode(incoming)

-- GOOD - graceful handling
local ok, data = pcall(json.decode, incoming)
if not ok then
    log.error("Invalid JSON")
    return
end
```

### 2. Ignoring howk.post Return Value

```lua
-- GOOD - log the created webhook ID
local id = howk.post("https://api.example.com/webhook", data)
log.info("Created webhook", {id = id})
```

### 3. Not Checking Config Exists

```lua
-- GOOD - provide default
local timeout = config.timeout or 30
local secret = config.webhook_secret  -- may be nil
```

### 4. Logging Sensitive Data

```lua
-- BAD - logs sensitive data
log.info("Received", {body = incoming, headers = headers})

-- GOOD - selective logging
log.info("Received webhook", {
    content_type = headers["Content-Type"],
    size = #incoming,
    event_type = data.event_type  -- safe field
})
```
