package modules

import (
	"encoding/base64"
	"testing"

	lua "github.com/yuin/gopher-lua"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadBase64(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	LoadBase64(L)

	// Verify module is loaded
	assert.NoError(t, L.DoString(`local base64 = require("base64"); return base64`))
}

func TestBase64Encode(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	LoadBase64(L)

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "simple string",
			input:    "hello",
			expected: base64.StdEncoding.EncodeToString([]byte("hello")),
		},
		{
			name:     "string with spaces",
			input:    "hello world",
			expected: base64.StdEncoding.EncodeToString([]byte("hello world")),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			code := `
local base64 = require("base64")
local result = base64.encode("` + escapeString(tc.input) + `")
return result
`
			err := L.DoString(code)
			require.NoError(t, err)

			result := L.Get(-1).String()
			assert.Equal(t, tc.expected, result)
			L.Pop(1)
		})
	}
}

func TestBase64Decode(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	LoadBase64(L)

	testCases := []struct {
		name      string
		input     string
		expected  string
		shouldErr bool
	}{
		{
			name:      "empty string",
			input:     "",
			expected:  "",
			shouldErr: false,
		},
		{
			name:      "valid base64",
			input:     base64.StdEncoding.EncodeToString([]byte("hello")),
			expected:  "hello",
			shouldErr: false,
		},
		{
			name:      "invalid base64",
			input:     "!!!",
			expected:  "",
			shouldErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			code := `
local base64 = require("base64")
local result, err = base64.decode("` + escapeString(tc.input) + `")
if err then
  return nil, err
end
return result
`
			err := L.DoString(code)
			require.NoError(t, err)

			if tc.shouldErr {
				// Check that we got an error
				errVal := L.Get(-1)
				assert.NotNil(t, errVal)
				assert.NotEqual(t, lua.LNil, errVal)
			} else {
				result := L.Get(-1).String()
				assert.Equal(t, tc.expected, result)
			}
			L.Pop(1)
		})
	}
}

func TestBase64RoundTrip(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	LoadBase64(L)

	testCases := []string{
		"hello",
		"hello world",
		"The quick brown fox jumps over the lazy dog",
		"Test with numbers: 123456789",
		"Special chars: !@#$%^&*()",
	}

	for _, original := range testCases {
		t.Run(original, func(t *testing.T) {
			code := `
local base64 = require("base64")
local encoded = base64.encode("` + escapeString(original) + `")
local decoded = base64.decode(encoded)
return decoded
`
			err := L.DoString(code)
			require.NoError(t, err)

			result := L.Get(-1).String()
			assert.Equal(t, original, result)
			L.Pop(1)
		})
	}
}

// Helper function to escape strings for Lua
func escapeString(s string) string {
	// Simple escape for testing - in production, you'd want more robust escaping
	result := ""
	for _, c := range s {
		if c == '"' {
			result += "\\\""
		} else if c == '\\' {
			result += "\\\\"
		} else if c == '\n' {
			result += "\\n"
		} else if c == '\r' {
			result += "\\r"
		} else if c == '\t' {
			result += "\\t"
		} else if c >= 32 && c < 127 {
			result += string(c)
		} else {
			// For control characters, use hex escape
			result += "\\x" + string("0123456789abcdef"[c>>4]) + string("0123456789abcdef"[c&0x0f])
		}
	}
	return result
}
