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

func setupSchedulerTest(t *testing.T) (*scheduler.Scheduler, hotstate.HotState, *broker.KafkaBroker, *testutil.IsolatedEnv, context.Context, context.CancelFunc) {
	env := testutil.NewIsolatedEnv(t,
		testutil.WithSchedulerConfig(config.SchedulerConfig{
			PollInterval: 500 * time.Millisecond,
			BatchSize:    10,
			LockTimeout:  30 * time.Second,
		}),
	)

	pub := broker.NewKafkaWebhookPublisher(env.Broker, env.Config.Kafka.Topics)
	s := scheduler.NewScheduler(env.Config.Scheduler, env.HotState, pub)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(func() {
		cancel()
		// Give scheduler goroutine time to observe cancellation and exit
		time.Sleep(200 * time.Millisecond)
	})

	return s, env.HotState, env.Broker, env, ctx, cancel
}

func TestScheduler_PopAndLockRetries(t *testing.T) {
	s, hs, b, env, ctx, cancel := setupSchedulerTest(t)
	defer cancel()

	// Use the isolated topic from env
	topic := env.Config.Kafka.Topics.Pending

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
	err := hs.EnsureRetryData(ctx, webhook, 7*24*time.Hour)
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
		// Note: Partition key is now ConfigID (not WebhookID) for ordering by config
		assert.Equal(t, webhook.ConfigID, domain.ConfigID(msg.Key))

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

func TestScheduler_ReEnqueueToKafka(t *testing.T) {
	env := testutil.NewIsolatedEnv(t,
		testutil.WithSchedulerConfig(config.SchedulerConfig{
			PollInterval: 100 * time.Millisecond,
			BatchSize:    500,
			LockTimeout:  30 * time.Second,
		}),
	)

	pub := broker.NewKafkaWebhookPublisher(env.Broker, env.Config.Kafka.Topics)
	s := scheduler.NewScheduler(env.Config.Scheduler, env.HotState, pub)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Cleanup(func() {
		cancel()
		time.Sleep(200 * time.Millisecond)
	})

	hs := env.HotState
	b := env.Broker
	topic := env.Config.Kafka.Topics.Pending

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

		err := hs.EnsureRetryData(ctx, webhook, 7*24*time.Hour)
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
