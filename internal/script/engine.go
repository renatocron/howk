package script

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
	luajson "layeh.com/gopher-json"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/script/modules"
)

// Engine executes Lua scripts in a sandboxed environment with pooling
type Engine struct {
	config  config.LuaConfig
	pool    *sync.Pool
	loader  *Loader
	crypto  *modules.CryptoModule
	http    *modules.HTTPModule
	log     *modules.LogModule
	rdb     *redis.Client
	logger  zerolog.Logger
}

// NewEngine creates a new Lua script engine with optional crypto, http, and redis modules
func NewEngine(cfg config.LuaConfig, loader *Loader, crypto *modules.CryptoModule, http *modules.HTTPModule, rdb *redis.Client, logger zerolog.Logger) *Engine {
	// Create default logger if not provided
	if logger.GetLevel() == 0 {
		logger = zerolog.New(nil).Level(zerolog.Disabled)
	}
	
	// Create log module
	logModule := modules.NewLogModule(logger)
	
	engine := &Engine{
		config: cfg,
		loader: loader,
		crypto: crypto,
		http:   http,
		log:    logModule,
		rdb:    rdb,
		logger: logger,
	}

	// Initialize state pool
	engine.pool = &sync.Pool{
		New: func() interface{} {
			return engine.newLuaState()
		},
	}

	return engine
}

// newLuaState creates a new sandboxed Lua state with resource limits
func (e *Engine) newLuaState() *lua.LState {
	// Configure options with resource limits if configured
	opts := lua.Options{
		SkipOpenLibs: true, // We'll open only safe libraries
	}
	
	// Set registry limits based on memory limit (approximate)
	// Each registry entry is roughly a pointer size, so we estimate
	if e.config.MemoryLimitMB > 0 {
		// Approximate: 1MB ~ 131072 entries (assuming 8 bytes per entry)
		// This is a rough approximation - actual memory usage depends on data stored
		maxEntries := e.config.MemoryLimitMB * 131072
		opts.RegistrySize = maxEntries
		opts.RegistryMaxSize = maxEntries
	}

	L := lua.NewState(opts)

	// Open safe standard libraries
	for _, pair := range []struct {
		n string
		f lua.LGFunction
	}{
		{lua.LoadLibName, lua.OpenPackage},  // Needed for require()
		{lua.BaseLibName, lua.OpenBase},     // Basic functions (print, type, etc.)
		{lua.TabLibName, lua.OpenTable},     // Table manipulation
		{lua.StringLibName, lua.OpenString}, // String manipulation
		{lua.MathLibName, lua.OpenMath},     // Math functions
	} {
		if err := L.CallByParam(lua.P{
			Fn:      L.NewFunction(pair.f),
			NRet:    0,
			Protect: true,
		}, lua.LString(pair.n)); err != nil {
			panic(err)
		}
	}

	// Remove unsafe functions from base library
	L.SetGlobal("dofile", lua.LNil)
	L.SetGlobal("loadfile", lua.LNil)
	L.SetGlobal("load", lua.LNil)

	// Disable unsafe modules by removing them from package.preload
	packageTable := L.GetGlobal("package").(*lua.LTable)
	preloadTable := packageTable.RawGetString("preload").(*lua.LTable)

	// Remove dangerous modules
	preloadTable.RawSetString("io", lua.LNil)
	preloadTable.RawSetString("os", lua.LNil)
	preloadTable.RawSetString("debug", lua.LNil)

	// Load built-in modules
	luajson.Preload(L)     // JSON encode/decode
	modules.LoadBase64(L)  // Base64 encode/decode

	// Load crypto module if available
	if e.crypto != nil {
		e.crypto.LoadCrypto(L)
	}

	return L
}

// loadKVModule loads the KV module for a specific webhook (needs config_id)
func (e *Engine) loadKVModule(L *lua.LState, configID string) {
	if e.rdb != nil {
		modules.LoadKV(L, e.rdb, configID)
	}
}

// loadHTTPModule loads the HTTP module for a specific namespace
func (e *Engine) loadHTTPModule(L *lua.LState, namespace string) {
	if e.http != nil {
		e.http.LoadHTTP(L, namespace)
	}
}

// loadLogModule loads the log module for a specific webhook
func (e *Engine) loadLogModule(L *lua.LState, webhookID string) {
	if e.log != nil {
		e.log.LoadLog(L, webhookID)
	}
}

