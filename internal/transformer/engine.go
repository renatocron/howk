package transformer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	lua "github.com/yuin/gopher-lua"
	luajson "layeh.com/gopher-json"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/script/modules"
)

// Engine executes transformer Lua scripts with howk.post() fan-out capability
type Engine struct {
	pool      *sync.Pool
	cfg       config.TransformerConfig
	luaCfg    config.LuaConfig
	publisher broker.WebhookPublisher
	hotstate  hotstate.HotState
	rdb       *redis.Client
	httpMod   *modules.HTTPModule
	cryptoMod *modules.CryptoModule
	logger    zerolog.Logger
}

// ExecutionResult contains the webhooks created during script execution
type ExecutionResult struct {
	Webhooks []WebhookRef
}

// WebhookRef represents a created webhook reference
type WebhookRef struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
}

// postOptions contains optional parameters for howk.post()
type postOptions struct {
	Headers       map[string]string
	SigningSecret string
	ConfigID      string
	MaxAttempts   int
}

// NewEngine creates a new transformer engine
func NewEngine(
	cfg config.TransformerConfig,
	luaCfg config.LuaConfig,
	publisher broker.WebhookPublisher,
	hs hotstate.HotState,
	rdb *redis.Client,
	httpMod *modules.HTTPModule,
	cryptoMod *modules.CryptoModule,
) *Engine {
	engine := &Engine{
		cfg:       cfg,
		luaCfg:    luaCfg,
		publisher: publisher,
		hotstate:  hs,
		rdb:       rdb,
		httpMod:   httpMod,
		cryptoMod: cryptoMod,
		logger:    log.With().Str("component", "transformer_engine").Logger(),
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
	opts := lua.Options{
		SkipOpenLibs: true, // Only open safe libraries
	}

	// Set registry limits based on memory limit
	if e.cfg.MemoryLimitMB > 0 {
		maxEntries := e.cfg.MemoryLimitMB * 131072 // Approximate
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
	packageTable := L.GetGlobal("package").(*lua.LTable)
	preloadTable := packageTable.RawGetString("preload").(*lua.LTable)
	preloadTable.RawSetString("io", lua.LNil)
	preloadTable.RawSetString("os", lua.LNil)
	preloadTable.RawSetString("debug", lua.LNil)

	// Load built-in modules
	luajson.Preload(L)
	modules.LoadBase64(L)

	return L
}

// Execute runs a transformer script with the given input
func (e *Engine) Execute(
	ctx context.Context,
	script *TransformerScript,
	body []byte,
	httpHeaders map[string]string,
) (*ExecutionResult, error) {
	// Set up timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()

	// Channels for result
	resultCh := make(chan *ExecutionResult, 1)
	errCh := make(chan error, 1)

	go func() {
		L := e.pool.Get().(*lua.LState)
		defer func() {
			L.SetTop(0)
			e.pool.Put(L)
		}()

		result, err := e.executeScript(timeoutCtx, L, script, body, httpHeaders)
		if err != nil {
			errCh <- err
		} else {
			resultCh <- result
		}
	}()

	// Wait for result or timeout
	select {
	case <-timeoutCtx.Done():
		return nil, fmt.Errorf("script execution exceeded timeout of %v", e.cfg.Timeout)
	case err := <-errCh:
		return nil, err
	case result := <-resultCh:
		return result, nil
	}
}

// executeScript runs the Lua script and collects created webhooks
func (e *Engine) executeScript(
	ctx context.Context,
	L *lua.LState,
	script *TransformerScript,
	body []byte,
	httpHeaders map[string]string,
) (*ExecutionResult, error) {
	L.SetContext(ctx)

	// Create result collector
	result := &ExecutionResult{
		Webhooks: make([]WebhookRef, 0),
	}

	// Load modules
	e.loadModules(L, script.Name)

	// Set globals
	L.SetGlobal("incoming", lua.LString(string(body)))

	// headers table
	headersTable := L.NewTable()
	for k, v := range httpHeaders {
		headersTable.RawSetString(k, lua.LString(v))
	}
	L.SetGlobal("headers", headersTable)

	// config table from script's JSON config
	configTable := L.NewTable()
	if script.Config != nil {
		for k, v := range script.Config {
			e.setLuaValue(L, configTable, k, v)
		}
	}
	L.SetGlobal("config", configTable)

	// Register howk module with post function
	howkTable := L.NewTable()
	L.SetField(howkTable, "post", L.NewFunction(e.makePostFn(script, result)))
	L.SetGlobal("howk", howkTable)

	// Execute script
	fn, err := L.LoadString(script.LuaCode)
	if err != nil {
		return nil, fmt.Errorf("lua syntax error: %w", err)
	}

	L.Push(fn)
	if err := L.PCall(0, 0, nil); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("lua runtime error: %w", err)
	}

	return result, nil
}

// loadModules loads optional modules (crypto, http, kv, log)
func (e *Engine) loadModules(L *lua.LState, scriptName string) {
	// Crypto module
	if e.cryptoMod != nil {
		e.cryptoMod.LoadCrypto(L)
	}

	// HTTP module - use script name as namespace
	if e.httpMod != nil {
		e.httpMod.LoadHTTP(L, scriptName)
	}

	// KV module - use script name as config_id
	if e.rdb != nil {
		modules.LoadKV(L, e.rdb, scriptName)
	}

	// Log module
	logMod := modules.NewLogModule(e.logger)
	logMod.LoadLog(L, "transformer:"+scriptName)
}

// makePostFn creates the howk.post() Lua function
func (e *Engine) makePostFn(script *TransformerScript, result *ExecutionResult) lua.LGFunction {
	return func(L *lua.LState) int {
		// Required arguments
		endpoint := L.CheckString(1)

		// Payload must be a table
		payloadTbl := L.CheckTable(2)
		payload, err := luajson.Encode(payloadTbl)
		if err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LString(fmt.Sprintf("failed to encode payload: %v", err)))
			return 2
		}

		// Optional options table
		opts := &postOptions{
			Headers:     make(map[string]string),
			MaxAttempts: 20, // Default
		}
		if L.GetTop() >= 3 {
			optsTbl := L.OptTable(3, nil)
			if optsTbl != nil {
				e.parsePostOptions(optsTbl, opts)
			}
		}

		// Validate endpoint domain against allowed_domains
		if err := e.validateDomain(endpoint, script.Config); err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LString(fmt.Sprintf("domain validation failed: %v", err)))
			return 2
		}

		// Create webhook
		webhookID := domain.WebhookID("wh_" + ulid.Make().String())
		configID := domain.ConfigID(opts.ConfigID)
		if configID == "" {
			configID = domain.ConfigID(script.Name) // Default to script name
		}

		webhook := &domain.Webhook{
			ID:             webhookID,
			ConfigID:       configID,
			Endpoint:       endpoint,
			EndpointHash:   domain.HashEndpoint(endpoint),
			Payload:        json.RawMessage(payload),
			Headers:        opts.Headers,
			SigningSecret:  opts.SigningSecret,
			Attempt:        0,
			MaxAttempts:    opts.MaxAttempts,
			CreatedAt:      time.Now(),
			ScheduledAt:    time.Now(),
		}

		// Publish to Kafka
		ctx := context.Background()
		if err := e.publisher.PublishWebhook(ctx, webhook); err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LString(fmt.Sprintf("failed to publish webhook: %v", err)))
			return 2
		}

		// Set initial status in hotstate
		status := &domain.WebhookStatus{
			WebhookID:   webhookID,
			State:       domain.StatePending,
			Attempts:    0,
			UpdatedAtNs: time.Now().UnixNano(),
		}
		if err := e.hotstate.SetStatus(ctx, status); err != nil {
			e.logger.Warn().
				Str("webhook_id", string(webhookID)).
				Err(err).
				Msg("Failed to set initial webhook status")
		}

		// Record in result
		result.Webhooks = append(result.Webhooks, WebhookRef{
			ID:       string(webhookID),
			Endpoint: endpoint,
		})

		// Return webhook ID
		L.Push(lua.LString(string(webhookID)))
		return 1
	}
}

