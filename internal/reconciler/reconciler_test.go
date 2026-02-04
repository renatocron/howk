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
	cfg := config.KafkaConfig{
		Brokers: []string{"localhost:9092"},
		Topics: config.TopicsConfig{
			Results: "results",
		},
	}

	r := NewReconciler(cfg, mockHS)

	assert.NotNil(t, r)
	assert.Equal(t, cfg, r.config)
	assert.Equal(t, mockHS, r.hotstate)
}

func TestReconciler_rebuildStatus_Success(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{hotstate: mockHS}

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:    webhookID,
		Success:      true,
		Attempt:      2,
		Timestamp:    now,
		StatusCode:   200,
		EndpointHash: "abc123",
	}

	mockHS.On("SetStatus", mock.Anything, mock.MatchedBy(func(s *domain.WebhookStatus) bool {
		return s.State == domain.StateDelivered &&
			s.Attempts == 3 &&
			s.DeliveredAt != nil &&
			s.LastStatusCode == 200
	})).Return(nil)

	err := r.rebuildStatus(context.Background(), result)
	assert.NoError(t, err)
	mockHS.AssertExpectations(t)
}

func TestReconciler_rebuildStatus_FailedRetryable(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{hotstate: mockHS}

	now := time.Now()
	nextRetry := now.Add(time.Minute)
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:    webhookID,
		Success:      false,
		ShouldRetry:  true,
		Attempt:      1,
		Timestamp:    now,
		NextRetryAt:  nextRetry,
		StatusCode:   500,
		Error:        "server error",
		EndpointHash: "abc123",
	}

	mockHS.On("SetStatus", mock.Anything, mock.MatchedBy(func(s *domain.WebhookStatus) bool {
		return s.State == domain.StateFailed &&
			s.Attempts == 2 &&
			s.NextRetryAt != nil &&
			s.LastStatusCode == 500 &&
			s.LastError == "server error"
	})).Return(nil)

	err := r.rebuildStatus(context.Background(), result)
	assert.NoError(t, err)
	mockHS.AssertExpectations(t)
}

func TestReconciler_rebuildStatus_Exhausted(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{hotstate: mockHS}

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:    webhookID,
		Success:      false,
		ShouldRetry:  false,
		Attempt:      4,
		Timestamp:    now,
		StatusCode:   400,
		Error:        "bad request",
		EndpointHash: "abc123",
	}

	mockHS.On("SetStatus", mock.Anything, mock.MatchedBy(func(s *domain.WebhookStatus) bool {
		return s.State == domain.StateExhausted &&
			s.Attempts == 5 &&
			s.LastStatusCode == 400
	})).Return(nil)

	err := r.rebuildStatus(context.Background(), result)
	assert.NoError(t, err)
	mockHS.AssertExpectations(t)
}

func TestReconciler_rebuildStatus_SetStatusError(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{hotstate: mockHS}

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:    webhookID,
		Success:      true,
		Attempt:      0,
		Timestamp:    now,
		EndpointHash: "abc123",
	}

	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(errors.New("redis error"))

	err := r.rebuildStatus(context.Background(), result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redis error")
}

func TestReconciler_rebuildCircuit(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{hotstate: mockHS}

	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:    webhookID,
		EndpointHash: "abc123",
		Success:      true,
	}

	// rebuildCircuit currently returns nil without doing anything
	err := r.rebuildCircuit(context.Background(), result)
	assert.NoError(t, err)
}

func TestReconciler_rebuildStats_Delivered(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{hotstate: mockHS}

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:    webhookID,
		Success:      true,
		Timestamp:    now,
		EndpointHash: "abc123",
	}

	bucket := now.Format("2006010215")
	mockHS.On("IncrStats", mock.Anything, bucket, map[string]int64{"delivered": 1}).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, "endpoints:"+bucket, []string{"abc123"}).Return(nil)

	err := r.rebuildStats(context.Background(), result)
	assert.NoError(t, err)
	mockHS.AssertExpectations(t)
}

func TestReconciler_rebuildStats_Failed(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{hotstate: mockHS}

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:    webhookID,
		Success:      false,
		ShouldRetry:  true,
		Timestamp:    now,
		EndpointHash: "abc123",
	}

	bucket := now.Format("2006010215")
	mockHS.On("IncrStats", mock.Anything, bucket, map[string]int64{"failed": 1}).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, "endpoints:"+bucket, []string{"abc123"}).Return(nil)

	err := r.rebuildStats(context.Background(), result)
	assert.NoError(t, err)
	mockHS.AssertExpectations(t)
}

func TestReconciler_rebuildStats_Exhausted(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{hotstate: mockHS}

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:    webhookID,
		Success:      false,
		ShouldRetry:  false,
		Timestamp:    now,
		EndpointHash: "abc123",
	}

	bucket := now.Format("2006010215")
	mockHS.On("IncrStats", mock.Anything, bucket, map[string]int64{"exhausted": 1}).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, "endpoints:"+bucket, []string{"abc123"}).Return(nil)

	err := r.rebuildStats(context.Background(), result)
	assert.NoError(t, err)
	mockHS.AssertExpectations(t)
}

