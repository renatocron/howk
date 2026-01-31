package script

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/domain"
)

// mockBroker implements broker.Broker for testing
type mockBroker struct {
	publishedMessages []broker.Message
	publishError      error
}

func (m *mockBroker) Publish(ctx context.Context, topic string, msgs ...broker.Message) error {
	if m.publishError != nil {
		return m.publishError
	}
	m.publishedMessages = append(m.publishedMessages, msgs...)
	return nil
}

func (m *mockBroker) Subscribe(ctx context.Context, topic string, group string, handler broker.Handler) error {
	return nil
}

func (m *mockBroker) Close() error {
	return nil
}

func TestPublisher_PublishScript(t *testing.T) {
	ctx := context.Background()
	mock := &mockBroker{}
	publisher := NewPublisher(mock, "howk.scripts")

	script := &ScriptConfig{
		ConfigID:  "test_config_123",
		LuaCode:   `headers["X-Custom"] = "value"`,
		Hash:      "abc123def456",
		Version:   "v1.0.0",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err := publisher.PublishScript(ctx, script)
	if err != nil {
		t.Fatalf("PublishScript() unexpected error: %v", err)
	}

	// Verify message was published
	if len(mock.publishedMessages) != 1 {
		t.Fatalf("Expected 1 published message, got %d", len(mock.publishedMessages))
	}

	msg := mock.publishedMessages[0]

	// Verify key is config_id
	if string(msg.Key) != string(script.ConfigID) {
		t.Errorf("Expected key %s, got %s", script.ConfigID, string(msg.Key))
	}

	// Verify value is marshaled ScriptConfig
	var decoded ScriptConfig
	if err := json.Unmarshal(msg.Value, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal published message: %v", err)
	}

	if decoded.ConfigID != script.ConfigID {
		t.Errorf("Expected ConfigID %s, got %s", script.ConfigID, decoded.ConfigID)
	}

	if decoded.LuaCode != script.LuaCode {
		t.Errorf("Expected LuaCode %s, got %s", script.LuaCode, decoded.LuaCode)
	}

	if decoded.Hash != script.Hash {
		t.Errorf("Expected Hash %s, got %s", script.Hash, decoded.Hash)
	}

	// Verify headers
	if msg.Headers["script_hash"] != script.Hash {
		t.Errorf("Expected header script_hash=%s, got %s", script.Hash, msg.Headers["script_hash"])
	}

	if msg.Headers["version"] != script.Version {
		t.Errorf("Expected header version=%s, got %s", script.Version, msg.Headers["version"])
	}
}

func TestPublisher_DeleteScript(t *testing.T) {
	ctx := context.Background()
	mock := &mockBroker{}
	publisher := NewPublisher(mock, "howk.scripts")

	configID := domain.ConfigID("test_config_123")

	err := publisher.DeleteScript(ctx, configID)
	if err != nil {
		t.Fatalf("DeleteScript() unexpected error: %v", err)
	}

	// Verify tombstone was published
	if len(mock.publishedMessages) != 1 {
		t.Fatalf("Expected 1 published message, got %d", len(mock.publishedMessages))
	}

	msg := mock.publishedMessages[0]

	// Verify key is config_id
	if string(msg.Key) != string(configID) {
		t.Errorf("Expected key %s, got %s", configID, string(msg.Key))
	}

	// Verify value is nil (tombstone)
	if msg.Value != nil {
		t.Errorf("Expected nil value for tombstone, got %v", msg.Value)
	}

	// Verify operation header
	if msg.Headers["operation"] != "delete" {
		t.Errorf("Expected header operation=delete, got %s", msg.Headers["operation"])
	}
}
