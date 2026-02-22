package modules

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lua "github.com/yuin/gopher-lua"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHostList(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"foo.dns,bar.dns", []string{"foo.dns", "bar.dns"}},
		{"single.dns", []string{"single.dns"}},
		{"  spaced  ,  hosts  ", []string{"spaced", "hosts"}},
		{"", nil},
		{"a,b,c,d", []string{"a", "b", "c", "d"}},
	}

	for _, tt := range tests {
		result := parseHostList(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("parseHostList(%q) = %v, want %v", tt.input, result, tt.expected)
			continue
		}
		for i := range tt.expected {
			if result[i] != tt.expected[i] {
				t.Errorf("parseHostList(%q)[%d] = %q, want %q", tt.input, i, result[i], tt.expected[i])
			}
		}
	}
}

func TestIsHostAllowed(t *testing.T) {
	tests := []struct {
		host      string
		allowlist []string
		expected  bool
	}{
		// Wildcard
		{"any.host.com", []string{"*"}, true},

		// Exact match
		{"api.example.com", []string{"api.example.com"}, true},
		{"api.example.com", []string{"other.example.com"}, false},

		// Subdomain wildcard
		{"foo.example.com", []string{"*.example.com"}, true},
		{"bar.example.com", []string{"*.example.com"}, true},
		{"deep.sub.example.com", []string{"*.example.com"}, true},
		{"other.com", []string{"*.example.com"}, false},

		// Case insensitive
		{"API.EXAMPLE.COM", []string{"api.example.com"}, true},

		// Multiple patterns
		{"a.com", []string{"*.b.com", "a.com"}, true},
		{"c.b.com", []string{"*.b.com", "a.com"}, true},
	}

	for _, tt := range tests {
		result := isHostAllowed(tt.host, tt.allowlist)
		if result != tt.expected {
			t.Errorf("isHostAllowed(%q, %v) = %v, want %v", tt.host, tt.allowlist, result, tt.expected)
		}
	}
}

func TestHTTPModule_ValidateHost(t *testing.T) {
	cfg := HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"*.example.com", "api.other.com"},
		NamespaceAllowlists: map[string]string{
			"music": "api.music.com,auth.music.com",
		},
	}

	hm := NewHTTPModule(cfg)

	tests := []struct {
		urlStr    string
		namespace string
		wantErr   bool
	}{
		// Global allowlist
		{"https://api.example.com/path", "default", false},
		{"https://sub.example.com/path", "default", false},
		{"https://api.other.com/path", "default", false},
		{"https://evil.com/path", "default", true},

		// Namespace-specific allowlist
		{"https://api.music.com/path", "music", false},
		{"https://auth.music.com/path", "music", false},
		{"https://api.example.com/path", "music", true}, // music namespace doesn't allow example.com

		// Invalid URLs
		{"not-a-url", "default", true},
		{"", "default", true},
	}

	for _, tt := range tests {
		err := hm.validateHost(tt.urlStr, tt.namespace)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateHost(%q, %q) error = %v, wantErr %v", tt.urlStr, tt.namespace, err, tt.wantErr)
		}
	}
}

func TestHTTPModule_BasicRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message": "hello"}`))
	}))
	defer server.Close()

	cfg := HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"*"},
		CacheEnabled:    false,
	}

	hm := NewHTTPModule(cfg)

	L := lua.NewState()
	defer L.Close()

	hm.LoadHTTP(L, "test")

	// Execute Lua code that makes HTTP request
	code := `
local http = require("http")
local resp = http.get("` + server.URL + `/test")
return resp.status_code, resp.body
`
	err := L.DoString(code)
	require.NoError(t, err)

	// Get results from stack
	body := L.Get(-1).String()
	statusCode := L.Get(-2).String()
	L.Pop(2)

	assert.Equal(t, "200", statusCode)
	assert.Equal(t, `{"message": "hello"}`, body)
}

func TestHTTPModule_WithHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo back the Authorization header
		auth := r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"auth": "` + auth + `"}`))
	}))
	defer server.Close()

	cfg := HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"*"},
		CacheEnabled:    false,
	}

	hm := NewHTTPModule(cfg)

	L := lua.NewState()
	defer L.Close()

	hm.LoadHTTP(L, "test")

	// Execute Lua code with headers
	code := `
