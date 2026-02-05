package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/IBM/sarama"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
)

// Reconciler rebuilds Redis state from the compacted Kafka state topic.
// This enables "zero maintenance" operation - Redis can be entirely reconstructed
// from Kafka on startup, eliminating the need for Redis persistence.
type Reconciler struct {
	config    config.KafkaConfig
	hotstate  hotstate.HotState
	ttlConfig config.TTLConfig
}

// NewReconciler creates a new reconciler
func NewReconciler(cfg config.KafkaConfig, hs hotstate.HotState, ttlCfg config.TTLConfig) *Reconciler {
	return &Reconciler{
		config:    cfg,
		hotstate:  hs,
		ttlConfig: ttlCfg,
	}
}

// Run rebuilds Redis state from the compacted state topic.
//
// DESIGN NOTE: This is a "stop-the-world" startup/recovery operation. It assumes:
// 1. Workers are stopped or Redis is in an inconsistent state
// 2. The reconciler is the only writer to Redis during this phase
// 3. Once complete, workers can resume and will naturally correct any minor drift
//
// The fromBeginning parameter is kept for API compatibility but is ignored
// because for a compacted topic, we ALWAYS read from the beginning to build
// the full state table.
//
// Race Condition Note: If workers are running during reconciliation, they may
// publish state updates to both Redis and Kafka after we've passed the HWM.
// This is acceptable because:
// 1. The reconciler is meant for disaster recovery (Redis completely lost)
// 2. Workers will naturally overwrite Redis state after reconciliation completes
// 3. The compacted topic always has the authoritative state
func (r *Reconciler) Run(ctx context.Context, fromBeginning bool) error {
	// Ignore fromBeginning - we always read from beginning for compacted topics
	_ = fromBeginning

	log.Info().Msg("Starting zero-maintenance reconciliation from howk.state topic...")

	// 1. Flush Redis state before rebuilding
	log.Info().Msg("Flushing Redis active state...")
	if err := r.hotstate.FlushForRebuild(ctx); err != nil {
		return fmt.Errorf("flush for rebuild: %w", err)
	}

	// 2. Setup Kafka consumer
	saramaConfig := sarama.NewConfig()
	saramaConfig.Consumer.Return.Errors = true
	// ALWAYS read from oldest for compacted topics
	saramaConfig.Consumer.Offsets.Initial = sarama.OffsetOldest

	consumer, err := sarama.NewConsumer(r.config.Brokers, saramaConfig)
	if err != nil {
		return fmt.Errorf("create kafka consumer: %w", err)
	}
	defer consumer.Close()

	// 3. Get partitions for state topic
	partitions, err := consumer.Partitions(r.config.Topics.State)
	if err != nil {
		return fmt.Errorf("get partitions: %w", err)
	}

	if len(partitions) == 0 {
		log.Warn().Msg("No partitions found for state topic, skipping reconciliation")
		return nil
	}

	var restoredCount int64
	var tombstoneCount int64
	var parseErrorCount int64
	startTime := time.Now()

	// 4. Consume each partition
	for _, partition := range partitions {
		if err := r.consumePartition(ctx, consumer, partition, &restoredCount, &tombstoneCount, &parseErrorCount); err != nil {
			if err == context.Canceled {
				return err
			}
			log.Error().Err(err).Int32("partition", partition).Msg("Failed to consume partition, continuing...")
			continue
		}
	}

	// 5. Set canary key to signal Redis is initialized
	// This allows other workers to know they can start processing
	if err := r.hotstate.SetCanary(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to set canary key")
		// Don't fail reconciliation for this
	}

	// 6. Set epoch marker after successful completion
	hostname, _ := os.Hostname()
	epoch := &domain.SystemEpoch{
		Epoch:            time.Now().Unix(),
		ReconcilerHost:   hostname,
		MessagesReplayed: restoredCount,
		CompletedAt:      time.Now(),
	}

	if err := r.hotstate.SetEpoch(ctx, epoch); err != nil {
		log.Error().Err(err).Msg("Failed to set epoch marker")
		// Don't fail reconciliation for this
	}

	log.Info().
		Int64("restored", restoredCount).
		Int64("tombstones_skipped", tombstoneCount).
		Int64("parse_errors", parseErrorCount).
		Int("partitions", len(partitions)).
		Dur("duration", time.Since(startTime)).
		Msg("Zero-maintenance reconciliation complete")

	return nil
}

