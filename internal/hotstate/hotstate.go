package hotstate

import (
	"context"
	"time"

	"github.com/howk/howk/internal/domain"
)

// StatusStore handles webhook status persistence.
type StatusStore interface {
	SetStatus(ctx context.Context, status *domain.WebhookStatus) error
	GetStatus(ctx context.Context, webhookID domain.WebhookID) (*domain.WebhookStatus, error)
}

// RetryStore handles retry scheduling and data management.
//
// Design: retries use a reference-based pattern with visibility timeouts.
// Webhook data is stored compressed once; references (webhook_id:attempt) are
// stored in a ZSET scored by scheduled time. PopAndLockRetries bumps the score
// to now+lockDuration to prevent duplicate processing without deleting the entry.
type RetryStore interface {
	// EnsureRetryData stores compressed webhook data if absent, or refreshes its
	// TTL if present (optimistic: subsequent retries skip re-compression).
	EnsureRetryData(ctx context.Context, webhook *domain.Webhook, ttl time.Duration) error

	ScheduleRetry(ctx context.Context, webhookID domain.WebhookID, attempt int, scheduledAt time.Time, reason string) error

	// PopAndLockRetries atomically selects due entries and advances their score to
	// now+lockDuration (visibility timeout), preventing duplicate processing.
	// Returns references in "webhook_id:attempt" format.
	PopAndLockRetries(ctx context.Context, limit int, lockDuration time.Duration) ([]string, error)

	GetRetryData(ctx context.Context, webhookID domain.WebhookID) (*domain.Webhook, error)

	// AckRetry removes the reference from the ZSET and its metadata. It does NOT
	// delete the compressed webhook data, which is shared across attempts and
	// removed only on terminal state via DeleteRetryData.
	AckRetry(ctx context.Context, reference string) error

	// DeleteRetryData removes the compressed webhook payload. Call only on terminal
	// states (success or DLQ/exhaustion) to free storage.
	DeleteRetryData(ctx context.Context, webhookID domain.WebhookID) error
}

// CircuitBreakerStore provides access to the circuit breaker subsystem.
type CircuitBreakerStore interface {
	CircuitBreaker() CircuitBreakerChecker
}

// StatsStore handles metrics aggregation and HyperLogLog cardinality estimation.
type StatsStore interface {
	IncrStats(ctx context.Context, bucket string, counters map[string]int64) error
	AddToHLL(ctx context.Context, key string, values ...string) error
	GetStats(ctx context.Context, from, to time.Time) (*domain.Stats, error)
}

// IdempotencyStore handles exactly-once processing markers.
type IdempotencyStore interface {
	CheckAndSetProcessed(ctx context.Context, webhookID domain.WebhookID, attempt int, ttl time.Duration) (bool, error)
}

// InflightStore tracks per-endpoint in-flight request counts (penalty box).
type InflightStore interface {
	// IncrInflight atomically increments the in-flight counter for an endpoint.
	// The TTL auto-expires the key if the worker crashes, preventing counter leaks.
	IncrInflight(ctx context.Context, endpointHash domain.EndpointHash, ttl time.Duration) (int64, error)

	// DecrInflight atomically decrements the in-flight counter using a Lua script
	// that floors the value at zero, preventing drift from edge cases.
	DecrInflight(ctx context.Context, endpointHash domain.EndpointHash) error
}

// EpochStore manages the system epoch marker used to detect data loss.
type EpochStore interface {
	GetEpoch(ctx context.Context) (*domain.SystemEpoch, error)
	SetEpoch(ctx context.Context, epoch *domain.SystemEpoch) error
}

// CanaryStore implements the sentinel pattern for zero-maintenance Redis recovery.
// The canary key signals that Redis is fully initialized; its absence triggers reconciliation.
type CanaryStore interface {
	CheckCanary(ctx context.Context) (bool, error)

	// SetCanary marks Redis as initialized. Absence of this key on startup signals
	// that Redis needs rebuilding from Kafka.
	SetCanary(ctx context.Context) error

	// WaitForCanary polls until the canary key appears or the timeout elapses.
	WaitForCanary(ctx context.Context, timeout time.Duration) bool

	// DelCanary removes the canary key, signalling that a rebuild is in progress.
	DelCanary(ctx context.Context) error

	// AcquireReconcilerLock attempts to acquire a distributed lock for reconciliation.
	// Returns true if acquired, and an unlock function to release it.
	// The lock TTL is extended via heartbeat until unlock is called.
	AcquireReconcilerLock(ctx context.Context, ttl time.Duration) (bool, func())
}

// ScriptStore handles Lua script configuration caching.
// (Mirrors the script.ScriptStore interface defined in the script package to avoid
// an import cycle while keeping the hotstate package self-contained.)
type ScriptStore interface {
	// GetScript returns a JSON-encoded ScriptConfig, or ErrScriptNotFound.
	GetScript(ctx context.Context, configID domain.ConfigID) (string, error)
	SetScript(ctx context.Context, configID domain.ConfigID, scriptJSON string, ttl time.Duration) error
	DeleteScript(ctx context.Context, configID domain.ConfigID) error
}

// AdminOps groups health-check and lifecycle operations.
type AdminOps interface {
	Ping(ctx context.Context) error
	FlushForRebuild(ctx context.Context) error
	GetRetryQueueSize(ctx context.Context) (int64, error)
	Close() error
}

// HotState is the composite hot-state interface used by components that require
// access to the full Redis abstraction. Prefer the focused sub-interfaces above
// when a component only needs a subset of operations.
type HotState interface {
	StatusStore
	RetryStore
	CircuitBreakerStore
	StatsStore
	IdempotencyStore
	InflightStore
	EpochStore
	CanaryStore
	ScriptStore
	AdminOps
}

// CircuitBreakerChecker provides circuit breaker functionality.
type CircuitBreakerChecker interface {
	Get(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)

	// ShouldAllow returns (allowed, isProbe, error). isProbe is true when the circuit
	// is HALF_OPEN and this call is the single permitted probe request.
	ShouldAllow(ctx context.Context, endpointHash domain.EndpointHash) (bool, bool, error)

	RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)
	RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)
}
