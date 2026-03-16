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
	"github.com/howk/howk/internal/metrics"
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
	domainLimiter  delivery.DomainLimiter
	retry          retry.Retrier
	scriptEngine   *script.Engine
	scriptConsumer *script.Consumer
}

// WorkerOption is a functional option for configuring a Worker at construction time.
// Using options eliminates fragile two-phase init patterns where callers must remember
// to invoke setters before Run().
type WorkerOption func(*Worker)

// WithScriptConsumer injects a script.Consumer into the Worker at construction time.
// This is the preferred way to wire up script synchronization; it avoids the
// post-construction setter call that could silently be forgotten.
func WithScriptConsumer(consumer *script.Consumer) WorkerOption {
	return func(w *Worker) {
		w.scriptConsumer = consumer
	}
}

// WithDeliveryClient sets the HTTP delivery client.  When omitted the Worker
// will panic on the first delivery attempt, so callers in production must
// always supply this option.
func WithDeliveryClient(d delivery.Deliverer) WorkerOption {
	return func(w *Worker) {
		w.delivery = d
	}
}

// WithRetryStrategy sets the retry strategy used to schedule back-off delays
// and determine when a webhook is exhausted.
func WithRetryStrategy(r retry.Retrier) WorkerOption {
	return func(w *Worker) {
		w.retry = r
	}
}

// WithScriptEngine sets the Lua script engine.  When omitted, webhooks that
// carry a ScriptHash will be sent to the DLQ (script execution is unavailable).
func WithScriptEngine(e *script.Engine) WorkerOption {
	return func(w *Worker) {
		w.scriptEngine = e
	}
}

// WithDomainLimiter sets the per-domain concurrency limiter.  Passing nil
// (or omitting the option) disables domain-level rate limiting.
func WithDomainLimiter(dl delivery.DomainLimiter) WorkerOption {
	return func(w *Worker) {
		w.domainLimiter = dl
	}
}

