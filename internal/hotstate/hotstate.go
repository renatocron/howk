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

	ScheduleRetry(ctx context.Context, webhookID domain.WebhookID, attempt int, scheduledAt time.Time, reason string) error

	// PopAndLockRetries atomically pops due retries and bumps their score to now+lockDuration
	// (visibility timeout), preventing duplicate processing. Returns references (webhook_id:attempt).
	PopAndLockRetries(ctx context.Context, limit int, lockDuration time.Duration) ([]string, error)

	GetRetryData(ctx context.Context, webhookID domain.WebhookID) (*domain.Webhook, error)

	// AckRetry removes the reference from the ZSET and its metadata. It does NOT delete
	// the compressed webhook data, which is shared across attempts and removed only on
	// terminal state via DeleteRetryData.
	AckRetry(ctx context.Context, reference string) error

	// DeleteRetryData removes the compressed webhook payload. Call only on terminal states
	// (success or DLQ/exhaustion) to free storage.
	DeleteRetryData(ctx context.Context, webhookID domain.WebhookID) error

	// Circuit breaker operations
	CircuitBreaker() CircuitBreakerChecker

	// Stats
	IncrStats(ctx context.Context, bucket string, counters map[string]int64) error
	AddToHLL(ctx context.Context, key string, values ...string) error
	GetStats(ctx context.Context, from, to time.Time) (*domain.Stats, error)

	// Idempotency
	CheckAndSetProcessed(ctx context.Context, webhookID domain.WebhookID, attempt int, ttl time.Duration) (bool, error)

	// Script operations. GetScript returns a JSON-encoded ScriptConfig.
	GetScript(ctx context.Context, configID domain.ConfigID) (string, error)
	SetScript(ctx context.Context, configID domain.ConfigID, scriptJSON string, ttl time.Duration) error
	DeleteScript(ctx context.Context, configID domain.ConfigID) error

	// Health & Admin
	Ping(ctx context.Context) error
	FlushForRebuild(ctx context.Context) error
	Close() error

	// Client exposes the underlying Redis client for operations not covered by this interface.
	// Prefer adding methods here over direct client access.
	Client() *redis.Client

	// IncrInflight atomically increments the in-flight counter for an endpoint.
	// The TTL auto-expires the key if the worker crashes, preventing counter leaks.
	IncrInflight(ctx context.Context, endpointHash domain.EndpointHash, ttl time.Duration) (int64, error)

	// DecrInflight atomically decrements the in-flight counter using a Lua script
	// that floors the value at zero, preventing drift from edge cases like duplicate processing.
	DecrInflight(ctx context.Context, endpointHash domain.EndpointHash) error

	// SystemEpoch operations
	GetEpoch(ctx context.Context) (*domain.SystemEpoch, error)
	SetEpoch(ctx context.Context, epoch *domain.SystemEpoch) error
	GetRetryQueueSize(ctx context.Context) (int64, error)

	// --- Zero Maintenance: Auto-Recovery (Sentinel Pattern) ---

	CheckCanary(ctx context.Context) (bool, error)

	// SetCanary marks Redis as initialized. If this key is absent on startup,
	// the reconciler knows Redis needs rebuilding from Kafka.
	SetCanary(ctx context.Context) error

	// WaitForCanary polls until the canary key appears or the timeout elapses.
	// Returns false if the timeout is reached without the key appearing.
	WaitForCanary(ctx context.Context, timeout time.Duration) bool

	// AcquireReconcilerLock attempts to acquire a distributed lock for reconciliation.
	// Returns true if lock acquired, and an unlock function to release it.
	// The lock TTL is extended automatically via heartbeat until unlock is called.
	AcquireReconcilerLock(ctx context.Context, ttl time.Duration) (bool, func())

	// DelCanary removes the canary key. Called by FlushForRebuild to signal that
	// Redis state is being rebuilt and other instances should wait.
	DelCanary(ctx context.Context) error
}

// CircuitBreakerChecker provides circuit breaker functionality
type CircuitBreakerChecker interface {
	Get(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)

	// ShouldAllow returns (allowed, isProbe, error). isProbe is true when the circuit is
	// HALF_OPEN and this call is the single permitted probe request.
	ShouldAllow(ctx context.Context, endpointHash domain.EndpointHash) (bool, bool, error)

	RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)
	RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)
}
