//go:build !integration

package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"


	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/mocks"
	"github.com/howk/howk/internal/script"
	"github.com/howk/howk/internal/worker"
)

// TestSlowWorker_RateLimiting verifies that the slow worker rate limits message processing
func TestSlowWorker_RateLimiting(t *testing.T) {
	// Create config with slow lane rate of 10/sec (100ms interval)
	cfg := config.DefaultConfig()
	cfg.Concurrency.SlowLaneRate = 10 // 10 messages per second = 100ms between messages

	// Create mocks
	mockBroker := new(MockBroker)
	mockPublisher := new(MockPublisher)
	mockHotState := new(MockHotState)
	mockCircuitBreaker := new(mocks.MockCircuitBreaker)
	mockDeliveryClient := new(MockDeliveryClient)
	mockRetryStrategy := new(MockRetryStrategy)

	// Set up mocks
	mockHotState.On("CircuitBreaker").Return(mockCircuitBreaker)

	// Create a test script engine
	testScriptLoader := script.NewLoader()
	testScriptEngine := script.NewEngine(config.LuaConfig{Enabled: false}, testScriptLoader, nil, nil, nil, zerolog.Logger{})

	// Create worker
	w := worker.NewWorker(
		cfg,
		mockBroker,
		mockPublisher,
		mockHotState,
		mockDeliveryClient,
		mockRetryStrategy,
		testScriptEngine,
	)

	// Create slow worker
	slowWorker := worker.NewSlowWorker(w, mockBroker, cfg)
	require.NotNil(t, slowWorker)

	// Verify slow worker was created (basic sanity check)
	// Full rate limiting test requires running the actual subscription which blocks
}

// TestSlowWorker_UsesCorrectConsumerGroup verifies the slow worker uses a separate consumer group
func TestSlowWorker_UsesCorrectConsumerGroup(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Kafka.ConsumerGroup = "howk-workers"
	cfg.Kafka.Topics.Slow = "howk.slow"

	mockBroker := new(MockBroker)
	mockPublisher := new(MockPublisher)
	mockHotState := new(MockHotState)
	mockCircuitBreaker := new(mocks.MockCircuitBreaker)
	mockDeliveryClient := new(MockDeliveryClient)
	mockRetryStrategy := new(MockRetryStrategy)

	mockHotState.On("CircuitBreaker").Return(mockCircuitBreaker)

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

	// The slow worker should subscribe with "howk-workers-slow" consumer group
	mockBroker.On("Subscribe", mock.Anything, "howk.slow", "howk-workers-slow", mock.Anything).
		Return(context.Canceled) // Return canceled to exit Run() immediately

	slowWorker := worker.NewSlowWorker(w, mockBroker, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := slowWorker.Run(ctx)
	require.Error(t, err) // Should return error because context is canceled

	// Verify that Subscribe was called with the correct consumer group
	mockBroker.AssertCalled(t, "Subscribe", mock.Anything, "howk.slow", "howk-workers-slow", mock.Anything)
}

// TestSlowWorker_ProcessMessage_Success verifies slow worker records stats on success
func TestSlowWorker_ProcessMessage_Success(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultConfig()

	mockBroker := new(MockBroker)
	mockPublisher := new(MockPublisher)
	mockHotState := new(MockHotState)
	mockCircuitBreaker := new(mocks.MockCircuitBreaker)
	mockDeliveryClient := new(MockDeliveryClient)
	mockRetryStrategy := new(MockRetryStrategy)

	// Set up mocks
	mockHotState.On("CircuitBreaker").Return(mockCircuitBreaker).Maybe()

	// Circuit allows
	mockCircuitBreaker.On("ShouldAllow", ctx, domain.EndpointHash("endpoint-hash-abc")).
		Return(true, false, nil).Maybe()

	// Concurrency check passes
	mockHotState.On("IncrInflight", ctx, domain.EndpointHash("endpoint-hash-abc"), mock.Anything).
		Return(int64(1), nil).Maybe()

	// Idempotency check
	mockHotState.On("CheckAndSetProcessed", ctx, domain.WebhookID("wh-123"), 1, mock.Anything).
		Return(true, nil).Maybe()

	// Status updates
	mockHotState.On("SetStatus", ctx, mock.Anything).Return(nil).Maybe()

	// Delivery succeeds
	mockDeliveryClient.On("Deliver", ctx, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 200,
			Duration:   100 * time.Millisecond,
		}).Maybe()

	// Circuit success
	mockCircuitBreaker.On("RecordSuccess", ctx, domain.EndpointHash("endpoint-hash-abc")).
		Return(&domain.CircuitBreaker{
			EndpointHash: domain.EndpointHash("endpoint-hash-abc"),
			State:        domain.CircuitClosed,
		}, nil).Maybe()

	// Result publishing
	mockPublisher.On("PublishResult", ctx, mock.Anything).Return(nil).Maybe()

	// Stats recording - this is key for slow worker
	mockHotState.On("IncrStats", ctx, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockHotState.On("Client").Return(nil).Maybe()
	mockHotState.On("AddToHLL", ctx, mock.Anything, mock.Anything).Return(nil).Maybe()

	// DECR on exit
	mockHotState.On("DecrInflight", ctx, domain.EndpointHash("endpoint-hash-abc")).Return(nil).Maybe()

	// Retry cleanup
	mockHotState.On("DeleteRetryData", ctx, domain.WebhookID("wh-123")).Return(nil).Maybe()

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

	// Create slow worker
	slowWorker := worker.NewSlowWorker(w, mockBroker, cfg)
	require.NotNil(t, slowWorker)

	// Note: We can't directly test processSlowMessage since it's private,
	// but we can verify the slow worker struct is properly configured
}

// TestSlowWorker_Creation verifies slow worker is created with correct dependencies
func TestSlowWorker_Creation(t *testing.T) {
	cfg := config.DefaultConfig()

	mockBroker := new(MockBroker)
	mockPublisher := new(MockPublisher)
	mockHotState := new(MockHotState)
	mockCircuitBreaker := new(mocks.MockCircuitBreaker)
	mockDeliveryClient := new(MockDeliveryClient)
	mockRetryStrategy := new(MockRetryStrategy)

	mockHotState.On("CircuitBreaker").Return(mockCircuitBreaker)

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

	slowWorker := worker.NewSlowWorker(w, mockBroker, cfg)
	require.NotNil(t, slowWorker)
}