// validateDomain checks if the endpoint's domain is in allowed_domains
func (e *Engine) validateDomain(endpoint string, scriptConfig map[string]any) error {
	// Parse endpoint URL
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing hostname in endpoint URL")
	}

	// If no allowed_domains specified, allow all
	if scriptConfig == nil {
		return nil
	}

	allowedDomainsRaw, ok := scriptConfig["allowed_domains"]
	if !ok {
		// No allowed_domains key = allow all
		return nil
	}

	// allowed_domains must be a slice
	allowedDomains, ok := allowedDomainsRaw.([]interface{})
	if !ok {
		return fmt.Errorf("allowed_domains must be an array")
	}

	// Check if host is in allowed list
	for _, domainRaw := range allowedDomains {
		domain, ok := domainRaw.(string)
		if !ok {
			continue
		}

		// Exact match
		if host == domain {
			return nil
		}

		// Subdomain match: *.example.com matches foo.example.com
		if len(domain) > 2 && domain[0] == '*' && domain[1] == '.' {
			suffix := domain[1:] // Keep the dot
			if len(host) > len(suffix) && host[len(host)-len(suffix):] == suffix {
				return nil
			}
		}
	}

	return fmt.Errorf("domain %q not in allowed_domains", host)
}

// parsePostOptions extracts options from the Lua options table
func (e *Engine) parsePostOptions(tbl *lua.LTable, opts *postOptions) {
	tbl.ForEach(func(k, v lua.LValue) {
		key := k.String()
		switch key {
		case "headers":
			if headersTbl, ok := v.(*lua.LTable); ok {
				headersTbl.ForEach(func(hk, hv lua.LValue) {
					opts.Headers[hk.String()] = hv.String()
				})
			}
		case "signing_secret":
			opts.SigningSecret = v.String()
		case "config_id":
			opts.ConfigID = v.String()
		case "max_attempts":
			if n, ok := v.(lua.LNumber); ok {
				opts.MaxAttempts = int(n)
			}
		}
	})
}

// setLuaValue sets a Go value in a Lua table
func (e *Engine) setLuaValue(L *lua.LState, tbl *lua.LTable, key string, value interface{}) {
	switch v := value.(type) {
	case string:
		tbl.RawSetString(key, lua.LString(v))
	case float64:
		tbl.RawSetString(key, lua.LNumber(v))
	case bool:
		tbl.RawSetString(key, lua.LBool(v))
	case map[string]interface{}:
		subTbl := L.NewTable()
		for k, val := range v {
			e.setLuaValue(L, subTbl, k, val)
		}
		tbl.RawSetString(key, subTbl)
	case []interface{}:
		arrTbl := L.NewTable()
		for i, val := range v {
			e.setLuaValue(L, arrTbl, fmt.Sprintf("%d", i+1), val)
		}
		tbl.RawSetString(key, arrTbl)
	case nil:
		tbl.RawSetString(key, lua.LNil)
	default:
		tbl.RawSetString(key, lua.LString(fmt.Sprintf("%v", v)))
	}
}

// Close shuts down the engine
func (e *Engine) Close() error {
	// sync.Pool doesn't need explicit cleanup
	return nil
}
