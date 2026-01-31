//go:build integration

package broker_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/testutil"
)

func TestPublish_Subscribe_RoundTrip(t *testing.T) {
	b := testutil.SetupKafka(t)
	topic := "test-roundtrip-" + ulid.Make().String()

	// Channel to receive messages
	received := make(chan *broker.Message, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

	// Wait for consumer group to join
	time.Sleep(5 * time.Second)

	// Publish message
	testMsg := broker.Message{
		Key:   []byte("test-key"),
		Value: []byte("test-value"),
		Headers: map[string]string{
			"header1": "value1",
			"header2": "value2",
		},
	}

	err := b.Publish(ctx, topic, testMsg)
	require.NoError(t, err)

	// Wait for message
	select {
	case msg := <-received:
		assert.Equal(t, testMsg.Key, msg.Key)
		assert.Equal(t, testMsg.Value, msg.Value)
		assert.Equal(t, "value1", msg.Headers["header1"])
		assert.Equal(t, "value2", msg.Headers["header2"])
	case <-time.After(15 * time.Second):
		t.Fatal("message not received within timeout")
	}
}

func TestPublishWebhook_Serialization(t *testing.T) {
	b := testutil.SetupKafka(t)
	topic := "test-webhook-" + ulid.Make().String()

	// Channel to receive webhooks
	received := make(chan *domain.Webhook, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start consumer
	go func() {
		err := b.Subscribe(ctx, topic, "test-group-"+ulid.Make().String(), func(ctx context.Context, msg *broker.Message) error {
			var wh domain.Webhook
			if err := json.Unmarshal(msg.Value, &wh); err != nil {
				return err
			}
			received <- &wh
			return nil
		})
		if err != nil && err != context.Canceled {
			t.Logf("Subscribe error: %v", err)
		}
	}()

	// Wait for consumer group to join
	time.Sleep(5 * time.Second)

	// Create and publish webhook
	webhook := testutil.NewTestWebhook("https://example.com/webhook")
	data, err := json.Marshal(webhook)
	require.NoError(t, err)

	msg := broker.Message{
		Key:   []byte(webhook.ID),
		Value: data,
		Headers: map[string]string{
			"config_id":     string(webhook.ConfigID),
			"endpoint_hash": string(webhook.EndpointHash),
			"attempt":       "1",
		},
	}

	err = b.Publish(ctx, topic, msg)
	require.NoError(t, err)

	// Wait for webhook
	select {
	case wh := <-received:
		assert.Equal(t, webhook.ID, wh.ID)
		assert.Equal(t, webhook.ConfigID, wh.ConfigID)
		assert.Equal(t, webhook.Endpoint, wh.Endpoint)
		assert.Equal(t, webhook.EndpointHash, wh.EndpointHash)
	case <-time.After(15 * time.Second):
		t.Fatal("webhook not received within timeout")
	}
}

func TestPublishResult_Headers(t *testing.T) {
	b := testutil.SetupKafka(t)
	topic := "test-result-" + ulid.Make().String()

	// Channel to receive results
	received := make(chan *broker.Message, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

	// Wait for consumer group to join
	time.Sleep(5 * time.Second)

	// Create and publish delivery result
	result := &domain.DeliveryResult{
		WebhookID:    "wh_test_result",
		ConfigID:     "test-config",
		EndpointHash: "test-hash",
		Success:      true,
		StatusCode:   200,
		Duration:     100 * time.Millisecond,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	msg := broker.Message{
		Key:   []byte(result.WebhookID),
		Value: data,
		Headers: map[string]string{
			"config_id":     string(result.ConfigID),
			"endpoint_hash": string(result.EndpointHash),
			"success":       "true",
		},
	}

	err = b.Publish(ctx, topic, msg)
	require.NoError(t, err)

	// Wait for result
	select {
	case msg := <-received:
		assert.Equal(t, "test-config", msg.Headers["config_id"])
		assert.Equal(t, "test-hash", msg.Headers["endpoint_hash"])
		assert.Equal(t, "true", msg.Headers["success"])

		var receivedResult domain.DeliveryResult
		err := json.Unmarshal(msg.Value, &receivedResult)
		require.NoError(t, err)
		assert.Equal(t, result.WebhookID, receivedResult.WebhookID)
		assert.True(t, receivedResult.Success)
		assert.Equal(t, 200, receivedResult.StatusCode)
	case <-time.After(15 * time.Second):
		t.Fatal("result not received within timeout")
	}
}

func TestPublishBatch(t *testing.T) {
	b := testutil.SetupKafka(t)
	topic := "test-batch-" + ulid.Make().String()

	// Channel to receive messages
	received := make(chan *broker.Message, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

	// Wait for consumer group to join
	time.Sleep(5 * time.Second)

	// Publish batch of 5 messages
	messages := make([]broker.Message, 5)
	for i := 0; i < 5; i++ {
		messages[i] = broker.Message{
			Key:   []byte("key-" + string(rune('0'+i))),
			Value: []byte("value-" + string(rune('0'+i))),
		}
	}

	err := b.Publish(ctx, topic, messages...)
	require.NoError(t, err)

	// Wait for all messages
	receivedCount := 0
	timeout := time.After(10 * time.Second)

	for receivedCount < 5 {
		select {
		case <-received:
			receivedCount++
		case <-timeout:
			t.Fatalf("received %d/5 messages before timeout", receivedCount)
		}
	}

	assert.Equal(t, 5, receivedCount)
}

func TestConsumerGroupRebalancing(t *testing.T) {
	// Use two separate broker instances to avoid shared sarama.Client closure issues
	b1 := testutil.SetupKafka(t)
	b2 := testutil.SetupKafka(t)

	// Create topic with multiple partitions so rebalancing distributes messages
	topic := testutil.CreateTestTopicWithPartitions(t, b1, 3)
	group := "test-group-" + ulid.Make().String()

	// Channels to receive messages
	consumer1 := make(chan *broker.Message, 10)
	consumer2 := make(chan *broker.Message, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Start first consumer using broker 1
	go func() {
		err := b1.Subscribe(ctx, topic, group, func(ctx context.Context, msg *broker.Message) error {
			consumer1 <- msg
			return nil
		})
		if err != nil && err != context.Canceled {
			t.Logf("Consumer 1 error: %v", err)
		}
	}()

	// Wait for first consumer to join
	time.Sleep(5 * time.Second)

	// Start second consumer using broker 2 (should trigger rebalance)
	go func() {
		err := b2.Subscribe(ctx, topic, group, func(ctx context.Context, msg *broker.Message) error {
			consumer2 <- msg
			return nil
		})
		if err != nil && err != context.Canceled {
			t.Logf("Consumer 2 error: %v", err)
		}
	}()

	// Wait for rebalance
	time.Sleep(5 * time.Second)

	// Publish 10 messages with distinct keys to distribute across partitions
	messages := make([]broker.Message, 10)
	for i := 0; i < 10; i++ {
		messages[i] = broker.Message{
			Key:   []byte("key-" + ulid.Make().String()),
			Value: []byte("value-" + string(rune('0'+i))),
		}
	}

	err := b1.Publish(ctx, topic, messages...)
	require.NoError(t, err)

	// Wait for all messages to be consumed
	count1 := 0
	count2 := 0
	timeout := time.After(10 * time.Second)

	for count1+count2 < 10 {
		select {
		case <-consumer1:
			count1++
		case <-consumer2:
			count2++
		case <-timeout:
			t.Fatalf("received %d/10 messages before timeout (consumer1: %d, consumer2: %d)", count1+count2, count1, count2)
		}
	}

	// Both consumers should have received some messages (load balancing)
	t.Logf("Consumer 1 received: %d, Consumer 2 received: %d", count1, count2)
	assert.Equal(t, 10, count1+count2, "total messages should be 10")
}
