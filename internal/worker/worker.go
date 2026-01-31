package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/retry"
)

// Worker processes webhooks from Kafka and delivers them
type Worker struct {
	config    *config.Config
	broker    broker.Broker
	publisher broker.WebhookPublisher
	hotstate  hotstate.HotState
	circuit   hotstate.CircuitBreakerChecker
	delivery  delivery.Deliverer
	retry     retry.Retrier
}

// NewWorker creates a new worker
func NewWorker(
	cfg *config.Config,
	brk broker.Broker,
	pub broker.WebhookPublisher,
	hs hotstate.HotState,
	cb hotstate.CircuitBreakerChecker,
	dc delivery.Deliverer,
	rs retry.Retrier,
) *Worker {
	return &Worker{
		config:    cfg,
		broker:    brk,
		publisher: pub,
		hotstate:  hs,
		circuit:   cb,
		delivery:  dc,
		retry:     rs,
	}
}

// Run starts the worker
func (w *Worker) Run(ctx context.Context) error {
	log.Info().Msg("Worker starting...")

	return w.broker.Subscribe(ctx, w.config.Kafka.Topics.Pending, w.config.Kafka.ConsumerGroup, func(ctx context.Context, msg *broker.Message) error {
		return w.processMessage(ctx, msg)
	})
}

func (w *Worker) processMessage(ctx context.Context, msg *broker.Message) error {
	// Parse webhook
	var webhook domain.Webhook
	if err := json.Unmarshal(msg.Value, &webhook); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal webhook")
		return nil // Don't retry malformed messages
	}

	logger := log.With().
		Str("webhook_id", string(webhook.ID)).
		Str("tenant_id", string(webhook.TenantID)).
		Str("endpoint_hash", string(webhook.EndpointHash)).
		Int("attempt", webhook.Attempt).
		Logger()

	// Check idempotency - have we already processed this exact attempt?
	alreadyProcessed, err := w.hotstate.CheckAndSetProcessed(ctx, webhook.ID, webhook.Attempt, 24*time.Hour)
	if err != nil {
		logger.Warn().Err(err).Msg("Idempotency check failed, proceeding anyway")
	} else if !alreadyProcessed {
		logger.Debug().Msg("Already processed, skipping")
		return nil
	}

	// Check circuit breaker
	allowed, isProbe, err := w.circuit.ShouldAllow(ctx, webhook.EndpointHash)
	if err != nil {
		logger.Warn().Err(err).Msg("Circuit check failed, allowing request")
		allowed = true
	}

	if !allowed {
		logger.Debug().Msg("Circuit open, scheduling retry")
		return w.scheduleRetryForCircuit(ctx, &webhook, domain.CircuitOpen)
	}

	if isProbe {
		logger.Debug().Msg("Probe request (circuit half-open)")
	}

	// Update status to delivering
	w.updateStatus(ctx, &webhook, domain.StateDelivering, nil)

	// Deliver the webhook
	result := w.delivery.Deliver(ctx, &webhook)

	// Build delivery result
	deliveryResult := &domain.DeliveryResult{
		WebhookID:    webhook.ID,
		TenantID:     webhook.TenantID,
		Endpoint:     webhook.Endpoint,
		EndpointHash: webhook.EndpointHash,
		Attempt:      webhook.Attempt,
		Success:      result.Error == nil && domain.IsSuccess(result.StatusCode),
		StatusCode:   result.StatusCode,
		Duration:     result.Duration,
		Timestamp:    time.Now(),
		Webhook:      &webhook,
	}

	if result.Error != nil {
		deliveryResult.Error = result.Error.Error()
	}

	// Handle success
	if deliveryResult.Success {
		logger.Info().
			Int("status_code", result.StatusCode).
			Dur("duration", result.Duration).
			Msg("Delivery succeeded")

		// Record success with circuit breaker
		cb, _ := w.circuit.RecordSuccess(ctx, webhook.EndpointHash)
		if cb != nil && cb.State == domain.CircuitClosed {
			logger.Debug().Msg("Circuit closed")
		}

		// Update status
		w.updateStatus(ctx, &webhook, domain.StateDelivered, deliveryResult)

		// Publish result
		if err := w.publisher.PublishResult(ctx, deliveryResult); err != nil {
			logger.Error().Err(err).Msg("Failed to publish result")
		}

		// Record stats
		w.recordStats(ctx, "delivered", &webhook)

		return nil
	}

	// Handle failure
	logger.Warn().
		Int("status_code", result.StatusCode).
		Str("error", deliveryResult.Error).
		Dur("duration", result.Duration).
		Msg("Delivery failed")

	// Record failure with circuit breaker
	cb, _ := w.circuit.RecordFailure(ctx, webhook.EndpointHash)
	circuitState := domain.CircuitClosed
	if cb != nil {
		circuitState = cb.State
		if cb.State == domain.CircuitOpen {
			logger.Warn().Msg("Circuit opened due to failures")
		}
	}

	// Check if we should retry
	shouldRetry := w.retry.ShouldRetry(&webhook, result.StatusCode, result.Error)
	deliveryResult.ShouldRetry = shouldRetry

	if shouldRetry {
		// Schedule retry
		nextRetryAt := w.retry.NextRetryAt(webhook.Attempt, circuitState)
		deliveryResult.NextRetryAt = nextRetryAt

		webhook.Attempt++
		webhook.ScheduledAt = nextRetryAt

		retryMsg := &hotstate.RetryMessage{
			Webhook:     &webhook,
			ScheduledAt: nextRetryAt,
			Reason:      fmt.Sprintf("status=%d, error=%s", result.StatusCode, deliveryResult.Error),
		}

		if err := w.hotstate.ScheduleRetry(ctx, retryMsg); err != nil {
			logger.Error().Err(err).Msg("Failed to schedule retry")
		} else {
			logger.Info().
				Time("next_retry_at", nextRetryAt).
				Str("circuit_state", string(circuitState)).
				Msg("Retry scheduled")
		}

		// Update status
		w.updateStatus(ctx, &webhook, domain.StateFailed, deliveryResult)

		// Record stats
		w.recordStats(ctx, "failed", &webhook)
	} else {
		// Exhausted retries
		logger.Error().Msg("Retries exhausted, sending to dead letter")

		// Update status
		w.updateStatus(ctx, &webhook, domain.StateExhausted, deliveryResult)

		// Send to dead letter queue
		reason := fmt.Sprintf("exhausted after %d attempts, last_status=%d", webhook.Attempt+1, result.StatusCode)
		if err := w.publisher.PublishDeadLetter(ctx, &webhook, reason); err != nil {
			logger.Error().Err(err).Msg("Failed to publish to dead letter")
		}

		// Record stats
		w.recordStats(ctx, "exhausted", &webhook)
	}

	// Publish result
	if err := w.publisher.PublishResult(ctx, deliveryResult); err != nil {
		logger.Error().Err(err).Msg("Failed to publish result")
	}

	return nil
}

