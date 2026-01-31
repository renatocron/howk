package scheduler

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/hotstate"
)

// Scheduler pops due retries from Redis and re-enqueues them to Kafka
type Scheduler struct {
	config    config.SchedulerConfig
	hotstate  *hotstate.RedisHotState
	publisher *broker.KafkaWebhookPublisher
}

// NewScheduler creates a new retry scheduler
func NewScheduler(
	cfg config.SchedulerConfig,
	hs *hotstate.RedisHotState,
	pub *broker.KafkaWebhookPublisher,
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
		Msg("Scheduler starting...")

	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("Scheduler stopping...")
			return ctx.Err()
		case <-ticker.C:
			if err := s.processBatch(ctx); err != nil {
				log.Error().Err(err).Msg("Scheduler batch processing failed")
			}
		}
	}
}

func (s *Scheduler) processBatch(ctx context.Context) error {
	// Pop due retries atomically
	retries, err := s.hotstate.PopDueRetries(ctx, s.config.BatchSize)
	if err != nil {
		return err
	}

	if len(retries) == 0 {
		return nil
	}

	log.Debug().Int("count", len(retries)).Msg("Processing due retries")

	var published, failed int

	for _, retry := range retries {
		if retry.Webhook == nil {
			log.Warn().Msg("Retry message has nil webhook, skipping")
			failed++
			continue
		}

		// Re-publish to Kafka pending topic
		if err := s.publisher.PublishWebhook(ctx, retry.Webhook); err != nil {
			log.Error().
				Err(err).
				Str("webhook_id", string(retry.Webhook.ID)).
				Msg("Failed to re-enqueue retry")
			
			// Re-schedule for later (don't lose the message)
			retry.ScheduledAt = time.Now().Add(30 * time.Second)
			if err := s.hotstate.ScheduleRetry(ctx, retry); err != nil {
				log.Error().Err(err).Msg("Failed to re-schedule retry")
			}
			failed++
			continue
		}

		published++
		log.Debug().
			Str("webhook_id", string(retry.Webhook.ID)).
			Int("attempt", retry.Webhook.Attempt).
			Msg("Retry re-enqueued")
	}

	if published > 0 || failed > 0 {
		log.Info().
			Int("published", published).
			Int("failed", failed).
			Msg("Batch processed")
	}

	return nil
}
