package worker

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
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

// ProcessSlowMessageForTest exports processSlowMessage for testing.
// This is a test helper that should only be used in tests.
func (sw *SlowWorker) ProcessSlowMessageForTest(ctx context.Context, msg *broker.Message) error {
	return sw.processSlowMessage(ctx, msg)
}

// processSlowMessage processes a message from the slow lane.
// Stat recording (including slow_delivered) is handled inside processMessage
// when isSlowLane=true, so there is no need to parse the webhook here.
func (sw *SlowWorker) processSlowMessage(ctx context.Context, msg *broker.Message) error {
	// Process through the same logic as fast lane, but pass isSlowLane=true.
	// processMessage handles slow_delivered stat recording on success.
	// This prevents infinite loops: if still over threshold in slow lane,
	// the message will be NACKed and retried via Kafka's consumer backoff.
	err := sw.worker.processMessage(ctx, msg, true)
	if err != nil {
		log.Warn().Err(err).Msg("Slow lane delivery error (will retry via Kafka)")
	}
	return err
}
