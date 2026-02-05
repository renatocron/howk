//go:build !integration

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockHotState implements hotstate.HotState for testing
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

func (m *MockHotState) IncrInflight(ctx context.Context, endpointHash domain.EndpointHash, ttl time.Duration) (int64, error) {
	args := m.Called(ctx, endpointHash, ttl)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockHotState) DecrInflight(ctx context.Context, endpointHash domain.EndpointHash) error {
	args := m.Called(ctx, endpointHash)
	return args.Error(0)
}

func (m *MockHotState) GetEpoch(ctx context.Context) (*domain.SystemEpoch, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.SystemEpoch), args.Error(1)
}

func (m *MockHotState) SetEpoch(ctx context.Context, epoch *domain.SystemEpoch) error {
	args := m.Called(ctx, epoch)
	return args.Error(0)
}

func (m *MockHotState) GetRetryQueueSize(ctx context.Context) (int64, error) {
	args := m.Called(ctx)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockHotState) CheckCanary(ctx context.Context) (bool, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.Error(1)
}

func (m *MockHotState) SetCanary(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *MockHotState) WaitForCanary(ctx context.Context, timeout time.Duration) bool {
	args := m.Called(ctx, timeout)
	return args.Bool(0)
}

func (m *MockHotState) AcquireReconcilerLock(ctx context.Context, ttl time.Duration) (bool, func()) {
	args := m.Called(ctx, ttl)
	unlockFunc := func() {}
	if fn, ok := args.Get(1).(func()); ok {
		unlockFunc = fn
	}
	return args.Bool(0), unlockFunc
}

func (m *MockHotState) DelCanary(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// MockPublisher implements broker.WebhookPublisher for testing
type MockPublisher struct {
	mock.Mock
}

func (m *MockPublisher) PublishWebhook(ctx context.Context, webhook *domain.Webhook) error {
	args := m.Called(ctx, webhook)
	return args.Error(0)
}

func (m *MockPublisher) PublishResult(ctx context.Context, result *domain.DeliveryResult) error {
	args := m.Called(ctx, result)
	return args.Error(0)
}

func (m *MockPublisher) PublishDeadLetter(ctx context.Context, dl *domain.DeadLetter) error {
	args := m.Called(ctx, dl)
	return args.Error(0)
}

func (m *MockPublisher) Close() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockPublisher) Ping(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockPublisher) PublishToSlow(ctx context.Context, webhook *domain.Webhook) error {
	args := m.Called(ctx, webhook)
	return args.Error(0)
}

func (m *MockPublisher) PublishState(ctx context.Context, snapshot *domain.WebhookStateSnapshot) error {
	args := m.Called(ctx, snapshot)
	return args.Error(0)
}

func (m *MockPublisher) PublishStateTombstone(ctx context.Context, webhookID domain.WebhookID) error {
	args := m.Called(ctx, webhookID)
	return args.Error(0)
}

func setupSchedulerTest() (*Scheduler, *MockHotState, *MockPublisher) {
	cfg := config.SchedulerConfig{
		PollInterval: time.Second,
		BatchSize:    10,
		LockTimeout:  time.Minute,
	}

	mockHotState := new(MockHotState)
	mockPublisher := new(MockPublisher)

	scheduler := NewScheduler(cfg, mockHotState, mockPublisher)

	return scheduler, mockHotState, mockPublisher
}

// TestParseReference_Valid tests valid reference parsing
func TestParseReference_Valid(t *testing.T) {
	webhookID, attempt, err := parseReference("wh_123:3")
	assert.NoError(t, err)
	assert.Equal(t, domain.WebhookID("wh_123"), webhookID)
	assert.Equal(t, 3, attempt)
}

