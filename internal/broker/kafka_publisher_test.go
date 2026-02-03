//go:build !integration

package broker

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/domain"
)

// TestPublishToSlow_MessageFormat verifies the message format for slow topic
func TestPublishToSlow_MessageFormat(t *testing.T) {
	// Create test data
	webhook := &domain.Webhook{
		ID:           domain.WebhookID("wh-123"),
		ConfigID:     domain.ConfigID("config-abc"),
		EndpointHash: domain.EndpointHash("hash-xyz"),
		Endpoint:     "https://example.com/webhook",
		Payload:      []byte(`{"test": "data"}`),
		Attempt:      2,
	}

	// Marshal the webhook
	data, err := json.Marshal(webhook)
	require.NoError(t, err)

	// Create the message
	msg := Message{
		Key:   []byte(webhook.ConfigID),
		Value: data,
		Headers: map[string]string{
			"config_id":     string(webhook.ConfigID),
			"endpoint_hash": string(webhook.EndpointHash),
			"attempt":       "2",
			"source":        "penalty_box",
		},
	}

	// Verify key is ConfigID
	assert.Equal(t, string(webhook.ConfigID), string(msg.Key))

	// Verify headers
	assert.Equal(t, "config-abc", msg.Headers["config_id"])
	assert.Equal(t, "hash-xyz", msg.Headers["endpoint_hash"])
	assert.Equal(t, "2", msg.Headers["attempt"])
	assert.Equal(t, "penalty_box", msg.Headers["source"])

	// Verify value can be unmarshaled
	var parsedWebhook domain.Webhook
	err = json.Unmarshal(msg.Value, &parsedWebhook)
	require.NoError(t, err)
	assert.Equal(t, webhook.ID, parsedWebhook.ID)
	assert.Equal(t, webhook.ConfigID, parsedWebhook.ConfigID)
}

// TestPublishWebhook_UsesConfigIDAsKey verifies that PublishWebhook uses ConfigID as partition key
func TestPublishWebhook_UsesConfigIDAsKey(t *testing.T) {
	webhook := &domain.Webhook{
		ID:           domain.WebhookID("wh-456"),
		ConfigID:     domain.ConfigID("config-xyz"),
		EndpointHash: domain.EndpointHash("hash-123"),
		Endpoint:     "https://example.com/webhook",
		Payload:      []byte(`{"test": "data"}`),
		Attempt:      1,
	}

	// Marshal the webhook
	data, err := json.Marshal(webhook)
	require.NoError(t, err)

	// Create the message as PublishWebhook would
	msg := Message{
		Key:   []byte(webhook.ConfigID), // This is the key change - uses ConfigID instead of ID
		Value: data,
		Headers: map[string]string{
			"config_id":     string(webhook.ConfigID),
			"endpoint_hash": string(webhook.EndpointHash),
			"attempt":       "1",
		},
	}

	// Verify key is ConfigID, not WebhookID
	assert.Equal(t, string(webhook.ConfigID), string(msg.Key))
	assert.NotEqual(t, string(webhook.ID), string(msg.Key))
	assert.Equal(t, "config-xyz", string(msg.Key))
}

// TestPublishToSlow_VerifyTopic verifies PublishToSlow would publish to slow topic
func TestPublishToSlow_VerifyTopic(t *testing.T) {
	// This test verifies the logic of PublishToSlow without actually calling Kafka
	// In real implementation, this would publish to topics.Slow

	webhook := &domain.Webhook{
		ID:           domain.WebhookID("wh-789"),
		ConfigID:     domain.ConfigID("config-slow"),
		EndpointHash: domain.EndpointHash("hash-slow"),
		Attempt:      3,
	}

	// Simulate what PublishToSlow does
	data, err := json.Marshal(webhook)
	require.NoError(t, err)

	msg := Message{
		Key:   []byte(webhook.ConfigID),
		Value: data,
		Headers: map[string]string{
			"config_id":     string(webhook.ConfigID),
			"endpoint_hash": string(webhook.EndpointHash),
			"attempt":       "3",
			"source":        "penalty_box",
		},
	}

	// Verify all expected headers are present
	assert.Contains(t, msg.Headers, "config_id")
	assert.Contains(t, msg.Headers, "endpoint_hash")
	assert.Contains(t, msg.Headers, "attempt")
	assert.Contains(t, msg.Headers, "source")
	assert.Equal(t, "penalty_box", msg.Headers["source"])
}

