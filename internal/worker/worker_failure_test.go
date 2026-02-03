//go:build !integration

package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestWorker_ProcessMessage_MalformedJSON tests handling of malformed JSON input
func TestWorker_ProcessMessage_MalformedJSON(t *testing.T) {
	w, _, _, _, _, _, _ := setupWorkerTest()

	msg := createTestMessage([]byte("{invalid-json"))

	// Should return nil (don't retry malformed messages) but log error
	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err) // Returns nil to not retry
}

// TestWorker_ProcessMessage_IdempotencyCheckError tests fail-open behavior when idempotency check fails
func TestWorker_ProcessMessage_IdempotencyCheckError(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, _ := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check fails but worker continues (fail open)
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(false, errors.New("redis connection died"))

	// Circuit breaker allows request
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery succeeds
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 200,
			Duration:   time.Millisecond * 100,
		})

	// Record success
	mockCircuitBreaker.On("RecordSuccess", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	// Stats recording - use Client() for pipelining
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Cleanup retry data
	mockHotState.On("DeleteRetryData", mock.Anything, webhook.ID).Return(nil)

	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)

	mockHotState.AssertExpectations(t)
	mockCircuitBreaker.AssertExpectations(t)
	mockDelivery.AssertExpectations(t)
}

// TestWorker_ProcessMessage_CircuitBreakerError tests fail-open behavior when circuit breaker fails
func TestWorker_ProcessMessage_CircuitBreakerError(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, _ := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker check fails - should still allow request
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(false, false, errors.New("redis timeout"))

	// Delivery succeeds
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 200,
			Duration:   time.Millisecond * 100,
		})

	// Record success
	mockCircuitBreaker.On("RecordSuccess", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Cleanup retry data
	mockHotState.On("DeleteRetryData", mock.Anything, webhook.ID).Return(nil)

	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)

	mockCircuitBreaker.AssertExpectations(t)
}

// TestWorker_ProcessMessage_CircuitOpen tests circuit open - request blocked and retried
func TestWorker_ProcessMessage_CircuitOpen(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, _, mockRetry := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker says NO (circuit open)
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(false, false, nil)

	// Retry scheduling
	mockRetry.On("NextRetryAt", webhook.Attempt, domain.CircuitOpen).
		Return(time.Now().Add(time.Minute))

	// Ensure retry data
	mockHotState.On("EnsureRetryData", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Schedule retry (note: attempt NOT incremented for circuit open)
	mockHotState.On("ScheduleRetry", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything, "circuit_open").
		Return(nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)

	mockCircuitBreaker.AssertExpectations(t)
}

// TestWorker_ProcessMessage_AlreadyProcessed tests duplicate detection
func TestWorker_ProcessMessage_AlreadyProcessed(t *testing.T) {
	w, _, _, mockHotState, _, _, _ := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Already processed - isFirstTime=false, no error
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(false, nil)

	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)

	mockHotState.AssertExpectations(t)
	// Should not call any other mocks since we skip processing
}

// TestWorker_ProcessMessage_DeliveryFailure_Retryable tests retry scheduling on delivery failure
func TestWorker_ProcessMessage_DeliveryFailure_Retryable(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, mockRetry := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery fails with 500 error
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 500,
			Error:      errors.New("internal server error"),
			Duration:   time.Millisecond * 100,
		})

	// Should retry
	mockRetry.On("ShouldRetry", mock.Anything, 500, mock.Anything).Return(true)
	mockRetry.On("NextRetryAt", webhook.Attempt, domain.CircuitClosed).
		Return(time.Now().Add(time.Minute))

	// Record failure
	mockCircuitBreaker.On("RecordFailure", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Schedule retry
	mockHotState.On("EnsureRetryData", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("ScheduleRetry", mock.Anything, webhook.ID, webhook.Attempt+1, mock.Anything, mock.Anything).
		Return(nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)

	mockRetry.AssertExpectations(t)
}

// TestWorker_ProcessMessage_DeliveryFailure_NonRetryable tests DLQ for non-retryable errors
func TestWorker_ProcessMessage_DeliveryFailure_NonRetryable(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, mockRetry := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery fails with 400 error (client error - non-retryable)
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 400,
			Error:      errors.New("bad request"),
			Duration:   time.Millisecond * 100,
		})

	// Should NOT retry
	mockRetry.On("ShouldRetry", mock.Anything, 400, mock.Anything).Return(false)

	// Record failure
	mockCircuitBreaker.On("RecordFailure", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Publish to DLQ
	mockPublisher.On("PublishDeadLetter", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Cleanup retry data
	mockHotState.On("DeleteRetryData", mock.Anything, webhook.ID).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)

	mockPublisher.AssertCalled(t, "PublishDeadLetter", mock.Anything, mock.Anything)
}

