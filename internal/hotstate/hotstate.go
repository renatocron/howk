package hotstate

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/howk/howk/internal/domain"
)

// HotState abstracts the hot state store (Redis)
type HotState interface {
	// Status operations
	SetStatus(ctx context.Context, status *domain.WebhookStatus) error
	GetStatus(ctx context.Context, webhookID domain.WebhookID) (*domain.WebhookStatus, error)

	// Retry scheduling (Reference-Based with Visibility Timeout)
	// EnsureRetryData ensures the webhook data exists in Redis.
	// If it exists, it only refreshes the TTL (efficient - no compression/payload transfer).
	// If it does not exist, it compresses and stores it.
	EnsureRetryData(ctx context.Context, webhook *domain.Webhook, ttl time.Duration) error
	// ScheduleRetry schedules the reference in ZSET
	ScheduleRetry(ctx context.Context, webhookID domain.WebhookID, attempt int, scheduledAt time.Time, reason string) error
	// PopAndLockRetries atomically pops due retries and pushes their score to the future (visibility timeout)
	// Returns list of references (webhook_id:attempt)
	PopAndLockRetries(ctx context.Context, limit int, lockDuration time.Duration) ([]string, error)
	// GetRetryData retrieves and decompresses webhook data by webhookID
	GetRetryData(ctx context.Context, webhookID domain.WebhookID) (*domain.Webhook, error)
	// AckRetry confirms processing and deletes reference from ZSET + metadata (NOT data)
	AckRetry(ctx context.Context, reference string) error
	// DeleteRetryData deletes the compressed webhook data (called on terminal state)
	DeleteRetryData(ctx context.Context, webhookID domain.WebhookID) error

	// Circuit breaker operations
	// CircuitBreaker returns the circuit breaker checker for this hot state
	CircuitBreaker() CircuitBreakerChecker

	// Stats
	IncrStats(ctx context.Context, bucket string, counters map[string]int64) error
	AddToHLL(ctx context.Context, key string, values ...string) error
	GetStats(ctx context.Context, from, to time.Time) (*domain.Stats, error)

	// Idempotency
	CheckAndSetProcessed(ctx context.Context, webhookID domain.WebhookID, attempt int, ttl time.Duration) (bool, error)

	// Script operations
	GetScript(ctx context.Context, configID domain.ConfigID) (string, error) // Returns JSON-encoded ScriptConfig
	SetScript(ctx context.Context, configID domain.ConfigID, scriptJSON string, ttl time.Duration) error
	DeleteScript(ctx context.Context, configID domain.ConfigID) error

	// Health & Admin
	Ping(ctx context.Context) error
	FlushForRebuild(ctx context.Context) error
	Close() error

	// Redis client access for advanced operations
	Client() *redis.Client

	// IncrInflight atomically increments the in-flight counter for an endpoint.
	// Returns the new count after increment.
	// The key has a TTL to auto-expire if workers crash (leak protection).
	IncrInflight(ctx context.Context, endpointHash domain.EndpointHash, ttl time.Duration) (int64, error)

	// DecrInflight atomically decrements the in-flight counter for an endpoint.
	// Uses a Lua script to ensure the counter never goes below zero.
	DecrInflight(ctx context.Context, endpointHash domain.EndpointHash) error

	// SystemEpoch operations
	// GetEpoch retrieves the system epoch marker from Redis
	GetEpoch(ctx context.Context) (*domain.SystemEpoch, error)
	// SetEpoch sets the system epoch marker in Redis
	SetEpoch(ctx context.Context, epoch *domain.SystemEpoch) error
	// GetRetryQueueSize returns the current size of the retry queue
	GetRetryQueueSize(ctx context.Context) (int64, error)

	// --- Zero Maintenance: Auto-Recovery (Sentinel Pattern) ---

	// CheckCanary checks if the system canary key exists (indicates Redis is initialized)
	CheckCanary(ctx context.Context) (bool, error)

	// SetCanary sets the system canary key (mark Redis as initialized)
	SetCanary(ctx context.Context) error

	// WaitForCanary polls until the canary key appears or timeout
	WaitForCanary(ctx context.Context, timeout time.Duration) bool

	// AcquireReconcilerLock attempts to acquire a distributed lock for reconciliation.
	// Returns true if lock acquired, and an unlock function to release it.
	// The lock has a TTL and is automatically extended via heartbeat until unlock.
	AcquireReconcilerLock(ctx context.Context, ttl time.Duration) (bool, func())

	// DelCanary removes the canary key (used during FlushForRebuild)
	DelCanary(ctx context.Context) error
}

// CircuitBreakerChecker provides circuit breaker functionality
type CircuitBreakerChecker interface {
	// Get retrieves the current circuit state for an endpoint
	Get(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)

	// ShouldAllow checks if a request should be allowed through
	// Returns: allowed, isProbe, error
	ShouldAllow(ctx context.Context, endpointHash domain.EndpointHash) (bool, bool, error)

	// RecordSuccess records a successful delivery
	RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)

	// RecordFailure records a failed delivery
	RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)
}
