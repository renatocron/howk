//go:build !integration

package broker

import (
	"context"
	"testing"

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
