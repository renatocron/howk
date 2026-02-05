//go:build !integration

package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
)

// MockHotState is a mock implementation of hotstate.HotState for testing
type MockHotState struct {
	mock.Mock
}

func (m *MockHotState) SetStatus(ctx context.Context, status *domain.WebhookStatus) error {
	return m.Called(ctx, status).Error(0)
}

func (m *MockHotState) GetStatus(ctx context.Context, webhookID domain.WebhookID) (*domain.WebhookStatus, error) {
	args := m.Called(ctx, webhookID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.WebhookStatus), args.Error(1)
}

func (m *MockHotState) EnsureRetryData(ctx context.Context, webhook *domain.Webhook, ttl time.Duration) error {
	return m.Called(ctx, webhook, ttl).Error(0)
}

func (m *MockHotState) ScheduleRetry(ctx context.Context, webhookID domain.WebhookID, attempt int, scheduledAt time.Time, reason string) error {
	return m.Called(ctx, webhookID, attempt, scheduledAt, reason).Error(0)
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
	return m.Called(ctx, reference).Error(0)
}

func (m *MockHotState) DeleteRetryData(ctx context.Context, webhookID domain.WebhookID) error {
	return m.Called(ctx, webhookID).Error(0)
}

func (m *MockHotState) CircuitBreaker() hotstate.CircuitBreakerChecker {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(hotstate.CircuitBreakerChecker)
}

func (m *MockHotState) IncrStats(ctx context.Context, bucket string, counters map[string]int64) error {
	return m.Called(ctx, bucket, counters).Error(0)
}

func (m *MockHotState) AddToHLL(ctx context.Context, key string, values ...string) error {
	return m.Called(ctx, key, values).Error(0)
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

func (m *MockHotState) GetScript(ctx context.Context, configID domain.ConfigID) (string, error) {
	args := m.Called(ctx, configID)
	return args.String(0), args.Error(1)
}

func (m *MockHotState) SetScript(ctx context.Context, configID domain.ConfigID, scriptJSON string, ttl time.Duration) error {
	return m.Called(ctx, configID, scriptJSON, ttl).Error(0)
}

func (m *MockHotState) DeleteScript(ctx context.Context, configID domain.ConfigID) error {
	return m.Called(ctx, configID).Error(0)
}

func (m *MockHotState) Ping(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *MockHotState) FlushForRebuild(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *MockHotState) Close() error {
	return m.Called().Error(0)
}

func (m *MockHotState) Client() *redis.Client {
	return m.Called().Get(0).(*redis.Client)
}

func (m *MockHotState) IncrInflight(ctx context.Context, endpointHash domain.EndpointHash, ttl time.Duration) (int64, error) {
	args := m.Called(ctx, endpointHash, ttl)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockHotState) DecrInflight(ctx context.Context, endpointHash domain.EndpointHash) error {
	return m.Called(ctx, endpointHash).Error(0)
}

func (m *MockHotState) GetEpoch(ctx context.Context) (*domain.SystemEpoch, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.SystemEpoch), args.Error(1)
}

func (m *MockHotState) SetEpoch(ctx context.Context, epoch *domain.SystemEpoch) error {
	return m.Called(ctx, epoch).Error(0)
}

func (m *MockHotState) GetRetryQueueSize(ctx context.Context) (int64, error) {
	args := m.Called(ctx)
	return args.Get(0).(int64), args.Error(1)
}

// MockCircuitBreakerChecker is a mock implementation of hotstate.CircuitBreakerChecker
type MockCircuitBreakerChecker struct {
	mock.Mock
}

func (m *MockCircuitBreakerChecker) Get(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
}

func (m *MockCircuitBreakerChecker) ShouldAllow(ctx context.Context, endpointHash domain.EndpointHash) (bool, bool, error) {
	args := m.Called(ctx, endpointHash)
	return args.Bool(0), args.Bool(1), args.Error(2)
}

func (m *MockCircuitBreakerChecker) RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
}

func (m *MockCircuitBreakerChecker) RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
}

func TestNewReconciler(t *testing.T) {
	mockHS := new(MockHotState)
	ttlCfg := config.TTLConfig{RetryDataTTL: time.Hour}
	cfg := config.KafkaConfig{
		Brokers: []string{"localhost:9092"},
		Topics: config.TopicsConfig{
			State: "howk.state",
		},
	}

	r := NewReconciler(cfg, mockHS, ttlCfg)

	assert.NotNil(t, r)
	assert.Equal(t, cfg, r.config)
	assert.Equal(t, mockHS, r.hotstate)
	assert.Equal(t, ttlCfg, r.ttlConfig)
}

