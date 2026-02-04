//go:build !integration

package broker

import (
	"context"
	"testing"

	"github.com/howk/howk/internal/config"
	"github.com/stretchr/testify/assert"
)

// TestKafkaBroker_Ping_Closed tests Ping on a closed broker
func TestKafkaBroker_Ping_Closed(t *testing.T) {
	// Create a manually closed broker
	b := &KafkaBroker{
		closed: true,
	}

	err := b.Ping(context.Background())
	assert.EqualError(t, err, "broker is closed")
}

// TestCompressionCodec tests the compression codec selection
func TestCompressionCodec(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int // CompressionNone = 0, CompressionGZIP = 1, etc.
	}{
		{"none", "none", 0},
		{"invalid", "invalid", 0},
		{"", "", 0},
		{"gzip", "gzip", 1},
		{"snappy", "snappy", 2},
		{"lz4", "lz4", 3},
		{"zstd", "zstd", 4},
		{"GZIP", "GZIP", 0}, // Case sensitive, falls back to none
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codec := compressionCodec(tt.input)
			assert.Equal(t, tt.expected, int(codec))
		})
	}
}

// TestKafkaWebhookPublisher_Ping_NilBroker tests Ping with nil broker
func TestKafkaWebhookPublisher_Ping_NilBroker(t *testing.T) {
	// Publisher with nil broker
	pub := &KafkaWebhookPublisher{
		broker: nil,
	}

	err := pub.Ping(context.Background())
	assert.EqualError(t, err, "publisher broker not configured")
}

// TestKafkaWebhookPublisher_Ping_NilPublisher tests Ping on nil publisher
func TestKafkaWebhookPublisher_Ping_NilPublisher(t *testing.T) {
	var pub *KafkaWebhookPublisher

	err := pub.Ping(context.Background())
	assert.EqualError(t, err, "publisher broker not configured")
}

// TestKafkaBroker_Publish_Closed tests Publish on closed broker
func TestKafkaBroker_Publish_Closed(t *testing.T) {
	b := &KafkaBroker{
		closed: true,
	}

	err := b.Publish(context.Background(), "test-topic", Message{
		Key:   []byte("key"),
		Value: []byte("value"),
	})
	assert.EqualError(t, err, "broker is closed")
}

// TestKafkaBroker_Close_Idempotent tests that Close is idempotent
func TestKafkaBroker_Close_Idempotent(t *testing.T) {
	// Just test the state management without real Kafka connection
	b := &KafkaBroker{
		closed: false,
	}

	// First close should work (but will fail since no real client)
	// Note: We can't fully test this without a real/mock sarama client

	// Mark as closed manually for testing
	b.closed = true

	// Second close should return nil immediately due to idempotency check
	err := b.Close()
	assert.NoError(t, err)
}

// TestKafkaBroker_Subscribe_NotInitialized tests Subscribe on uninitialized broker
func TestKafkaBroker_Subscribe_NotInitialized(t *testing.T) {
	b := &KafkaBroker{
		closed: false,
		// client is nil
	}

	// Should fail when trying to create consumer group from nil client
	err := b.Subscribe(context.Background(), "test-topic", "test-group", func(ctx context.Context, msg *Message) error {
		return nil
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "create consumer group")
}

// TestMessage_NilKey tests Message with nil key
func TestMessage_NilKey(t *testing.T) {
	msg := Message{
		Key:     nil,
		Value:   []byte("value"),
		Headers: map[string]string{"header": "value"},
	}
	assert.Nil(t, msg.Key)
	assert.Equal(t, []byte("value"), msg.Value)
}

// TestMessage_EmptyValue tests Message with empty value
func TestMessage_EmptyValue(t *testing.T) {
	msg := Message{
		Key:     []byte("key"),
		Value:   []byte{},
		Headers: map[string]string{},
	}
	assert.Equal(t, []byte("key"), msg.Key)
	assert.Empty(t, msg.Value)
}

// TestMessage_WithHeaders tests Message with headers
func TestMessage_WithHeaders(t *testing.T) {
	msg := Message{
		Key:   []byte("key"),
		Value: []byte("value"),
		Headers: map[string]string{
			"config_id": "cfg-123",
			"attempt":   "3",
		},
	}
	assert.Equal(t, "cfg-123", msg.Headers["config_id"])
	assert.Equal(t, "3", msg.Headers["attempt"])
}

// TestNewKafkaWebhookPublisher tests the publisher constructor
func TestNewKafkaWebhookPublisher(t *testing.T) {
	broker := &KafkaBroker{}
	topics := config.TopicsConfig{
		Pending:    "pending",
		Results:    "results",
		DeadLetter: "dl",
		Scripts:    "scripts",
		Slow:       "slow",
	}

	pub := NewKafkaWebhookPublisher(broker, topics)
	assert.NotNil(t, pub)
	assert.Equal(t, broker, pub.broker)
	assert.Equal(t, topics, pub.topics)
}

// TestKafkaWebhookPublisher_Close tests Close on publisher
func TestKafkaWebhookPublisher_Close(t *testing.T) {
	// Create a closed broker
	broker := &KafkaBroker{closed: true}
	pub := NewKafkaWebhookPublisher(broker, config.TopicsConfig{})

	// Should delegate to broker.Close() which returns nil if already closed
	err := pub.Close()
	assert.NoError(t, err)
}
