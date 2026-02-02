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
	"github.com/howk/howk/internal/script"
)

// Worker processes webhooks from Kafka and delivers them
type Worker struct {
	config         *config.Config
	broker         broker.Broker
	publisher      broker.WebhookPublisher
	hotstate       hotstate.HotState
	circuit        hotstate.CircuitBreakerChecker
	delivery       delivery.Deliverer
	retry          retry.Retrier
	scriptEngine   *script.Engine
	scriptConsumer *script.ScriptConsumer
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
	se *script.Engine,
) *Worker {
	return &Worker{
		config:       cfg,
		broker:       brk,
		publisher:    pub,
		hotstate:     hs,
		circuit:      cb,
		delivery:     dc,
		retry:        rs,
		scriptEngine: se,
	}
}

// SetScriptConsumer sets the script consumer for the worker
// This should be called before Run() if script synchronization is needed
func (w *Worker) SetScriptConsumer(consumer *script.ScriptConsumer) {
	w.scriptConsumer = consumer
}

// GetScriptConsumer returns the script consumer
func (w *Worker) GetScriptConsumer() *script.ScriptConsumer {
	return w.scriptConsumer
}

// Run starts the worker
func (w *Worker) Run(ctx context.Context) error {
	log.Info().Msg("Worker starting...")

	// Start script consumer if configured
	if w.scriptConsumer != nil {
		if err := w.scriptConsumer.Start(ctx); err != nil {
			log.Error().Err(err).Msg("Failed to start script consumer")
			// Continue anyway - scripts can still be loaded lazily from Redis
		}
	}

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
		Str("config_id", string(webhook.ConfigID)).
		Str("endpoint_hash", string(webhook.EndpointHash)).
		Int("attempt", webhook.Attempt).
		Logger()

	// Check idempotency - have we already processed this exact attempt?
	isFirstTime, err := w.hotstate.CheckAndSetProcessed(ctx, webhook.ID, webhook.Attempt, w.config.TTL.IdempotencyTTL)
	if err != nil {
		logger.Warn().Err(err).Msg("Idempotency check failed, proceeding anyway")
	} else if !isFirstTime {
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

	// Execute script transformation if configured
	if webhook.ScriptHash != "" {
		// Safety: DLQ if scripts disabled but transformation expected
		if !w.config.Lua.Enabled {
			logger.Warn().Msg("Script execution disabled but ScriptHash set, sending to DLQ")
			return w.sendToDLQForScriptDisabled(ctx, &webhook)
		}

		// Try to load script from Redis if not in cache
		// This handles the case where the worker starts before scripts are loaded
		scriptJSON, err := w.hotstate.GetScript(ctx, webhook.ConfigID)
		if err == nil && scriptJSON != "" {
			// Parse and load into script engine's loader
			var scriptConfig script.ScriptConfig
			if err := json.Unmarshal([]byte(scriptJSON), &scriptConfig); err == nil {
				w.scriptEngine.GetLoader().SetScript(&scriptConfig)
				logger.Debug().
					Str("config_id", string(webhook.ConfigID)).
					Str("script_hash", scriptConfig.Hash).
					Msg("Loaded script from Redis into cache")
			}
		}

		// Execute script
		logger.Debug().Str("script_hash", webhook.ScriptHash).Msg("Executing script transformation")
		transformed, scriptErr := w.scriptEngine.Execute(ctx, &webhook)
		if scriptErr != nil {
			logger.Error().Err(scriptErr).Msg("Script execution failed")
			return w.handleScriptError(ctx, &webhook, scriptErr)
		}

		// Use transformed webhook for delivery
		webhook = *transformed
		logger.Debug().Msg("Script transformation applied successfully")
	}

	// Deliver the webhook
	result := w.delivery.Deliver(ctx, &webhook)

	// Build delivery result
	deliveryResult := &domain.DeliveryResult{
		WebhookID:    webhook.ID,
		ConfigID:     webhook.ConfigID,
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

		// Cleanup retry data on success (terminal state)
		w.cleanupRetryData(ctx, &webhook)

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

		if err := w.scheduleRetry(ctx, &webhook, nextRetryAt, fmt.Sprintf("status=%d, error=%s", result.StatusCode, deliveryResult.Error)); err != nil {
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
		// No retry - send to dead letter queue
		// Determine the reason type
		var reasonType domain.DeadLetterReason
		var reason string

		// Check if we exhausted attempts or hit an unrecoverable error
		if webhook.Attempt >= webhook.MaxAttempts {
			reasonType = domain.DLQReasonExhausted
			reason = fmt.Sprintf("exhausted after %d attempts, last_status=%d", webhook.Attempt+1, result.StatusCode)
			logger.Error().Msg("Retries exhausted, sending to dead letter")
		} else {
			// Non-retryable error (e.g., 4xx client error except 408/429)
			reasonType = domain.DLQReasonUnrecoverable
			reason = fmt.Sprintf("unrecoverable error: status=%d", result.StatusCode)
			logger.Error().Msg("Unrecoverable error, sending to dead letter")
		}

		// Update status
		w.updateStatus(ctx, &webhook, domain.StateExhausted, deliveryResult)

		// Send to dead letter queue
		deadLetter := &domain.DeadLetter{
			Webhook:    &webhook,
			Reason:     reason,
			ReasonType: reasonType,
			LastError:  deliveryResult.Error,
			StatusCode: result.StatusCode,
			Time:       time.Now(),
		}

		if err := w.publisher.PublishDeadLetter(ctx, deadLetter); err != nil {
			logger.Error().Err(err).Msg("Failed to publish to dead letter")
		}

		// Record stats
		w.recordStats(ctx, "exhausted", &webhook)

		// Cleanup retry data on exhaustion (terminal state)
		w.cleanupRetryData(ctx, &webhook)
	}

	// Publish result
	if err := w.publisher.PublishResult(ctx, deliveryResult); err != nil {
		logger.Error().Err(err).Msg("Failed to publish result")
	}

	return nil
}

// scheduleRetry stores retry data and schedules the reference in ZSET
func (w *Worker) scheduleRetry(ctx context.Context, webhook *domain.Webhook, scheduledAt time.Time, reason string) error {
	// 1. Ensure Data (Lazy) - only writes if missing, otherwise refreshes TTL (no compression/payload transfer)
	if err := w.hotstate.EnsureRetryData(ctx, webhook, w.config.TTL.RetryDataTTL); err != nil {
		return err
	}
	// 2. Schedule Reference in ZSET
	return w.hotstate.ScheduleRetry(ctx, webhook.ID, webhook.Attempt, scheduledAt, reason)
}

// cleanupRetryData deletes the retry data on terminal states (success or DLQ)
func (w *Worker) cleanupRetryData(ctx context.Context, webhook *domain.Webhook) {
	if err := w.hotstate.DeleteRetryData(ctx, webhook.ID); err != nil {
		log.Warn().Err(err).Str("webhook_id", string(webhook.ID)).Msg("Failed to cleanup retry data")
	}
}

func (w *Worker) scheduleRetryForCircuit(ctx context.Context, webhook *domain.Webhook, circuitState domain.CircuitState) error {
	nextRetryAt := w.retry.NextRetryAt(webhook.Attempt, circuitState)
	webhook.ScheduledAt = nextRetryAt

	return w.scheduleRetry(ctx, webhook, nextRetryAt, "circuit_open")
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

	// Use Redis pipelining for better performance when a client is available
	if client := w.hotstate.Client(); client != nil {
		pipe := client.Pipeline()

		// Batch both operations
		statsKey := fmt.Sprintf("stats:%s:%s", stat, bucket)
		pipe.IncrBy(ctx, statsKey, 1)
		pipe.Expire(ctx, statsKey, w.config.TTL.StatsTTL)

		hllKey := fmt.Sprintf("hll:endpoints:%s", bucket)
		pipe.PFAdd(ctx, hllKey, string(webhook.EndpointHash))
		pipe.Expire(ctx, hllKey, w.config.TTL.StatsTTL)

		if _, err := pipe.Exec(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to record stats")
		}
		return
	}

	// Fallback to individual operations if pipeline unavailable (e.g., mocks)
	if err := w.hotstate.IncrStats(ctx, bucket, map[string]int64{stat: 1}); err != nil {
		log.Warn().Err(err).Msg("Failed to increment stats")
	}

	if err := w.hotstate.AddToHLL(ctx, "endpoints:"+bucket, string(webhook.EndpointHash)); err != nil {
		log.Warn().Err(err).Msg("Failed to add to HLL")
	}
}

// handleScriptError handles errors from script execution
func (w *Worker) handleScriptError(ctx context.Context, webhook *domain.Webhook, scriptErr error) error {
	logger := log.With().
		Str("webhook_id", string(webhook.ID)).
		Str("config_id", string(webhook.ConfigID)).
		Logger()

	// Extract script error type
	scriptError, ok := scriptErr.(*script.ScriptError)
	if !ok {
		// Unknown error type, send to DLQ
		logger.Error().Err(scriptErr).Msg("Unknown script error type, sending to DLQ")
		return w.sendToDLQ(ctx, webhook, domain.DLQReasonScriptRuntimeError, scriptErr.Error())
	}

	// Determine if error is retryable
	if scriptError.Type.IsRetryable() {
		// Transient error (Redis/HTTP unavailable) - retry
		logger.Warn().Str("error_type", string(scriptError.Type)).Msg("Retryable script error, scheduling retry")

		// Schedule retry with normal backoff
		circuitState := domain.CircuitClosed // Script errors don't affect circuit
		nextRetryAt := w.retry.NextRetryAt(webhook.Attempt, circuitState)
		webhook.Attempt++
		webhook.ScheduledAt = nextRetryAt

		if err := w.scheduleRetry(ctx, webhook, nextRetryAt, fmt.Sprintf("script_error: %s", scriptError.Type)); err != nil {
			logger.Error().Err(err).Msg("Failed to schedule retry for script error")
		}

		// Update status to failed
		w.updateStatus(ctx, webhook, domain.StateFailed, &domain.DeliveryResult{
			WebhookID:   webhook.ID,
			ConfigID:    webhook.ConfigID,
			Endpoint:    webhook.Endpoint,
			Attempt:     webhook.Attempt,
			Success:     false,
			Error:       scriptErr.Error(),
			ShouldRetry: true,
			NextRetryAt: nextRetryAt,
			Timestamp:   time.Now(),
		})

		w.recordStats(ctx, "failed", webhook)
		return nil
	}

	// Non-retryable script error - send to DLQ
	logger.Error().Str("error_type", string(scriptError.Type)).Msg("Non-retryable script error, sending to DLQ")

	// Map script error type to DLQ reason
	var dlqReason domain.DeadLetterReason
	switch scriptError.Type {
	case script.ScriptErrorNotFound:
		dlqReason = domain.DLQReasonScriptNotFound
	case script.ScriptErrorSyntax:
		dlqReason = domain.DLQReasonScriptSyntaxError
	case script.ScriptErrorRuntime:
		dlqReason = domain.DLQReasonScriptRuntimeError
	case script.ScriptErrorTimeout:
		dlqReason = domain.DLQReasonScriptTimeout
	case script.ScriptErrorMemoryLimit:
		dlqReason = domain.DLQReasonScriptMemoryLimit
	case script.ScriptErrorModuleCrypto:
		// Check if crypto key not found
		if scriptError.Message == "crypto key not found" || scriptError.Cause != nil && scriptError.Cause.Error() == "key not found" {
			dlqReason = domain.DLQReasonCryptoKeyNotFound
		} else {
			dlqReason = domain.DLQReasonScriptRuntimeError
		}
	case script.ScriptErrorInvalidOutput:
		dlqReason = domain.DLQReasonScriptInvalidOutput
	default:
		dlqReason = domain.DLQReasonScriptRuntimeError
	}

	return w.sendToDLQ(ctx, webhook, dlqReason, scriptErr.Error())
}

// sendToDLQForScriptDisabled sends a webhook to DLQ when scripts are disabled
func (w *Worker) sendToDLQForScriptDisabled(ctx context.Context, webhook *domain.Webhook) error {
	return w.sendToDLQ(ctx, webhook, domain.DLQReasonScriptDisabled,
		"Script execution is disabled but ScriptHash is set - this would leak raw/untransformed payload")
}

// sendToDLQ is a helper to send webhooks to the dead letter queue
func (w *Worker) sendToDLQ(ctx context.Context, webhook *domain.Webhook, reasonType domain.DeadLetterReason, reasonMsg string) error {
	logger := log.With().
		Str("webhook_id", string(webhook.ID)).
		Str("reason", string(reasonType)).
		Logger()

	deadLetter := &domain.DeadLetter{
		Webhook:    webhook,
		Reason:     reasonMsg,
		ReasonType: reasonType,
		Time:       time.Now(),
	}

	if err := w.publisher.PublishDeadLetter(ctx, deadLetter); err != nil {
		logger.Error().Err(err).Msg("Failed to publish to dead letter queue")
		return err
	}

	// Update status to exhausted
	w.updateStatus(ctx, webhook, domain.StateExhausted, &domain.DeliveryResult{
		WebhookID: webhook.ID,
		ConfigID:  webhook.ConfigID,
		Endpoint:  webhook.Endpoint,
		Attempt:   webhook.Attempt,
		Success:   false,
		Error:     reasonMsg,
		Timestamp: time.Now(),
	})

	w.recordStats(ctx, "exhausted", webhook)

	logger.Warn().Msg("Webhook sent to dead letter queue")
	return nil
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