// NewWorker creates a new worker.
//
// Required positional dependencies (config, broker, publisher, hotstate) must
// always be provided.  Optional dependencies (delivery client, retry strategy,
// script engine, domain limiter) are injected via WorkerOption functions such as
// WithDeliveryClient, WithRetryStrategy, WithScriptEngine, and WithDomainLimiter.
func NewWorker(
	cfg *config.Config,
	kafkaBroker broker.Broker,
	publisher broker.WebhookPublisher,
	hotState hotstate.HotState,
	opts ...WorkerOption,
) *Worker {
	w := &Worker{
		config:    cfg,
		broker:    kafkaBroker,
		publisher: publisher,
		hotstate:  hotState,
		circuit:   hotState.CircuitBreaker(),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// GetScriptConsumer returns the script.Consumer. Used by the process owner for
// graceful shutdown sequencing.
func (w *Worker) GetScriptConsumer() *script.Consumer {
	return w.scriptConsumer
}

// Run starts the worker
func (w *Worker) Run(ctx context.Context) error {
	log.Info().Msg("Worker starting...")

	// Start script consumer if configured
	if w.scriptConsumer != nil {
		if err := w.scriptConsumer.Start(ctx); err != nil {
			log.Error().Err(err).Msg("Failed to start script consumer, continuing with lazy loading only")
			// Continue anyway - scripts can still be loaded lazily from Redis
		} else {
			log.Info().Msg("Script consumer started - scripts will be synchronized from Kafka")
		}
	}

	return w.broker.Subscribe(ctx, w.config.Kafka.Topics.Pending, w.config.Kafka.ConsumerGroup, func(ctx context.Context, msg *broker.Message) error {
		return w.processMessage(ctx, msg, false)
	})
}

func (w *Worker) processMessage(ctx context.Context, msg *broker.Message, isSlowLane bool) error {
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
		metrics.DeliveriesTotal.WithLabelValues("circuit_open").Inc()
		return w.scheduleRetryForCircuit(ctx, &webhook, domain.CircuitOpen)
	}

	if isProbe {
		logger.Debug().Msg("Probe request (circuit half-open)")
	}

	// Check domain-level concurrency.
	// runConcurrencyGate encapsulates the acquire→divert-or-NACK pattern used by
	// both the domain limiter and the inflight counter; see its definition below.
	domainAcquired, err := w.runConcurrencyGate(ctx, &webhook, isSlowLane, ConcurrencyGate{
		// active guards against a nil domainLimiter so the gate is a no-op when absent
		Active: w.domainLimiter != nil,
		TryAcquire: func() (bool, error) {
			return w.domainLimiter.TryAcquire(ctx, webhook.Endpoint)
		},
		Release: func() error {
			return w.domainLimiter.Release(ctx, webhook.Endpoint)
		},
		// Domain gate has no fail-open re-acquire; if divert fails we proceed without re-taking the slot.
		ReacquireOnDivertFail: nil,
		NackErr:    fmt.Errorf("domain concurrency limit reached in slow lane: %s", webhook.Endpoint),
		LogDivert:  "Domain concurrency limit reached, diverting to slow lane",
		LogSatured: "Domain concurrency limit reached even in slow lane, NACKing for retry",
	})
	if err != nil {
		// err is either the NACK sentinel or the gated divert-succeeded signal
		if err == ErrDiverted {
			w.recordStats(ctx, "diverted", &webhook)
			return nil
		}
		return err
	}
	defer func() {
		if domainAcquired && w.domainLimiter != nil {
			if err := w.domainLimiter.Release(ctx, webhook.Endpoint); err != nil {
				logger.Warn().Err(err).Msg("Failed to release domain concurrency slot")
			}
		}
	}()

	// Check in-flight concurrency (penalty box gate).
	inflightAcquired, err := w.runConcurrencyGate(ctx, &webhook, isSlowLane, ConcurrencyGate{
		Active: true,
		TryAcquire: func() (bool, error) {
			count, err := w.hotstate.IncrInflight(ctx, webhook.EndpointHash, w.config.Concurrency.InflightTTL)
			if err != nil {
				return false, err
			}
			if count > int64(w.config.Concurrency.MaxInflightPerEndpoint) {
				// Undo the increment; the gate will handle the divert/NACK path.
				w.hotstate.DecrInflight(ctx, webhook.EndpointHash)
				return false, nil
			}
			return true, nil
		},
		Release: func() error {
			return w.hotstate.DecrInflight(ctx, webhook.EndpointHash)
		},
		// Fail-open: if PublishToSlow fails, re-acquire the inflight slot so the
		// deferred release remains balanced.
		ReacquireOnDivertFail: func() {
			w.hotstate.IncrInflight(ctx, webhook.EndpointHash, w.config.Concurrency.InflightTTL)
		},
		NackErr: fmt.Errorf("endpoint saturated in slow lane: threshold=%d",
			w.config.Concurrency.MaxInflightPerEndpoint),
		LogDivert:  "Endpoint over concurrency threshold, diverting to slow lane",
		LogSatured: "Endpoint saturated even in slow lane, NACKing for retry",
	})
	if err != nil {
		if err == ErrDiverted {
			w.recordStats(ctx, "diverted", &webhook)
			return nil
		}
		return err
	}
	defer func() {
		if inflightAcquired {
			if err := w.hotstate.DecrInflight(ctx, webhook.EndpointHash); err != nil {
				logger.Warn().Err(err).Msg("Failed to decrement inflight counter")
			}
		}
	}()

	// Update status to delivering
	w.updateStatus(ctx, &webhook, domain.StateDelivering, nil)

	// Execute script transformation if configured
	if webhook.ScriptHash != "" {
		// Safety: DLQ if scripts disabled but transformation expected
		if !w.config.Lua.Enabled {
			logger.Warn().Msg("Script execution disabled but ScriptHash set, sending to DLQ")
			return w.sendToDLQForScriptDisabled(ctx, &webhook)
		}

		// Ensure the script is in the engine's in-memory cache before executing.
		// EnsureScript fetches from Redis when the cache is cold (e.g. worker
		// restarted before the Kafka script consumer replayed the latest config).
		w.scriptEngine.EnsureScript(ctx, webhook.ConfigID, w.hotstate)

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
		metrics.DeliveriesTotal.WithLabelValues("success").Inc()
		metrics.DeliveryDuration.Observe(result.Duration.Seconds())

		logger.Info().
			Int("status_code", result.StatusCode).
			Dur("duration", result.Duration).
			Msg("Delivery succeeded")

		// Record success with circuit breaker
		cb, err := w.circuit.RecordSuccess(ctx, webhook.EndpointHash)
		if err != nil {
			logger.Warn().Err(err).Msg("failed to record circuit success")
		}
		if cb != nil && cb.State == domain.CircuitClosed {
			logger.Debug().Msg("Circuit closed")
		}

		// Update status
		w.updateStatus(ctx, &webhook, domain.StateDelivered, deliveryResult)

		// Publish result
		if err := w.publisher.PublishResult(ctx, deliveryResult); err != nil {
			logger.Error().Err(err).Msg("Failed to publish result")
		}

		// Record stats for the fast lane; slow lane adds its own counter below
		w.recordStats(ctx, "delivered", &webhook)
		if isSlowLane {
			w.recordStats(ctx, "slow_delivered", &webhook)
		}

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
	cb, err := w.circuit.RecordFailure(ctx, webhook.EndpointHash)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to record circuit failure")
	}
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
		metrics.DeliveriesTotal.WithLabelValues("failure").Inc()
		metrics.DeliveryDuration.Observe(result.Duration.Seconds())

		// Schedule retry
		nextRetryAt := w.retry.NextRetryAt(webhook.Attempt, circuitState)
		deliveryResult.NextRetryAt = nextRetryAt

		webhook.Attempt++
		webhook.ScheduledAt = nextRetryAt

		if err := w.scheduleRetry(ctx, &webhook, nextRetryAt, fmt.Sprintf("status=%d, error=%s", result.StatusCode, deliveryResult.Error)); err != nil {
			// Return error to NACK message and trigger redelivery
			// This creates backpressure when Redis is down
			// Note: If delivery succeeded before this failure, that retry may be lost
			// (idempotency will skip on redelivery) - reconciler handles this edge case
			logger.Error().Err(err).Msg("Failed to schedule retry, will redeliver from Kafka")
			return fmt.Errorf("schedule retry: %w", err)
		}

		logger.Info().
			Time("next_retry_at", nextRetryAt).
			Str("circuit_state", string(circuitState)).
			Msg("Retry scheduled")

		// Update status
		w.updateStatus(ctx, &webhook, domain.StateFailed, deliveryResult)

		// Record stats
		w.recordStats(ctx, "failed", &webhook)
	} else {
		metrics.DeliveriesTotal.WithLabelValues("exhausted").Inc()
		metrics.DeliveryDuration.Observe(result.Duration.Seconds())

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

	if err := w.scheduleRetry(ctx, webhook, nextRetryAt, "circuit_open"); err != nil {
		return fmt.Errorf("schedule retry for circuit: %w", err)
	}
	return nil
}

func (w *Worker) updateStatus(ctx context.Context, webhook *domain.Webhook, state domain.WebhookState, result *domain.DeliveryResult) {
	now := time.Now()
	status := &domain.WebhookStatus{
		WebhookID:   webhook.ID,
		State:       state,
		Attempts:    webhook.Attempt + 1,
		UpdatedAtNs: now.UnixNano(), // LWW timestamp for conflict resolution
	}

	if result != nil {
		attemptTime := result.Timestamp
		status.LastAttemptAt = &attemptTime
		status.LastStatusCode = result.StatusCode
		status.LastError = result.Error

		if result.ShouldRetry {
			status.NextRetryAt = &result.NextRetryAt
		}

		if result.Success {
			status.DeliveredAt = &attemptTime
		}
	}

	if err := w.hotstate.SetStatus(ctx, status); err != nil {
		log.Error().Err(err).Str("webhook_id", string(webhook.ID)).Msg("Failed to update status")
	}

	// Publish state snapshot to compacted topic for zero-maintenance reconciliation
	// This runs asynchronously to avoid blocking the worker flow
	w.publishStateSnapshot(ctx, webhook, state, status)
}

// publishStateSnapshot publishes the webhook state to the compacted Kafka topic.
// Terminal states (delivered/exhausted) publish tombstones to remove from topic.
// Failed states publish full snapshots to enable Redis reconstruction on restart.
func (w *Worker) publishStateSnapshot(ctx context.Context, webhook *domain.Webhook, state domain.WebhookState, status *domain.WebhookStatus) {
	// Only publish for terminal or retryable-failed states
	// Skip transient states (delivering, pending) to reduce churn
	if state != domain.StateDelivered && state != domain.StateExhausted && state != domain.StateFailed {
		return
	}

	// Use a goroutine to avoid blocking the main worker flow
	go func() {
		// Create a separate context with timeout to ensure publish completes
		// even if parent context is cancelled
		pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		switch state {
		case domain.StateDelivered, domain.StateExhausted:
			// Terminal state: Send tombstone to remove from compacted topic
			if err := w.publisher.PublishStateTombstone(pubCtx, webhook.ID); err != nil {
				log.Warn().Err(err).Str("webhook_id", string(webhook.ID)).Str("state", string(state)).
					Msg("Failed to publish state tombstone")
			}

		case domain.StateFailed:
			// Retryable failure: Snapshot the active state for reconstruction
			// Use nanosecond timestamp for LWW conflict resolution
			snapshot := &domain.WebhookStateSnapshot{
				WebhookID:      webhook.ID,
				ConfigID:       webhook.ConfigID,
				Endpoint:       webhook.Endpoint,
				EndpointHash:   webhook.EndpointHash,
				Payload:        webhook.Payload,
				Headers:        webhook.Headers,
				IdempotencyKey: webhook.IdempotencyKey,
				SigningSecret:  webhook.SigningSecret,
				ScriptHash:     webhook.ScriptHash,
				State:          state,
				Attempt:        webhook.Attempt,
				MaxAttempts:    webhook.MaxAttempts,
				CreatedAt:      webhook.CreatedAt,
				NextRetryAt:    status.NextRetryAt,
				LastError:      status.LastError,
				UpdatedAtNs:    time.Now().UnixNano(), // LWW timestamp
			}

			if err := w.publisher.PublishState(pubCtx, snapshot); err != nil {
				log.Warn().Err(err).Str("webhook_id", string(webhook.ID)).
					Msg("Failed to publish state snapshot")
			}
		}
	}()
}

func (w *Worker) recordStats(ctx context.Context, stat string, webhook *domain.Webhook) {
	bucket := time.Now().Format("2006010215") // hourly bucket

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
			// Return error to NACK message and trigger redelivery
			logger.Error().Err(err).Msg("Failed to schedule retry for script error, will redeliver from Kafka")
			return fmt.Errorf("schedule retry for script error: %w", err)
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
		if scriptError.Message == "crypto key not found" || (scriptError.Cause != nil && scriptError.Cause.Error() == "key not found") {
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

// ErrDiverted is a sentinel error returned by runConcurrencyGate when the
// webhook was successfully forwarded to the slow lane.  Callers should record
// stats and return nil to ACK the original message.  Exported so tests can
// distinguish divert from NACK without depending on the error message string.
var ErrDiverted = fmt.Errorf("diverted to slow lane")

// ConcurrencyGate holds the callbacks and metadata for one concurrency gate
// (domain limiter or inflight counter).  Both gates share the same acquire →
// divert-or-NACK logic, differing only in how the slot is acquired/released
// and whether a fail-open re-acquire is needed.
//
// Fields are exported so that tests in the worker_test package can construct
// gate configurations via RunConcurrencyGate without reflection.
type ConcurrencyGate struct {
	// Active skips the gate entirely when false (e.g. no domain limiter configured).
	Active bool

	// TryAcquire attempts to claim a slot.  Returns (true, nil) on success,
	// (false, nil) when at capacity, or (false, err) on infrastructure failure
	// (treated as fail-open: proceed without a slot).
	TryAcquire func() (bool, error)

	// Release frees the slot on all exit paths after a successful acquire.
	Release func() error

	// ReacquireOnDivertFail is called when PublishToSlow fails so the deferred
	// release remains balanced.  May be nil for gates that skip the re-acquire.
	ReacquireOnDivertFail func()

	// NackErr is returned when the webhook is in the slow lane and still over
	// threshold, signalling Kafka to redeliver after backoff.
	NackErr error

	// LogDivert / LogSatured are the log messages emitted before divert / NACK.
	LogDivert  string
	LogSatured string
}

// runConcurrencyGate executes the acquire→divert-or-NACK pattern for a single
// concurrency gate.  It returns:
//   - (true,  nil)          — slot acquired; caller must release via defer
//   - (false, nil)          — gate inactive or infrastructure error (fail-open)
//   - (false, ErrDiverted)  — message forwarded to slow lane; caller should return nil after recording stats
//   - (false, NackErr)      — slow-lane saturation; caller must propagate the error to NACK
func (w *Worker) runConcurrencyGate(
	ctx context.Context,
	webhook *domain.Webhook,
	isSlowLane bool,
	gate ConcurrencyGate,
) (acquired bool, err error) {
	if !gate.Active {
		return false, nil
	}

	ok, acquireErr := gate.TryAcquire()
	if acquireErr != nil {
		// Infrastructure failure → fail-open (proceed without holding a slot)
		log.Warn().Err(acquireErr).Msg("Concurrency gate check failed, proceeding")
		return false, nil
	}

	if ok {
		return true, nil
	}

	// At capacity: divert or NACK depending on lane
	if isSlowLane {
		log.Info().Msg(gate.LogSatured)
		return false, gate.NackErr
	}

	log.Info().Msg(gate.LogDivert)

	if publishErr := w.publisher.PublishToSlow(ctx, webhook); publishErr != nil {
		log.Error().Err(publishErr).Msg("Failed to divert to slow lane, proceeding with delivery")
		// Fail-open: if a re-acquire func is provided, restore the slot so the
		// deferred release in the caller stays balanced.
		if gate.ReacquireOnDivertFail != nil {
			gate.ReacquireOnDivertFail()
			return true, nil
		}
		// No re-acquire needed; proceed without holding a slot.
		return false, nil
	}

	return false, ErrDiverted
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