func (w *Worker) scheduleRetryForCircuit(ctx context.Context, webhook *domain.Webhook, circuitState domain.CircuitState) error {
	nextRetryAt := w.retry.NextRetryAt(webhook.Attempt, circuitState)
	webhook.ScheduledAt = nextRetryAt

	retryMsg := &hotstate.RetryMessage{
		Webhook:     webhook,
		ScheduledAt: nextRetryAt,
		Reason:      "circuit_open",
	}

	return w.hotstate.ScheduleRetry(ctx, retryMsg)
}

func (w *Worker) updateStatus(ctx context.Context, webhook *domain.Webhook, state string, result *domain.DeliveryResult) {
	status := &domain.WebhookStatus{
		WebhookID: webhook.ID,
		State:     state,
		Attempts:  webhook.Attempt + 1,
	}

	if result != nil {
		now := result.Timestamp
		status.LastAttemptAt = &now
		status.LastStatusCode = result.StatusCode
		status.LastError = result.Error

		if result.ShouldRetry {
			status.NextRetryAt = &result.NextRetryAt
		}

		if result.Success {
			status.DeliveredAt = &now
		}
	}

	if err := w.hotstate.SetStatus(ctx, status); err != nil {
		log.Error().Err(err).Str("webhook_id", string(webhook.ID)).Msg("Failed to update status")
	}
}

func (w *Worker) recordStats(ctx context.Context, stat string, webhook *domain.Webhook) {
	bucket := time.Now().Format("2006010215") // hourly bucket

	// Increment counter
	if err := w.hotstate.IncrStats(ctx, bucket, map[string]int64{stat: 1}); err != nil {
		log.Warn().Err(err).Msg("Failed to increment stats")
	}

	// Add to HLL for unique endpoints
	if err := w.hotstate.AddToHLL(ctx, "endpoints:"+bucket, string(webhook.EndpointHash)); err != nil {
		log.Warn().Err(err).Msg("Failed to add to HLL")
	}
}

func (w *Worker) GetConfig() *config.Config {
	return w.config
}

func (w *Worker) GetBroker() broker.Broker {
	return w.broker
}

func (w *Worker) GetPublisher() broker.WebhookPublisher {
	return w.publisher
}

func (w *Worker) GetHotState() hotstate.HotState {
	return w.hotstate
}

func (w *Worker) GetCircuit() hotstate.CircuitBreakerChecker {
	return w.circuit
}

func (w *Worker) GetDelivery() delivery.Deliverer {
	return w.delivery
}

func (w *Worker) GetRetry() retry.Retrier {
	return w.retry
}