//go:build !integration

package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/mocks"
	"github.com/howk/howk/internal/script"
	"github.com/howk/howk/internal/worker"
)

// setupWorkerWithMocks creates a worker with mock dependencies for concurrency testing
func setupWorkerWithMocks(t *testing.T) (*worker.Worker, *MockPublisher, *MockHotState, *mocks.MockCircuitBreaker, *MockDeliveryClient, *MockRetryStrategy) {
	cfg := config.DefaultConfig()

	mockBroker := new(MockBroker)
	mockPublisher := new(MockPublisher)
	mockHotState := new(MockHotState)
	mockCircuitBreaker := new(mocks.MockCircuitBreaker)
	mockDeliveryClient := new(MockDeliveryClient)
	mockRetryStrategy := new(MockRetryStrategy)

	// Set up the mock to return the circuit breaker
	mockHotState.On("CircuitBreaker").Return(mockCircuitBreaker)

	// Default: state publishing (async, may not complete before test ends)
	mockPublisher.On("PublishState", mock.Anything, mock.Anything).Return(nil).Maybe()
	mockPublisher.On("PublishStateTombstone", mock.Anything, mock.Anything).Return(nil).Maybe()

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
		nil, // domainLimiter not tested here
	)

	return w, mockPublisher, mockHotState, mockCircuitBreaker, mockDeliveryClient, mockRetryStrategy
}

func createConcurrencyTestWebhook() *domain.Webhook {
	return &domain.Webhook{
		ID:           domain.WebhookID("wh-123"),
		ConfigID:     domain.ConfigID("config-1"),
		EndpointHash: domain.EndpointHash("endpoint-hash-abc"),
		Endpoint:     "https://example.com/webhook",
		Payload:      []byte(`{"test": "data"}`),
		Attempt:      1,
		MaxAttempts:  3,
	}
}

func createConcurrencyTestMessage(webhook *domain.Webhook) *broker.Message {
	data, _ := json.Marshal(webhook)
	return &broker.Message{
		Key:     []byte(webhook.ConfigID),
		Value:   data,
		Headers: map[string]string{},
	}
}

