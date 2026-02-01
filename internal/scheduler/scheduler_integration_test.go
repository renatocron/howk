//go:build integration

package scheduler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/scheduler"
	"github.com/howk/howk/internal/testutil"
)

func setupSchedulerTest(t *testing.T) (*scheduler.Scheduler, *hotstate.RedisHotState, *broker.KafkaBroker, context.Context, context.CancelFunc) {
	cfg := config.DefaultConfig()
	cfg.Scheduler.PollInterval = 500 * time.Millisecond // Fast polling for tests
	cfg.Scheduler.BatchSize = 10

	hs := testutil.SetupRedis(t)
	b := testutil.SetupKafka(t)

	pub := broker.NewKafkaWebhookPublisher(b, cfg.Kafka.Topics)
	s := scheduler.NewScheduler(cfg.Scheduler, hs, pub)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	// Register cancel in cleanup so it runs before Redis flush (LIFO order —
	// this was registered after SetupRedis, so it runs first during cleanup)
	t.Cleanup(func() {
		cancel()
		// Give scheduler goroutine time to observe cancellation and exit
		time.Sleep(200 * time.Millisecond)
	})

	return s, hs, b, ctx, cancel
}

func TestScheduler_PopAndLockRetries(t *testing.T) {
	s, hs, b, ctx, cancel := setupSchedulerTest(t)
	defer cancel()

	// Create unique topic for this test
	topic := "test-pending-" + ulid.Make().String()

	// Update publisher to use test topic
	cfg := config.DefaultConfig()
	cfg.Kafka.Topics.Pending = topic
	pub := broker.NewKafkaWebhookPublisher(b, cfg.Kafka.Topics)
	s = scheduler.NewScheduler(cfg.Scheduler, hs, pub)

	// Channel to receive published webhooks
	received := make(chan *broker.Message, 10)

	// Start consumer
	go func() {
		err := b.Subscribe(ctx, topic, "test-group-"+ulid.Make().String(), func(ctx context.Context, msg *broker.Message) error {
			received <- msg
			return nil
		})
		if err != nil && err != context.Canceled {
			t.Logf("Subscribe error: %v", err)
		}
	}()

	// Wait for consumer to join
	time.Sleep(2 * time.Second)

	// Schedule retry that is due now
	webhook := testutil.NewTestWebhook("https://example.com/webhook")
	webhook.Attempt = 1

	// Store data and schedule retry
	err := hs.StoreRetryData(ctx, webhook, 7*24*time.Hour)
	require.NoError(t, err)
	err = hs.ScheduleRetry(ctx, webhook.ID, webhook.Attempt, time.Now().Add(-1*time.Second), "test")
	require.NoError(t, err)

	// Run scheduler once
	go func() {
		err := s.Run(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Scheduler error: %v", err)
		}
	}()

	// Wait for webhook to be re-enqueued
	select {
	case msg := <-received:
		assert.Equal(t, webhook.ID, domain.WebhookID(msg.Key))

		// Verify it's the webhook we scheduled
		var receivedWebhook domain.Webhook
		err := json.Unmarshal(msg.Value, &receivedWebhook)
		if err == nil {
			t.Logf("Received webhook ID from Kafka: %v", receivedWebhook)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("webhook not re-enqueued within timeout")
	}
}

func TestScheduler_NotDueYet(t *testing.T) {
	s, hs, b, ctx, cancel := setupSchedulerTest(t)
	defer cancel()

	// Create unique topic for this test
	topic := "test-pending-" + ulid.Make().String()

	// Update publisher to use test topic
	cfg := config.DefaultConfig()
	cfg.Kafka.Topics.Pending = topic
	pub := broker.NewKafkaWebhookPublisher(b, cfg.Kafka.Topics)
	s = scheduler.NewScheduler(cfg.Scheduler, hs, pub)

	// Channel to receive published webhooks
	received := make(chan *broker.Message, 10)

	// Start consumer
	go func() {
		err := b.Subscribe(ctx, topic, "test-group-"+ulid.Make().String(), func(ctx context.Context, msg *broker.Message) error {
			received <- msg
			return nil
		})
		if err != nil && err != context.Canceled {
			t.Logf("Subscribe error: %v", err)
		}
	}()

	// Wait for consumer to join
	time.Sleep(2 * time.Second)

	// Schedule retry for future
	webhook := testutil.NewTestWebhook("https://example.com/webhook")
	err := hs.StoreRetryData(ctx, webhook, 7*24*time.Hour)
	require.NoError(t, err)
	err = hs.ScheduleRetry(ctx, webhook.ID, 1, time.Now().Add(10*time.Hour), "test future")
	require.NoError(t, err)

	// Run scheduler once
	go func() {
		err := s.Run(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Scheduler error: %v", err)
		}
	}()

	// Wait a bit to ensure scheduler has run
	time.Sleep(2 * time.Second)

	// Should NOT receive anything
	select {
	case <-received:
		t.Fatal("webhook should not be re-enqueued (not due yet)")
	case <-time.After(3 * time.Second):
		// Expected - webhook not published
	}

	// Verify webhook is still in retry queue (locked in future)
	refs, err := hs.PopAndLockRetries(ctx, 10, 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, refs, "webhook should not be due yet")

	// Cleanup
	hs.DeleteRetryData(ctx, webhook.ID)
}

func TestScheduler_ReEnqueueToKafka(t *testing.T) {
	s, hs, b, ctx, cancel := setupSchedulerTest(t)
	defer cancel()

	// Create unique topic for this test
	topic := "test-pending-" + ulid.Make().String()

	// Update publisher to use test topic
	cfg := config.DefaultConfig()
	cfg.Kafka.Topics.Pending = topic
	cfg.Scheduler.PollInterval = 100 * time.Millisecond
	pub := broker.NewKafkaWebhookPublisher(b, cfg.Kafka.Topics)
	s = scheduler.NewScheduler(cfg.Scheduler, hs, pub)

	// Channel to receive published webhooks
	received := make(chan *broker.Message, 10)

	// Start consumer
	go func() {
		err := b.Subscribe(ctx, topic, "test-group-"+ulid.Make().String(), func(ctx context.Context, msg *broker.Message) error {
			received <- msg
			return nil
		})
		if err != nil && err != context.Canceled {
			t.Logf("Subscribe error: %v", err)
		}
	}()

	// Wait for consumer to join
	time.Sleep(2 * time.Second)

	// Schedule 3 retries that are all due
	webhooks := make([]*domain.Webhook, 3)
	for i := 0; i < 3; i++ {
		webhook := testutil.NewTestWebhook("https://example.com/webhook")
		webhooks[i] = webhook

		err := hs.StoreRetryData(ctx, webhook, 7*24*time.Hour)
		require.NoError(t, err)
		err = hs.ScheduleRetry(ctx, webhook.ID, i+1, time.Now().Add(-1*time.Second), "test batch")
		require.NoError(t, err)
	}

	// Run scheduler
	go func() {
		err := s.Run(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Scheduler error: %v", err)
		}
	}()

	// Wait for all webhooks to be re-enqueued
	receivedCount := 0
	timeout := time.After(10 * time.Second)

	for receivedCount < 3 {
		select {
		case <-received:
			receivedCount++
		case <-timeout:
			t.Fatalf("received %d/3 webhooks before timeout", receivedCount)
		}
	}

	assert.Equal(t, 3, receivedCount, "all 3 webhooks should be re-enqueued")

	// Verify retry queue is empty (all acked)
	refs, err := hs.PopAndLockRetries(ctx, 10, 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, refs, "all retries should have been acked")

	// Cleanup data
	for _, wh := range webhooks {
		hs.DeleteRetryData(ctx, wh.ID)
	}
}

func TestScheduler_BatchSizeLimit(t *testing.T) {
	s, hs, b, ctx, cancel := setupSchedulerTest(t)
	defer cancel()

	// Create unique topic for this test
	topic := "test-pending-" + ulid.Make().String()

	// Update publisher to use test topic with small batch size
	cfg := config.DefaultConfig()
	cfg.Kafka.Topics.Pending = topic
	cfg.Scheduler.PollInterval = 100 * time.Millisecond
	cfg.Scheduler.BatchSize = 5 // Small batch size
	pub := broker.NewKafkaWebhookPublisher(b, cfg.Kafka.Topics)
	s = scheduler.NewScheduler(cfg.Scheduler, hs, pub)

	// Channel to receive published webhooks
	received := make(chan *broker.Message, 20)

	// Start consumer
	go func() {
		err := b.Subscribe(ctx, topic, "test-group-"+ulid.Make().String(), func(ctx context.Context, msg *broker.Message) error {
			received <- msg
			return nil
		})
		if err != nil && err != context.Canceled {
			t.Logf("Subscribe error: %v", err)
		}
	}()

	// Wait for consumer to join
	time.Sleep(2 * time.Second)

	// Schedule 10 retries (more than batch size)
	webhooks := make([]*domain.Webhook, 10)
	for i := 0; i < 10; i++ {
		webhook := testutil.NewTestWebhook("https://example.com/webhook")
		webhooks[i] = webhook

		err := hs.StoreRetryData(ctx, webhook, 7*24*time.Hour)
		require.NoError(t, err)
		err = hs.ScheduleRetry(ctx, webhook.ID, i+1, time.Now().Add(-1*time.Second), "test batch limit")
		require.NoError(t, err)
	}

	// Run scheduler
	go func() {
		err := s.Run(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Scheduler error: %v", err)
		}
	}()

	// Wait for all webhooks to be eventually re-enqueued
	// Should take at least 2 polling cycles (5 + 5)
	receivedCount := 0
	timeout := time.After(15 * time.Second)

	for receivedCount < 10 {
		select {
		case <-received:
			receivedCount++
		case <-timeout:
			t.Fatalf("received %d/10 webhooks before timeout", receivedCount)
		}
	}

	assert.Equal(t, 10, receivedCount, "all 10 webhooks should eventually be re-enqueued")

	// Cleanup data
	for _, wh := range webhooks {
		hs.DeleteRetryData(ctx, wh.ID)
	}
}
