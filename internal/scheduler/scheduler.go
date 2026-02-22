package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/metrics"
)

// Scheduler pops due retries from Redis and re-enqueues them to Kafka
type Scheduler struct {
	config    config.SchedulerConfig
	hotstate  hotstate.HotState
	publisher broker.WebhookPublisher
}

// NewScheduler creates a new retry scheduler
func NewScheduler(
	cfg config.SchedulerConfig,
	hs hotstate.HotState,
	pub broker.WebhookPublisher,
) *Scheduler {
	return &Scheduler{
		config:    cfg,
		hotstate:  hs,
		publisher: pub,
	}
}

// Run starts the scheduler loop
func (s *Scheduler) Run(ctx context.Context) error {
	log.Info().
		Dur("poll_interval", s.config.PollInterval).
		Int("batch_size", s.config.BatchSize).
		Dur("lock_timeout", s.config.LockTimeout).
		Msg("Scheduler starting...")

	// Check system epoch on startup
	if err := s.checkSystemHealth(ctx); err != nil {
		log.Warn().Err(err).Msg("System health check failed")
		// Continue anyway - don't block scheduler startup
	}

	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("Scheduler stopping...")
			return ctx.Err()
		case <-ticker.C:
			if size, err := s.hotstate.GetRetryQueueSize(ctx); err == nil {
				metrics.RetryQueueDepth.Set(float64(size))
			}
			if err := s.processBatch(ctx); err != nil {
				log.Error().Err(err).Msg("Scheduler batch processing failed")
			}
		}
	}
}

func (s *Scheduler) processBatch(ctx context.Context) error {
	// Pop due retries and lock them (visibility timeout)
	// We use LockTimeout from config as the visibility timeout
	lockDuration := s.config.LockTimeout

	refs, err := s.hotstate.PopAndLockRetries(ctx, s.config.BatchSize, lockDuration)
	if err != nil {
		return err
	}

	if len(refs) == 0 {
		return nil
	}

	log.Debug().Int("count", len(refs)).Msg("Processing due retries")

	var published, failed, expired int

	for _, ref := range refs {
		// 1. Parse reference to get webhookID and attempt
		webhookID, attempt, err := parseReference(ref)
		if err != nil {
			log.Error().Str("ref", ref).Msg("Invalid reference format, removing")
			s.hotstate.AckRetry(ctx, ref)
			continue
		}

		// 2. Fetch Data by webhookID (not by full reference)
		webhook, err := s.hotstate.GetRetryData(ctx, webhookID)
		if err != nil {
			// Data missing means likely expired or already handled
			log.Warn().Str("ref", ref).Str("webhook_id", string(webhookID)).Msg("Retry data missing, removing reference")
			s.hotstate.AckRetry(ctx, ref)
			expired++
			continue
		}

		// 3. Update attempt from reference (data may have stale attempt number)
		webhook.Attempt = attempt

		// 4. Publish to Kafka
		if err := s.publisher.PublishWebhook(ctx, webhook); err != nil {
			log.Error().
				Err(err).
				Str("webhook_id", string(webhook.ID)).
				Msg("Failed to re-enqueue retry")

			// Do NOT Ack.
			// The item remains in ZSET with future score (locked).
			// It will be retried automatically when lock expires.
			failed++
			continue
		}

		// 5. Ack (remove from ZSET + meta only, NOT data)
		if err := s.hotstate.AckRetry(ctx, ref); err != nil {
			// Non-fatal, just means it might get redelivered if we don't clean up
			log.Warn().Err(err).Str("ref", ref).Msg("Failed to ack retry")
		}

		published++
		log.Debug().
			Str("webhook_id", string(webhook.ID)).
			Int("attempt", webhook.Attempt).
			Msg("Retry re-enqueued")
	}

	if published > 0 || failed > 0 || expired > 0 {
		log.Info().
			Int("published", published).
			Int("failed", failed).
			Int("expired", expired).
			Msg("Batch processed")
	}

	return nil
}

// parseReference parses a reference string "webhook_id:attempt" into its components
func parseReference(ref string) (domain.WebhookID, int, error) {
	parts := strings.Split(ref, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid reference format: %s", ref)
	}
	attempt, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("invalid attempt number in reference %s: %w", ref, err)
	}
	return domain.WebhookID(parts[0]), attempt, nil
}

// checkSystemHealth checks the system epoch on startup to detect potential data loss
func (s *Scheduler) checkSystemHealth(ctx context.Context) error {
	epoch, err := s.hotstate.GetEpoch(ctx)
	if err != nil {
		return fmt.Errorf("get epoch: %w", err)
	}

	if epoch == nil {
		log.Warn().Msg("No system epoch found - Redis may have been flushed. Consider running reconciler.")
		return nil
	}

	age := time.Since(epoch.CompletedAt)
	if age > 24*time.Hour {
		log.Warn().
			Time("epoch_completed_at", epoch.CompletedAt).
			Dur("age", age).
			Msg("System epoch is old - consider running reconciler if Redis was restored from backup")
	} else {
		log.Info().
			Time("epoch_completed_at", epoch.CompletedAt).
			Int64("messages_replayed", epoch.MessagesReplayed).
			Msg("System epoch valid")
	}

	return nil
}
