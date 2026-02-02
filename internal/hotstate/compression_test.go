package hotstate

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/howk/howk/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompressDecompressWebhook(t *testing.T) {
	original := &domain.Webhook{
		ID:             domain.WebhookID("test-webhook-123"),
		ConfigID:       domain.ConfigID("config-456"),
		Endpoint:       "https://example.com/webhook",
		EndpointHash:   domain.EndpointHash("hash123"),
		Payload:        json.RawMessage(`{"key":"value","nested":{"foo":"bar"}}`),
		Headers:        map[string]string{"Content-Type": "application/json"},
		IdempotencyKey: "idem-key-123",
		SigningSecret:  "secret456",
		Attempt:        1,
		MaxAttempts:    3,
		CreatedAt:      time.Now().UTC(),
		ScheduledAt:    time.Now().UTC().Add(time.Hour),
		ScriptHash:     "abc123hash",
	}

	// Test compress
	compressed, err := compressWebhook(original)
	require.NoError(t, err)
	require.NotNil(t, compressed)
	assert.Greater(t, len(compressed), 0, "compressed data should not be empty")

	// Test decompress
	decompressed, err := decompressWebhook(compressed)
	require.NoError(t, err)
	require.NotNil(t, decompressed)

	// Verify data integrity
	assert.Equal(t, original.ID, decompressed.ID)
	assert.Equal(t, original.ConfigID, decompressed.ConfigID)
	assert.Equal(t, original.Endpoint, decompressed.Endpoint)
	assert.Equal(t, original.EndpointHash, decompressed.EndpointHash)
	assert.Equal(t, original.Payload, decompressed.Payload)
	assert.Equal(t, original.Headers, decompressed.Headers)
	assert.Equal(t, original.IdempotencyKey, decompressed.IdempotencyKey)
	assert.Equal(t, original.SigningSecret, decompressed.SigningSecret)
	assert.Equal(t, original.Attempt, decompressed.Attempt)
	assert.Equal(t, original.MaxAttempts, decompressed.MaxAttempts)
	assert.True(t, original.CreatedAt.Equal(decompressed.CreatedAt), "CreatedAt should match")
	assert.True(t, original.ScheduledAt.Equal(decompressed.ScheduledAt), "ScheduledAt should match")
	assert.Equal(t, original.ScriptHash, decompressed.ScriptHash)
}

func TestDecompressWebhook_InvalidData(t *testing.T) {
	// Test with invalid data (not valid zstd)
	invalidData := []byte("invalid compressed data")
	_, err := decompressWebhook(invalidData)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read zstd data")
}

func TestDecompressWebhook_EmptyData(t *testing.T) {
	// Test with empty data
	_, err := decompressWebhook([]byte{})
	assert.Error(t, err)
}

func TestCompressWebhook_LargePayload(t *testing.T) {
	// Create a large payload to test compression efficiency
	largeData := make(map[string]string)
	for i := 0; i < 1000; i++ {
		largeData[string(rune('a'+i%26))+string(rune('0'+i/26))] = "This is a test value that will be repeated to create a large payload"
	}
	payloadBytes, _ := json.Marshal(largeData)

	webhook := &domain.Webhook{
		ID:          domain.WebhookID("large-webhook"),
		ConfigID:    domain.ConfigID("bulk-config"),
		Endpoint:    "https://example.com/webhook",
		EndpointHash: domain.EndpointHash("bulk-hash"),
		Payload:     json.RawMessage(payloadBytes),
		Headers:     map[string]string{"Content-Type": "application/json"},
		Attempt:     0,
		MaxAttempts: 5,
		CreatedAt:   time.Now().UTC(),
		ScheduledAt: time.Now().UTC(),
	}

	compressed, err := compressWebhook(webhook)
	require.NoError(t, err)
	require.NotNil(t, compressed)

	// Verify decompression
	decompressed, err := decompressWebhook(compressed)
	require.NoError(t, err)
	assert.Equal(t, webhook.ID, decompressed.ID)
	assert.Equal(t, len(webhook.Payload), len(decompressed.Payload))
}

func TestCompressDecompressWebhook_NilPayload(t *testing.T) {
	webhook := &domain.Webhook{
		ID:          domain.WebhookID("nil-payload-webhook"),
		ConfigID:    domain.ConfigID("config-nil"),
		Endpoint:    "https://example.com/webhook",
		EndpointHash: domain.EndpointHash("nil-hash"),
		Payload:     nil,
		Headers:     map[string]string{},
		Attempt:     0,
		MaxAttempts: 3,
		CreatedAt:   time.Now().UTC(),
		ScheduledAt: time.Now().UTC(),
	}

	compressed, err := compressWebhook(webhook)
	require.NoError(t, err)

	decompressed, err := decompressWebhook(compressed)
	require.NoError(t, err)
	assert.Equal(t, webhook.ID, decompressed.ID)
}

func TestCompressDecompressWebhook_EmptyHeaders(t *testing.T) {
	webhook := &domain.Webhook{
		ID:          domain.WebhookID("empty-headers-webhook"),
		ConfigID:    domain.ConfigID("config-empty"),
		Endpoint:    "https://example.com/webhook",
		EndpointHash: domain.EndpointHash("empty-hash"),
		Payload:     json.RawMessage(`{"simple":true}`),
		Headers:     map[string]string{},
		Attempt:     1,
		MaxAttempts: 3,
		CreatedAt:   time.Now().UTC(),
		ScheduledAt: time.Now().UTC(),
	}

	compressed, err := compressWebhook(webhook)
	require.NoError(t, err)

	decompressed, err := decompressWebhook(compressed)
	require.NoError(t, err)
	// JSON unmarshal of empty map results in nil, which is equivalent
	assert.Empty(t, decompressed.Headers)
}