local http = require("http")
local resp = http.get("` + server.URL + `/test", {Authorization = "Bearer token123"})
return resp.body
`
	err := L.DoString(code)
	require.NoError(t, err)

	body := L.Get(-1).String()
	L.Pop(1)

	assert.Contains(t, body, "Bearer token123")
}

func TestHTTPModule_HostValidationError(t *testing.T) {
	cfg := HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"api.example.com"},
		CacheEnabled:    false,
	}

	hm := NewHTTPModule(cfg)

	L := lua.NewState()
	defer L.Close()

	hm.LoadHTTP(L, "test")

	// Try to access disallowed host
	code := `
local http = require("http")
local resp, err = http.get("https://evil.com/api")
return resp, err
`
	err := L.DoString(code)
	require.NoError(t, err)

	// Get results
	errMsg := L.Get(-1)
	result := L.Get(-2)
	L.Pop(2)

	assert.Equal(t, lua.LNil, result)
	assert.NotEqual(t, lua.LNil, errMsg)
	assert.Contains(t, errMsg.String(), "host validation failed")
}

func TestHTTPModule_Cache(t *testing.T) {
	// Create a test server that counts requests
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"count": ` + string(rune('0'+count)) + `}`))
	}))
	defer server.Close()

	cfg := HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"*"},
		CacheEnabled:    true,
		CacheTTL:        1 * time.Minute,
	}

	hm := NewHTTPModule(cfg)

	L := lua.NewState()
	defer L.Close()

	hm.LoadHTTP(L, "test")

	url := strings.ReplaceAll(server.URL, `"`, `\"`)

	// First request - should hit the server
	code1 := `
local http = require("http")
local resp = http.get("` + url + `/cached", nil, {cache_ttl = 60})
return resp.body
`
	err := L.DoString(code1)
	require.NoError(t, err)
	body1 := L.Get(-1).String()
	L.Pop(1)

	// Second request - should be cached
	code2 := `
local http = require("http")
local resp = http.get("` + url + `/cached", nil, {cache_ttl = 60})
return resp.body
`
	err = L.DoString(code2)
	require.NoError(t, err)
	body2 := L.Get(-1).String()
	L.Pop(1)

	// Both should have the same body (first response cached)
	assert.Equal(t, body1, body2)

	// Should have only made 1 HTTP request
	count := atomic.LoadInt32(&requestCount)
	assert.Equal(t, int32(1), count, "Expected 1 request due to cache, got %d", count)
}

func TestHTTPModule_Singleflight(t *testing.T) {
	// Create a test server that counts requests
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		// Add small delay to ensure concurrent requests hit singleflight
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data": "test"}`))
	}))
	defer server.Close()

	cfg := HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"*"},
		CacheEnabled:    false, // Disable cache to test singleflight specifically
	}

	hm := NewHTTPModule(cfg)

	url := strings.ReplaceAll(server.URL, `"`, `\"`)

	// Make multiple concurrent requests
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			L := lua.NewState()
			defer L.Close()

			hm.LoadHTTP(L, "test")

			code := `
local http = require("http")
local resp = http.get("` + url + `/test")
return resp.status_code
`
			err := L.DoString(code)
			require.NoError(t, err)

			statusCode := L.Get(-1).String()
			L.Pop(1)

			assert.Equal(t, "200", statusCode)
		}()
	}

	wg.Wait()

	// Should have only made 1 actual HTTP request due to singleflight
	count := atomic.LoadInt32(&requestCount)
	if count != 1 {
		t.Errorf("Expected 1 request due to singleflight, got %d", count)
	}
}

