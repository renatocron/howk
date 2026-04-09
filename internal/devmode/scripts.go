package devmode

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/script"
)

// LoadScriptsFromDir loads Lua scripts from a directory into the script loader.
//
// Supported formats:
//   - .json files: full script.Config JSON (config_id, lua_code, hash, version)
//   - .lua files: raw Lua code; filename (minus extension) becomes config_id,
//     hash is computed from content. If a companion .json with the same base name
//     exists (and was NOT already loaded as a full Config), it is parsed as
//     key-value config and stored in ScriptConfig (accessible as config.* in Lua).
func LoadScriptsFromDir(dir string, loader *script.Loader) (int, error) {
	count := 0

	// Track which .json files were consumed as full configs
	consumedJSON := make(map[string]bool)

	// Load .json script configs
	jsonFiles, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, path := range jsonFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			return count, fmt.Errorf("read %s: %w", path, err)
		}

		var cfg script.Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			return count, fmt.Errorf("parse %s: %w", path, err)
		}

		// Only treat as full config if it has lua_code (the required field)
		if cfg.LuaCode == "" {
			continue
		}

		loader.SetScript(&cfg)
		consumedJSON[filepath.Base(path)] = true
		count++
	}

	// Load .lua files directly (filename = config_id)
	luaFiles, _ := filepath.Glob(filepath.Join(dir, "*.lua"))
	for _, path := range luaFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			return count, fmt.Errorf("read %s: %w", path, err)
		}

		name := strings.TrimSuffix(filepath.Base(path), ".lua")
		code := string(data)
		hash := fmt.Sprintf("%x", sha256.Sum256(data))[:12]

		cfg := &script.Config{
			ConfigID: domain.ConfigID(name),
			LuaCode:  code,
			Hash:     hash,
			Version:  "dev",
		}

		// Load companion .json as ScriptConfig if it exists and wasn't a full config
		jsonFile := name + ".json"
		if !consumedJSON[jsonFile] {
			jsonPath := filepath.Join(dir, jsonFile)
			if jsonData, err := os.ReadFile(jsonPath); err == nil {
				var sc map[string]interface{}
				if err := json.Unmarshal(jsonData, &sc); err != nil {
					return count, fmt.Errorf("parse %s: %w", jsonPath, err)
				}
				cfg.ScriptConfig = sc
			}
		}

		loader.SetScript(cfg)
		count++
	}

	return count, nil
}
