//go:build !integration

package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockBroker implements broker.Broker
type MockBroker struct {
	mock.Mock
}

func (m *MockBroker) Publish(ctx context.Context, topic string, msgs ...broker.Message) error {
	args := m.Called(ctx, topic, msgs)
	return args.Error(0)
}

func (m *MockBroker) Subscribe(ctx context.Context, topic, group string, handler broker.Handler) error {
	args := m.Called(ctx, topic, group, handler)
	// For testing, we might want to manually call the handler
	// or return immediately if the test doesn't need to block on subscribe
	return args.Error(0)
}

func (m *MockBroker) Close() error {
	args := m.Called()
	return args.Error(0)
}

// MockPublisher implements broker.WebhookPublisher
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

func (m *MockPublisher) PublishDeadLetter(ctx context.Context, webhook *domain.Webhook, reason string) error {
	args := m.Called(ctx, webhook, reason)
	return args.Error(0)
}

func (m *MockPublisher) Close() error {
	args := m.Called()
	return args.Error(0)
}

// MockHotState implements hotstate.HotState
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

func (m *MockHotState) ScheduleRetry(ctx context.Context, msg *hotstate.RetryMessage) error {
	args := m.Called(ctx, msg)
	return args.Error(0)
}

func (m *MockHotState) PopDueRetries(ctx context.Context, limit int) ([]*hotstate.RetryMessage, error) {
	args := m.Called(ctx, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*hotstate.RetryMessage), args.Error(1)
}

func (m *MockHotState) GetCircuit(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
}

func (m *MockHotState) UpdateCircuit(ctx context.Context, cb *domain.CircuitBreaker) error {
	args := m.Called(ctx, cb)
	return args.Error(0)
}

func (m *MockHotState) RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
}

func (m *MockHotState) RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
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

func (m *MockHotState) Client() interface{} {
	args := m.Called()
	return args.Get(0)
}

// MockCircuitBreaker implements methods used by Worker from circuit.Breaker
type MockCircuitBreaker struct {
	mock.Mock
}

func (m *MockCircuitBreaker) ShouldAllow(ctx context.Context, endpointHash domain.EndpointHash) (bool, bool, error) {
	args := m.Called(ctx, endpointHash)
	return args.Bool(0), args.Bool(1), args.Error(2)
}

func (m *MockCircuitBreaker) RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
}

func (m *MockCircuitBreaker) RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
}

func (m *MockCircuitBreaker) GetDelayForState(state domain.CircuitState, baseDelay time.Duration) time.Duration {
	args := m.Called(state, baseDelay)
	return args.Get(0).(time.Duration)
}

// MockDeliveryClient implements methods used by Worker from delivery.Client
type MockDeliveryClient struct {
	mock.Mock
}

func (m *MockDeliveryClient) Deliver(ctx context.Context, webhook *domain.Webhook) *delivery.Result {
	args := m.Called(ctx, webhook)
	return args.Get(0).(*delivery.Result)
}

func (m *MockDeliveryClient) Close() {
	m.Called()
}

// MockRetryStrategy implements methods used by Worker from retry.Strategy
type MockRetryStrategy struct {
	mock.Mock
}

func (m *MockRetryStrategy) ShouldRetry(webhook *domain.Webhook, statusCode int, err error) bool {
	args := m.Called(webhook, statusCode, err)
	return args.Bool(0)
}

func (m *MockRetryStrategy) NextDelay(attempt int, circuitState domain.CircuitState) time.Duration {
	args := m.Called(attempt, circuitState)
	return args.Get(0).(time.Duration)
}

func (m *MockRetryStrategy) NextRetryAt(attempt int, circuitState domain.CircuitState) time.Time {
	args := m.Called(attempt, circuitState)
	return args.Get(0).(time.Time)
}

func (m *MockRetryStrategy) IsExhausted(attempt int) bool {
	args := m.Called(attempt)
	return args.Bool(0)
}

func (m *MockRetryStrategy) MaxAttempts() int {
	args := m.Called()
	return args.Int(0)
}

func (m *MockRetryStrategy) RetrySchedule() []string {
	args := m.Called()
	return args.Get(0).([]string)
}

func setupWorkerTest() (*worker.Worker, *MockBroker, *MockPublisher, *MockHotState, *MockCircuitBreaker, *MockDeliveryClient, *MockRetryStrategy) {
	cfg := config.DefaultConfig()

	mockBroker := new(MockBroker)
	mockPublisher := new(MockPublisher)
	mockHotState := new(MockHotState)
	mockCircuitBreaker := new(MockCircuitBreaker)
	mockDeliveryClient := new(MockDeliveryClient)
	mockRetryStrategy := new(MockRetryStrategy)

	w := worker.NewWorker(
		cfg,
		mockBroker, // Passed as broker.Broker interface
		mockPublisher, // Passed as broker.WebhookPublisher interface
		mockHotState,  // Passed as hotstate.HotState interface
		mockCircuitBreaker, // Passed as hotstate.CircuitBreakerChecker interface
		mockDeliveryClient, // Passed as delivery.Deliverer interface
		mockRetryStrategy, // Passed as retry.Retrier interface
	)

	return w, mockBroker, mockPublisher, mockHotState, mockCircuitBreaker, mockDeliveryClient, mockRetryStrategy
}

// TestNewWorker verifies that the NewWorker function correctly initializes the Worker struct
func TestNewWorker(t *testing.T) {
	cfg := config.DefaultConfig()
	mockBroker := new(MockBroker)
	mockPublisher := new(MockPublisher)
	mockHotState := new(MockHotState)
	mockCircuitBreaker := new(MockCircuitBreaker)
	mockDeliveryClient := new(MockDeliveryClient)
	mockRetryStrategy := new(MockRetryStrategy)

	w := worker.NewWorker(
		cfg,
		mockBroker,
		mockPublisher,
		mockHotState,
		mockCircuitBreaker,
		mockDeliveryClient,
		mockRetryStrategy,
	)

	assert.NotNil(t, w)
	assert.Equal(t, cfg, w.GetConfig())
	assert.Equal(t, mockBroker, w.GetBroker())
	assert.Equal(t, mockPublisher, w.GetPublisher())
	assert.Equal(t, mockHotState, w.GetHotState())
	assert.Equal(t, mockCircuitBreaker, w.GetCircuit())
	assert.Equal(t, mockDeliveryClient, w.GetDelivery())
	assert.Equal(t, mockRetryStrategy, w.GetRetry())
}