// Execute runs a Lua script to transform a webhook
func (e *Engine) Execute(ctx context.Context, webhook *domain.Webhook) (*domain.Webhook, error) {
	// Check if scripts are enabled
	if !e.config.Enabled {
		return nil, &ScriptError{
			Type:    ScriptErrorDisabled,
			Message: "Script execution is disabled",
		}
	}

	// Get script from loader
	scriptConfig, err := e.loader.GetScript(webhook.ConfigID)
	if err != nil {
		return nil, &ScriptError{
			Type:    ScriptErrorNotFound,
			Message: fmt.Sprintf("Script not found for config_id: %s", webhook.ConfigID),
			Cause:   err,
		}
	}

	// Extract namespace from config_id
	namespace := extractNamespace(string(webhook.ConfigID))

	// Set up timeout that covers the entire execution
	timeoutCtx, cancel := context.WithTimeout(ctx, e.config.Timeout)
	defer cancel()

	// Channels to receive result from goroutine
	resultCh := make(chan *TransformResult, 1)
	errCh := make(chan error, 1)

	go func() {
		// The goroutine now manages the lifecycle of the Lua state
		L := e.pool.Get().(*lua.LState)
		defer func() {
			L.SetTop(0)
			e.pool.Put(L)
		}()

		// Load modules for this specific webhook
		e.loadKVModule(L, string(webhook.ConfigID))
		e.loadHTTPModule(L, namespace)
		e.loadLogModule(L, string(webhook.ID))

		// Pass the timeout context to the script executor
		result, err := e.executeScript(timeoutCtx, L, scriptConfig.LuaCode, webhook)
		if err != nil {
			errCh <- err
		} else {
			resultCh <- result
		}
	}()

	// Wait for result, or timeout
	select {
	case <-timeoutCtx.Done():
		// The context cancellation will cause the PCall in the goroutine to fail.
		// The goroutine will clean itself up. We just return the timeout error.
		return nil, &ScriptError{
			Type:    ScriptErrorTimeout,
			Message: fmt.Sprintf("Script execution exceeded timeout of %v", e.config.Timeout),
			Cause:   ctx.Err(),
		}
	case err := <-errCh:
		// If the error is due to context cancellation, classify it as a timeout.
		if err == context.Canceled || err == context.DeadlineExceeded {
			return nil, &ScriptError{
				Type:    ScriptErrorTimeout,
				Message: "Script execution cancelled",
				Cause:   err,
			}
		}
		return nil, err
	case result := <-resultCh:
		// Apply transformation to webhook
		return e.applyTransformation(webhook, result), nil
	}
}

// executeScript runs the Lua code and extracts the transformation
func (e *Engine) executeScript(ctx context.Context, L *lua.LState, luaCode string, webhook *domain.Webhook) (*TransformResult, error) {
	// Set context for cancellation
	L.SetContext(ctx)

	// Inject input globals
	if err := e.injectInputGlobals(L, webhook); err != nil {
		return nil, &ScriptError{
			Type:    ScriptErrorRuntime,
			Message: "Failed to inject input globals",
			Cause:   err,
		}
	}

	// Initialize output tables
	requestTable := L.NewTable()
	L.SetGlobal("request", requestTable)

	configTable := L.NewTable()
	L.SetGlobal("config", configTable)

	// Compile and execute script
	fn, err := L.LoadString(luaCode)
	if err != nil {
		return nil, &ScriptError{
			Type:    ScriptErrorSyntax,
			Message: "Lua syntax error",
			Cause:   err,
		}
	}

	L.Push(fn)
	if err := L.PCall(0, 0, nil); err != nil {
		// If PCall fails, check if it was due to the context being cancelled.
		if ctx.Err() != nil {
			return nil, ctx.Err() // Return context error directly
		}
		
		// Check for memory limit errors
		// Only check when memory limit is actually configured
		errStr := err.Error()
		if e.config.MemoryLimitMB > 0 && containsSubstring(errStr, "registry overflow") {
			return nil, &ScriptError{
				Type:    ScriptErrorMemoryLimit,
				Message: fmt.Sprintf("Script exceeded memory limit of %d MB", e.config.MemoryLimitMB),
				Cause:   err,
			}
		}
		
		return nil, &ScriptError{
			Type:    ScriptErrorRuntime,
			Message: "Lua runtime error",
			Cause:   err,
		}
	}

	// Extract transformation result
	return e.extractTransformation(L), nil
}

