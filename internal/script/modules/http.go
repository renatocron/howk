package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
	"golang.org/x/sync/singleflight"
)

// HTTPResponse represents the result of an HTTP request
type HTTPResponse struct {
	StatusCode int
	Body       string
	Headers    map[string]string
}

// HTTPModule provides HTTP client functionality for Lua scripts
type HTTPModule struct {
	client       *http.Client
	singleflight singleflight.Group
	
	// Global allowlist (from config.AllowedHosts)
	globalAllowlist []string
	
	// Namespace-specific allowlists (from config.AllowHostsByNamespace)
	namespaceAllowlists map[string][]string
	
	// Cache
	cacheEnabled bool
	cacheTTL     time.Duration
	cache        map[string]*cacheEntry
	cacheMu      sync.RWMutex
}

// cacheEntry stores a cached response
type cacheEntry struct {
	response  *HTTPResponse
	cachedAt  time.Time
}

// HTTPConfig holds configuration for the HTTP module
type HTTPConfig struct {
	Timeout             time.Duration
	GlobalAllowlist     []string
	NamespaceAllowlists map[string]string
	CacheEnabled        bool
	CacheTTL            time.Duration
}

// NewHTTPModule creates a new HTTP module with the given configuration
func NewHTTPModule(cfg HTTPConfig) *HTTPModule {
	// Parse namespace allowlists from map[string]string to map[string][]string
	namespaceLists := make(map[string][]string)
	for ns, hosts := range cfg.NamespaceAllowlists {
		namespaceLists[ns] = parseHostList(hosts)
	}
	
	return &HTTPModule{
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
		globalAllowlist:     cfg.GlobalAllowlist,
		namespaceAllowlists: namespaceLists,
		cacheEnabled:        cfg.CacheEnabled,
		cacheTTL:            cfg.CacheTTL,
		cache:               make(map[string]*cacheEntry),
	}
}

// parseHostList parses a comma-separated list of hosts
func parseHostList(hosts string) []string {
	if hosts == "" {
		return nil
	}
	
	parts := strings.Split(hosts, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		host := strings.TrimSpace(p)
		if host != "" {
			result = append(result, host)
		}
	}
	return result
}

// LoadHTTP loads the http module into the Lua state
func (h *HTTPModule) LoadHTTP(L *lua.LState, namespace string) {
	httpMod := L.SetFuncs(L.NewTable(), map[string]lua.LGFunction{
		"get":    h.makeHTTPFunc("GET", namespace, false),
		"post":   h.makeHTTPFunc("POST", namespace, true),
		"put":    h.makeHTTPFunc("PUT", namespace, true),
		"delete": h.makeHTTPFunc("DELETE", namespace, true),
	})

	L.PreloadModule("http", func(L *lua.LState) int {
		L.Push(httpMod)
		return 1
	})
}

// makeHTTPFunc creates an HTTP function for the given method, namespace, and
// whether the method accepts a request body.
//
// Lua signatures:
//
//	http.get(url [, headers [, options]])
//	http.post(url, body [, headers [, options]])
//	http.put(url, body [, headers [, options]])
//	http.delete(url [, body [, headers [, options]]])
func (h *HTTPModule) makeHTTPFunc(method, namespace string, hasBody bool) lua.LGFunction {
	return func(L *lua.LState) int {
		urlStr := L.CheckString(1)

		// Parse positional args depending on whether method carries a body.
		var body string
		var headers map[string]string
		var cacheTTL time.Duration

		argIdx := 2
		if hasBody {
			// Body is the second argument (string or nil).
			if L.GetTop() >= argIdx {
				if lv := L.Get(argIdx); lv != lua.LNil {
					body = lv.String()
				}
			}
			argIdx++
		}

		// Optional headers table
		if L.GetTop() >= argIdx {
			if tbl := L.OptTable(argIdx, nil); tbl != nil {
				headers = tableToStringMap(tbl)
			}
		}
		argIdx++

		// Optional options table (for cache_ttl, etc.)
		if L.GetTop() >= argIdx {
			if tbl := L.OptTable(argIdx, nil); tbl != nil {
				if ttl := tbl.RawGetString("cache_ttl"); ttl != lua.LNil {
					cacheTTL = time.Duration(lua.LVAsNumber(ttl)) * time.Second
				}
			}
		}

		// Validate URL and check allowlist
		if err := h.validateHost(urlStr, namespace); err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LString(fmt.Sprintf("host validation failed: %v", err)))
			return 2
		}

		// Cache/singleflight only for GET — mutating methods always execute.
		if method == "GET" {
			cacheKey := h.buildCacheKey(method, namespace, urlStr, headers)

			if h.cacheEnabled && cacheTTL > 0 {
				if cached := h.getCached(cacheKey); cached != nil {
					return h.pushResponse(L, cached)
				}
			}

			result, err, _ := h.singleflight.Do(cacheKey, func() (interface{}, error) {
				ctx, cancel := context.WithTimeout(context.Background(), h.client.Timeout)
				defer cancel()
				return h.doRequest(ctx, method, urlStr, "", headers)
			})
			if err != nil {
				L.Push(lua.LNil)
				L.Push(lua.LString(fmt.Sprintf("http request failed: %v", err)))
				return 2
			}

			resp := result.(*HTTPResponse)
			if h.cacheEnabled && cacheTTL > 0 {
				h.setCached(cacheKey, resp, cacheTTL)
			}
			return h.pushResponse(L, resp)
		}

		// Non-GET: no cache, no singleflight.
		ctx, cancel := context.WithTimeout(context.Background(), h.client.Timeout)
		defer cancel()

		resp, err := h.doRequest(ctx, method, urlStr, body, headers)
		if err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LString(fmt.Sprintf("http request failed: %v", err)))
			return 2
		}

		return h.pushResponse(L, resp)
	}
}

