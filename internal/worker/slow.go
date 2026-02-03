package worker

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

// SlowWorker consumes from howk.slow and processes at a rate-limited pace.
// It reuses the core Worker logic but wraps it with a rate limiter.
type SlowWorker struct {
	worker *Worker
	broker broker.Broker
	config *config.Config
}

// NewSlowWorker creates a new slow worker that wraps an existing Worker
func NewSlowWorker(w *Worker, brk broker.Broker, cfg *config.Config) *SlowWorker {
	return &SlowWorker{
		worker: w,
		broker: brk,
		config: cfg,
	}
}

// Run subscribes to howk.slow with a separate consumer group.
// Each message is processed through the same processMessage logic as the
// fast lane, but delivery is throttled via a time.Ticker.
func (sw *SlowWorker) Run(ctx context.Context) error {
	log.Info().Msg("Slow worker starting...")

	// Create rate limiter ticker based on configured slow lane rate
	interval := time.Second / time.Duration(sw.config.Concurrency.SlowLaneRate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	return sw.broker.Subscribe(ctx, sw.config.Kafka.Topics.Slow, sw.config.Kafka.ConsumerGroup+"-slow", func(ctx context.Context, msg *broker.Message) error {
		// Wait for rate limiter tick before processing
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Continue with processing
		}

		return sw.processSlowMessage(ctx, msg)
	})
}

// processSlowMessage processes a message from the slow lane.
// It wraps the standard processMessage but records slow_delivered stat on success.
func (sw *SlowWorker) processSlowMessage(ctx context.Context, msg *broker.Message) error {
	// Parse webhook first to get metadata for logging
	var webhook domain.Webhook
	if err := json.Unmarshal(msg.Value, &webhook); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal webhook in slow lane")
		return nil // Don't retry malformed messages
	}

	logger := log.With().
		Str("webhook_id", string(webhook.ID)).
		Str("config_id", string(webhook.ConfigID)).
		Str("endpoint_hash", string(webhook.EndpointHash)).
		Int("attempt", webhook.Attempt).
		Str("lane", "slow").
		Logger()

	// Process through the same logic as fast lane
	// The concurrency check in processMessage will re-check the endpoint.
	// If the endpoint has recovered (inflight < threshold), the message proceeds normally.
	// If still over threshold, it will be diverted back to slow topic (backpressure loop).
	err := sw.worker.processMessage(ctx, msg)

	// Record slow_delivered stat on successful processing
	// Note: processMessage returns nil on success or terminal failure (DLQ)
	// We only record stats for successful deliveries, not for diverted/re-diverted messages
	if err == nil {
		sw.worker.recordStats(ctx, "slow_delivered", &webhook)
		logger.Debug().Msg("Slow lane delivery completed")
	} else {
		logger.Warn().Err(err).Msg("Slow lane delivery error")
	}

	return err
}
