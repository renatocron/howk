package script

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	lua "github.com/yuin/gopher-lua"

	"github.com/howk/howk/internal/domain"
)

// CompiledScript represents a pre-compiled Lua script
type CompiledScript struct {
	Config       *ScriptConfig
	CompiledCode *lua.FunctionProto
}

// Loader loads and caches scripts from Kafka/Redis
type Loader struct {
	mu      sync.RWMutex
	scripts map[domain.ConfigID]*ScriptConfig
}

// NewLoader creates a new script loader
func NewLoader() *Loader {
	return &Loader{
		scripts: make(map[domain.ConfigID]*ScriptConfig),
	}
}

// GetScript retrieves a script by config ID
func (l *Loader) GetScript(configID domain.ConfigID) (*ScriptConfig, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	script, ok := l.scripts[configID]
	if !ok {
		return nil, fmt.Errorf("script not found for config_id: %s", configID)
	}

	return script, nil
}

// GetScriptHash retrieves just the hash for a config ID
func (l *Loader) GetScriptHash(configID domain.ConfigID) (string, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	script, ok := l.scripts[configID]
	if !ok {
		return "", fmt.Errorf("script not found")
	}

	return script.Hash, nil
}

// SetScript stores or updates a script in the cache
func (l *Loader) SetScript(script *ScriptConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.scripts[script.ConfigID] = script
}

// DeleteScript removes a script from the cache
func (l *Loader) DeleteScript(configID domain.ConfigID) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.scripts, configID)
}

// LoadFromJSON loads a script from JSON-encoded ScriptConfig
func (l *Loader) LoadFromJSON(configID domain.ConfigID, scriptJSON string) error {
	var script ScriptConfig
	if err := json.Unmarshal([]byte(scriptJSON), &script); err != nil {
		return fmt.Errorf("unmarshal script config: %w", err)
	}

	l.SetScript(&script)
	return nil
}

// LoadFromRedis loads all scripts from Redis cache
func (l *Loader) LoadFromRedis(ctx context.Context, getScriptFunc func(context.Context, domain.ConfigID) (string, error), configIDs []domain.ConfigID) error {
	for _, configID := range configIDs {
		scriptJSON, err := getScriptFunc(ctx, configID)
		if err != nil {
			// Skip missing scripts
			continue
		}

		if err := l.LoadFromJSON(configID, scriptJSON); err != nil {
			return fmt.Errorf("load script for %s: %w", configID, err)
		}
	}

	return nil
}

// Count returns the number of loaded scripts
func (l *Loader) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return len(l.scripts)
}
