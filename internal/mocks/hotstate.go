package mocks

import (
	"context"
	"time"

	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/mock"
)

// MockHotState implements hotstate.HotState interface for testing
type MockHotState struct {
	mock.Mock
}

func (m *MockHotState) SetStatus(ctx context.Context, status *domain.WebhookStatus) error {
	args := m.Called(ctx, status)
	return args.Error(0)
}

func (m *MockHotState) GetStatus(ctx context.Context, webhookID domain.WebhookID) (*domain.WebhookStatus, error) {
	args := m.Called(ctx, webhookID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.WebhookStatus), args.Error(1)
}

func (m *MockHotState) EnsureRetryData(ctx context.Context, webhook *domain.Webhook, ttl time.Duration) error {
	args := m.Called(ctx, webhook, ttl)
	return args.Error(0)
}

func (m *MockHotState) ScheduleRetry(ctx context.Context, webhookID domain.WebhookID, attempt int, scheduledAt time.Time, reason string) error {
	args := m.Called(ctx, webhookID, attempt, scheduledAt, reason)
	return args.Error(0)
}

func (m *MockHotState) PopAndLockRetries(ctx context.Context, limit int, lockDuration time.Duration) ([]string, error) {
	args := m.Called(ctx, limit, lockDuration)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}

func (m *MockHotState) GetRetryData(ctx context.Context, webhookID domain.WebhookID) (*domain.Webhook, error) {
	args := m.Called(ctx, webhookID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Webhook), args.Error(1)
}

func (m *MockHotState) AckRetry(ctx context.Context, reference string) error {
	args := m.Called(ctx, reference)
	return args.Error(0)
}

func (m *MockHotState) DeleteRetryData(ctx context.Context, webhookID domain.WebhookID) error {
	args := m.Called(ctx, webhookID)
	return args.Error(0)
}

func (m *MockHotState) CircuitBreaker() hotstate.CircuitBreakerChecker {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(hotstate.CircuitBreakerChecker)
}

func (m *MockHotState) IncrStats(ctx context.Context, bucket string, counters map[string]int64) error {
	args := m.Called(ctx, bucket, counters)
	return args.Error(0)
}

func (m *MockHotState) AddToHLL(ctx context.Context, key string, values ...string) error {
	args := m.Called(ctx, key, values)
	return args.Error(0)
}

func (m *MockHotState) GetStats(ctx context.Context, from, to time.Time) (*domain.Stats, error) {
	args := m.Called(ctx, from, to)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Stats), args.Error(1)
}

func (m *MockHotState) CheckAndSetProcessed(ctx context.Context, webhookID domain.WebhookID, attempt int, ttl time.Duration) (bool, error) {
	args := m.Called(ctx, webhookID, attempt, ttl)
	return args.Bool(0), args.Error(1)
}

func (m *MockHotState) Ping(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockHotState) FlushForRebuild(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockHotState) Close() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockHotState) Client() *redis.Client {
	args := m.Called()
	if client, ok := args.Get(0).(*redis.Client); ok {
		return client
	}
	return nil
}

func (m *MockHotState) GetScript(ctx context.Context, configID domain.ConfigID) (string, error) {
	args := m.Called(ctx, configID)
	return args.String(0), args.Error(1)
}

func (m *MockHotState) SetScript(ctx context.Context, configID domain.ConfigID, scriptJSON string, ttl time.Duration) error {
	args := m.Called(ctx, configID, scriptJSON, ttl)
	return args.Error(0)
}

func (m *MockHotState) DeleteScript(ctx context.Context, configID domain.ConfigID) error {
	args := m.Called(ctx, configID)
	return args.Error(0)
}

// Compile-time assertion - importing hotstate would cause import cycle
// The mock is verified by usage in tests
