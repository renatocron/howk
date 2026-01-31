package script

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

func TestEngine_Execute_SimpleHeaderTransform(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode:  `headers["X-Custom"] = "test_value"`,
		Hash:     "abc123",
	})

	engine := NewEngine(cfg, loader)
	defer engine.Close()

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(`{"test": "data"}`),
		Headers:  map[string]string{},
	}

	result, err := engine.Execute(context.Background(), webhook)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}

	if result.Headers["X-Custom"] != "test_value" {
		t.Errorf("Expected header X-Custom=test_value, got %s", result.Headers["X-Custom"])
	}
}

func TestEngine_Execute_JSONTransform(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode: `
			local json = require("json")
			local data = json.decode(payload)
			data.enriched = true
			request.body = json.encode(data)
		`,
		Hash: "def456",
	})

	engine := NewEngine(cfg, loader)
	defer engine.Close()

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(`{"test": "data"}`),
		Headers:  map[string]string{},
	}

	result, err := engine.Execute(context.Background(), webhook)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}

	// Parse the transformed payload
	var transformed map[string]interface{}
	if err := json.Unmarshal(result.Payload, &transformed); err != nil {
		t.Fatalf("Failed to unmarshal transformed payload: %v", err)
	}

	if enriched, ok := transformed["enriched"].(bool); !ok || !enriched {
		t.Errorf("Expected enriched=true in payload, got %v", transformed)
	}

	if test, ok := transformed["test"].(string); !ok || test != "data" {
		t.Errorf("Expected test=data in payload, got %v", transformed)
	}
}

func TestEngine_Execute_Base64(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode: `
			local base64 = require("base64")
			local encoded = base64.encode("hello world")
			headers["X-Encoded"] = encoded

			local decoded = base64.decode(encoded)
			headers["X-Decoded"] = decoded
		`,
		Hash: "ghi789",
	})

	engine := NewEngine(cfg, loader)
	defer engine.Close()

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(`{}`),
		Headers:  map[string]string{},
	}

	result, err := engine.Execute(context.Background(), webhook)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}

	if result.Headers["X-Encoded"] != "aGVsbG8gd29ybGQ=" {
		t.Errorf("Expected X-Encoded=aGVsbG8gd29ybGQ=, got %s", result.Headers["X-Encoded"])
	}

	if result.Headers["X-Decoded"] != "hello world" {
		t.Errorf("Expected X-Decoded='hello world', got %s", result.Headers["X-Decoded"])
	}
}

func TestEngine_Execute_MetadataAccess(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode: `
			headers["X-Webhook-ID"] = metadata.webhook_id
			headers["X-Config-ID"] = metadata.config_id
			headers["X-Attempt"] = tostring(metadata.attempt)
		`,
		Hash: "jkl012",
	})

	engine := NewEngine(cfg, loader)
	defer engine.Close()

	webhook := &domain.Webhook{
		ID:       "wh_123",
		ConfigID: "test_config",
		Payload:  json.RawMessage(`{}`),
		Headers:  map[string]string{},
		Attempt:  2,
	}

	result, err := engine.Execute(context.Background(), webhook)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}

	if result.Headers["X-Webhook-ID"] != "wh_123" {
		t.Errorf("Expected X-Webhook-ID=wh_123, got %s", result.Headers["X-Webhook-ID"])
	}

	if result.Headers["X-Config-ID"] != "test_config" {
		t.Errorf("Expected X-Config-ID=test_config, got %s", result.Headers["X-Config-ID"])
	}

	if result.Headers["X-Attempt"] != "2" {
		t.Errorf("Expected X-Attempt=2, got %s", result.Headers["X-Attempt"])
	}
}

func TestEngine_Execute_ScriptNotFound(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	engine := NewEngine(cfg, loader)
	defer engine.Close()

	webhook := &domain.Webhook{
		ConfigID: "nonexistent",
		Payload:  json.RawMessage(`{}`),
	}

	_, err := engine.Execute(context.Background(), webhook)
	if err == nil {
		t.Fatal("Expected error for nonexistent script")
	}

	scriptErr, ok := err.(*ScriptError)
	if !ok {
		t.Fatalf("Expected ScriptError, got %T", err)
	}

	if scriptErr.Type != ScriptErrorNotFound {
		t.Errorf("Expected ScriptErrorNotFound, got %s", scriptErr.Type)
	}
}

func TestEngine_Execute_Disabled(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: false,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode:  `headers["X-Test"] = "value"`,
		Hash:     "xyz",
	})

	engine := NewEngine(cfg, loader)
	defer engine.Close()

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(`{}`),
	}

	_, err := engine.Execute(context.Background(), webhook)
	if err == nil {
		t.Fatal("Expected error when scripts disabled")
	}

	scriptErr, ok := err.(*ScriptError)
	if !ok {
		t.Fatalf("Expected ScriptError, got %T", err)
	}

	if scriptErr.Type != ScriptErrorDisabled {
		t.Errorf("Expected ScriptErrorDisabled, got %s", scriptErr.Type)
	}
}

func TestEngine_Execute_Timeout(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 100 * time.Millisecond,
	}

	loader := NewLoader()
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode: `
			-- Infinite loop to trigger timeout
			while true do
				local x = 1 + 1
			end
		`,
		Hash: "timeout_test",
	})

	engine := NewEngine(cfg, loader)
	defer engine.Close()

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(`{}`),
	}

	_, err := engine.Execute(context.Background(), webhook)
	if err == nil {
		t.Fatal("Expected timeout error")
	}

	scriptErr, ok := err.(*ScriptError)
	if !ok {
		t.Fatalf("Expected ScriptError, got %T", err)
	}

	if scriptErr.Type != ScriptErrorTimeout {
		t.Errorf("Expected ScriptErrorTimeout, got %s", scriptErr.Type)
	}
}

func TestEngine_Execute_RuntimeError(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode:  `error("intentional error")`,
		Hash:     "error_test",
	})

	engine := NewEngine(cfg, loader)
	defer engine.Close()

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(`{}`),
	}

	_, err := engine.Execute(context.Background(), webhook)
	if err == nil {
		t.Fatal("Expected runtime error")
	}

	scriptErr, ok := err.(*ScriptError)
	if !ok {
		t.Fatalf("Expected ScriptError, got %T", err)
	}

	if scriptErr.Type != ScriptErrorRuntime {
		t.Errorf("Expected ScriptErrorRuntime, got %s", scriptErr.Type)
	}
}
