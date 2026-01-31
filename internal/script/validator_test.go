package script

import (
	"testing"
)

func TestValidator_ValidateSyntax(t *testing.T) {
	validator := NewValidator()

	tests := []struct {
		name      string
		luaCode   string
		wantError bool
	}{
		{
			name:      "valid simple script",
			luaCode:   `headers["X-Custom"] = "value"`,
			wantError: false,
		},
		{
			name: "valid complex script",
			luaCode: `
				local json = require("json")
				local data = json.decode(payload)
				data.enriched = true
				request.body = json.encode(data)
			`,
			wantError: false,
		},
		{
			name: "valid script with functions",
			luaCode: `
				function transform(data)
					return data .. "_transformed"
				end
				local result = transform("test")
			`,
			wantError: false,
		},
		{
			name: "valid script with conditionals",
			luaCode: `
				if metadata.attempt > 1 then
					headers["X-Retry"] = "true"
				end
			`,
			wantError: false,
		},
		{
			name:      "syntax error - missing end",
			luaCode:   `if true then headers["X-Test"] = "value"`,
			wantError: true,
		},
		{
			name:      "syntax error - invalid lua",
			luaCode:   `this is not valid lua code @#$%`,
			wantError: true,
		},
		{
			name:      "syntax error - unclosed string",
			luaCode:   `headers["X-Test] = "value"`,
			wantError: true,
		},
		{
			name:      "syntax error - invalid assignment",
			luaCode:   `local = "value"`,
			wantError: true,
		},
		{
			name:      "empty script - valid",
			luaCode:   ``,
			wantError: false,
		},
		{
			name:      "comment only - valid",
			luaCode:   `-- This is a comment`,
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.ValidateSyntax(tt.luaCode)

			if tt.wantError && err == nil {
				t.Errorf("ValidateSyntax() expected error but got nil")
			}

			if !tt.wantError && err != nil {
				t.Errorf("ValidateSyntax() unexpected error: %v", err)
			}

			// If error expected, verify it's a ScriptError
			if tt.wantError && err != nil {
				if _, ok := err.(*ScriptError); !ok {
					t.Errorf("ValidateSyntax() error is not a ScriptError: %T", err)
				}
			}
		})
	}
}
