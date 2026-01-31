package modules

import (
	"encoding/base64"

	lua "github.com/yuin/gopher-lua"
)

// LoadBase64 loads the base64 module into the Lua state
func LoadBase64(L *lua.LState) {
	L.PreloadModule("base64", func(L *lua.LState) int {
		mod := L.SetFuncs(L.NewTable(), map[string]lua.LGFunction{
			"encode": base64Encode,
			"decode": base64Decode,
		})
		L.Push(mod)
		return 1
	})
}

// base64Encode encodes a string to Base64
func base64Encode(L *lua.LState) int {
	input := L.CheckString(1)
	encoded := base64.StdEncoding.EncodeToString([]byte(input))
	L.Push(lua.LString(encoded))
	return 1
}

// base64Decode decodes a Base64 string
func base64Decode(L *lua.LState) int {
	input := L.CheckString(1)
	decoded, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	L.Push(lua.LString(string(decoded)))
	return 1
}
