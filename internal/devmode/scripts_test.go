package devmode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/script"
)

func TestLoadScriptsFromDir_JSONFiles(t *testing.T) {
	dir := t.TempDir()

	// Write a .json script config (full Config with lua_code)
	jsonContent := `{
		"config_id": "wh",
		"lua_code": "request.body = payload",
		"hash": "abc123",
		"version": "1.0"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wh.json"), []byte(jsonContent), 0644))

	loader := script.NewLoader()
	n, err := LoadScriptsFromDir(dir, loader)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	cfg, err := loader.GetScript(domain.ConfigID("wh"))
	require.NoError(t, err)
	assert.Equal(t, "request.body = payload", cfg.LuaCode)
	assert.Equal(t, "abc123", cfg.Hash)
	assert.Equal(t, "1.0", cfg.Version)
}

func TestLoadScriptsFromDir_LuaWithCompanionJSON(t *testing.T) {
	dir := t.TempDir()

	// Write a .lua file with a companion .json config (no lua_code in JSON)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wh.lua"), []byte("request.body = payload"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wh.json"), []byte(`{"internal_api_url":"http://localhost:3000"}`), 0644))

	loader := script.NewLoader()
	n, err := LoadScriptsFromDir(dir, loader)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	cfg, err := loader.GetScript(domain.ConfigID("wh"))
	require.NoError(t, err)
	assert.Equal(t, "request.body = payload", cfg.LuaCode)
	assert.Equal(t, "dev", cfg.Version)
	assert.Equal(t, "http://localhost:3000", cfg.ScriptConfig["internal_api_url"])
}

func TestLoadScriptsFromDir_LuaFiles(t *testing.T) {
	dir := t.TempDir()

	luaCode := `-- test script
request.headers["X-Custom"] = "value"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "my-transform.lua"), []byte(luaCode), 0644))

	loader := script.NewLoader()
	n, err := LoadScriptsFromDir(dir, loader)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	cfg, err := loader.GetScript(domain.ConfigID("my-transform"))
	require.NoError(t, err)
	assert.Equal(t, luaCode, cfg.LuaCode)
	assert.Equal(t, "dev", cfg.Version)
	assert.Len(t, cfg.Hash, 12) // truncated sha256 hex
}

func TestLoadScriptsFromDir_MixedFiles(t *testing.T) {
	dir := t.TempDir()

	// Full config JSON
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "a.json"),
		[]byte(`{"config_id":"a","lua_code":"-- a","hash":"h1","version":"1"}`),
		0644,
	))

	// Lua with companion JSON (config values, no lua_code)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.lua"), []byte("-- b"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"), []byte(`{"key":"val"}`), 0644))

	// Lua without companion
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.lua"), []byte("-- c"), 0644))

	// Non-matching file (should be ignored)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# ignore me"), 0644))

	loader := script.NewLoader()
	n, err := LoadScriptsFromDir(dir, loader)
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	cfgB, err := loader.GetScript(domain.ConfigID("b"))
	require.NoError(t, err)
	assert.Equal(t, "val", cfgB.ScriptConfig["key"])

	cfgC, err := loader.GetScript(domain.ConfigID("c"))
	require.NoError(t, err)
	assert.Nil(t, cfgC.ScriptConfig)
}

func TestLoadScriptsFromDir_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	loader := script.NewLoader()
	n, err := LoadScriptsFromDir(dir, loader)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestLoadScriptsFromDir_InvalidJSON(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "bad.json"),
		[]byte(`{invalid json`),
		0644,
	))

	loader := script.NewLoader()
	_, err := LoadScriptsFromDir(dir, loader)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestLoadScriptsFromDir_InvalidCompanionJSON(t *testing.T) {
	dir := t.TempDir()

	// .lua file with invalid companion .json (no lua_code so not consumed as full config)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.lua"), []byte("-- code"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{invalid json`), 0644))

	loader := script.NewLoader()
	_, err := LoadScriptsFromDir(dir, loader)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestLoadScriptsFromDir_NonexistentDir(t *testing.T) {
	loader := script.NewLoader()
	n, err := LoadScriptsFromDir("/nonexistent/path", loader)
	require.NoError(t, err) // filepath.Glob returns nil for no matches, not an error
	assert.Equal(t, 0, n)
}
