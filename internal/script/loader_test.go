package script

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/howk/howk/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoader_GetScript_NotFound tests GetScript returns error for non-existent script
func TestLoader_GetScript_NotFound(t *testing.T) {
	loader := NewLoader()

	script, err := loader.GetScript("non-existent-config")
	assert.Error(t, err)
	assert.Nil(t, script)
	assert.Contains(t, err.Error(), "script not found")
}

// TestLoader_GetScriptHash_NotFound tests GetScriptHash returns error for non-existent script
func TestLoader_GetScriptHash_NotFound(t *testing.T) {
	loader := NewLoader()

	hash, err := loader.GetScriptHash("non-existent-config")
	assert.Error(t, err)
	assert.Empty(t, hash)
	assert.Contains(t, err.Error(), "script not found")
}

// TestLoader_LoadFromJSON_InvalidJSON tests LoadFromJSON with invalid JSON
func TestLoader_LoadFromJSON_InvalidJSON(t *testing.T) {
	loader := NewLoader()

	err := loader.LoadFromJSON("cfg_123", "{invalid json}")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal script config")
}

// TestLoader_LoadFromJSON_MissingRequiredFields tests LoadFromJSON with JSON missing required fields
func TestLoader_LoadFromJSON_MissingRequiredFields(t *testing.T) {
	loader := NewLoader()

	// Valid JSON but missing required fields - this actually works because
	// ScriptConfig fields are optional at the JSON level
	json := `{"config_id": "cfg_123"}`
	err := loader.LoadFromJSON("cfg_123", json)
	assert.NoError(t, err) // Loading succeeds even with partial data

	script, err := loader.GetScript("cfg_123")
	assert.NoError(t, err)
	assert.Equal(t, domain.ConfigID("cfg_123"), script.ConfigID)
	assert.Empty(t, script.Hash) // Hash was not provided
}

// TestLoader_LoadFromJSON_EmptyJSON tests LoadFromJSON with empty JSON object
// Note: Empty JSON results in empty ConfigID, which gets stored under empty key
func TestLoader_LoadFromJSON_EmptyJSON(t *testing.T) {
	loader := NewLoader()

	// Empty JSON object - will create script with empty ConfigID
	err := loader.LoadFromJSON("cfg_empty", "{}")
	assert.NoError(t, err) // Loading succeeds

	// Script is stored with empty ConfigID (since JSON had no config_id field)
	// It won't be retrievable via "cfg_empty" since the map key is the ConfigID from JSON
	// This is expected behavior - the config_id field in JSON should match the key
	assert.Equal(t, 1, loader.Count()) // Script is in the loader

	// Can retrieve by empty key (though not useful in practice)
	script, err := loader.GetScript("")
	assert.NoError(t, err)
	assert.NotNil(t, script)
	assert.Empty(t, script.ConfigID)
}

// TestLoader_ConcurrentAccess tests concurrent read/write access to loader
func TestLoader_ConcurrentAccess(t *testing.T) {
	loader := NewLoader()

	// Pre-load some scripts
	for i := 0; i < 10; i++ {
		loader.SetScript(&ScriptConfig{
			ConfigID: domain.ConfigID("cfg_" + string(rune('0'+i))),
			Hash:     "hash_" + string(rune('0'+i)),
			LuaCode:  "print('hello ' + " + string(rune('0'+i)) + ")",
		})
	}

	var wg sync.WaitGroup
	numGoroutines := 100
	iterations := 50

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				configID := domain.ConfigID("cfg_" + string(rune('0'+(j%10))))
				_, _ = loader.GetScript(configID)
				_, _ = loader.GetScriptHash(configID)
				_ = loader.Count()
			}
		}(i)
	}

	// Concurrent writes
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations/2; j++ {
				loader.SetScript(&ScriptConfig{
					ConfigID: domain.ConfigID("cfg_new_" + string(rune('0'+id))),
					Hash:     "hash_new_" + string(rune('0'+j)),
				})
			}
		}(i)
	}

	// Concurrent deletes
	for i := 0; i < numGoroutines/4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations/4; j++ {
				configID := domain.ConfigID("cfg_" + string(rune('0'+((id+j)%10))))
				loader.DeleteScript(configID)
			}
		}(i)
	}

	wg.Wait()

	// No panic = success for race detector
	assert.True(t, true)
}

