package script

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockScriptStore implements ScriptStore interface
type MockScriptStore struct {
	mock.Mock
}

func (m *MockScriptStore) GetScript(ctx context.Context, configID domain.ConfigID) (string, error) {
	args := m.Called(ctx, configID)
	return args.String(0), args.Error(1)
}

func (m *MockScriptStore) SetScript(ctx context.Context, configID domain.ConfigID, scriptJSON string, ttl time.Duration) error {
	args := m.Called(ctx, configID, scriptJSON, ttl)
	return args.Error(0)
}

func (m *MockScriptStore) DeleteScript(ctx context.Context, configID domain.ConfigID) error {
	args := m.Called(ctx, configID)
	return args.Error(0)
}

// MockBrokerForConsumer implements broker.Broker
type MockBrokerForConsumer struct {
	mock.Mock
	handler broker.Handler
}

func (m *MockBrokerForConsumer) Publish(ctx context.Context, topic string, msgs ...broker.Message) error {
	args := m.Called(ctx, topic, msgs)
	return args.Error(0)
}

func (m *MockBrokerForConsumer) Subscribe(ctx context.Context, topic, group string, handler broker.Handler) error {
	args := m.Called(ctx, topic, group, handler)
	m.handler = handler
	return args.Error(0)
}

func (m *MockBrokerForConsumer) Close() error {
	args := m.Called()
	return args.Error(0)
}

// TriggerMessage simulates receiving a message (for testing)
func (m *MockBrokerForConsumer) TriggerMessage(ctx context.Context, msg *broker.Message) error {
	if m.handler != nil {
		return m.handler(ctx, msg)
	}
	return errors.New("no handler registered")
}

func TestScriptConsumer_NewScriptConsumer(t *testing.T) {
	mockBroker := new(MockBrokerForConsumer)
	loader := NewLoader()
	mockStore := new(MockScriptStore)

	consumer := NewScriptConsumer(
		mockBroker,
		loader,
		mockStore,
		"test.scripts",
		"test-group",
		1*time.Hour,
	)

	assert.NotNil(t, consumer)
	assert.False(t, consumer.IsRunning())
}

func TestScriptConsumer_StartStop(t *testing.T) {
	mockBroker := new(MockBrokerForConsumer)
	loader := NewLoader()
	mockStore := new(MockScriptStore)

	consumer := NewScriptConsumer(
		mockBroker,
		loader,
		mockStore,
		"test.scripts",
		"test-group",
		1*time.Hour,
	)

	// Mock the subscription to return immediately (simulating context cancellation)
	mockBroker.On("Subscribe", mock.Anything, "test.scripts", "test-group", mock.Anything).
		Return(context.Canceled)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the consumer
	err := consumer.Start(ctx)
	assert.NoError(t, err)
	assert.True(t, consumer.IsRunning())

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	// Stop the consumer
	err = consumer.Stop()
	assert.NoError(t, err)
	assert.False(t, consumer.IsRunning())

	mockBroker.AssertExpectations(t)
}

