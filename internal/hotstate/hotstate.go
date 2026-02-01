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
	// StoreRetryData stores the compressed webhook data (one per webhook, refreshed TTL on each call)
	StoreRetryData(ctx context.Context, webhook *domain.Webhook, ttl time.Duration) error
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

	// Circuit breaker
	GetCircuit(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)
	UpdateCircuit(ctx context.Context, cb *domain.CircuitBreaker) error
	RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)
	RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)

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
}

// CircuitBreakerChecker is a subset of HotState for circuit checking
type CircuitBreakerChecker interface {
	// ShouldAllow checks if a request should be allowed through
	// Returns: allowed, isProbe, error
	ShouldAllow(ctx context.Context, endpointHash domain.EndpointHash) (bool, bool, error)

	// RecordSuccess records a successful delivery
	RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)

	// RecordFailure records a failed delivery
	RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error)
}