// validateHost checks if the URL's hostname is in the allowlist
func (h *HTTPModule) validateHost(urlStr, namespace string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing hostname in URL")
	}
	
	// Check namespace-specific allowlist first
	if nsHosts, ok := h.namespaceAllowlists[namespace]; ok && len(nsHosts) > 0 {
		if isHostAllowed(host, nsHosts) {
			return nil
		}
		return fmt.Errorf("host %q not in namespace %q allowlist", host, namespace)
	}
	
	// Fall back to global allowlist
	if isHostAllowed(host, h.globalAllowlist) {
		return nil
	}
	
	return fmt.Errorf("host %q not in allowlist", host)
}

// isHostAllowed checks if a host matches any entry in the allowlist
func isHostAllowed(host string, allowlist []string) bool {
	for _, allowed := range allowlist {
		// Allow wildcard "*" for all hosts
		if allowed == "*" {
			return true
		}
		// Exact match
		if strings.EqualFold(host, allowed) {
			return true
		}
		// Subdomain match: *.example.com matches foo.example.com
		if strings.HasPrefix(allowed, "*.") {
			suffix := allowed[1:] // Remove the * but keep the dot
			if strings.HasSuffix(host, suffix) {
				return true
			}
		}
	}
	return false
}

// doRequest performs the actual HTTP request
func (h *HTTPModule) doRequest(ctx context.Context, method, urlStr, body string, headers map[string]string) (*HTTPResponse, error) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, bodyReader)
	if err != nil {
		return nil, err
	}
	
	// Set headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Extract response headers
	respHeaders := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			respHeaders[k] = v[0]
		}
	}

	return &HTTPResponse{
		StatusCode: resp.StatusCode,
		Body:       string(respBody),
		Headers:    respHeaders,
	}, nil
}

// buildCacheKey creates a unique key for caching
func (h *HTTPModule) buildCacheKey(method, namespace, urlStr string, headers map[string]string) string {
	// Include headers in cache key for accurate deduplication
	hash := sha256.New()
	hash.Write([]byte(method))
	hash.Write([]byte(namespace))
	hash.Write([]byte(urlStr))
	for k, v := range headers {
		hash.Write([]byte(k))
		hash.Write([]byte(v))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// getCached retrieves a cached response
func (h *HTTPModule) getCached(key string) *HTTPResponse {
	h.cacheMu.RLock()
	defer h.cacheMu.RUnlock()
	
	entry, ok := h.cache[key]
	if !ok {
		return nil
	}
	
	// Check if expired
	if time.Since(entry.cachedAt) > h.cacheTTL {
		return nil
	}
	
	return entry.response
}

// setCached stores a response in the cache
func (h *HTTPModule) setCached(key string, resp *HTTPResponse, ttl time.Duration) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	
	h.cache[key] = &cacheEntry{
		response:  resp,
		cachedAt:  time.Now(),
	}
	
	// Simple cleanup: remove expired entries if cache is too large
	if len(h.cache) > 1000 {
		h.cleanupExpired()
	}
}

// cleanupExpired removes expired entries from the cache
func (h *HTTPModule) cleanupExpired() {
	now := time.Now()
	for key, entry := range h.cache {
		if now.Sub(entry.cachedAt) > h.cacheTTL {
			delete(h.cache, key)
		}
	}
}

// pushResponse pushes an HTTP response onto the Lua stack
func (h *HTTPModule) pushResponse(L *lua.LState, resp *HTTPResponse) int {
	// Create response table
	tbl := L.NewTable()
	
	// Set status_code
	L.SetField(tbl, "status_code", lua.LNumber(resp.StatusCode))
	
	// Set body
	L.SetField(tbl, "body", lua.LString(resp.Body))
	
	// Set headers table
	headersTbl := L.NewTable()
	for k, v := range resp.Headers {
		L.SetField(headersTbl, k, lua.LString(v))
	}
	L.SetField(tbl, "headers", headersTbl)
	
	L.Push(tbl)
	return 1
}

// tableToStringMap converts a Lua table to a Go map[string]string
func tableToStringMap(tbl *lua.LTable) map[string]string {
	result := make(map[string]string)
	if tbl == nil {
		return result
	}
	
	tbl.ForEach(func(k, v lua.LValue) {
		result[k.String()] = v.String()
	})
	
	return result
}