func TestReconciler_rebuildStats_IncrStatsError(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{hotstate: mockHS}

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:    webhookID,
		Success:      true,
		Timestamp:    now,
		EndpointHash: "abc123",
	}

	bucket := now.Format("2006010215")
	mockHS.On("IncrStats", mock.Anything, bucket, mock.Anything).Return(errors.New("stats error"))

	err := r.rebuildStats(context.Background(), result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stats error")
}

func TestReconciler_scheduleRetry_FutureRetry(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{
		hotstate:  mockHS,
		ttlConfig: config.TTLConfig{RetryDataTTL: time.Hour},
	}

	futureRetry := time.Now().Add(time.Hour)
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:   webhookID,
		Success:     false,
		ShouldRetry: true,
		Webhook: &domain.Webhook{
			ID:      webhookID,
			Attempt: 2,
		},
		NextRetryAt: futureRetry,
	}

	mockHS.On("EnsureRetryData", mock.Anything, mock.Anything, time.Hour).Return(nil)
	mockHS.On("ScheduleRetry", mock.Anything, webhookID, 2, futureRetry, "reconciled").Return(nil)

	err := r.scheduleRetry(context.Background(), result)
	assert.NoError(t, err)
	mockHS.AssertExpectations(t)
}

func TestReconciler_scheduleRetry_PastRetry(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{
		hotstate:  mockHS,
		ttlConfig: config.TTLConfig{RetryDataTTL: time.Hour},
	}

	pastRetry := time.Now().Add(-time.Hour) // Already in the past
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:   webhookID,
		Success:     false,
		ShouldRetry: true,
		Webhook: &domain.Webhook{
			ID:      webhookID,
			Attempt: 2,
		},
		NextRetryAt: pastRetry,
	}

	// When retry is in the past, it should schedule for "now" (approximately)
	mockHS.On("EnsureRetryData", mock.Anything, mock.Anything, time.Hour).Return(nil)
	mockHS.On("ScheduleRetry", mock.Anything, webhookID, 2, mock.MatchedBy(func(t time.Time) bool {
		// Should be scheduled for now (or very close to it)
		return time.Since(t) < time.Second
	}), "reconciled").Return(nil)

	err := r.scheduleRetry(context.Background(), result)
	assert.NoError(t, err)
	mockHS.AssertExpectations(t)
}

func TestReconciler_scheduleRetry_EnsureRetryDataError(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{
		hotstate:  mockHS,
		ttlConfig: config.TTLConfig{RetryDataTTL: time.Hour},
	}

	futureRetry := time.Now().Add(time.Hour)
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:   webhookID,
		Success:     false,
		ShouldRetry: true,
		Webhook: &domain.Webhook{
			ID: webhookID,
		},
		NextRetryAt: futureRetry,
	}

	mockHS.On("EnsureRetryData", mock.Anything, mock.Anything, time.Hour).Return(errors.New("redis error"))

	err := r.scheduleRetry(context.Background(), result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redis error")
}

func TestReconciler_scheduleRetry_ScheduleRetryError(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{
		hotstate:  mockHS,
		ttlConfig: config.TTLConfig{RetryDataTTL: time.Hour},
	}

	futureRetry := time.Now().Add(time.Hour)
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:   webhookID,
		Success:     false,
		ShouldRetry: true,
		Webhook: &domain.Webhook{
			ID: webhookID,
		},
		NextRetryAt: futureRetry,
	}

	mockHS.On("EnsureRetryData", mock.Anything, mock.Anything, time.Hour).Return(nil)
	mockHS.On("ScheduleRetry", mock.Anything, mock.Anything, mock.Anything, mock.Anything, "reconciled").Return(errors.New("schedule error"))

	err := r.scheduleRetry(context.Background(), result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schedule error")
}

func TestReconciler_rebuildStats_AddToHLLError(t *testing.T) {
	mockHS := new(MockHotState)
	r := &Reconciler{hotstate: mockHS}

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())
	result := &domain.DeliveryResult{
		WebhookID:    webhookID,
		Success:      true,
		Timestamp:    now,
		EndpointHash: "abc123",
	}

	bucket := now.Format("2006010215")
	mockHS.On("IncrStats", mock.Anything, bucket, mock.Anything).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, "endpoints:"+bucket, mock.Anything).Return(errors.New("hll error"))

	err := r.rebuildStats(context.Background(), result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "hll error")
}

func TestDeliveryResult_JSONSerialization(t *testing.T) {
	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())

	result := domain.DeliveryResult{
		WebhookID:    webhookID,
		Success:      true,
		StatusCode:   200,
		Attempt:      2,
		Timestamp:    now,
		EndpointHash: "abc123",
		ShouldRetry:  false,
		Webhook: &domain.Webhook{
			ID:      webhookID,
			Attempt: 3,
		},
	}

	// Test JSON marshaling/unmarshaling
	data, err := json.Marshal(result)
	assert.NoError(t, err)

	var unmarshaled domain.DeliveryResult
	err = json.Unmarshal(data, &unmarshaled)
	assert.NoError(t, err)

	assert.Equal(t, result.WebhookID, unmarshaled.WebhookID)
	assert.Equal(t, result.Success, unmarshaled.Success)
	assert.Equal(t, result.StatusCode, unmarshaled.StatusCode)
	assert.Equal(t, result.Attempt, unmarshaled.Attempt)
	assert.Equal(t, result.EndpointHash, unmarshaled.EndpointHash)
	assert.Equal(t, result.ShouldRetry, unmarshaled.ShouldRetry)
}