// TestParseReference_InvalidFormat tests invalid reference formats
func TestParseReference_InvalidFormat(t *testing.T) {
	tests := []struct {
		name          string
		ref           string
		expectedError string
	}{
		{"no colon", "wh_123", "invalid reference format"},
		{"multiple colons", "wh_123:3:extra", "invalid reference format"},
		{"empty", "", "invalid reference format"},
		{"only colon", ":", "invalid attempt number"},     // Splits to ["", ""], Atoi fails on ""
		{"only webhook", "wh_123:", "invalid attempt number"}, // Splits to ["wh_123", ""], Atoi fails on ""
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseReference(tt.ref)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

// TestParseReference_EmptyWebhookID tests that ":3" is actually valid format (empty webhook ID is allowed)
func TestParseReference_EmptyWebhookID(t *testing.T) {
	// Note: ":3" splits to ["", "3"] - technically valid format with empty webhook ID
	// The parseReference function doesn't validate that webhook ID is non-empty
	webhookID, attempt, err := parseReference(":3")
	assert.NoError(t, err) // This actually succeeds!
	assert.Equal(t, domain.WebhookID(""), webhookID)
	assert.Equal(t, 3, attempt)
}

// TestParseReference_InvalidAttempt tests non-numeric attempt
func TestParseReference_InvalidAttempt(t *testing.T) {
	_, _, err := parseReference("wh_123:abc")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid attempt number")
}

// TestScheduler_ProcessBatch_InvalidReference tests handling of malformed references
func TestScheduler_ProcessBatch_InvalidReference(t *testing.T) {
	scheduler, mockHotState, _ := setupSchedulerTest()

	// Return malformed references
	mockHotState.On("PopAndLockRetries", mock.Anything, 10, mock.Anything).
		Return([]string{
			"malformed-reference",     // No colon
			"wh_123:",                 // Empty attempt - invalid Atoi
			"wh_456:not-a-number",     // Invalid attempt
			"too:many:parts",          // Too many colons
		}, nil)

	// All malformed references should be acked (removed)
	mockHotState.On("AckRetry", mock.Anything, "malformed-reference").Return(nil)
	mockHotState.On("AckRetry", mock.Anything, "wh_123:").Return(nil)
	mockHotState.On("AckRetry", mock.Anything, "wh_456:not-a-number").Return(nil)
	mockHotState.On("AckRetry", mock.Anything, "too:many:parts").Return(nil)

	err := scheduler.processBatch(context.Background())
	assert.NoError(t, err)

	mockHotState.AssertExpectations(t)
}

// TestScheduler_ProcessBatch_MissingRetryData tests handling of expired/missing retry data
func TestScheduler_ProcessBatch_MissingRetryData(t *testing.T) {
	scheduler, mockHotState, mockPublisher := setupSchedulerTest()

	// Return valid references
	mockHotState.On("PopAndLockRetries", mock.Anything, 10, mock.Anything).
		Return([]string{
			"wh_valid:1",
			"wh_ghost:2", // This one will have missing data
		}, nil)

	// First webhook has data
	validWebhook := &domain.Webhook{ID: "wh_valid"}
	mockHotState.On("GetRetryData", mock.Anything, domain.WebhookID("wh_valid")).
		Return(validWebhook, nil)

	// Second webhook data is missing (expired or deleted)
	mockHotState.On("GetRetryData", mock.Anything, domain.WebhookID("wh_ghost")).
		Return(nil, errors.New("redis: nil"))

	// Missing data reference should be acked (removed)
	mockHotState.On("AckRetry", mock.Anything, "wh_ghost:2").Return(nil)

	// Valid webhook should be published
	mockPublisher.On("PublishWebhook", mock.Anything, validWebhook).Return(nil)
	mockHotState.On("AckRetry", mock.Anything, "wh_valid:1").Return(nil)

	err := scheduler.processBatch(context.Background())
	assert.NoError(t, err)

	mockHotState.AssertExpectations(t)
	mockPublisher.AssertExpectations(t)
}

// TestScheduler_ProcessBatch_PublishFailure tests handling of publish failures
func TestScheduler_ProcessBatch_PublishFailure(t *testing.T) {
	scheduler, mockHotState, mockPublisher := setupSchedulerTest()

	// Return valid reference
	mockHotState.On("PopAndLockRetries", mock.Anything, 10, mock.Anything).
		Return([]string{"wh_test:1"}, nil)

	// Webhook data exists
	webhook := &domain.Webhook{ID: "wh_test", Attempt: 1}
	mockHotState.On("GetRetryData", mock.Anything, domain.WebhookID("wh_test")).
		Return(webhook, nil)

	// But publish fails
	mockPublisher.On("PublishWebhook", mock.Anything, webhook).
		Return(errors.New("kafka unavailable"))

	// Note: AckRetry is NOT called on publish failure - item stays locked for retry
	err := scheduler.processBatch(context.Background())
	assert.NoError(t, err) // Scheduler continues despite publish failure

	mockPublisher.AssertExpectations(t)
	mockHotState.AssertNotCalled(t, "AckRetry", mock.Anything, "wh_test:1")
}

// TestScheduler_ProcessBatch_AckFailure tests non-fatal handling of ack failures
func TestScheduler_ProcessBatch_AckFailure(t *testing.T) {
	scheduler, mockHotState, mockPublisher := setupSchedulerTest()

	// Return valid reference
	mockHotState.On("PopAndLockRetries", mock.Anything, 10, mock.Anything).
		Return([]string{"wh_test:1"}, nil)

	// Webhook data exists
	webhook := &domain.Webhook{ID: "wh_test", Attempt: 1}
	mockHotState.On("GetRetryData", mock.Anything, domain.WebhookID("wh_test")).
		Return(webhook, nil)

	// Publish succeeds
	mockPublisher.On("PublishWebhook", mock.Anything, webhook).Return(nil)

	// But ack fails (non-fatal)
	mockHotState.On("AckRetry", mock.Anything, "wh_test:1").
		Return(errors.New("redis error"))

	// Should continue without error
	err := scheduler.processBatch(context.Background())
	assert.NoError(t, err)
}

// TestScheduler_ProcessBatch_EmptyBatch tests handling of empty batch
func TestScheduler_ProcessBatch_EmptyBatch(t *testing.T) {
	scheduler, mockHotState, _ := setupSchedulerTest()

	// Return empty batch
	mockHotState.On("PopAndLockRetries", mock.Anything, 10, mock.Anything).
		Return([]string{}, nil)

	err := scheduler.processBatch(context.Background())
	assert.NoError(t, err)

	// No other methods should be called
	mockHotState.AssertExpectations(t)
}

// TestScheduler_ProcessBatch_PopError tests handling of PopAndLockRetries error
func TestScheduler_ProcessBatch_PopError(t *testing.T) {
	scheduler, mockHotState, _ := setupSchedulerTest()

	// Pop fails
	mockHotState.On("PopAndLockRetries", mock.Anything, 10, mock.Anything).
		Return(nil, errors.New("redis error"))

	err := scheduler.processBatch(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redis error")
}

// TestScheduler_ProcessBatch_MultipleAttempts tests that attempt from reference is used
func TestScheduler_ProcessBatch_MultipleAttempts(t *testing.T) {
	scheduler, mockHotState, mockPublisher := setupSchedulerTest()

	// Return reference with attempt 5
	mockHotState.On("PopAndLockRetries", mock.Anything, 10, mock.Anything).
		Return([]string{"wh_test:5"}, nil)

	// Webhook data has stale attempt number
	webhook := &domain.Webhook{ID: "wh_test", Attempt: 2}
	mockHotState.On("GetRetryData", mock.Anything, domain.WebhookID("wh_test")).
		Return(webhook, nil)

	// Publish should receive webhook with attempt=5 (from reference, not data)
	mockPublisher.On("PublishWebhook", mock.Anything, mock.MatchedBy(func(w *domain.Webhook) bool {
		return w.ID == "wh_test" && w.Attempt == 5
	})).Return(nil)

	mockHotState.On("AckRetry", mock.Anything, "wh_test:5").Return(nil)

	err := scheduler.processBatch(context.Background())
	assert.NoError(t, err)

	mockPublisher.AssertExpectations(t)
}

// TestScheduler_ProcessBatch_NotDueYet tests that when no retries are due,
// nothing is published (converted from integration test)
func TestScheduler_ProcessBatch_NotDueYet(t *testing.T) {
	scheduler, mockHotState, mockPublisher := setupSchedulerTest()

	// PopAndLockRetries returns empty - no items are due
	mockHotState.On("PopAndLockRetries", mock.Anything, 10, mock.Anything).
		Return([]string{}, nil)

	err := scheduler.processBatch(context.Background())
	assert.NoError(t, err)

	mockHotState.AssertExpectations(t)
	// Verify no webhooks were published
	mockPublisher.AssertNotCalled(t, "PublishWebhook", mock.Anything, mock.Anything)
}

// TestScheduler_ProcessBatch_BatchSizeRespected tests that processBatch handles
// a full batch of items correctly (converted from integration test)
func TestScheduler_ProcessBatch_BatchSizeRespected(t *testing.T) {
	cfg := config.SchedulerConfig{
		PollInterval: time.Second,
		BatchSize:    5, // Small batch size
		LockTimeout:  time.Minute,
	}

	mockHotState := new(MockHotState)
	mockPublisher := new(MockPublisher)
	scheduler := NewScheduler(cfg, mockHotState, mockPublisher)

	// Return 5 items (matching batch size)
	refs := []string{"wh1:1", "wh2:1", "wh3:1", "wh4:1", "wh5:1"}
	mockHotState.On("PopAndLockRetries", mock.Anything, 5, mock.Anything).
		Return(refs, nil)

	// Mock GetRetryData and PublishWebhook for each item
	for i := 1; i <= 5; i++ {
		id := domain.WebhookID(fmt.Sprintf("wh%d", i))
		wh := &domain.Webhook{ID: id, Attempt: 0}
		mockHotState.On("GetRetryData", mock.Anything, id).Return(wh, nil)
		mockPublisher.On("PublishWebhook", mock.Anything, mock.MatchedBy(func(w *domain.Webhook) bool {
			return w.ID == id && w.Attempt == 1
		})).Return(nil)
		mockHotState.On("AckRetry", mock.Anything, fmt.Sprintf("wh%d:1", i)).Return(nil)
	}

	err := scheduler.processBatch(context.Background())
	assert.NoError(t, err)

	// Verify all 5 webhooks were published
	mockPublisher.AssertNumberOfCalls(t, "PublishWebhook", 5)
	mockHotState.AssertExpectations(t)
}
