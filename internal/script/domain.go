package script

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/howk/howk/internal/domain"
)

// ScriptConfig represents a Lua script configuration for a config_id
type ScriptConfig struct {
	ConfigID  domain.ConfigID `json:"config_id"`
	LuaCode   string          `json:"lua_code"`
	Hash      string          `json:"hash"`     // SHA256 of lua_code
	Version   string          `json:"version"`  // User-provided version identifier
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// ScriptHash computes the SHA256 hash of Lua code
func ScriptHash(luaCode string) string {
	h := sha256.Sum256([]byte(luaCode))
	return hex.EncodeToString(h[:])
}

// ScriptError represents an error that occurred during script execution
type ScriptError struct {
	Type    ScriptErrorType
	Message string
	Cause   error
}

func (e *ScriptError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

func (e *ScriptError) Unwrap() error {
	return e.Cause
}

// ScriptErrorType categorizes script execution errors
type ScriptErrorType string

const (
	ScriptErrorNotFound       ScriptErrorType = "script_not_found"       // Script not in cache or Kafka
	ScriptErrorSyntax         ScriptErrorType = "script_syntax_error"    // Lua syntax error
	ScriptErrorRuntime        ScriptErrorType = "script_runtime_error"   // Lua runtime error/panic
	ScriptErrorTimeout        ScriptErrorType = "script_timeout"         // CPU timeout exceeded
	ScriptErrorMemoryLimit    ScriptErrorType = "script_memory_limit"    // Memory limit exceeded
	ScriptErrorModuleKV       ScriptErrorType = "script_module_kv"       // KV module error (Redis)
	ScriptErrorModuleHTTP     ScriptErrorType = "script_module_http"     // HTTP module error
	ScriptErrorModuleCrypto   ScriptErrorType = "script_module_crypto"   // Crypto module error
	ScriptErrorInvalidOutput  ScriptErrorType = "script_invalid_output"  // Script produced invalid output
	ScriptErrorDisabled       ScriptErrorType = "script_disabled"        // Script execution disabled
)

// IsRetryable returns whether a script error should trigger webhook retry
func (t ScriptErrorType) IsRetryable() bool {
	switch t {
	case ScriptErrorModuleKV, ScriptErrorModuleHTTP:
		// Transient infrastructure errors
		return true
	default:
		// Code errors, timeouts, disabled feature → DLQ
		return false
	}
}

// TransformResult represents the output of a script execution
type TransformResult struct {
	Body                   string            // Transformed payload (can be binary)
	Headers                map[string]string // Additional/override headers
	OptOutDefaultHeaders   bool              // Skip X-Webhook-* headers
}