func TestReconciler_restoreState_FailedWithRetry(t *testing.T) {
	mockHS := new(MockHotState)
	ttlCfg := config.TTLConfig{RetryDataTTL: time.Hour}
	r := &Reconciler{
		hotstate:  mockHS,
		ttlConfig: ttlCfg,
	}

	now := time.Now()
	nextRetry := now.Add(time.Minute)
	webhookID := domain.WebhookID(ulid.Make().String())

	snapshot := &domain.WebhookStateSnapshot{
		WebhookID:    webhookID,
		ConfigID:     "config-123",
		Endpoint:     "https://example.com/webhook",
		EndpointHash: "abc123",
		Payload:      json.RawMessage(`{"key":"value"}`),
		Headers:      map[string]string{"Content-Type": "application/json"},
		State:        domain.StateFailed,
		Attempt:      2,
		MaxAttempts:  10,
		CreatedAt:    now,
		NextRetryAt:  &nextRetry,
		LastError:    "connection timeout",
	}

	// Expect SetStatus to be called
	mockHS.On("SetStatus", mock.Anything, mock.MatchedBy(func(s *domain.WebhookStatus) bool {
		return s.WebhookID == webhookID &&
			s.State == domain.StateFailed &&
			s.Attempts == 3 && // 1-based
			s.LastError == "connection timeout"
	})).Return(nil)

	// Expect EnsureRetryData to be called with configured TTL
	mockHS.On("EnsureRetryData", mock.Anything, mock.Anything, time.Hour).Return(nil)

	// Expect ScheduleRetry to be called
	mockHS.On("ScheduleRetry", mock.Anything, webhookID, 2, nextRetry, "reconciled").Return(nil)

	err := r.restoreState(context.Background(), snapshot)
	assert.NoError(t, err)
	mockHS.AssertExpectations(t)
}

func TestReconciler_restoreState_Delivered(t *testing.T) {
	mockHS := new(MockHotState)
	ttlCfg := config.TTLConfig{RetryDataTTL: time.Hour}
	r := &Reconciler{
		hotstate:  mockHS,
		ttlConfig: ttlCfg,
	}

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())

	snapshot := &domain.WebhookStateSnapshot{
		WebhookID:    webhookID,
		ConfigID:     "config-123",
		Endpoint:     "https://example.com/webhook",
		EndpointHash: "abc123",
		Payload:      json.RawMessage(`{"key":"value"}`),
		State:        domain.StateDelivered,
		Attempt:      1,
		MaxAttempts:  10,
		CreatedAt:    now,
	}

	// Expect only SetStatus to be called (no retry scheduling for terminal states)
	mockHS.On("SetStatus", mock.Anything, mock.MatchedBy(func(s *domain.WebhookStatus) bool {
		return s.WebhookID == webhookID && s.State == domain.StateDelivered
	})).Return(nil)

	err := r.restoreState(context.Background(), snapshot)
	assert.NoError(t, err)
	mockHS.AssertExpectations(t)
}

func TestReconciler_restoreState_SetStatusError(t *testing.T) {
	mockHS := new(MockHotState)
	ttlCfg := config.TTLConfig{RetryDataTTL: time.Hour}
	r := &Reconciler{
		hotstate:  mockHS,
		ttlConfig: ttlCfg,
	}

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())

	snapshot := &domain.WebhookStateSnapshot{
		WebhookID:   webhookID,
		State:       domain.StateFailed,
		Attempt:     1,
		MaxAttempts: 10,
		CreatedAt:   now,
		NextRetryAt: &now,
	}

	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(errors.New("redis error"))

	err := r.restoreState(context.Background(), snapshot)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redis error")
}

func TestReconciler_restoreState_EnsureRetryDataError(t *testing.T) {
	mockHS := new(MockHotState)
	ttlCfg := config.TTLConfig{RetryDataTTL: time.Hour}
	r := &Reconciler{
		hotstate:  mockHS,
		ttlConfig: ttlCfg,
	}

	now := time.Now()
	nextRetry := now.Add(time.Minute)
	webhookID := domain.WebhookID(ulid.Make().String())

	snapshot := &domain.WebhookStateSnapshot{
		WebhookID:   webhookID,
		State:       domain.StateFailed,
		Attempt:     1,
		MaxAttempts: 10,
		CreatedAt:   now,
		NextRetryAt: &nextRetry,
	}

	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("EnsureRetryData", mock.Anything, mock.Anything, time.Hour).Return(errors.New("redis error"))

	err := r.restoreState(context.Background(), snapshot)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redis error")
}

