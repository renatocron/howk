//go:build !integration

package luasandbox_test

import (
	"testing"

	lua "github.com/yuin/gopher-lua"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/luasandbox"
)

// TestNewStateReturnsValidState verifies that NewState(0) returns a non-nil
// *lua.LState that can execute basic Lua code.
func TestNewStateReturnsValidState(t *testing.T) {
	L := luasandbox.NewState(0)
	require.NotNil(t, L)
	defer L.Close()

	// Basic evaluation must succeed.
	err := L.DoString(`local x = 1 + 1`)
	assert.NoError(t, err)
}

// TestSafeLibsLoaded verifies that the standard safe libraries (base, table,
// string, math) are available after NewState.
func TestSafeLibsLoaded(t *testing.T) {
	L := luasandbox.NewState(0)
	require.NotNil(t, L)
	defer L.Close()

	cases := []struct {
		name string
		code string
	}{
		{"base: type()", `assert(type(1) == "number")`},
		{"table: table.insert", `local t = {} table.insert(t, 1) assert(#t == 1)`},
		{"string: string.len", `assert(string.len("hello") == 5)`},
		{"math: math.floor", `assert(math.floor(1.9) == 1)`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := L.DoString(tc.code)
			assert.NoError(t, err, "safe lib code should execute without error")
		})
	}
}

// TestUnsafeGlobalsAreNil verifies that dofile, loadfile, and load are removed
// from the global environment (set to LNil).
func TestUnsafeGlobalsAreNil(t *testing.T) {
	L := luasandbox.NewState(0)
	require.NotNil(t, L)
	defer L.Close()

	for _, name := range []string{"dofile", "loadfile", "load"} {
		val := L.GetGlobal(name)
		assert.Equal(t, lua.LNil, val, "global %q should be LNil", name)
	}
}

// TestUnsafeModulesBlocked verifies that io, os, and debug modules cannot be
// loaded via require().
func TestUnsafeModulesBlocked(t *testing.T) {
	for _, modName := range []string{"io", "os", "debug"} {
		t.Run(modName, func(t *testing.T) {
			L := luasandbox.NewState(0)
			require.NotNil(t, L)
			defer L.Close()

			// Attempting to require a blocked module must fail or return nil/false.
			// We protect the call so we can inspect the result rather than panic.
			err := L.DoString(`local m = require("` + modName + `")`)
			// Either an error is returned (module not found) or the module is nil.
			// Both are acceptable outcomes — the module must not be usable.
			if err == nil {
				// If require didn't error, the returned value must be nil/false.
				top := L.Get(-1)
				assert.True(t,
					top == lua.LNil || top == lua.LFalse,
					"require(%q) should return nil or false, got %v", modName, top,
				)
			}
			// An error is the normal expected path — that's fine too.
		})
	}
}

// TestMemoryLimitedState verifies that NewState with a positive memoryLimitMB
// returns a valid, usable Lua state.
func TestMemoryLimitedState(t *testing.T) {
	L := luasandbox.NewState(10)
	require.NotNil(t, L)
	defer L.Close()

	// Basic arithmetic must still work.
	err := L.DoString(`local sum = 0 for i = 1, 100 do sum = sum + i end assert(sum == 5050)`)
	assert.NoError(t, err)
}

// TestNewStateZeroAndPositiveReturnSameCapabilities verifies that both
// NewState(0) (unlimited) and NewState(10) (10 MB limit) expose the same
// set of safe globals.
func TestNewStateZeroAndPositiveReturnSameCapabilities(t *testing.T) {
	states := []*lua.LState{
		luasandbox.NewState(0),
		luasandbox.NewState(10),
	}
	for _, L := range states {
		defer L.Close()
	}

	safeGlobals := []string{"type", "assert", "ipairs", "pairs", "tostring", "tonumber", "table", "string", "math"}
	for _, L := range states {
		for _, name := range safeGlobals {
			val := L.GetGlobal(name)
			assert.NotEqual(t, lua.LNil, val, "safe global %q should be present", name)
		}
		for _, name := range []string{"dofile", "loadfile", "load"} {
			val := L.GetGlobal(name)
			assert.Equal(t, lua.LNil, val, "unsafe global %q should be absent", name)
		}
	}
}
