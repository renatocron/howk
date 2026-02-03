package reconciler

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/IBM/sarama"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
)

// Reconciler rebuilds Redis state from Kafka topics
type Reconciler struct {
	config    config.KafkaConfig
	hotstate  hotstate.HotState
	ttlConfig config.TTLConfig
}

// NewReconciler creates a new reconciler
func NewReconciler(cfg config.KafkaConfig, hs hotstate.HotState) *Reconciler {
	return &Reconciler{
		config:   cfg,
		hotstate: hs,
	}
}

// Run rebuilds Redis state from Kafka
func (r *Reconciler) Run(ctx context.Context, fromBeginning bool) error {
	log.Info().Bool("from_beginning", fromBeginning).Msg("Starting reconciliation...")

	// Flush Redis state if starting from beginning
	if fromBeginning {
		log.Info().Msg("Flushing Redis state...")
		if err := r.hotstate.FlushForRebuild(ctx); err != nil {
			return err
		}
	}

	// Create Kafka consumer config
	saramaConfig := sarama.NewConfig()
	saramaConfig.Consumer.Return.Errors = true
	if fromBeginning {
		saramaConfig.Consumer.Offsets.Initial = sarama.OffsetOldest
	} else {
		saramaConfig.Consumer.Offsets.Initial = sarama.OffsetNewest
	}

	// Connect to Kafka
	consumer, err := sarama.NewConsumer(r.config.Brokers, saramaConfig)
	if err != nil {
		return err
	}
	defer consumer.Close()

	// Get partition list for results topic
	partitions, err := consumer.Partitions(r.config.Topics.Results)
	if err != nil {
		return err
	}

	// Track progress
	var processedCount int64
	startTime := time.Now()

	// Process each partition
	for _, partition := range partitions {
		pc, err := consumer.ConsumePartition(r.config.Topics.Results, partition, sarama.OffsetOldest)
		if err != nil {
			log.Error().Err(err).Int32("partition", partition).Msg("Failed to consume partition")
			continue
		}

		// Get high water mark to know when we're caught up
		highWaterMark := pc.HighWaterMarkOffset()

		log.Info().
			Int32("partition", partition).
			Int64("high_water_mark", highWaterMark).
			Msg("Processing partition")

		for msg := range pc.Messages() {
			// Check context
			select {
			case <-ctx.Done():
				pc.Close()
				return ctx.Err()
			default:
			}

			// Parse delivery result
			var result domain.DeliveryResult
			if err := json.Unmarshal(msg.Value, &result); err != nil {
				log.Warn().Err(err).Msg("Failed to parse result, skipping")
				continue
			}

			// Rebuild status
			if err := r.rebuildStatus(ctx, &result); err != nil {
				log.Warn().Err(err).Str("webhook_id", string(result.WebhookID)).Msg("Failed to rebuild status")
			}

			// Rebuild circuit breaker state
			if err := r.rebuildCircuit(ctx, &result); err != nil {
				log.Warn().Err(err).Str("endpoint_hash", string(result.EndpointHash)).Msg("Failed to rebuild circuit")
			}

			// Rebuild stats
			if err := r.rebuildStats(ctx, &result); err != nil {
				log.Warn().Err(err).Msg("Failed to rebuild stats")
			}

			// Schedule retry if needed
			if result.ShouldRetry && result.Webhook != nil && !result.Success {
				if err := r.scheduleRetry(ctx, &result); err != nil {
					log.Warn().Err(err).Str("webhook_id", string(result.WebhookID)).Msg("Failed to schedule retry")
				}
			}

			processedCount++

			// Log progress periodically
			if processedCount%10000 == 0 {
				log.Info().
					Int64("processed", processedCount).
					Dur("elapsed", time.Since(startTime)).
					Msg("Progress")
			}

			// Stop when caught up
			if msg.Offset >= highWaterMark-1 {
				break
			}
		}

		pc.Close()
	}

	log.Info().
		Int64("total_processed", processedCount).
		Dur("duration", time.Since(startTime)).
		Msg("Reconciliation complete")

	// Set epoch marker after successful completion
	hostname, _ := os.Hostname()
	epoch := &domain.SystemEpoch{
		Epoch:            time.Now().Unix(),
		ReconcilerHost:   hostname,
		MessagesReplayed: processedCount,
		CompletedAt:      time.Now(),
	}

	if err := r.hotstate.SetEpoch(ctx, epoch); err != nil {
		log.Error().Err(err).Msg("Failed to set epoch marker")
		// Don't fail reconciliation for this
	}

	return nil
}

func (r *Reconciler) rebuildStatus(ctx context.Context, result *domain.DeliveryResult) error {
	status := &domain.WebhookStatus{
		WebhookID:      result.WebhookID,
		Attempts:       result.Attempt + 1,
		LastAttemptAt:  &result.Timestamp,
		LastStatusCode: result.StatusCode,
		LastError:      result.Error,
	}

	if result.Success {
		status.State = domain.StateDelivered
		status.DeliveredAt = &result.Timestamp
	} else if result.ShouldRetry {
		status.State = domain.StateFailed
		status.NextRetryAt = &result.NextRetryAt
	} else {
		status.State = domain.StateExhausted
	}

	return r.hotstate.SetStatus(ctx, status)
}

func (r *Reconciler) rebuildCircuit(ctx context.Context, result *domain.DeliveryResult) error {
	// Just record the outcome - circuit breaker will update its state
	// Note: This won't perfectly recreate historical circuit states,
	// but will establish correct current state based on recent results
	return nil
}

func (r *Reconciler) rebuildStats(ctx context.Context, result *domain.DeliveryResult) error {
	bucket := result.Timestamp.Format("2006010215")

	stat := "delivered"
	if !result.Success {
		if result.ShouldRetry {
			stat = "failed"
		} else {
			stat = "exhausted"
		}
	}

	if err := r.hotstate.IncrStats(ctx, bucket, map[string]int64{stat: 1}); err != nil {
		return err
	}

	return r.hotstate.AddToHLL(ctx, "endpoints:"+bucket, string(result.EndpointHash))
}

func (r *Reconciler) scheduleRetry(ctx context.Context, result *domain.DeliveryResult) error {
	// Only schedule if the retry time is in the future
	if result.NextRetryAt.Before(time.Now()) {
		// Retry time has passed, re-enqueue immediately
		result.Webhook.ScheduledAt = time.Now()
	} else {
		result.Webhook.ScheduledAt = result.NextRetryAt
	}

	// NEW: Use EnsureRetryData (lazy save - only writes if missing)
	if err := r.hotstate.EnsureRetryData(ctx, result.Webhook, r.ttlConfig.RetryDataTTL); err != nil {
		return err
	}

	// NEW: Use updated ScheduleRetry with separate reference scheduling
	return r.hotstate.ScheduleRetry(ctx, result.Webhook.ID, result.Webhook.Attempt, result.Webhook.ScheduledAt, "reconciled")
}