func TestHTTPModule_ResponseStructure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Header", "custom-value")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"message": "created"}`))
	}))
	defer server.Close()

	cfg := HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"*"},
		CacheEnabled:    false,
	}

	hm := NewHTTPModule(cfg)

	L := lua.NewState()
	defer L.Close()

	hm.LoadHTTP(L, "test")

	code := `
local http = require("http")
local resp = http.get("` + server.URL + `/test")
return resp.status_code, resp.body, resp.headers["Content-Type"], resp.headers["X-Custom-Header"]
`
	err := L.DoString(code)
	require.NoError(t, err)

	// Get results from stack (in reverse order)
	customHeader := L.Get(-1).String()
	contentType := L.Get(-2).String()
	body := L.Get(-3).String()
	statusCode := L.Get(-4).String()
	L.Pop(4)

	assert.Equal(t, "201", statusCode)
	assert.Equal(t, `{"message": "created"}`, body)
	assert.Equal(t, "application/json", contentType)
	assert.Equal(t, "custom-value", customHeader)
}

func TestHTTPModule_Post(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		body, _ := io.ReadAll(r.Body)
		ct := r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"method":"POST","body":"` + string(body) + `","ct":"` + ct + `"}`))
	}))
	defer server.Close()

	hm := NewHTTPModule(HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"*"},
	})

	L := lua.NewState()
	defer L.Close()
	hm.LoadHTTP(L, "test")

	code := `
local http = require("http")
local resp = http.post("` + server.URL + `/api", '{"key":"val"}', {["Content-Type"] = "application/json"})
return resp.status_code, resp.body
`
	err := L.DoString(code)
	require.NoError(t, err)

	body := L.Get(-1).String()
	statusCode := L.Get(-2).String()
	L.Pop(2)

	assert.Equal(t, "201", statusCode)
	assert.Contains(t, body, `"method":"POST"`)
	assert.Contains(t, body, `"body":"{"key":"val"}"`)
	assert.Contains(t, body, `"ct":"application/json"`)
}

func TestHTTPModule_Put(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer server.Close()

	hm := NewHTTPModule(HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"*"},
	})

	L := lua.NewState()
	defer L.Close()
	hm.LoadHTTP(L, "test")

	code := `
local http = require("http")
local resp = http.put("` + server.URL + `", "updated")
return resp.status_code, resp.body
`
	err := L.DoString(code)
	require.NoError(t, err)

	body := L.Get(-1).String()
	statusCode := L.Get(-2).String()
	L.Pop(2)

	assert.Equal(t, "200", statusCode)
	assert.Equal(t, "updated", body)
}

func TestHTTPModule_Delete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	hm := NewHTTPModule(HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"*"},
	})

	L := lua.NewState()
	defer L.Close()
	hm.LoadHTTP(L, "test")

	// DELETE with no body
	code := `
local http = require("http")
local resp = http.delete("` + server.URL + `/resource/1")
return resp.status_code
`
	err := L.DoString(code)
	require.NoError(t, err)

	statusCode := L.Get(-1).String()
	L.Pop(1)

	assert.Equal(t, "204", statusCode)
}

func TestHTTPModule_DeleteWithBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"deleted":"` + string(body) + `"}`))
	}))
	defer server.Close()

	hm := NewHTTPModule(HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"*"},
	})

	L := lua.NewState()
	defer L.Close()
	hm.LoadHTTP(L, "test")

	code := `
local http = require("http")
local resp = http.delete("` + server.URL + `/resource", '{"id":42}', {["Content-Type"] = "application/json"})
return resp.status_code, resp.body
`
	err := L.DoString(code)
	require.NoError(t, err)

	body := L.Get(-1).String()
	statusCode := L.Get(-2).String()
	L.Pop(2)

	assert.Equal(t, "200", statusCode)
	assert.Contains(t, body, `{"id":42}`)
}

func TestHTTPModule_PostHostValidation(t *testing.T) {
	hm := NewHTTPModule(HTTPConfig{
		Timeout:         5 * time.Second,
		GlobalAllowlist: []string{"api.example.com"},
	})

	L := lua.NewState()
	defer L.Close()
	hm.LoadHTTP(L, "test")

	code := `
local http = require("http")
local resp, err = http.post("https://evil.com/api", "data")
return resp, err
`
	err := L.DoString(code)
	require.NoError(t, err)

	errMsg := L.Get(-1)
	result := L.Get(-2)
	L.Pop(2)

	assert.Equal(t, lua.LNil, result)
	assert.Contains(t, errMsg.String(), "host validation failed")
}
