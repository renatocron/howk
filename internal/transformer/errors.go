package transformer

import "fmt"

// ErrorKind classifies transformer execution errors so callers can distinguish
// user-visible script faults from internal infrastructure failures.
type ErrorKind string

const (
	// ErrorKindSyntax indicates a Lua syntax error in the script source.
	// This is a user/operator error — safe to surface as 422.
	ErrorKindSyntax ErrorKind = "syntax_error"

	// ErrorKindRuntime indicates a Lua runtime error during execution.
	// This is a user/operator error — safe to surface as 422.
	ErrorKindRuntime ErrorKind = "runtime_error"

	// ErrorKindTimeout indicates the script exceeded its CPU budget.
	// Operator concern — surface as 504 or 422.
	ErrorKindTimeout ErrorKind = "timeout"

	// ErrorKindInternal indicates an infrastructure failure (broker, Redis, etc.)
	// unrelated to the script itself. Do not surface details to the API caller.
	ErrorKindInternal ErrorKind = "internal"
)

// TransformerError is a typed error returned by Engine.Execute.
// API handlers use Kind to select the appropriate HTTP status code without
// leaking internal error messages to callers.
type TransformerError struct {
	Kind    ErrorKind
	Message string
	Cause   error
}

func (e *TransformerError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e *TransformerError) Unwrap() error {
	return e.Cause
}
