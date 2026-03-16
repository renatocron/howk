//go:build !integration

package transformer

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/howk/howk/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper: write a file inside a temp directory
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
	require.NoError(t, err)
}

// --------------------------------------------------------------------
// Registry.Load
// --------------------------------------------------------------------

func TestRegistry_Load_ValidLuaFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "myscript.lua", `-- hello`)

	reg := NewRegistry(config.TransformerConfig{ScriptDirs: []string{dir}})
	require.NoError(t, reg.Load())

	script, ok := reg.Get("myscript")
	require.True(t, ok, "expected myscript to be loaded")
	assert.Equal(t, "myscript", script.Name)
	assert.Equal(t, "-- hello", script.LuaCode)
	assert.Nil(t, script.Config)
	assert.Nil(t, script.Auth)
}

func TestRegistry_Load_LuaWithJSONConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "fanout.lua", `howk.post("http://a.example.com", {})`)
	writeFile(t, dir, "fanout.json", `{"key":"value","num":42}`)

	reg := NewRegistry(config.TransformerConfig{ScriptDirs: []string{dir}})
	require.NoError(t, reg.Load())

	script, ok := reg.Get("fanout")
	require.True(t, ok)
	require.NotNil(t, script.Config)
	assert.Equal(t, "value", script.Config["key"])
	assert.Equal(t, float64(42), script.Config["num"])
}

func TestRegistry_Load_LuaWithPasswdAuth(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "secured.lua", `-- secured script`)
	writeFile(t, dir, "secured.passwd", "alice:secret\n")

	reg := NewRegistry(config.TransformerConfig{ScriptDirs: []string{dir}})
	require.NoError(t, reg.Load())

	script, ok := reg.Get("secured")
	require.True(t, ok)
	require.NotNil(t, script.Auth)
	assert.True(t, script.Auth.HasUser("alice"))
}

func TestRegistry_Load_InvalidScriptNameSkipped(t *testing.T) {
	dir := t.TempDir()
	// Names starting with uppercase or special chars don't match ^[a-z0-9][a-z0-9_-]*$
	writeFile(t, dir, "My-Script.lua", `-- bad name`)
	writeFile(t, dir, "valid.lua", `-- good`)

	reg := NewRegistry(config.TransformerConfig{ScriptDirs: []string{dir}})
	require.NoError(t, reg.Load())

	_, ok := reg.Get("My-Script")
	assert.False(t, ok, "My-Script should have been skipped")

	_, ok = reg.Get("valid")
	assert.True(t, ok)
}

func TestRegistry_Load_MissingDirectorySkipped(t *testing.T) {
	// Directory that does not exist — Load should succeed silently.
	reg := NewRegistry(config.TransformerConfig{
		ScriptDirs: []string{"/nonexistent/path/xyz"},
	})
	err := reg.Load()
	// Missing directories are skipped, not an error.
	assert.NoError(t, err)
	assert.Empty(t, reg.List())
}

func TestRegistry_Load_DuplicateNameOverwrites(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	writeFile(t, dir1, "common.lua", `-- version 1`)
	writeFile(t, dir2, "common.lua", `-- version 2`)

	reg := NewRegistry(config.TransformerConfig{ScriptDirs: []string{dir1, dir2}})
	require.NoError(t, reg.Load())

	script, ok := reg.Get("common")
	require.True(t, ok)
	// Second directory wins (last one overwrites).
	assert.Equal(t, "-- version 2", script.LuaCode)
}

// --------------------------------------------------------------------
// Registry.Get and List — thread safety
// --------------------------------------------------------------------

func TestRegistry_GetList_ThreadSafety(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "alpha.lua", `-- a`)
	writeFile(t, dir, "beta.lua", `-- b`)

	reg := NewRegistry(config.TransformerConfig{ScriptDirs: []string{dir}})
	require.NoError(t, reg.Load())

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.Get("alpha")
			reg.List()
		}()
	}
	wg.Wait()
}

func TestRegistry_List_ReturnsAllNames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "alpha.lua", ``)
	writeFile(t, dir, "beta.lua", ``)
	writeFile(t, dir, "gamma.lua", ``)

	reg := NewRegistry(config.TransformerConfig{ScriptDirs: []string{dir}})
	require.NoError(t, reg.Load())

	names := reg.List()
	sort.Strings(names)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, names)
}

// --------------------------------------------------------------------
// Registry.Reload
// --------------------------------------------------------------------

func TestRegistry_Reload_ReplacesScriptsAtomically(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "script1.lua", `-- v1`)

	reg := NewRegistry(config.TransformerConfig{ScriptDirs: []string{dir}})
	require.NoError(t, reg.Load())

	_, ok := reg.Get("script1")
	require.True(t, ok)

	// Add a new script and remove old one (simulate file change)
	require.NoError(t, os.Remove(filepath.Join(dir, "script1.lua")))
	writeFile(t, dir, "script2.lua", `-- v2`)

	require.NoError(t, reg.Reload())

	_, ok = reg.Get("script1")
	assert.False(t, ok, "script1 should no longer exist after reload")

	script2, ok := reg.Get("script2")
	require.True(t, ok)
	assert.Equal(t, "-- v2", script2.LuaCode)
}
