package devmode

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

func TestMemBroker_PublishSubscribe(t *testing.T) {
	mb := NewMemBroker()
	defer mb.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan *broker.Message, 1)

	// Start subscriber in background
	go func() {
		_ = mb.Subscribe(ctx, "test-topic", "group", func(_ context.Context, msg *broker.Message) error {
			received <- msg
			return nil
		})
	}()

	// Give subscriber time to register
	time.Sleep(10 * time.Millisecond)

	// Publish
	err := mb.Publish(ctx, "test-topic", broker.Message{
		Key:   []byte("key1"),
		Value: []byte(`{"hello":"world"}`),
	})
	require.NoError(t, err)

	select {
	case msg := <-received:
		assert.Equal(t, "key1", string(msg.Key))
		assert.Equal(t, `{"hello":"world"}`, string(msg.Value))
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestMemBroker_MultipleSubscribers(t *testing.T) {
	mb := NewMemBroker()
	defer mb.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	counts := make(map[string]int)

	for _, name := range []string{"sub1", "sub2"} {
		name := name
		go func() {
			_ = mb.Subscribe(ctx, "topic", name, func(_ context.Context, msg *broker.Message) error {
				mu.Lock()
				counts[name]++
				mu.Unlock()
				return nil
			})
		}()
	}

	time.Sleep(10 * time.Millisecond)

	err := mb.Publish(ctx, "topic", broker.Message{Value: []byte("msg")})
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	assert.Equal(t, 1, counts["sub1"], "sub1 should receive 1 message")
	assert.Equal(t, 1, counts["sub2"], "sub2 should receive 1 message")
	mu.Unlock()
}

func TestMemBroker_SubscribeReturnsOnClose(t *testing.T) {
	mb := NewMemBroker()

	done := make(chan error, 1)
	go func() {
		done <- mb.Subscribe(context.Background(), "topic", "g", func(_ context.Context, _ *broker.Message) error {
			return nil
		})
	}()

	time.Sleep(10 * time.Millisecond)
	mb.Close()

	select {
	case err := <-done:
		assert.NoError(t, err) // returns nil when channel closed
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after Close")
	}
}

func TestMemBroker_PublishAfterClose(t *testing.T) {
	mb := NewMemBroker()
	mb.Close()

	err := mb.Publish(context.Background(), "topic", broker.Message{Value: []byte("x")})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestMemBroker_NoSubscribersNoPanic(t *testing.T) {
	mb := NewMemBroker()
	defer mb.Close()

	// Publishing to a topic with no subscribers should not panic or error
	err := mb.Publish(context.Background(), "empty-topic", broker.Message{Value: []byte("x")})
	assert.NoError(t, err)
}

func TestMemWebhookPublisher_PublishWebhook(t *testing.T) {
	mb := NewMemBroker()
	defer mb.Close()

	topics := config.TopicsConfig{
		Pending:    "howk.pending",
		Results:    "howk.results",
		DeadLetter: "howk.deadletter",
		Slow:       "howk.slow",
		State:      "howk.state",
	}
	pub := NewMemWebhookPublisher(mb, topics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan *broker.Message, 1)
	go func() {
		_ = mb.Subscribe(ctx, "howk.pending", "g", func(_ context.Context, msg *broker.Message) error {
			received <- msg
			return nil
		})
	}()
	time.Sleep(10 * time.Millisecond)

	wh := &domain.Webhook{
		ID:           "wh_test123",
		ConfigID:     "my-config",
		EndpointHash: "abc123",
		Endpoint:     "https://example.com/hook",
		Payload:      json.RawMessage(`{"event":"test"}`),
		Attempt:      0,
	}

	err := pub.PublishWebhook(ctx, wh)
	require.NoError(t, err)

	select {
	case msg := <-received:
		assert.Equal(t, "my-config", string(msg.Key))
		assert.Equal(t, "my-config", msg.Headers["config_id"])
		assert.Equal(t, "abc123", msg.Headers["endpoint_hash"])
		assert.Equal(t, "0", msg.Headers["attempt"])

		var decoded domain.Webhook
		require.NoError(t, json.Unmarshal(msg.Value, &decoded))
		assert.Equal(t, domain.WebhookID("wh_test123"), decoded.ID)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for webhook")
	}
}

func TestMemWebhookPublisher_PublishResult(t *testing.T) {
	mb := NewMemBroker()
	defer mb.Close()

	topics := config.TopicsConfig{Results: "howk.results"}
	pub := NewMemWebhookPublisher(mb, topics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan *broker.Message, 1)
	go func() {
		_ = mb.Subscribe(ctx, "howk.results", "g", func(_ context.Context, msg *broker.Message) error {
			received <- msg
			return nil
		})
	}()
	time.Sleep(10 * time.Millisecond)

	result := &domain.DeliveryResult{
		WebhookID:    "wh_r1",
		ConfigID:     "cfg1",
		EndpointHash: "ep1",
		Success:      true,
		StatusCode:   200,
	}

	err := pub.PublishResult(ctx, result)
	require.NoError(t, err)

	select {
	case msg := <-received:
		assert.Equal(t, "true", msg.Headers["success"])
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestMemWebhookPublisher_PublishDeadLetter(t *testing.T) {
	mb := NewMemBroker()
	defer mb.Close()

	topics := config.TopicsConfig{DeadLetter: "howk.dl"}
	pub := NewMemWebhookPublisher(mb, topics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan *broker.Message, 1)
	go func() {
		_ = mb.Subscribe(ctx, "howk.dl", "g", func(_ context.Context, msg *broker.Message) error {
			received <- msg
			return nil
		})
	}()
	time.Sleep(10 * time.Millisecond)

	dl := &domain.DeadLetter{
		Webhook: &domain.Webhook{
			ID:       "wh_dead",
			ConfigID: "cfg_dead",
		},
		Reason:     "max retries exceeded",
		ReasonType: "exhausted",
	}

	err := pub.PublishDeadLetter(ctx, dl)
	require.NoError(t, err)

	select {
	case msg := <-received:
		assert.Equal(t, "max retries exceeded", msg.Headers["reason"])
		assert.Equal(t, "exhausted", msg.Headers["reason_type"])
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestMemWebhookPublisher_PublishStateTombstone(t *testing.T) {
	mb := NewMemBroker()
	defer mb.Close()

	topics := config.TopicsConfig{State: "howk.state"}
	pub := NewMemWebhookPublisher(mb, topics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan *broker.Message, 1)
	go func() {
		_ = mb.Subscribe(ctx, "howk.state", "g", func(_ context.Context, msg *broker.Message) error {
			received <- msg
			return nil
		})
	}()
	time.Sleep(10 * time.Millisecond)

	err := pub.PublishStateTombstone(ctx, "wh_gone")
	require.NoError(t, err)

	select {
	case msg := <-received:
		assert.Equal(t, "wh_gone", string(msg.Key))
		assert.Nil(t, msg.Value) // tombstone
		assert.Equal(t, "tombstone", msg.Headers["type"])
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestMemWebhookPublisher_PublishStateNilErrors(t *testing.T) {
	mb := NewMemBroker()
	defer mb.Close()

	pub := NewMemWebhookPublisher(mb, config.TopicsConfig{State: "s"})
	err := pub.PublishState(context.Background(), nil)
	assert.Error(t, err)
}
