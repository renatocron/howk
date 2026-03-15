// Package luasandbox provides shared Lua sandbox initialization for script engines.
package luasandbox

import (
	lua "github.com/yuin/gopher-lua"
	luajson "layeh.com/gopher-json"

	"github.com/howk/howk/internal/script/modules"
)

// NewState creates a new sandboxed Lua state with resource limits.
// It opens only safe standard libraries and removes dangerous globals/modules.
func NewState(memoryLimitMB int) *lua.LState {
	opts := lua.Options{
		SkipOpenLibs: true,
	}

	if memoryLimitMB > 0 {
		maxEntries := memoryLimitMB * 131072
		opts.RegistrySize = maxEntries
		opts.RegistryMaxSize = maxEntries
	}

	L := lua.NewState(opts)

	// Open safe standard libraries
	for _, pair := range []struct {
		n string
		f lua.LGFunction
	}{
		{lua.LoadLibName, lua.OpenPackage},
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		if err := L.CallByParam(lua.P{
			Fn:      L.NewFunction(pair.f),
			NRet:    0,
			Protect: true,
		}, lua.LString(pair.n)); err != nil {
			panic(err)
		}
	}

	// Remove unsafe functions
	L.SetGlobal("dofile", lua.LNil)
	L.SetGlobal("loadfile", lua.LNil)
	L.SetGlobal("load", lua.LNil)

	// Disable unsafe modules
	if packageTable, ok := L.GetGlobal("package").(*lua.LTable); ok {
		if preloadTable, ok := packageTable.RawGetString("preload").(*lua.LTable); ok {
			preloadTable.RawSetString("io", lua.LNil)
			preloadTable.RawSetString("os", lua.LNil)
			preloadTable.RawSetString("debug", lua.LNil)
		}
	}

	// Load built-in modules
	luajson.Preload(L)
	modules.LoadBase64(L)

	return L
}