func TestScriptConsumer_HandleMessage_CreateScript(t *testing.T) {
	mockBroker := new(MockBrokerForConsumer)
	loader := NewLoader()
	mockStore := new(MockScriptStore)

	consumer := NewScriptConsumer(
		mockBroker,
		loader,
		mockStore,
		"test.scripts",
		"test-group",
		1*time.Hour,
	)

	// Create a script config
	script := ScriptConfig{
		ConfigID:  "test-config",
		LuaCode:   `headers["X-Test"] = "value"`,
		Hash:      "abc123",
		Version:   "1.0.0",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	scriptJSON, _ := json.Marshal(script)

	// Expect the script to be stored in Redis
	mockStore.On("SetScript", mock.Anything, domain.ConfigID("test-config"), string(scriptJSON), 1*time.Hour).
		Return(nil)

	// Create message
	msg := &broker.Message{
		Key:   []byte("test-config"),
		Value: scriptJSON,
		Headers: map[string]string{
			"script_hash": "abc123",
			"version":     "1.0.0",
		},
	}

	// Handle the message
	ctx := context.Background()
	err := consumer.handleMessage(ctx, msg)
	assert.NoError(t, err)

	// Verify script was loaded into the loader
	loadedScript, err := loader.GetScript(domain.ConfigID("test-config"))
	assert.NoError(t, err)
	assert.Equal(t, "test-config", string(loadedScript.ConfigID))
	assert.Equal(t, "abc123", loadedScript.Hash)

	mockStore.AssertExpectations(t)
}

func TestScriptConsumer_HandleMessage_DeleteScript(t *testing.T) {
	mockBroker := new(MockBrokerForConsumer)
	loader := NewLoader()
	mockStore := new(MockScriptStore)

	consumer := NewScriptConsumer(
		mockBroker,
		loader,
		mockStore,
		"test.scripts",
		"test-group",
		1*time.Hour,
	)

	// First add a script
	script := ScriptConfig{
		ConfigID: "test-config",
		LuaCode:  `headers["X-Test"] = "value"`,
		Hash:     "abc123",
	}
	loader.SetScript(&script)

	// Verify it's there
	_, err := loader.GetScript(domain.ConfigID("test-config"))
	assert.NoError(t, err)

	// Expect the script to be deleted from Redis
	mockStore.On("DeleteScript", mock.Anything, domain.ConfigID("test-config")).
		Return(nil)

	// Create tombstone message (nil value)
	msg := &broker.Message{
		Key:     []byte("test-config"),
		Value:   nil,
		Headers: map[string]string{"operation": "delete"},
	}

	// Handle the message
	ctx := context.Background()
	err = consumer.handleMessage(ctx, msg)
	assert.NoError(t, err)

	// Verify script was removed from loader
	_, err = loader.GetScript(domain.ConfigID("test-config"))
	assert.Error(t, err)

	mockStore.AssertExpectations(t)
}

func TestScriptConsumer_HandleMessage_InvalidJSON(t *testing.T) {
	mockBroker := new(MockBrokerForConsumer)
	loader := NewLoader()
	mockStore := new(MockScriptStore)

	consumer := NewScriptConsumer(
		mockBroker,
		loader,
		mockStore,
		"test.scripts",
		"test-group",
		1*time.Hour,
	)

	// Create invalid JSON message
	msg := &broker.Message{
		Key:   []byte("test-config"),
		Value: []byte("not valid json"),
	}

	// Handle the message
	ctx := context.Background()
	err := consumer.handleMessage(ctx, msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestScriptConsumer_HandleMessage_MissingConfigID(t *testing.T) {
	mockBroker := new(MockBrokerForConsumer)
	loader := NewLoader()
	mockStore := new(MockScriptStore)

	consumer := NewScriptConsumer(
		mockBroker,
		loader,
		mockStore,
		"test.scripts",
		"test-group",
		1*time.Hour,
	)

	// Create script config without ConfigID
	script := ScriptConfig{
		ConfigID: "", // Empty ConfigID
		LuaCode:  `headers["X-Test"] = "value"`,
		Hash:     "abc123",
	}
	scriptJSON, _ := json.Marshal(script)

	// Create message
	msg := &broker.Message{
		Key:   []byte("test-config"),
		Value: scriptJSON,
	}

	// Handle the message
	ctx := context.Background()
	err := consumer.handleMessage(ctx, msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ConfigID")
}

func TestScriptConsumer_HandleMessage_MissingLuaCode(t *testing.T) {
	mockBroker := new(MockBrokerForConsumer)
	loader := NewLoader()
	mockStore := new(MockScriptStore)

	consumer := NewScriptConsumer(
		mockBroker,
		loader,
		mockStore,
		"test.scripts",
		"test-group",
		1*time.Hour,
	)

	// Create script config without LuaCode
	script := ScriptConfig{
		ConfigID: "test-config",
		LuaCode:  "", // Empty LuaCode
		Hash:     "abc123",
	}
	scriptJSON, _ := json.Marshal(script)

	// Create message
	msg := &broker.Message{
		Key:   []byte("test-config"),
		Value: scriptJSON,
	}

	// Handle the message
	ctx := context.Background()
	err := consumer.handleMessage(ctx, msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "LuaCode")
}

func TestScriptConsumer_HandleMessage_RedisStoreError(t *testing.T) {
	mockBroker := new(MockBrokerForConsumer)
	loader := NewLoader()
	mockStore := new(MockScriptStore)

	consumer := NewScriptConsumer(
		mockBroker,
		loader,
		mockStore,
		"test.scripts",
		"test-group",
		1*time.Hour,
	)

	// Create a script config
	script := ScriptConfig{
		ConfigID:  "test-config",
		LuaCode:   `headers["X-Test"] = "value"`,
		Hash:      "abc123",
		Version:   "1.0.0",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	scriptJSON, _ := json.Marshal(script)

	// Expect the script storage to fail (but the handler should still succeed)
	mockStore.On("SetScript", mock.Anything, domain.ConfigID("test-config"), string(scriptJSON), 1*time.Hour).
		Return(errors.New("redis connection failed"))

	// Create message
	msg := &broker.Message{
		Key:   []byte("test-config"),
		Value: scriptJSON,
	}

	// Handle the message - should succeed even if Redis fails (logs warning)
	ctx := context.Background()
	err := consumer.handleMessage(ctx, msg)
	assert.NoError(t, err) // Should not fail - Redis error is logged but not returned

	// Verify script was still loaded into the loader (local cache)
	loadedScript, err := loader.GetScript(domain.ConfigID("test-config"))
	assert.NoError(t, err)
	assert.Equal(t, "test-config", string(loadedScript.ConfigID))

	mockStore.AssertExpectations(t)
}

func TestScriptConsumer_Start_AlreadyRunning(t *testing.T) {
	mockBroker := new(MockBrokerForConsumer)
	loader := NewLoader()
	mockStore := new(MockScriptStore)

	consumer := NewScriptConsumer(
		mockBroker,
		loader,
		mockStore,
		"test.scripts",
		"test-group",
		1*time.Hour,
	)

	// Mock the subscription
	mockBroker.On("Subscribe", mock.Anything, "test.scripts", "test-group", mock.Anything).
		Return(context.Canceled)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the consumer first time
	err := consumer.Start(ctx)
	assert.NoError(t, err)

	// Give it a moment
	time.Sleep(50 * time.Millisecond)

	// Try to start again - should fail
	err = consumer.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	// Cleanup
	consumer.Stop()
}

func TestScriptConsumer_Stop_NotRunning(t *testing.T) {
	mockBroker := new(MockBrokerForConsumer)
	loader := NewLoader()
	mockStore := new(MockScriptStore)

	consumer := NewScriptConsumer(
		mockBroker,
		loader,
		mockStore,
		"test.scripts",
		"test-group",
		1*time.Hour,
	)

	// Stop when not running - should not error
	err := consumer.Stop()
	assert.NoError(t, err)
}
