package script

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func TestEngine_Execute_BinaryPayload(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode:  "request.body = payload",
		Hash:     "binary_test",
	})

	engine := NewEngine(cfg, loader, nil, nil, nil, zerolog.Logger{})
	defer engine.Close()

	binaryPayload := []byte{
		0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x03, 0xcb, 0x48, 0xcd, 0xc9, 0xc9, 0x07, 0x00,
	}

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(binaryPayload),
		Headers:  map[string]string{},
	}

	result, err := engine.Execute(context.Background(), webhook)
	assert.NoError(t, err)
	assert.True(t, bytes.Equal(binaryPayload, []byte(result.Payload)),
		"Binary payload should be preserved without corruption")
}

func TestEngine_Execute_BinaryPayload_Transform(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	// Lua 5.1 does not support \xNN escapes in strings, use string.char instead
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode:  "request.body = payload .. string.char(0, 1, 2, 3)",
		Hash:     "binary_transform_test",
	})

	engine := NewEngine(cfg, loader, nil, nil, nil, zerolog.Logger{})
	defer engine.Close()

	originalPayload := []byte{0xde, 0xad, 0xbe, 0xef}

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(originalPayload),
		Headers:  map[string]string{},
	}

	result, err := engine.Execute(context.Background(), webhook)
	assert.NoError(t, err)

	expectedPayload := append(originalPayload, 0x00, 0x01, 0x02, 0x03)
	assert.True(t, bytes.Equal(expectedPayload, []byte(result.Payload)),
		"Binary transformation should preserve byte values")
}

func TestEngine_Execute_BinaryPayload_WithNullBytes(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode:  "request.body = payload",
		Hash:     "null_byte_test",
	})

	engine := NewEngine(cfg, loader, nil, nil, nil, zerolog.Logger{})
	defer engine.Close()

	originalPayload := []byte{
		0x00, 0x01, 0x00, 0x02, 0x00,
		0x48, 0x65, 0x6c, 0x6c, 0x6f,
		0x00,
		0x57, 0x6f, 0x72, 0x6c, 0x64,
	}

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(originalPayload),
		Headers:  map[string]string{},
	}

	result, err := engine.Execute(context.Background(), webhook)
	assert.NoError(t, err)
	assert.Equal(t, len(originalPayload), len(result.Payload),
		"Payload length should be preserved")
	assert.True(t, bytes.Equal(originalPayload, []byte(result.Payload)),
		"Payload with null bytes should be preserved exactly")
}

func TestEngine_Execute_BinaryPayload_Base64RoundTrip(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	luaCode := `
		local base64 = require("base64")
		local encoded = base64.encode(payload)
		request.body = base64.decode(encoded)
	`
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode:  luaCode,
		Hash:     "base64_roundtrip_test",
	})

	engine := NewEngine(cfg, loader, nil, nil, nil, zerolog.Logger{})
	defer engine.Close()

	originalPayload := make([]byte, 256)
	for i := 0; i < 256; i++ {
		originalPayload[i] = byte(i)
	}

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(originalPayload),
		Headers:  map[string]string{},
	}

	result, err := engine.Execute(context.Background(), webhook)
	assert.NoError(t, err)
	assert.True(t, bytes.Equal(originalPayload, []byte(result.Payload)),
		"All byte values 0-255 should be preserved through base64 round-trip")
}

func TestEngine_Execute_BinaryPayload_ImageData(t *testing.T) {
	cfg := config.LuaConfig{
		Enabled: true,
		Timeout: 1 * time.Second,
	}

	loader := NewLoader()
	luaCode := `
		request.body = payload
		headers["X-Payload-Size"] = tostring(#payload)
	`
	loader.SetScript(&ScriptConfig{
		ConfigID: "test_config",
		LuaCode:  luaCode,
		Hash:     "image_data_test",
	})

	engine := NewEngine(cfg, loader, nil, nil, nil, zerolog.Logger{})
	defer engine.Close()

	pngLikeData := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00,
	}

	webhook := &domain.Webhook{
		ConfigID: "test_config",
		Payload:  json.RawMessage(pngLikeData),
		Headers:  map[string]string{},
	}

	result, err := engine.Execute(context.Background(), webhook)
	assert.NoError(t, err)
	assert.True(t, bytes.Equal(pngLikeData, []byte(result.Payload)),
		"PNG-like binary data should be preserved")
	expectedSize := fmt.Sprintf("%d", len(pngLikeData))
	assert.Equal(t, expectedSize, result.Headers["X-Payload-Size"],
		"Payload size header should be correct")
}