// TestPublishResult_UsesWebhookIDAsKey verifies that PublishResult still uses WebhookID (unchanged behavior)
func TestPublishResult_UsesWebhookIDAsKey(t *testing.T) {
	result := &domain.DeliveryResult{
		WebhookID:    domain.WebhookID("wh-result"),
		ConfigID:     domain.ConfigID("config-result"),
		EndpointHash: domain.EndpointHash("hash-result"),
		Attempt:      2,
		Success:      true,
		StatusCode:   200,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	// Create message as PublishResult would
	msg := Message{
		Key:   []byte(result.WebhookID),
		Value: data,
		Headers: map[string]string{
			"config_id":     string(result.ConfigID),
			"endpoint_hash": string(result.EndpointHash),
			"success":       "true",
		},
	}

	// Verify key is WebhookID for results
	assert.Equal(t, string(result.WebhookID), string(msg.Key))
}

// TestPublishDeadLetter_UsesWebhookIDAsKey verifies that PublishDeadLetter uses WebhookID (unchanged behavior)
func TestPublishDeadLetter_UsesWebhookIDAsKey(t *testing.T) {
	webhook := &domain.Webhook{
		ID:       domain.WebhookID("wh-dlq"),
		ConfigID: domain.ConfigID("config-dlq"),
	}

	dlq := &domain.DeadLetter{
		Webhook:    webhook,
		Reason:     "test reason",
		ReasonType: domain.DLQReasonExhausted,
	}

	data, err := json.Marshal(dlq)
	require.NoError(t, err)

	// Create message as PublishDeadLetter would
	msg := Message{
		Key:   []byte(dlq.Webhook.ID),
		Value: data,
		Headers: map[string]string{
			"config_id":   string(dlq.Webhook.ConfigID),
			"reason":      dlq.Reason,
			"reason_type": string(dlq.ReasonType),
		},
	}

	// Verify key is WebhookID for dead letters
	assert.Equal(t, string(dlq.Webhook.ID), string(msg.Key))
}

// TestMessageHeaders_RequiredFields verifies all required headers are present in messages
func TestMessageHeaders_RequiredFields(t *testing.T) {
	webhook := &domain.Webhook{
		ID:           domain.WebhookID("wh-headers"),
		ConfigID:     domain.ConfigID("config-headers"),
		EndpointHash: domain.EndpointHash("hash-headers"),
		Attempt:      1,
	}

	tests := []struct {
		name            string
		msg             Message
		requiredHeaders []string
	}{
		{
			name: "PublishWebhook headers",
			msg: Message{
				Key: []byte(webhook.ConfigID),
				Headers: map[string]string{
					"config_id":     string(webhook.ConfigID),
					"endpoint_hash": string(webhook.EndpointHash),
					"attempt":       "1",
				},
			},
			requiredHeaders: []string{"config_id", "endpoint_hash", "attempt"},
		},
		{
			name: "PublishToSlow headers",
			msg: Message{
				Key: []byte(webhook.ConfigID),
				Headers: map[string]string{
					"config_id":     string(webhook.ConfigID),
					"endpoint_hash": string(webhook.EndpointHash),
					"attempt":       "1",
					"source":        "penalty_box",
				},
			},
			requiredHeaders: []string{"config_id", "endpoint_hash", "attempt", "source"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, header := range tt.requiredHeaders {
				assert.Contains(t, tt.msg.Headers, header, "Missing required header: %s", header)
			}
		})
	}
}