// consumePartition consumes a single partition from the state topic
func (r *Reconciler) consumePartition(
	ctx context.Context,
	consumer sarama.Consumer,
	partition int32,
	restoredCount, tombstoneCount, parseErrorCount *int64,
) error {
	pc, err := consumer.ConsumePartition(r.config.Topics.State, partition, sarama.OffsetOldest)
	if err != nil {
		return fmt.Errorf("consume partition %d: %w", partition, err)
	}
	defer pc.Close()

	highWaterMark := pc.HighWaterMarkOffset()
	if highWaterMark == 0 {
		log.Info().Int32("partition", partition).Msg("Partition is empty, skipping")
		return nil
	}

	log.Info().
		Int32("partition", partition).
		Int64("high_water_mark", highWaterMark).
		Msg("Processing partition")

	for msg := range pc.Messages() {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Handle tombstone (nil value = deletion)
		if msg.Value == nil || len(msg.Value) == 0 {
			*tombstoneCount++
			log.Debug().
				Str("webhook_id", string(msg.Key)).
				Msg("Skipping tombstone")
			continue
		}

		// Parse snapshot
		var snapshot domain.WebhookStateSnapshot
		if err := json.Unmarshal(msg.Value, &snapshot); err != nil {
			*parseErrorCount++
			log.Warn().
				Err(err).
				Str("webhook_id", string(msg.Key)).
				Int64("offset", msg.Offset).
				Msg("Failed to parse snapshot, skipping")
			continue
		}

		// Restore state from snapshot
		if err := r.restoreState(ctx, &snapshot); err != nil {
			log.Error().
				Err(err).
				Str("webhook_id", string(snapshot.WebhookID)).
				Msg("Failed to restore state, continuing")
			continue
		}

		*restoredCount++

		// Check AFTER processing if we've reached the high water mark
		// In a compacted topic, this means we've processed all unique keys
		if msg.Offset >= highWaterMark-1 {
			log.Debug().
				Int32("partition", partition).
				Int64("offset", msg.Offset).
				Msg("Reached high water mark")
			break
		}
	}

	return nil
}

// restoreState restores Redis state from a snapshot
func (r *Reconciler) restoreState(ctx context.Context, snap *domain.WebhookStateSnapshot) error {
	// 1. Restore status object
	// Use snapshot's UpdatedAtNs for LWW conflict resolution
	// If not set (legacy snapshot), use current time
	updatedAtNs := snap.UpdatedAtNs
	if updatedAtNs == 0 {
		updatedAtNs = time.Now().UnixNano()
	}

	status := &domain.WebhookStatus{
		WebhookID:     snap.WebhookID,
		State:         snap.State,
		Attempts:      snap.Attempt + 1, // Attempts in status is 1-based
		NextRetryAt:   snap.NextRetryAt,
		LastError:     snap.LastError,
		LastAttemptAt: &snap.CreatedAt, // Approximate
		UpdatedAtNs:   updatedAtNs,     // LWW timestamp from snapshot
	}

	if err := r.hotstate.SetStatus(ctx, status); err != nil {
		return fmt.Errorf("set status: %w", err)
	}

	// 2. Restore retry schedule if webhook is in failed state (pending retry)
	if snap.State == domain.StateFailed && snap.NextRetryAt != nil {
		// Reconstruct webhook for retry scheduling
		webhook := &domain.Webhook{
			ID:             snap.WebhookID,
			ConfigID:       snap.ConfigID,
			Endpoint:       snap.Endpoint,
			EndpointHash:   snap.EndpointHash,
			Payload:        snap.Payload,
			Headers:        snap.Headers,
			IdempotencyKey: snap.IdempotencyKey,
			SigningSecret:  snap.SigningSecret,
			ScriptHash:     snap.ScriptHash,
			Attempt:        snap.Attempt,
			MaxAttempts:    snap.MaxAttempts,
			CreatedAt:      snap.CreatedAt,
			ScheduledAt:    *snap.NextRetryAt,
		}

		// Ensure retry data exists in Redis (compressed)
		// Use configured TTL for restored items
		if err := r.hotstate.EnsureRetryData(ctx, webhook, r.ttlConfig.RetryDataTTL); err != nil {
			return fmt.Errorf("ensure retry data: %w", err)
		}

		// Schedule in ZSET
		if err := r.hotstate.ScheduleRetry(ctx, webhook.ID, webhook.Attempt, *snap.NextRetryAt, "reconciled"); err != nil {
			return fmt.Errorf("schedule retry: %w", err)
		}
	}

	return nil
}
