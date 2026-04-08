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

	// Write a .json script config
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

	// JSON
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "a.json"),
		[]byte(`{"config_id":"a","lua_code":"-- a","hash":"h1","version":"1"}`),
		0644,
	))

	// Lua
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "b.lua"),
		[]byte("-- b"),
		0644,
	))

	// Non-matching file (should be ignored)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "readme.md"),
		[]byte("# ignore me"),
		0644,
	))

	loader := script.NewLoader()
	n, err := LoadScriptsFromDir(dir, loader)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
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

func TestLoadScriptsFromDir_NonexistentDir(t *testing.T) {
	loader := script.NewLoader()
	n, err := LoadScriptsFromDir("/nonexistent/path", loader)
	require.NoError(t, err) // filepath.Glob returns nil for no matches, not an error
	assert.Equal(t, 0, n)
}
