package hotstate

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/howk/howk/internal/domain"
)

// RetryMessage wraps a webhook for retry scheduling
type RetryMessage struct {
	Webhook     *domain.Webhook `json:"webhook"`
	ScheduledAt time.Time       `json:"scheduled_at"`
	Reason      string          `json:"reason,omitempty"`
}

// HotState abstracts the hot state store (Redis)
type HotState interface {
	// Status operations
	SetStatus(ctx context.Context, status *domain.WebhookStatus) error
	GetStatus(ctx context.Context, webhookID domain.WebhookID) (*domain.WebhookStatus, error)

	// Retry scheduling
	ScheduleRetry(ctx context.Context, msg *RetryMessage) error
	PopDueRetries(ctx context.Context, limit int) ([]*RetryMessage, error)

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
