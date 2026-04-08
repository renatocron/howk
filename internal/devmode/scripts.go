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
//     hash is computed from content
func LoadScriptsFromDir(dir string, loader *script.Loader) (int, error) {
	count := 0

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

		loader.SetScript(&cfg)
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

		loader.SetScript(&script.Config{
			ConfigID: domain.ConfigID(name),
			LuaCode:  code,
			Hash:     hash,
			Version:  "dev",
		})
		count++
	}

	return count, nil
}
