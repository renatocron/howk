package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
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

// WebhookStatus represents the current state of a webhook
type WebhookStatus struct {
	WebhookID      WebhookID  `json:"webhook_id"`
	State          string     `json:"state"` // pending, delivering, delivered, failed, exhausted
	Attempts       int        `json:"attempts"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastStatusCode int        `json:"last_status_code,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
}

// State constants
const (
	StatePending    = "pending"
	StateDelivering = "delivering"
	StateDelivered  = "delivered"
	StateFailed     = "failed"
	StateExhausted  = "exhausted"
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
	Time         time.Time        `json:"time"`
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
	Payload        json.RawMessage   `json:"payload"`
	Headers        map[string]string `json:"headers,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	SigningSecret  string            `json:"signing_secret,omitempty"`
	ScriptHash     string            `json:"script_hash,omitempty"`

	// State Info
	State       string    `json:"state"`
	Attempt     int       `json:"attempt"`
	MaxAttempts int       `json:"max_attempts"`
	CreatedAt   time.Time `json:"created_at"`

	// Scheduling
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}