// TestLoader_LoadFromRedis_MissingScripts tests loading from Redis with some missing scripts
func TestLoader_LoadFromRedis_MissingScripts(t *testing.T) {
	loader := NewLoader()

	// Mock function that returns error for some scripts
	getScriptFunc := func(ctx context.Context, configID domain.ConfigID) (string, error) {
		if configID == "cfg_missing" {
			return "", errors.New("script not found in redis")
		}
		return `{"config_id": "` + string(configID) + `", "hash": "hash_123", "source": "print('hello')"}`, nil
	}

	configIDs := []domain.ConfigID{"cfg_1", "cfg_missing", "cfg_2"}

	err := loader.LoadFromRedis(context.Background(), getScriptFunc, configIDs)
	assert.NoError(t, err) // Should not fail even if some scripts are missing

	// cfg_1 should be loaded
	script, err := loader.GetScript("cfg_1")
	assert.NoError(t, err)
	assert.Equal(t, domain.ConfigID("cfg_1"), script.ConfigID)

	// cfg_2 should be loaded
	script, err = loader.GetScript("cfg_2")
	assert.NoError(t, err)
	assert.Equal(t, domain.ConfigID("cfg_2"), script.ConfigID)

	// cfg_missing should not be loaded (skipped silently)
	_, err = loader.GetScript("cfg_missing")
	assert.Error(t, err)
}

// TestLoader_LoadFromRedis_InvalidJSON tests loading from Redis with invalid JSON
func TestLoader_LoadFromRedis_InvalidJSON(t *testing.T) {
	loader := NewLoader()

	getScriptFunc := func(ctx context.Context, configID domain.ConfigID) (string, error) {
		return "{invalid json}", nil
	}

	configIDs := []domain.ConfigID{"cfg_bad"}

	err := loader.LoadFromRedis(context.Background(), getScriptFunc, configIDs)
	assert.Error(t, err) // Should fail because JSON is invalid
	assert.Contains(t, err.Error(), "load script for cfg_bad")
}

// TestLoader_Count_Empty tests Count on empty loader
func TestLoader_Count_Empty(t *testing.T) {
	loader := NewLoader()
	assert.Equal(t, 0, loader.Count())
}

// TestLoader_Count_AfterDelete tests Count decreases after delete
func TestLoader_Count_AfterDelete(t *testing.T) {
	loader := NewLoader()

	loader.SetScript(&ScriptConfig{
		ConfigID: "cfg_1",
		Hash:     "hash_1",
	})
	loader.SetScript(&ScriptConfig{
		ConfigID: "cfg_2",
		Hash:     "hash_2",
	})

	assert.Equal(t, 2, loader.Count())

	loader.DeleteScript("cfg_1")
	assert.Equal(t, 1, loader.Count())

	loader.DeleteScript("cfg_2")
	assert.Equal(t, 0, loader.Count())

	// Deleting non-existent doesn't break
	loader.DeleteScript("cfg_nonexistent")
	assert.Equal(t, 0, loader.Count())
}

// TestLoader_SetScript_Update tests that SetScript updates existing scripts
func TestLoader_SetScript_Update(t *testing.T) {
	loader := NewLoader()

	// Set initial script
	loader.SetScript(&ScriptConfig{
		ConfigID: "cfg_1",
		Hash:     "hash_v1",
		LuaCode:  "print('v1')",
	})

	script, err := loader.GetScript("cfg_1")
	require.NoError(t, err)
	assert.Equal(t, "hash_v1", script.Hash)

	// Update same config ID
	loader.SetScript(&ScriptConfig{
		ConfigID: "cfg_1",
		Hash:     "hash_v2",
		LuaCode:  "print('v2')",
	})

	script, err = loader.GetScript("cfg_1")
	require.NoError(t, err)
	assert.Equal(t, "hash_v2", script.Hash)
	assert.Equal(t, "print('v2')", script.LuaCode)
}

// TestLoader_RaceDetector is specifically designed to trigger the race detector
func TestLoader_RaceDetector(t *testing.T) {
	loader := NewLoader()
	ctx := context.Background()

	var wg sync.WaitGroup

	// Writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			loader.SetScript(&ScriptConfig{
				ConfigID: domain.ConfigID("cfg_race"),
				Hash:     "hash",
			})
		}
	}()

	// Reader
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_, _ = loader.GetScript("cfg_race")
			_, _ = loader.GetScriptHash("cfg_race")
		}
	}()

	// Counter
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = loader.Count()
		}
	}()

	// Deleter
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			loader.DeleteScript("cfg_race")
		}
	}()

	// Redis loader (simulated)
	wg.Add(1)
	go func() {
		defer wg.Done()
		getScriptFunc := func(ctx context.Context, configID domain.ConfigID) (string, error) {
			return `{"config_id": "cfg_race", "hash": "hash", "lua_code": "print(1)"}`, nil
		}
		for i := 0; i < 100; i++ {
			_ = loader.LoadFromRedis(ctx, getScriptFunc, []domain.ConfigID{"cfg_race"})
		}
	}()

	wg.Wait()
}