func TestReconciler_restoreState_ScheduleRetryError(t *testing.T) {
	mockHS := new(MockHotState)
	ttlCfg := config.TTLConfig{RetryDataTTL: time.Hour}
	r := &Reconciler{
		hotstate:  mockHS,
		ttlConfig: ttlCfg,
	}

	now := time.Now()
	nextRetry := now.Add(time.Minute)
	webhookID := domain.WebhookID(ulid.Make().String())

	snapshot := &domain.WebhookStateSnapshot{
		WebhookID:   webhookID,
		State:       domain.StateFailed,
		Attempt:     1,
		MaxAttempts: 10,
		CreatedAt:   now,
		NextRetryAt: &nextRetry,
	}

	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("EnsureRetryData", mock.Anything, mock.Anything, time.Hour).Return(nil)
	mockHS.On("ScheduleRetry", mock.Anything, webhookID, 1, nextRetry, "reconciled").Return(errors.New("schedule error"))

	err := r.restoreState(context.Background(), snapshot)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schedule error")
}

func TestWebhookStateSnapshot_JSONSerialization(t *testing.T) {
	now := time.Now()
	nextRetry := now.Add(time.Minute)
	webhookID := domain.WebhookID(ulid.Make().String())

	snapshot := domain.WebhookStateSnapshot{
		WebhookID:      webhookID,
		ConfigID:       "config-123",
		Endpoint:       "https://example.com/webhook",
		EndpointHash:   "abc123",
		Payload:        json.RawMessage(`{"key":"value"}`),
		Headers:        map[string]string{"Content-Type": "application/json"},
		IdempotencyKey: "idem-key-123",
		SigningSecret:  "secret-123",
		ScriptHash:     "hash-123",
		State:          domain.StateFailed,
		Attempt:        3,
		MaxAttempts:    10,
		CreatedAt:      now,
		NextRetryAt:    &nextRetry,
		LastError:      "connection timeout",
	}

	// Test JSON marshaling/unmarshaling
	data, err := json.Marshal(snapshot)
	assert.NoError(t, err)

	var unmarshaled domain.WebhookStateSnapshot
	err = json.Unmarshal(data, &unmarshaled)
	assert.NoError(t, err)

	assert.Equal(t, snapshot.WebhookID, unmarshaled.WebhookID)
	assert.Equal(t, snapshot.ConfigID, unmarshaled.ConfigID)
	assert.Equal(t, snapshot.Endpoint, unmarshaled.Endpoint)
	assert.Equal(t, snapshot.EndpointHash, unmarshaled.EndpointHash)
	assert.Equal(t, snapshot.Payload, unmarshaled.Payload)
	assert.Equal(t, snapshot.Headers, unmarshaled.Headers)
	assert.Equal(t, snapshot.IdempotencyKey, unmarshaled.IdempotencyKey)
	assert.Equal(t, snapshot.SigningSecret, unmarshaled.SigningSecret)
	assert.Equal(t, snapshot.ScriptHash, unmarshaled.ScriptHash)
	assert.Equal(t, snapshot.State, unmarshaled.State)
	assert.Equal(t, snapshot.Attempt, unmarshaled.Attempt)
	assert.Equal(t, snapshot.MaxAttempts, unmarshaled.MaxAttempts)
	assert.WithinDuration(t, snapshot.CreatedAt, unmarshaled.CreatedAt, time.Millisecond)
	assert.NotNil(t, unmarshaled.NextRetryAt)
	assert.WithinDuration(t, *snapshot.NextRetryAt, *unmarshaled.NextRetryAt, time.Millisecond)
	assert.Equal(t, snapshot.LastError, unmarshaled.LastError)
}

func TestWebhookStateSnapshot_Tombstone(t *testing.T) {
	// Test that nil value represents a tombstone
	var nilSnapshot *domain.WebhookStateSnapshot
	assert.Nil(t, nilSnapshot)

	// Test unmarshaling nil value (empty bytes)
	var snapshot domain.WebhookStateSnapshot
	err := json.Unmarshal([]byte("null"), &snapshot)
	// "null" JSON results in zero values
	assert.NoError(t, err)
	assert.Empty(t, snapshot.WebhookID)
}
