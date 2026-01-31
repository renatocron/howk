package script

import (
	"fmt"

	lua "github.com/yuin/gopher-lua"
)

// Validator validates Lua script syntax
type Validator struct{}

// NewValidator creates a new script validator
func NewValidator() *Validator {
	return &Validator{}
}

// ValidateSyntax checks if the Lua code is syntactically valid
// Returns nil if valid, error if syntax error detected
func (v *Validator) ValidateSyntax(luaCode string) error {
	// Create a new Lua state for validation
	L := lua.NewState()
	defer L.Close()

	// Try to compile the code
	// This validates syntax without executing the script
	fn, err := L.LoadString(luaCode)
	if err != nil {
		return &ScriptError{
			Type:    ScriptErrorSyntax,
			Message: "Lua syntax error",
			Cause:   err,
		}
	}

	// Check if we got a function back
	if fn == nil {
		return &ScriptError{
			Type:    ScriptErrorSyntax,
			Message: "Failed to compile Lua code",
			Cause:   fmt.Errorf("compilation returned nil function"),
		}
	}

	return nil
}
