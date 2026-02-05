//go:build !integration

package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/mocks"
	"github.com/howk/howk/internal/script"
	"github.com/howk/howk/internal/worker"
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

func (m *MockPublisher) PublishDeadLetter(ctx context.Context, dl *domain.DeadLetter) error {
	args := m.Called(ctx, dl)
	return args.Error(0)
}

func (m *MockPublisher) Close() error {
	args := m.Called()
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

func (m *MockHotState) CircuitBreaker() hotstate.CircuitBreakerChecker {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(hotstate.CircuitBreakerChecker)
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

func setupWorkerTest() (*worker.Worker, *MockBroker, *MockPublisher, *MockHotState, *mocks.MockCircuitBreaker, *MockDeliveryClient, *MockRetryStrategy) {
	cfg := config.DefaultConfig()

	mockBroker := new(MockBroker)
	mockPublisher := new(MockPublisher)
	mockHotState := new(MockHotState)
	mockCircuitBreaker := new(mocks.MockCircuitBreaker)
	mockDeliveryClient := new(MockDeliveryClient)
	mockRetryStrategy := new(MockRetryStrategy)

	// Set up the mock to return the circuit breaker
	mockHotState.On("CircuitBreaker").Return(mockCircuitBreaker)

	// Default: concurrency check passes (inflight count = 1, below threshold)
	mockHotState.On("IncrInflight", mock.Anything, mock.Anything, mock.Anything).
		Return(int64(1), nil).Maybe()
	mockHotState.On("DecrInflight", mock.Anything, mock.Anything).
		Return(nil).Maybe()

	// Default: state publishing (async, may not complete before test ends)
	mockPublisher.On("PublishState", mock.Anything, mock.Anything).Return(nil).Maybe()
	mockPublisher.On("PublishStateTombstone", mock.Anything, mock.Anything).Return(nil).Maybe()

	// Create a test script engine with disabled config (scripts won't execute)
	testScriptLoader := script.NewLoader()
	testScriptEngine := script.NewEngine(config.LuaConfig{Enabled: false}, testScriptLoader, nil, nil, nil, zerolog.Logger{})

	w := worker.NewWorker(
		cfg,
		mockBroker,         // Passed as broker.Broker interface
		mockPublisher,      // Passed as broker.WebhookPublisher interface
		mockHotState,       // Passed as hotstate.HotState interface
		mockDeliveryClient, // Passed as delivery.Deliverer interface
		mockRetryStrategy,  // Passed as retry.Retrier interface
		testScriptEngine,   // Passed as *script.Engine
	)

	return w, mockBroker, mockPublisher, mockHotState, mockCircuitBreaker, mockDeliveryClient, mockRetryStrategy
}

// TestNewWorker verifies that the NewWorker function correctly initializes the Worker struct
func TestNewWorker(t *testing.T) {
	cfg := config.DefaultConfig()
	mockBroker := new(MockBroker)
	mockPublisher := new(MockPublisher)
	mockHotState := new(MockHotState)
	mockCircuitBreaker := new(mocks.MockCircuitBreaker)
	mockDeliveryClient := new(MockDeliveryClient)
	mockRetryStrategy := new(MockRetryStrategy)

	// Set up the mock to return the circuit breaker
	mockHotState.On("CircuitBreaker").Return(mockCircuitBreaker)

	// Create a test script engine with disabled config
	testScriptLoader := script.NewLoader()
	testScriptEngine := script.NewEngine(config.LuaConfig{Enabled: false}, testScriptLoader, nil, nil, nil, zerolog.Logger{})

	w := worker.NewWorker(
		cfg,
		mockBroker,
		mockPublisher,
		mockHotState,
		mockDeliveryClient,
		mockRetryStrategy,
		testScriptEngine,
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

// TestWorkerGetConfig tests the GetConfig accessor
func TestWorkerGetConfig(t *testing.T) {
	w, _, _, _, _, _, _ := setupWorkerTest()
	cfg := w.GetConfig()
	assert.NotNil(t, cfg)
}

// TestWorkerGetBroker tests the GetBroker accessor
func TestWorkerGetBroker(t *testing.T) {
	w, broker, _, _, _, _, _ := setupWorkerTest()
	assert.Equal(t, broker, w.GetBroker())
}

// TestWorkerGetPublisher tests the GetPublisher accessor
func TestWorkerGetPublisher(t *testing.T) {
	w, _, publisher, _, _, _, _ := setupWorkerTest()
	assert.Equal(t, publisher, w.GetPublisher())
}

// TestWorkerGetHotState tests the GetHotState accessor
func TestWorkerGetHotState(t *testing.T) {
	w, _, _, hotstate, _, _, _ := setupWorkerTest()
	assert.Equal(t, hotstate, w.GetHotState())
}

// TestWorkerGetCircuit tests the GetCircuit accessor
func TestWorkerGetCircuit(t *testing.T) {
	w, _, _, _, circuit, _, _ := setupWorkerTest()
	assert.Equal(t, circuit, w.GetCircuit())
}

// TestWorkerGetDelivery tests the GetDelivery accessor
func TestWorkerGetDelivery(t *testing.T) {
	w, _, _, _, _, delivery, _ := setupWorkerTest()
	assert.Equal(t, delivery, w.GetDelivery())
}

// TestWorkerGetRetry tests the GetRetry accessor
func TestWorkerGetRetry(t *testing.T) {
	w, _, _, _, _, _, retrier := setupWorkerTest()
	assert.Equal(t, retrier, w.GetRetry())
}

// TestWorkerSetScriptConsumer tests the SetScriptConsumer and GetScriptConsumer accessors
func TestWorkerSetScriptConsumer(t *testing.T) {
	w, _, _, _, _, _, _ := setupWorkerTest()

	// Initially nil
	assert.Nil(t, w.GetScriptConsumer())

	// Set a mock consumer (we can't create a real one without Kafka, but we can test the setter)
	// Just verify the method exists and can be called
	// The actual consumer would be created elsewhere

	// For this test, we just verify the initial state is nil
	// The SetScriptConsumer would be tested in integration tests
}