// TestWorker_ProcessMessage_ExhaustedAttempts tests DLQ when max attempts reached
func TestWorker_ProcessMessage_ExhaustedAttempts(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, mockRetry := setupWorkerTest()

	webhook := createTestWebhook()
	webhook.Attempt = 2 // Already attempted twice
	webhook.MaxAttempts = 3
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery fails
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 500,
			Error:      errors.New("server error"),
			Duration:   time.Millisecond * 100,
		})

	// Should NOT retry because attempts exhausted
	mockRetry.On("ShouldRetry", mock.Anything, 500, mock.Anything).Return(false)

	// Record failure
	mockCircuitBreaker.On("RecordFailure", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Publish to DLQ
	mockPublisher.On("PublishDeadLetter", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Cleanup retry data
	mockHotState.On("DeleteRetryData", mock.Anything, webhook.ID).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)
}

// TestWorker_ProcessMessage_StatusUpdateError tests graceful handling of status update failures
func TestWorker_ProcessMessage_StatusUpdateError(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, _ := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery succeeds
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 200,
			Duration:   time.Millisecond * 100,
		})

	// Record success
	mockCircuitBreaker.On("RecordSuccess", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Status update FAILS but shouldn't stop processing
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(errors.New("redis unavailable"))

	// Publish result still happens
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Cleanup retry data
	mockHotState.On("DeleteRetryData", mock.Anything, webhook.ID).Return(nil)

	// Should complete without error despite status update failure
	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)
}

// TestWorker_ProcessMessage_PublishResultError tests graceful handling of publish result failures
func TestWorker_ProcessMessage_PublishResultError(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, _ := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery succeeds
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 200,
			Duration:   time.Millisecond * 100,
		})

	// Record success
	mockCircuitBreaker.On("RecordSuccess", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Publish result FAILS but shouldn't stop processing
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(errors.New("kafka unavailable"))

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Cleanup retry data
	mockHotState.On("DeleteRetryData", mock.Anything, webhook.ID).Return(nil)

	// Should complete without error despite publish failure
	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)
}

// TestWorker_ProcessMessage_CircuitOpensDuringFailure tests circuit state transition to open
func TestWorker_ProcessMessage_CircuitOpensDuringFailure(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, mockRetry := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery fails
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 500,
			Error:      errors.New("server error"),
			Duration:   time.Millisecond * 100,
		})

	// Should retry
	mockRetry.On("ShouldRetry", mock.Anything, 500, mock.Anything).Return(true)
	mockRetry.On("NextRetryAt", webhook.Attempt, domain.CircuitOpen).
		Return(time.Now().Add(time.Minute * 5))

	// Record failure - CIRCUIT OPENS
	mockCircuitBreaker.On("RecordFailure", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitOpen}, nil)

	// Schedule retry with circuit open delay
	mockHotState.On("EnsureRetryData", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("ScheduleRetry", mock.Anything, webhook.ID, webhook.Attempt+1, mock.Anything, mock.Anything).
		Return(nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)
}

// TestWorker_ProcessMessage_RetrySchedulingError tests handling of retry scheduling failure
func TestWorker_ProcessMessage_RetrySchedulingError(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, mockRetry := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery fails
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 500,
			Error:      errors.New("server error"),
			Duration:   time.Millisecond * 100,
		})

	// Should retry
	mockRetry.On("ShouldRetry", mock.Anything, 500, mock.Anything).Return(true)
	mockRetry.On("NextRetryAt", webhook.Attempt, domain.CircuitClosed).
		Return(time.Now().Add(time.Minute))

	// Record failure
	mockCircuitBreaker.On("RecordFailure", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Schedule retry FAILS
	mockHotState.On("EnsureRetryData", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("ScheduleRetry", mock.Anything, webhook.ID, webhook.Attempt+1, mock.Anything, mock.Anything).
		Return(errors.New("redis zadd failed"))

	// Status update still happens
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	// Now returns error to NACK message and trigger redelivery (creates backpressure)
	err := w.ProcessMessage(context.Background(), msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schedule retry")
}

// TestWorker_ProcessMessage_ProbeRequest tests handling of probe requests when circuit is half-open
func TestWorker_ProcessMessage_ProbeRequest(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, _ := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows as PROBE (half-open)
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, true, nil) // isProbe=true

	// Delivery succeeds
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 200,
			Duration:   time.Millisecond * 100,
		})

	// Record success
	mockCircuitBreaker.On("RecordSuccess", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Cleanup retry data
	mockHotState.On("DeleteRetryData", mock.Anything, webhook.ID).Return(nil)

	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)
}