// containsSubstring checks if a string contains a substring
func containsSubstring(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// injectInputGlobals sets up the input globals for the script
func (e *Engine) injectInputGlobals(L *lua.LState, webhook *domain.Webhook) error {
	// payload - raw JSON payload as string
	// Use lua.LString which can hold arbitrary binary data
	// Go strings can contain any byte sequence, and lua.LString preserves this
	L.SetGlobal("payload", lua.LString(string(webhook.Payload)))

	// headers - table of HTTP headers
	headersTable := L.NewTable()
	for k, v := range webhook.Headers {
		headersTable.RawSetString(k, lua.LString(v))
	}
	L.SetGlobal("headers", headersTable)

	// metadata - webhook metadata
	metadataTable := L.NewTable()
	metadataTable.RawSetString("webhook_id", lua.LString(webhook.ID))
	metadataTable.RawSetString("config_id", lua.LString(webhook.ConfigID))
	metadataTable.RawSetString("attempt", lua.LNumber(webhook.Attempt))
	metadataTable.RawSetString("max_attempts", lua.LNumber(webhook.MaxAttempts))
	metadataTable.RawSetString("created_at", lua.LString(webhook.CreatedAt.Format(time.RFC3339)))
	L.SetGlobal("metadata", metadataTable)

	// previous_error - nil for now (will be populated on retries in future)
	L.SetGlobal("previous_error", lua.LNil)

	return nil
}

// extractTransformation extracts the transformation result from Lua globals
func (e *Engine) extractTransformation(L *lua.LState) *TransformResult {
	result := &TransformResult{
		Headers: make(map[string]string),
	}

	// Extract request.body
	requestTable := L.GetGlobal("request")
	if requestTable != lua.LNil {
		if tbl, ok := requestTable.(*lua.LTable); ok {
			if body := tbl.RawGetString("body"); body != lua.LNil {
				result.Body = body.String()
			}

			// Extract request.headers (additional/override headers)
			if headers := tbl.RawGetString("headers"); headers != lua.LNil {
				if headersTbl, ok := headers.(*lua.LTable); ok {
					headersTbl.ForEach(func(k, v lua.LValue) {
						result.Headers[k.String()] = v.String()
					})
				}
			}
		}
	}

	// Extract config.opt_out_default_headers
	configTable := L.GetGlobal("config")
	if configTable != lua.LNil {
		if tbl, ok := configTable.(*lua.LTable); ok {
			if optOut := tbl.RawGetString("opt_out_default_headers"); optOut != lua.LNil {
				result.OptOutDefaultHeaders = lua.LVAsBool(optOut)
			}
		}
	}

	// Extract modified headers from global headers table
	headersTable := L.GetGlobal("headers")
	if headersTable != lua.LNil {
		if tbl, ok := headersTable.(*lua.LTable); ok {
			tbl.ForEach(func(k, v lua.LValue) {
				result.Headers[k.String()] = v.String()
			})
		}
	}

	return result
}

// applyTransformation applies the script transformation to the webhook
func (e *Engine) applyTransformation(webhook *domain.Webhook, result *TransformResult) *domain.Webhook {
	// Create a copy to avoid modifying the original
	transformed := *webhook

	// Apply body transformation if provided
	if result.Body != "" {
		// Handle binary data correctly by converting string to RawMessage
		// This preserves binary content without JSON re-encoding
		// Go strings can hold arbitrary bytes, so this conversion is safe
		transformed.Payload = json.RawMessage(result.Body)
	}

	// Apply header transformations
	if len(result.Headers) > 0 {
		if transformed.Headers == nil {
			transformed.Headers = make(map[string]string)
		}
		for k, v := range result.Headers {
			transformed.Headers[k] = v
		}
	}

	// Note: OptOutDefaultHeaders will be handled by the delivery client

	return &transformed
}

// Close shuts down the engine and cleans up resources
func (e *Engine) Close() error {
	// Note: sync.Pool doesn't need explicit cleanup
	// Lua states will be garbage collected
	return nil
}

// GetLoader returns the script loader (for loading scripts from Redis)
func (e *Engine) GetLoader() *Loader {
	return e.loader
}

// GetCrypto returns the crypto module (for testing)
func (e *Engine) GetCrypto() *modules.CryptoModule {
	return e.crypto
}

// GetHTTP returns the HTTP module (for testing)
func (e *Engine) GetHTTP() *modules.HTTPModule {
	return e.http
}

// extractNamespace extracts the namespace from config_id
// If config_id contains ":", takes the part before the first ":"
// Otherwise, uses the entire config_id
func extractNamespace(configID string) string {
	if idx := findFirstColon(configID); idx != -1 {
		return configID[:idx]
	}
	return configID
}

// findFirstColon finds the index of the first colon in a string
func findFirstColon(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}
