package transformer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/config"
)

// ScriptNamePattern validates transformer script names
// Must start with alphanumeric, can contain alphanumerics, underscores, and hyphens
var ScriptNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// TransformerScript represents a loaded transformer script with its config and auth
type TransformerScript struct {
	Name    string         // filename without .lua
	LuaCode string         // raw Lua source
	Config  map[string]any // from [name].json, nil if absent
	Auth    *AuthConfig    // from [name].passwd, nil if open
}

// Registry manages transformer scripts with hot-reload support
type Registry struct {
	mu      sync.RWMutex
	scripts map[string]*TransformerScript
	cfg     config.TransformerConfig
	logger  zerolog.Logger
}

// NewRegistry creates a new transformer registry
func NewRegistry(cfg config.TransformerConfig) *Registry {
	return &Registry{
		scripts: make(map[string]*TransformerScript),
		cfg:     cfg,
		logger:  log.With().Str("component", "transformer_registry").Logger(),
	}
}

// Load scans all ScriptDirs and loads transformer scripts
func (r *Registry) Load() error {
	newScripts := make(map[string]*TransformerScript)

	for _, dir := range r.cfg.ScriptDirs {
		if err := r.loadFromDir(dir, newScripts); err != nil {
			r.logger.Error().
				Str("dir", dir).
				Err(err).
				Msg("Failed to load scripts from directory")
			// Continue with other directories rather than failing completely
		}
	}

	// Atomically swap the scripts map
	r.mu.Lock()
	r.scripts = newScripts
	r.mu.Unlock()

	r.logger.Info().
		Int("count", len(newScripts)).
		Msg("Loaded transformer scripts")

	return nil
}

// Reload performs a hot reload of all scripts (call on SIGHUP)
func (r *Registry) Reload() error {
	r.logger.Info().Msg("Reloading transformer scripts")
	return r.Load()
}

// Get retrieves a script by name (thread-safe)
func (r *Registry) Get(name string) (*TransformerScript, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	script, ok := r.scripts[name]
	return script, ok
}

// List returns all available script names (thread-safe)
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.scripts))
	for name := range r.scripts {
		names = append(names, name)
	}
	return names
}

// loadFromDir loads all .lua files from a directory
func (r *Registry) loadFromDir(dir string, dest map[string]*TransformerScript) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Directory doesn't exist, skip silently
			return nil
		}
		return fmt.Errorf("failed to read directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".lua") {
			continue
		}

		// Extract script name (without .lua extension)
		scriptName := strings.TrimSuffix(name, ".lua")

		// Validate script name
		if !ScriptNamePattern.MatchString(scriptName) {
			r.logger.Warn().
				Str("file", name).
				Str("dir", dir).
				Msg("Skipping script with invalid name (must match ^[a-z0-9][a-z0-9_-]*$)")
			continue
		}

		// Load the script
		script, err := r.loadScript(dir, scriptName)
		if err != nil {
			r.logger.Error().
				Str("script", scriptName).
				Str("dir", dir).
				Err(err).
				Msg("Failed to load script")
			continue
		}

		// Check for duplicate names across directories (last one wins)
		if _, exists := dest[scriptName]; exists {
			r.logger.Warn().
				Str("script", scriptName).
				Msg("Duplicate script name found, overwriting with later version")
		}

		dest[scriptName] = script
		r.logger.Debug().
			Str("script", scriptName).
			Str("dir", dir).
			Msg("Loaded transformer script")
	}

	return nil
}

// loadScript loads a single script with its companion files
func (r *Registry) loadScript(dir, name string) (*TransformerScript, error) {
	// Load .lua file
	luaPath := filepath.Join(dir, name+".lua")
	luaCode, err := os.ReadFile(luaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read lua file: %w", err)
	}

	script := &TransformerScript{
		Name:    name,
		LuaCode: string(luaCode),
		Config:  nil,
		Auth:    nil,
	}

	// Load optional .json config file (from same directory)
	jsonPath := filepath.Join(dir, name+".json")
	if jsonData, err := os.ReadFile(jsonPath); err == nil {
		var config map[string]any
		if err := json.Unmarshal(jsonData, &config); err != nil {
			r.logger.Warn().
				Str("script", name).
				Str("file", jsonPath).
				Err(err).
				Msg("Failed to parse JSON config file")
		} else {
			script.Config = config
		}
	}

	// Load optional .passwd auth file (from PasswdDirs or ScriptDirs)
	auth := r.loadPasswd(name)
	if auth != nil {
		script.Auth = auth
	}

	return script, nil
}

// loadPasswd loads the passwd file for a script
// Searches in PasswdDirs first (if configured), then falls back to ScriptDirs
func (r *Registry) loadPasswd(name string) *AuthConfig {
	passwdDirs := r.cfg.PasswdDirs
	if len(passwdDirs) == 0 {
		// Default to script directories
		passwdDirs = r.cfg.ScriptDirs
	}

	for _, dir := range passwdDirs {
		passwdPath := filepath.Join(dir, name+".passwd")
		if data, err := os.ReadFile(passwdPath); err == nil {
			auth, err := ParsePasswdFile(data)
			if err != nil {
				r.logger.Warn().
					Str("script", name).
					Str("file", passwdPath).
					Err(err).
					Msg("Failed to parse passwd file")
				continue
			}
			return auth
		}
	}

	return nil
}