// TestWorker_ProcessMessage_DLQPublishError tests handling of DLQ publish failure
func TestWorker_ProcessMessage_DLQPublishError(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, mockRetry := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery fails with 400 (non-retryable)
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 400,
			Error:      errors.New("bad request"),
			Duration:   time.Millisecond * 100,
		})

	// Should NOT retry
	mockRetry.On("ShouldRetry", mock.Anything, 400, mock.Anything).Return(false)

	// Record failure
	mockCircuitBreaker.On("RecordFailure", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Publish to DLQ FAILS - worker logs but doesn't return error
	mockPublisher.On("PublishDeadLetter", mock.Anything, mock.Anything).Return(errors.New("kafka publish failed"))

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Cleanup retry data
	mockHotState.On("DeleteRetryData", mock.Anything, webhook.ID).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	// Worker continues despite DLQ publish failure (error is logged only)
	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)
}

// TestWorker_ProcessMessage_CleanupRetryDataError tests graceful handling of cleanup failure
func TestWorker_ProcessMessage_CleanupRetryDataError(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, _ := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery succeeds
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 200,
			Duration:   time.Millisecond * 100,
		})

	// Record success
	mockCircuitBreaker.On("RecordSuccess", mock.Anything, webhook.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Cleanup retry data FAILS but should be logged, not fail the operation
	mockHotState.On("DeleteRetryData", mock.Anything, webhook.ID).Return(errors.New("redis delete failed"))

	// Should complete without error despite cleanup failure
	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)
}

// TestWorker_ProcessMessage_RecordSuccessError tests graceful handling of circuit success recording failure
func TestWorker_ProcessMessage_RecordSuccessError(t *testing.T) {
	w, _, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, _ := setupWorkerTest()

	webhook := createTestWebhook()
	webhookJSON, _ := json.Marshal(webhook)

	msg := createTestMessage(webhookJSON)

	// Idempotency check passes
	mockHotState.On("CheckAndSetProcessed", mock.Anything, webhook.ID, webhook.Attempt, mock.Anything).
		Return(true, nil)

	// Circuit breaker allows
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, webhook.EndpointHash).
		Return(true, false, nil)

	// Delivery succeeds
	mockDelivery.On("Deliver", mock.Anything, mock.Anything).
		Return(&delivery.Result{
			StatusCode: 200,
			Duration:   time.Millisecond * 100,
		})

	// Record success FAILS but shouldn't stop processing
	mockCircuitBreaker.On("RecordSuccess", mock.Anything, webhook.EndpointHash).
		Return(nil, errors.New("redis error"))

	// Status update
	mockHotState.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	// Publish result
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)

	// Stats recording
	mockHotState.On("Client").Return(nil)
	mockHotState.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Cleanup retry data
	mockHotState.On("DeleteRetryData", mock.Anything, webhook.ID).Return(nil)

	// Should complete without error despite circuit recording failure
	err := w.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err)
}

// createTestMessage creates a broker.Message for testing
func createTestMessage(value []byte) *broker.Message {
	return &broker.Message{
		Value:   value,
		Key:     []byte("test-key"),
		Headers: map[string]string{},
	}
}

// Helper function moved from worker_test.go
func createTestWebhook() *domain.Webhook {
	return &domain.Webhook{
		ID:           "wh_test123",
		ConfigID:     "cfg_test",
		EndpointHash: "hash_abc123",
		Endpoint:     "https://example.com/webhook",
		Payload:      json.RawMessage(`{"test":"data"}`),
		Attempt:      0,
		MaxAttempts:  3,
	}
}

// Hook log output for testing
func init() {
	// Ensure log level is appropriate for tests
	log.Logger = log.Logger.Level(0) // Debug level
}