// TestProcessMessage_ProceedsWhenUnderThreshold verifies delivery proceeds when inflight < threshold
func TestProcessMessage_ProceedsWhenUnderThreshold(t *testing.T) {
	ctx := context.Background()
	w, mockPublisher, mockHotState, mockCircuitBreaker, mockDeliveryClient, _ := setupWorkerWithMocks(t)

	webhook := createConcurrencyTestWebhook()
	msg := createConcurrencyTestMessage(webhook)

	// Circuit allows
	mockCircuitBreaker.On("ShouldAllow", ctx, webhook.EndpointHash).Return(true, false, nil)

	// Concurrency check: under threshold (returns 1, below default of 50)
	mockHotState.On("IncrInflight", ctx, webhook.EndpointHash, mock.Anything).Return(int64(1), nil).Once()

	// Idempotency check
	mockHotState.On("CheckAndSetProcessed", ctx, webhook.ID, webhook.Attempt, mock.Anything).Return(true, nil)

	// Status update
	mockHotState.On("SetStatus", ctx, mock.Anything).Return(nil)

	// Delivery succeeds
	mockDeliveryClient.On("Deliver", ctx, mock.Anything).Return(&delivery.Result{
		StatusCode: 200,
		Duration:   100 * time.Millisecond,
	})

	// Circuit breaker success recording
	mockCircuitBreaker.On("RecordSuccess", ctx, webhook.EndpointHash).Return(&domain.CircuitBreaker{
		EndpointHash: webhook.EndpointHash,
		State:        domain.CircuitClosed,
	}, nil)

	// Result publishing
	mockPublisher.On("PublishResult", ctx, mock.Anything).Return(nil)

	// Stats recording - accept any calls
	mockHotState.On("IncrStats", ctx, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("Client").Return(nil)
	mockHotState.On("AddToHLL", ctx, mock.Anything, mock.Anything).Return(nil)

	// DECR should be called on exit
	mockHotState.On("DecrInflight", ctx, webhook.EndpointHash).Return(nil).Once()

	// Retry cleanup
	mockHotState.On("DeleteRetryData", ctx, webhook.ID).Return(nil)

	// Process the message using ProcessMessage (exported method)
	err := w.ProcessMessage(ctx, msg)
	require.NoError(t, err)

	// Verify DECR was called
	mockHotState.AssertExpectations(t)
}

// TestProcessMessage_FailOpenOnRedisError verifies delivery proceeds when Redis fails (fail-open)
func TestProcessMessage_FailOpenOnRedisError(t *testing.T) {
	ctx := context.Background()
	w, mockPublisher, mockHotState, mockCircuitBreaker, mockDeliveryClient, _ := setupWorkerWithMocks(t)

	webhook := createConcurrencyTestWebhook()
	msg := createConcurrencyTestMessage(webhook)

	// Circuit allows
	mockCircuitBreaker.On("ShouldAllow", ctx, webhook.EndpointHash).Return(true, false, nil)

	// Concurrency check: Redis error (fail-open path)
	mockHotState.On("IncrInflight", ctx, webhook.EndpointHash, mock.Anything).Return(int64(0), errors.New("redis connection failed"))

	// Idempotency check
	mockHotState.On("CheckAndSetProcessed", ctx, webhook.ID, webhook.Attempt, mock.Anything).Return(true, nil)

	// Status update
	mockHotState.On("SetStatus", ctx, mock.Anything).Return(nil)

	// Delivery succeeds
	mockDeliveryClient.On("Deliver", ctx, mock.Anything).Return(&delivery.Result{
		StatusCode: 200,
		Duration:   100 * time.Millisecond,
	})

	// Circuit breaker success recording
	mockCircuitBreaker.On("RecordSuccess", ctx, webhook.EndpointHash).Return(&domain.CircuitBreaker{
		EndpointHash: webhook.EndpointHash,
		State:        domain.CircuitClosed,
	}, nil)

	// Result publishing
	mockPublisher.On("PublishResult", ctx, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("IncrStats", ctx, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("Client").Return(nil)
	mockHotState.On("AddToHLL", ctx, mock.Anything, mock.Anything).Return(nil)

	// DECR should NOT be called when IncrInflight failed (no slot was acquired)
	// (no DecrInflight expectation)

	// Retry cleanup
	mockHotState.On("DeleteRetryData", ctx, webhook.ID).Return(nil)

	// Process the message
	err := w.ProcessMessage(ctx, msg)
	require.NoError(t, err)

	mockHotState.AssertExpectations(t)
}

// TestProcessMessage_DecrCalledOnSuccess verifies DECR is called after successful delivery
func TestProcessMessage_DecrCalledOnSuccess(t *testing.T) {
	ctx := context.Background()
	w, mockPublisher, mockHotState, mockCircuitBreaker, mockDeliveryClient, _ := setupWorkerWithMocks(t)

	webhook := createConcurrencyTestWebhook()
	msg := createConcurrencyTestMessage(webhook)

	// Circuit allows
	mockCircuitBreaker.On("ShouldAllow", ctx, webhook.EndpointHash).Return(true, false, nil)

	// Concurrency check: under threshold
	mockHotState.On("IncrInflight", ctx, webhook.EndpointHash, mock.Anything).Return(int64(1), nil).Once()

	// Idempotency check
	mockHotState.On("CheckAndSetProcessed", ctx, webhook.ID, webhook.Attempt, mock.Anything).Return(true, nil)

	// Status update
	mockHotState.On("SetStatus", ctx, mock.Anything).Return(nil)

	// Delivery succeeds
	mockDeliveryClient.On("Deliver", ctx, mock.Anything).Return(&delivery.Result{
		StatusCode: 200,
		Duration:   100 * time.Millisecond,
	})

	// Circuit breaker success recording
	mockCircuitBreaker.On("RecordSuccess", ctx, webhook.EndpointHash).Return(&domain.CircuitBreaker{
		EndpointHash: webhook.EndpointHash,
		State:        domain.CircuitClosed,
	}, nil)

	// Result publishing
	mockPublisher.On("PublishResult", ctx, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("IncrStats", ctx, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("Client").Return(nil)
	mockHotState.On("AddToHLL", ctx, mock.Anything, mock.Anything).Return(nil)

	// DECR must be called exactly once on success
	mockHotState.On("DecrInflight", ctx, webhook.EndpointHash).Return(nil).Once()

	// Retry cleanup
	mockHotState.On("DeleteRetryData", ctx, webhook.ID).Return(nil)

	// Process the message
	err := w.ProcessMessage(ctx, msg)
	require.NoError(t, err)

	// Verify DECR was called
	mockHotState.AssertExpectations(t)
}

// TestProcessMessage_DecrCalledOnFailure verifies DECR is called after failed delivery
func TestProcessMessage_DecrCalledOnFailure(t *testing.T) {
	ctx := context.Background()
	w, mockPublisher, mockHotState, mockCircuitBreaker, mockDeliveryClient, mockRetryStrategy := setupWorkerWithMocks(t)

	webhook := createConcurrencyTestWebhook()
	msg := createConcurrencyTestMessage(webhook)

	// Circuit allows
	mockCircuitBreaker.On("ShouldAllow", ctx, webhook.EndpointHash).Return(true, false, nil)

	// Concurrency check: under threshold
	mockHotState.On("IncrInflight", ctx, webhook.EndpointHash, mock.Anything).Return(int64(1), nil).Once()

	// Idempotency check
	mockHotState.On("CheckAndSetProcessed", ctx, webhook.ID, webhook.Attempt, mock.Anything).Return(true, nil)

	// Status update
	mockHotState.On("SetStatus", ctx, mock.Anything).Return(nil).Twice() // Delivering + Failed

	// Delivery fails with retryable error
	mockDeliveryClient.On("Deliver", ctx, mock.Anything).Return(&delivery.Result{
		StatusCode: 500,
		Duration:   100 * time.Millisecond,
		Error:      errors.New("server error"),
	})

	// Circuit breaker failure recording
	mockCircuitBreaker.On("RecordFailure", ctx, webhook.EndpointHash).Return(&domain.CircuitBreaker{
		EndpointHash: webhook.EndpointHash,
		State:        domain.CircuitClosed,
	}, nil)

	// Retry strategy
	mockRetryStrategy.On("ShouldRetry", mock.Anything, 500, mock.Anything).Return(true)
	mockRetryStrategy.On("NextRetryAt", webhook.Attempt, domain.CircuitClosed).Return(time.Now().Add(time.Minute))
	mockRetryStrategy.On("MaxAttempts").Return(3)

	// Retry scheduling
	mockHotState.On("EnsureRetryData", ctx, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("ScheduleRetry", ctx, webhook.ID, webhook.Attempt+1, mock.Anything, mock.Anything).Return(nil)

	// Result publishing
	mockPublisher.On("PublishResult", ctx, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("IncrStats", ctx, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("Client").Return(nil)
	mockHotState.On("AddToHLL", ctx, mock.Anything, mock.Anything).Return(nil)

	// DECR must be called exactly once even on failure
	mockHotState.On("DecrInflight", ctx, webhook.EndpointHash).Return(nil).Once()

	// Process the message
	err := w.ProcessMessage(ctx, msg)
	require.NoError(t, err)

	// Verify DECR was called
	mockHotState.AssertExpectations(t)
}

// TestProcessMessage_DecrCalledOnDLQ verifies DECR is called when message goes to DLQ
func TestProcessMessage_DecrCalledOnDLQ(t *testing.T) {
	ctx := context.Background()
	w, mockPublisher, mockHotState, mockCircuitBreaker, mockDeliveryClient, mockRetryStrategy := setupWorkerWithMocks(t)

	webhook := createConcurrencyTestWebhook()
	msg := createConcurrencyTestMessage(webhook)

	// Circuit allows
	mockCircuitBreaker.On("ShouldAllow", ctx, webhook.EndpointHash).Return(true, false, nil)

	// Concurrency check: under threshold
	mockHotState.On("IncrInflight", ctx, webhook.EndpointHash, mock.Anything).Return(int64(1), nil).Once()

	// Idempotency check
	mockHotState.On("CheckAndSetProcessed", ctx, webhook.ID, webhook.Attempt, mock.Anything).Return(true, nil)

	// Status update
	mockHotState.On("SetStatus", ctx, mock.Anything).Return(nil).Twice() // Delivering + Exhausted

	// Delivery fails with non-retryable error (4xx)
	mockDeliveryClient.On("Deliver", ctx, mock.Anything).Return(&delivery.Result{
		StatusCode: 400,
		Duration:   100 * time.Millisecond,
		Error:      errors.New("bad request"),
	})

	// Circuit breaker failure recording
	mockCircuitBreaker.On("RecordFailure", ctx, webhook.EndpointHash).Return(&domain.CircuitBreaker{
		EndpointHash: webhook.EndpointHash,
		State:        domain.CircuitClosed,
	}, nil)

	// Retry strategy says no retry
	mockRetryStrategy.On("ShouldRetry", mock.Anything, 400, mock.Anything).Return(false)

	// DLQ publishing
	mockPublisher.On("PublishDeadLetter", ctx, mock.Anything).Return(nil)

	// Result publishing
	mockPublisher.On("PublishResult", ctx, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("IncrStats", ctx, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("Client").Return(nil)
	mockHotState.On("AddToHLL", ctx, mock.Anything, mock.Anything).Return(nil)

	// DECR must be called exactly once even on DLQ
	mockHotState.On("DecrInflight", ctx, webhook.EndpointHash).Return(nil).Once()

	// Retry cleanup
	mockHotState.On("DeleteRetryData", ctx, webhook.ID).Return(nil)

	// Process the message
	err := w.ProcessMessage(ctx, msg)
	require.NoError(t, err)

	// Verify DECR was called
	mockHotState.AssertExpectations(t)
}

// TestProcessMessage_NoDECRWhenDiverted verifies DECR is NOT called when diverted to slow lane
func TestProcessMessage_NoDECRWhenDiverted(t *testing.T) {
	ctx := context.Background()
	w, mockPublisher, mockHotState, mockCircuitBreaker, _, _ := setupWorkerWithMocks(t)

	webhook := createConcurrencyTestWebhook()
	msg := createConcurrencyTestMessage(webhook)

	// Circuit allows
	mockCircuitBreaker.On("ShouldAllow", ctx, webhook.EndpointHash).Return(true, false, nil)

	// Concurrency check: OVER threshold (51 > 50 default)
	mockHotState.On("IncrInflight", ctx, webhook.EndpointHash, mock.Anything).Return(int64(51), nil).Once()

	// DECR should be called immediately when diverting (to reverse the INCR)
	mockHotState.On("DecrInflight", ctx, webhook.EndpointHash).Return(nil).Once()

	// Idempotency check happens after concurrency check in the actual implementation
	// But since we divert early, it may or may not be called depending on order
	mockHotState.On("CheckAndSetProcessed", ctx, webhook.ID, webhook.Attempt, mock.Anything).Return(true, nil).Maybe()

	// Publish to slow succeeds
	mockPublisher.On("PublishToSlow", ctx, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("IncrStats", ctx, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("Client").Return(nil)
	mockHotState.On("AddToHLL", ctx, mock.Anything, mock.Anything).Return(nil)

	// Process the message
	err := w.ProcessMessage(ctx, msg)
	require.NoError(t, err)

	// Verify expectations
	mockHotState.AssertExpectations(t)
	mockPublisher.AssertExpectations(t)
}

// TestProcessMessage_DivertFailsProceedsWithDelivery verifies delivery proceeds when divert fails
func TestProcessMessage_DivertFailsProceedsWithDelivery(t *testing.T) {
	ctx := context.Background()
	w, mockPublisher, mockHotState, mockCircuitBreaker, mockDeliveryClient, _ := setupWorkerWithMocks(t)

	webhook := createConcurrencyTestWebhook()
	msg := createConcurrencyTestMessage(webhook)

	// Circuit allows
	mockCircuitBreaker.On("ShouldAllow", ctx, webhook.EndpointHash).Return(true, false, nil)

	// Concurrency check: OVER threshold (51 > 50 default)
	mockHotState.On("IncrInflight", ctx, webhook.EndpointHash, mock.Anything).Return(int64(51), nil).Once()

	// DECR should be called immediately when diverting
	mockHotState.On("DecrInflight", ctx, webhook.EndpointHash).Return(nil).Once()

	// Idempotency check
	mockHotState.On("CheckAndSetProcessed", ctx, webhook.ID, webhook.Attempt, mock.Anything).Return(true, nil)

	// Publish to slow FAILS - should re-increment and proceed
	mockPublisher.On("PublishToSlow", ctx, mock.Anything).Return(errors.New("kafka unavailable"))

	// Re-INCR after divert failure
	mockHotState.On("IncrInflight", ctx, webhook.EndpointHash, mock.Anything).Return(int64(52), nil).Once()

	// Status update
	mockHotState.On("SetStatus", ctx, mock.Anything).Return(nil)

	// Delivery succeeds
	mockDeliveryClient.On("Deliver", ctx, mock.Anything).Return(&delivery.Result{
		StatusCode: 200,
		Duration:   100 * time.Millisecond,
	})

	// Circuit breaker success recording
	mockCircuitBreaker.On("RecordSuccess", ctx, webhook.EndpointHash).Return(&domain.CircuitBreaker{
		EndpointHash: webhook.EndpointHash,
		State:        domain.CircuitClosed,
	}, nil)

	// Result publishing
	mockPublisher.On("PublishResult", ctx, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("IncrStats", ctx, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("Client").Return(nil)
	mockHotState.On("AddToHLL", ctx, mock.Anything, mock.Anything).Return(nil)

	// DECR should be called at the end (from defer)
	mockHotState.On("DecrInflight", ctx, webhook.EndpointHash).Return(nil).Once()

	// Retry cleanup
	mockHotState.On("DeleteRetryData", ctx, webhook.ID).Return(nil)

	// Process the message
	err := w.ProcessMessage(ctx, msg)
	require.NoError(t, err)

	// Verify expectations
	mockHotState.AssertExpectations(t)
}
