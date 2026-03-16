//go:build !integration

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/mocks"
	"github.com/howk/howk/internal/transformer"
)

// newIncomingTestServer creates a gin router wired with the transformer feature.
// luaCode is written to a temp dir and loaded via Registry.Load() so the test
// does not need Docker or external state.  If passwdContent is non-empty, a
// matching .passwd file is also written (enabling auth for that script).
func newIncomingTestServer(
	t *testing.T,
	scriptName string,
	luaCode string,
	passwdContent string,
) (*gin.Engine, *mocks.MockWebhookPublisher, *mocks.MockHotState) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()

	if err := os.WriteFile(dir+"/"+scriptName+".lua", []byte(luaCode), 0o644); err != nil {
		t.Fatalf("write lua file: %v", err)
	}

	if passwdContent != "" {
		if err := os.WriteFile(dir+"/"+scriptName+".passwd", []byte(passwdContent), 0o644); err != nil {
			t.Fatalf("write passwd file: %v", err)
		}
	}

	mockPub := new(mocks.MockWebhookPublisher)
	mockHS := new(mocks.MockHotState)
	mockValidator := new(mocks.MockValidator)

	apiCfg := config.APIConfig{
		Port:         8080,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	tCfg := config.TransformerConfig{
		ScriptDirs:    []string{dir},
		Timeout:       500 * time.Millisecond,
		MemoryLimitMB: 50,
	}
	luaCfg := config.LuaConfig{
		Enabled:      true,
		Timeout:      500 * time.Millisecond,
		AllowedHosts: []string{"*"},
	}

	reg := transformer.NewRegistry(tCfg)
	if err := reg.Load(); err != nil {
		t.Fatalf("load registry: %v", err)
	}
	eng := transformer.NewEngine(tCfg, luaCfg, mockPub, mockHS, nil, nil, nil)

	srv := NewServer(apiCfg, mockPub, mockHS, mockValidator, nil,
		WithTransformers(reg, eng))

	return srv.router, mockPub, mockHS
}

// newIncomingTestServerNoScript creates a server with no scripts loaded.
// Used for 404 (script-not-found) tests.
func newIncomingTestServerNoScript(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir() // empty; no .lua files

	mockPub := new(mocks.MockWebhookPublisher)
	mockHS := new(mocks.MockHotState)
	mockValidator := new(mocks.MockValidator)

	apiCfg := config.APIConfig{
		Port:         8080,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	tCfg := config.TransformerConfig{
		ScriptDirs:    []string{dir},
		Timeout:       500 * time.Millisecond,
		MemoryLimitMB: 50,
	}

	reg := transformer.NewRegistry(tCfg)
	_ = reg.Load() // loads nothing

	eng := transformer.NewEngine(tCfg, config.LuaConfig{}, mockPub, mockHS, nil, nil, nil)

	srv := NewServer(apiCfg, mockPub, mockHS, mockValidator, nil,
		WithTransformers(reg, eng))

	return srv.router
}

// TestHandleIncoming_FeatureDisabled verifies that when the server has no
// transformer configured, POST /incoming/:script_name returns 404.
func TestHandleIncoming_FeatureDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPub := new(mocks.MockWebhookPublisher)
	mockHS := new(mocks.MockHotState)
	mockValidator := new(mocks.MockValidator)

	cfg := config.APIConfig{
		Port:         8080,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	// No WithTransformers option: registry and engine stay nil.
	srv := NewServer(cfg, mockPub, mockHS, mockValidator, nil)

	// The route is not registered when transformerRegistry is nil, so the
	// router returns 404 by default.  We directly call the handler via a
	// synthetic context to verify the nil-check path.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/incoming/myscript", bytes.NewBufferString(`{}`))
	c.Params = gin.Params{{Key: "script_name", Value: "myscript"}}

	srv.handleIncoming(c)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Contains(t, resp["error"], "transformer feature not enabled")
}

// TestHandleIncoming_ScriptNotFound verifies that requesting a non-existent
// script name returns 404.
func TestHandleIncoming_ScriptNotFound(t *testing.T) {
	router := newIncomingTestServerNoScript(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/incoming/does-not-exist",
		bytes.NewBufferString(`{"hello":"world"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "script not found", resp["error"])
}

// TestHandleIncoming_InvalidAuth verifies that a script protected by a
// .passwd file rejects requests with wrong or missing credentials.
func TestHandleIncoming_InvalidAuth(t *testing.T) {
	// Plaintext password in passwd file (no bcrypt for test speed).
	router, _, _ := newIncomingTestServer(t,
		"mywebhook",
		`-- no-op script`,
		"alice:correct-horse-battery", // user:password plaintext
	)

	// Wrong password.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/incoming/mywebhook",
		bytes.NewBufferString(`body`))
	req.SetBasicAuth("alice", "wrong-password")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "invalid credentials", resp["error"])
}

// TestHandleIncoming_ScriptExecutionError verifies that a Lua runtime error
// returns HTTP 500.
func TestHandleIncoming_ScriptExecutionError(t *testing.T) {
	// Script that triggers a Lua runtime error: calling nil as a function.
	brokenLua := `local x = nil; x()`
	router, _, _ := newIncomingTestServer(t, "broken", brokenLua, "")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/incoming/broken",
		bytes.NewBufferString(`some-body`))
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Contains(t, resp["error"], "script execution failed")
}

// TestHandleIncoming_Success verifies that a valid request to a no-op script
// returns 200 with the webhook list.
func TestHandleIncoming_Success(t *testing.T) {
	// No-op script: does nothing; result will have zero webhooks.
	noopLua := `-- nothing`
	router, mockPub, mockHS := newIncomingTestServer(t, "noop", noopLua, "")

	_ = mockPub
	_ = mockHS

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/incoming/noop",
		bytes.NewBufferString(`{"event":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp IncomingResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, 0, resp.Count)
}

// TestHandleIncoming_ValidAuthPasses verifies that a correctly authenticated
// request to a protected script is allowed through.
func TestHandleIncoming_ValidAuthPasses(t *testing.T) {
	noopLua := `-- nothing`
	router, _, _ := newIncomingTestServer(t,
		"protected",
		noopLua,
		"alice:correct-horse-battery",
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/incoming/protected",
		bytes.NewBufferString(`body`))
	req.SetBasicAuth("alice", "correct-horse-battery")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
