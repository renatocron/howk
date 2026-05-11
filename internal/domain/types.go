package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// WebhookID is a ULID-based unique identifier
type WebhookID string

// ConfigID identifies the customer or configuration
type ConfigID string

// EndpointHash is a SHA256 hash of the endpoint URL (for circuit breaker keys)
type EndpointHash string

// Webhook represents a webhook to be delivered
type Webhook struct {
	ID             WebhookID         `json:"id"`
	ConfigID       ConfigID          `json:"config_id"`
	Endpoint       string            `json:"endpoint"`
	EndpointHash   EndpointHash      `json:"endpoint_hash"`
	Payload        json.RawMessage   `json:"payload"`
	Headers        map[string]string `json:"headers,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	SigningSecret  string            `json:"signing_secret,omitempty"`

	// Delivery metadata
	Attempt     int       `json:"attempt"`
	MaxAttempts int       `json:"max_attempts"`
	CreatedAt   time.Time `json:"created_at"`
	ScheduledAt time.Time `json:"scheduled_at"`

	// Script execution
	ScriptHash string `json:"script_hash,omitempty"` // SHA256 of lua_code, indicates transformation expected

	// Late-bound delivery overrides. Both maps hold UNRESOLVED templates of
	// the form ${VAR_NAME}; the worker resolves them against its process
	// environment at HTTP-send time so secrets never enter Kafka. Set by the
	// API-side transformer from its companion .json (_delivery_query_params /
	// _delivery_headers) and propagated through retry/state-snapshot paths.
	DeliveryQueryParams map[string]string `json:"_delivery_query_params,omitempty"`
	DeliveryHeaders     map[string]string `json:"_delivery_headers,omitempty"`

	// Per-delivery retry-classifier override. Populated by a Lua script via
	// request.retry_on_status when credentials are dynamic (e.g. OAuth tokens)
	// and a 4xx may resolve on retry with a fresh fetch. Merged on top of
	// domain.IsRetryable() — it does NOT replace the default classifier.
	RetryOnStatus []int `json:"retry_on_status,omitempty"`
}

// DeliveryResult represents the outcome of a delivery attempt
type DeliveryResult struct {
	WebhookID    WebhookID     `json:"webhook_id"`
	ConfigID     ConfigID      `json:"config_id"`
	Endpoint     string        `json:"endpoint"`
	EndpointHash EndpointHash  `json:"endpoint_hash"`
	Attempt      int           `json:"attempt"`
	Success      bool          `json:"success"`
	StatusCode   int           `json:"status_code,omitempty"`
	Error        string        `json:"error,omitempty"`
	Duration     time.Duration `json:"duration_ns"`
	Timestamp    time.Time     `json:"timestamp"`

	// For retry scheduling
	ShouldRetry bool      `json:"should_retry"`
	NextRetryAt time.Time `json:"next_retry_at,omitempty"`

	// Original webhook for retry
	Webhook *Webhook `json:"webhook,omitempty"`
}

// WebhookState represents the delivery lifecycle state of a webhook.
// It mirrors the CircuitState pattern for type-safe state comparisons.
type WebhookState string

// WebhookStatus represents the current state of a webhook
type WebhookStatus struct {
	WebhookID      WebhookID    `json:"webhook_id"`
	State          WebhookState `json:"state"`
	Attempts       int          `json:"attempts"`
	LastAttemptAt  *time.Time   `json:"last_attempt_at,omitempty"`
	LastStatusCode int          `json:"last_status_code,omitempty"`
	LastError      string       `json:"last_error,omitempty"`
	NextRetryAt    *time.Time   `json:"next_retry_at,omitempty"`
	DeliveredAt    *time.Time   `json:"delivered_at,omitempty"`
	UpdatedAtNs    int64        `json:"updated_at_ns"` // Nanosecond timestamp for LWW conflict resolution
}

// WebhookState constants representing the delivery lifecycle.
const (
	StatePending    WebhookState = "pending"
	StateDelivering WebhookState = "delivering"
	StateDelivered  WebhookState = "delivered"
	StateFailed     WebhookState = "failed"
	StateExhausted  WebhookState = "exhausted"
)

// CircuitState represents the circuit breaker state
type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half_open"
)

// CircuitBreaker represents the circuit breaker state for an endpoint
type CircuitBreaker struct {
	EndpointHash   EndpointHash `json:"endpoint_hash"`
	State          CircuitState `json:"state"`
	Failures       int          `json:"failures"`
	Successes      int          `json:"successes"` // for half-open state
	LastFailureAt  *time.Time   `json:"last_failure_at,omitempty"`
	LastSuccessAt  *time.Time   `json:"last_success_at,omitempty"`
	StateChangedAt time.Time    `json:"state_changed_at"`
	NextProbeAt    *time.Time   `json:"next_probe_at,omitempty"`
}

// Stats represents aggregated statistics
type Stats struct {
	Period          string `json:"period"`
	Enqueued        int64  `json:"enqueued"`
	Delivered       int64  `json:"delivered"`
	Failed          int64  `json:"failed"`
	Exhausted       int64  `json:"exhausted"`
	UniqueEndpoints int64  `json:"unique_endpoints"`
}

// HashEndpoint creates a consistent hash for an endpoint URL
func HashEndpoint(endpoint string) EndpointHash {
	h := sha256.Sum256([]byte(endpoint))
	return EndpointHash(hex.EncodeToString(h[:16])) // 32 chars
}

// MatchesDomain reports whether hostname matches pattern using the same rules
// applied by both the HTTP module allowlist and the transformer domain list:
//
//   - "*"            — matches any hostname (global wildcard)
//   - "*.example.com" — matches any subdomain of example.com, case-insensitive
//   - "api.example.com" — exact match, case-insensitive
//
// This is the single canonical implementation; callers in internal/script/modules
// and internal/transformer delegate here to keep the matching behaviour identical.
func MatchesDomain(hostname, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		// Remove the leading "*", keep the dot so we match ".example.com".
		suffix := pattern[1:]
		return strings.EqualFold(hostname, pattern[2:]) ||
			strings.HasSuffix(strings.ToLower(hostname), strings.ToLower(suffix))
	}
	return strings.EqualFold(hostname, pattern)
}

// defaultMaxAttempts is the retry budget applied when callers pass MaxAttempts <= 0.
const defaultMaxAttempts = 20

// NewWebhookOpts carries the caller-supplied fields for NewWebhook.
// Zero values produce sensible defaults: MaxAttempts → 20, Attempt → 0,
// CreatedAt/ScheduledAt → time.Now().
type NewWebhookOpts struct {
	ConfigID            ConfigID
	Endpoint            string
	Payload             json.RawMessage
	Headers             map[string]string
	IdempotencyKey      string
	SigningSecret       string
	ScriptHash          string
	MaxAttempts         int
	DeliveryQueryParams map[string]string
	DeliveryHeaders     map[string]string
}

// NewWebhook constructs a Webhook with a fresh ULID identifier, a consistent
// EndpointHash, and delivery defaults (Attempt=0, MaxAttempts=20).  It is the
// single canonical place for Webhook construction so that field population
// remains consistent across the API server and the transformer engine.
func NewWebhook(opts NewWebhookOpts) *Webhook {
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}

	now := time.Now()
	return &Webhook{
		ID:                  WebhookID("wh_" + ulid.Make().String()),
		ConfigID:            opts.ConfigID,
		Endpoint:            opts.Endpoint,
		EndpointHash:        HashEndpoint(opts.Endpoint),
		Payload:             opts.Payload,
		Headers:             opts.Headers,
		IdempotencyKey:      opts.IdempotencyKey,
		SigningSecret:       opts.SigningSecret,
		ScriptHash:          opts.ScriptHash,
		DeliveryQueryParams: opts.DeliveryQueryParams,
		DeliveryHeaders:     opts.DeliveryHeaders,
		Attempt:             0,
		MaxAttempts:         maxAttempts,
		CreatedAt:           now,
		ScheduledAt:         now,
	}
}

// IsRetryable returns whether a status code indicates a retryable failure
func IsRetryable(statusCode int) bool {
	// Retry on server errors and specific client errors
	switch {
	case statusCode >= 500:
		return true // Server errors
	case statusCode == 408: // Request Timeout
		return true
	case statusCode == 429: // Too Many Requests
		return true
	default:
		return false
	}
}

// IsSuccess returns whether a status code indicates success
func IsSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}

// DeadLetterReason indicates why a webhook was sent to DLQ
type DeadLetterReason string

const (
	// DLQReasonExhausted indicates the webhook exhausted all retry attempts
	DLQReasonExhausted DeadLetterReason = "exhausted"

	// DLQReasonUnrecoverable indicates an unrecoverable error (e.g., 4xx client error)
	DLQReasonUnrecoverable DeadLetterReason = "unrecoverable"

	// Script-related DLQ reasons
	DLQReasonScriptDisabled      DeadLetterReason = "script_disabled"       // Script execution disabled but ScriptHash set
	DLQReasonScriptNotFound      DeadLetterReason = "script_not_found"      // Script missing after sync timeout
	DLQReasonScriptSyntaxError   DeadLetterReason = "script_syntax_error"   // Lua syntax error
	DLQReasonScriptRuntimeError  DeadLetterReason = "script_runtime_error"  // Lua runtime error/panic
	DLQReasonScriptTimeout       DeadLetterReason = "script_timeout"        // Script exceeded CPU timeout
	DLQReasonScriptMemoryLimit   DeadLetterReason = "script_memory_limit"   // Script exceeded memory limit
	DLQReasonCryptoKeyNotFound   DeadLetterReason = "crypto_key_not_found"  // Crypto key not configured
	DLQReasonScriptInvalidOutput DeadLetterReason = "script_invalid_output" // Script produced invalid output
)

// DeadLetter represents a webhook that has been sent to the dead letter queue
type DeadLetter struct {
	Webhook      *Webhook         `json:"webhook"`
	Reason       string           `json:"reason"`        // Human-readable reason
	ReasonType   DeadLetterReason `json:"reason_type"`   // Machine-readable type
	LastError    string           `json:"last_error,omitempty"`
	StatusCode   int              `json:"status_code,omitempty"`
	ResponseBody string           `json:"response_body,omitempty"` // First 1KB of endpoint response — useful to diagnose 4xx
	Time         time.Time        `json:"time"`
}

// DefaultSensitiveHeaders is the built-in list of header names whose values are
// redacted before a webhook is persisted to the DLQ. Operators can override the
// list via DLQConfig.RedactHeaders; this slice is the fallback when no override
// is configured. Names are matched case-insensitively.
var DefaultSensitiveHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Cookie",
	"Set-Cookie",
	"X-API-Key",
	"X-Auth-Token",
}

// RedactHeaders returns a shallow clone of headers with values of any header
// whose name appears in redactNames replaced by "[REDACTED]". Matching is
// case-insensitive. A nil/empty redactNames means "no redaction" — the headers
// are still cloned (so the caller's map is independent), but no values change.
// Returns nil when headers is nil.
func RedactHeaders(headers map[string]string, redactNames []string) map[string]string {
	if headers == nil {
		return nil
	}
	out := make(map[string]string, len(headers))
	if len(redactNames) == 0 {
		for k, v := range headers {
			out[k] = v
		}
		return out
	}
	redactSet := make(map[string]struct{}, len(redactNames))
	for _, n := range redactNames {
		redactSet[strings.ToLower(n)] = struct{}{}
	}
	for k, v := range headers {
		if _, ok := redactSet[strings.ToLower(k)]; ok {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = v
	}
	return out
}

// CloneForDLQ returns a shallow copy of the webhook with headers passed through
// RedactHeaders so it is safe to publish to the dead letter topic. The original
// webhook is left untouched. Pass nil/empty redactNames to skip redaction (the
// clone is still independent of the original).
func (w *Webhook) CloneForDLQ(redactNames []string) *Webhook {
	if w == nil {
		return nil
	}
	clone := *w
	clone.Headers = RedactHeaders(w.Headers, redactNames)
	return &clone
}

// SystemEpoch tracks when Redis state was last reconciled
// Used to detect potential Redis data loss on startup
type SystemEpoch struct {
	Epoch            int64     `json:"epoch"`
	ReconcilerHost   string    `json:"reconciler_host"`
	MessagesReplayed int64     `json:"messages_replayed"`
	CompletedAt      time.Time `json:"completed_at"`
}

// WebhookStateSnapshot represents the full state of an active webhook
// stored in the compacted topic (howk.state). This enables "zero maintenance"
// reconciliation - Redis can be rebuilt entirely from Kafka on startup.
type WebhookStateSnapshot struct {
	// Identity
	WebhookID    WebhookID    `json:"webhook_id"`
	ConfigID     ConfigID     `json:"config_id"`
	Endpoint     string       `json:"endpoint"`
	EndpointHash EndpointHash `json:"endpoint_hash"`

	// Request Data (Required to reconstruct the call)
	Payload             json.RawMessage   `json:"payload"`
	Headers             map[string]string `json:"headers,omitempty"`
	IdempotencyKey      string            `json:"idempotency_key,omitempty"`
	SigningSecret       string            `json:"signing_secret,omitempty"`
	ScriptHash          string            `json:"script_hash,omitempty"`
	DeliveryQueryParams map[string]string `json:"_delivery_query_params,omitempty"`
	DeliveryHeaders     map[string]string `json:"_delivery_headers,omitempty"`
	RetryOnStatus       []int             `json:"retry_on_status,omitempty"`

	// State Info
	State       WebhookState `json:"state"`
	Attempt     int          `json:"attempt"`
	MaxAttempts int       `json:"max_attempts"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAtNs int64     `json:"updated_at_ns"` // Nanosecond timestamp for LWW conflict resolution

	// Scheduling
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}
