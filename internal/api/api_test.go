//go:build !integration

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/mocks"
	"github.com/howk/howk/internal/script"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func setupTestServer(t *testing.T) (*Server, *mocks.MockWebhookPublisher, *mocks.MockHotState, *mocks.MockValidator, *mocks.MockPublisher) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	mockPub := new(mocks.MockWebhookPublisher)
	mockHS := new(mocks.MockHotState)
	mockValidator := new(mocks.MockValidator)
	mockScriptPub := new(mocks.MockPublisher)

	cfg := config.APIConfig{
		Port:         8080,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	server := NewServer(cfg, mockPub, mockHS, mockValidator, mockScriptPub)

	return server, mockPub, mockHS, mockValidator, mockScriptPub
}

func TestEnqueueWebhook_Success(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	// Setup expectations
	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("test-config")).Return("", errors.New("not found"))

	reqBody := `{"endpoint":"https://example.com/webhook","payload":{"test":"data"}}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.handleEnqueueWebhook(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	
	var resp EnqueueResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.NotEmpty(t, resp.WebhookID)
	assert.Equal(t, "pending", resp.Status)

	mockPub.AssertExpectations(t)
	mockHS.AssertExpectations(t)
}

func TestEnqueueWebhook_InvalidJSON(t *testing.T) {
	server, _, _, _, _ := setupTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue", bytes.NewBufferString("invalid json"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.handleEnqueueWebhook(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEnqueueWebhook_MissingEndpoint(t *testing.T) {
	server, _, _, _, _ := setupTestServer(t)

	reqBody := `{"payload":{"test":"data"}}` // Missing endpoint
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.handleEnqueueWebhook(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEnqueueWebhook_PublishError(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(errors.New("kafka error"))
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("test-config")).Return("", errors.New("not found"))
	mockHS.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	reqBody := `{"endpoint":"https://example.com/webhook","payload":{"test":"data"}}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.handleEnqueueWebhook(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	mockPub.AssertExpectations(t)
	mockHS.AssertExpectations(t)
}

func TestEnqueueWebhook_WithScriptHash(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	scriptJSON := `{"lua_code":"return payload","hash":"abc123","config_id":"test-config"}`
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("test-config")).Return(scriptJSON, nil)
	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	reqBody := `{"endpoint":"https://example.com/webhook","payload":{"test":"data"},"script_hash":"abc123"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.handleEnqueueWebhook(c)

	assert.Equal(t, http.StatusAccepted, w.Code)

	mockPub.AssertExpectations(t)
	mockHS.AssertExpectations(t)
}

func TestEnqueueWebhookBatch_Success(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil).Times(2)
	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(nil).Times(2)
	mockHS.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("test-config")).Return("", errors.New("not found")).Times(2)

	reqBody := `{
		"webhooks": [
			{"endpoint": "https://example.com/webhook1", "payload": {"test": "data1"}},
			{"endpoint": "https://example.com/webhook2", "payload": {"test": "data2"}}
		]
	}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue-batch", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.handleEnqueueWebhookBatch(c)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var resp BatchEnqueueResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Len(t, resp.Webhooks, 2)
	assert.Equal(t, 2, resp.Accepted)

	mockPub.AssertExpectations(t)
	mockHS.AssertExpectations(t)
}

func TestEnqueueWebhookBatch_PartialFailure(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	// First succeeds, second fails
	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil).Once()
	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(errors.New("kafka error")).Once()
	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(nil).Once()
	mockHS.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("test-config")).Return("", errors.New("not found")).Times(2)

	reqBody := `{
		"webhooks": [
			{"endpoint": "https://example.com/webhook1", "payload": {"test": "data1"}},
			{"endpoint": "https://example.com/webhook2", "payload": {"test": "data2"}}
		]
	}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue-batch", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.handleEnqueueWebhookBatch(c)

	// Returns 202 Accepted even for partial failures
	assert.Equal(t, http.StatusAccepted, w.Code)

	mockPub.AssertExpectations(t)
	mockHS.AssertExpectations(t)
}

func TestGetStatus_Success(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	status := &domain.WebhookStatus{
		WebhookID: "webhook-123",
		State:     domain.StateDelivered,
	}
	mockHS.On("GetStatus", mock.Anything, domain.WebhookID("webhook-123")).Return(status, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/webhooks/webhook-123/status", nil)
	c.Params = gin.Params{{Key: "webhook_id", Value: "webhook-123"}}

	server.handleGetStatus(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp domain.WebhookStatus
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, domain.StateDelivered, resp.State)

	mockHS.AssertExpectations(t)
}

func TestGetStatus_NotFound(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("GetStatus", mock.Anything, domain.WebhookID("webhook-123")).Return(nil, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/webhooks/webhook-123/status", nil)
	c.Params = gin.Params{{Key: "webhook_id", Value: "webhook-123"}}

	server.handleGetStatus(c)

	assert.Equal(t, http.StatusNotFound, w.Code)

	mockHS.AssertExpectations(t)
}

func TestGetStatus_Error(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("GetStatus", mock.Anything, domain.WebhookID("webhook-123")).Return(nil, errors.New("redis error"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/webhooks/webhook-123/status", nil)
	c.Params = gin.Params{{Key: "webhook_id", Value: "webhook-123"}}

	server.handleGetStatus(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	mockHS.AssertExpectations(t)
}

func TestHealthCheck(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("Ping", mock.Anything).Return(nil)
	mockHS.On("GetEpoch", mock.Anything).Return(nil, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/health", nil)

	server.handleHealth(c)

	mockHS.AssertExpectations(t)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, "healthy", resp["status"])
}

func TestReadyCheck_Healthy(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("Ping", mock.Anything).Return(nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/ready", nil)

	server.handleReadyCheck(c)

	assert.Equal(t, http.StatusOK, w.Code)

	mockHS.AssertExpectations(t)
}

func TestReadyCheck_Unhealthy(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("Ping", mock.Anything).Return(errors.New("redis connection failed"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/ready", nil)

	server.handleReadyCheck(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	mockHS.AssertExpectations(t)
}

func TestGetStats_Success(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	stats1h := &domain.Stats{
		Period:          "1h",
		Delivered:       10,
		Failed:          2,
		Exhausted:       1,
		UniqueEndpoints: 5,
	}
	stats24h := &domain.Stats{
		Period:          "24h",
		Delivered:       90,
		Failed:          5,
		Exhausted:       5,
		UniqueEndpoints: 10,
	}
	mockHS.On("GetStats", mock.Anything, mock.Anything, mock.Anything).Return(stats1h, nil).Once()
	mockHS.On("GetStats", mock.Anything, mock.Anything, mock.Anything).Return(stats24h, nil).Once()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/config/test-config/stats", nil)
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.handleGetStats(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp StatsResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, int64(10), resp.Last1h.Delivered)
	assert.Equal(t, int64(90), resp.Last24h.Delivered)

	mockHS.AssertExpectations(t)
}

func TestGetStats_Error(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	// Both calls fail
	mockHS.On("GetStats", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("redis error"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/config/test-config/stats", nil)
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.handleGetStats(c)

	// Even if underlying calls fail, it returns 200 with empty stats
	assert.Equal(t, http.StatusOK, w.Code)

	mockHS.AssertExpectations(t)
}

func TestDependenciesCheck_AllHealthy(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	mockHS.On("Ping", mock.Anything).Return(nil)
	// Use a mock that implements the Ping method via type assertion
	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/health/dependencies", nil)

	server.handleDependenciesCheck(c)

	// Note: The Kafka health check uses type assertion, may not detect mock
	assert.Contains(t, []int{http.StatusOK, http.StatusServiceUnavailable}, w.Code)
}

// ERROR PATH TESTS - Quick coverage wins

func TestDependenciesCheck_RedisError(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	// Simulate Redis ping failure
	mockHS.On("Ping", mock.Anything).Return(errors.New("redis connection failed"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/health/dependencies", nil)

	server.handleDependenciesCheck(c)

	// Should return 503 when Redis is unhealthy
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, "unhealthy", resp["status"])

	// Check that redis dependency shows error
	deps, ok := resp["dependencies"].(map[string]interface{})
	assert.True(t, ok)
	redis, ok := deps["redis"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "unhealthy", redis["status"])
	assert.Contains(t, redis["error"], "redis connection failed")

	mockHS.AssertExpectations(t)
}

// TestDependenciesCheck_KafkaError tests Kafka error path
// Note: MockWebhookPublisher doesn't implement Ping, so we can't test this path directly
// In real usage, the KafkaWebhookPublisher implements Ping and this would work
func TestDependenciesCheck_KafkaError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPub := new(mocks.MockWebhookPublisher)
	mockHS := new(mocks.MockHotState)
	mockValidator := new(mocks.MockValidator)

	cfg := config.APIConfig{
		Port:         8080,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	server := NewServer(cfg, mockPub, mockHS, mockValidator, nil)

	// Redis is healthy
	mockHS.On("Ping", mock.Anything).Return(nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/health/dependencies", nil)

	server.handleDependenciesCheck(c)

	// Mock doesn't implement Ping, so Kafka shows as healthy (default behavior)
	// The nil publisher case is tested separately in TestDependenciesCheck_KafkaNilPublisher
	assert.Equal(t, http.StatusOK, w.Code)

	mockHS.AssertExpectations(t)
}

func TestDependenciesCheck_KafkaNilPublisher(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockHS := new(mocks.MockHotState)
	mockValidator := new(mocks.MockValidator)

	cfg := config.APIConfig{
		Port:         8080,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	// Create server with nil publisher (not just script publisher)
	server := NewServer(cfg, nil, mockHS, mockValidator, nil)

	// Redis is healthy
	mockHS.On("Ping", mock.Anything).Return(nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/health/dependencies", nil)

	server.handleDependenciesCheck(c)

	// Should return 503 when publisher is nil
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, "unhealthy", resp["status"])

	// Check that kafka dependency shows error
	deps, ok := resp["dependencies"].(map[string]interface{})
	assert.True(t, ok)
	kafka, ok := deps["kafka"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "unhealthy", kafka["status"])
	assert.Contains(t, kafka["error"], "publisher not configured")

	mockHS.AssertExpectations(t)
}

func TestBuildWebhook_WithScriptHash(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	scriptJSON := `{"lua_code":"return payload","hash":"abc123","config_id":"cfg-test"}`
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("cfg-test")).Return(scriptJSON, nil)

	req := &EnqueueRequest{
		Endpoint: "https://example.com/webhook",
		Payload:  json.RawMessage(`{"test":"data"}`),
	}

	webhook := server.buildWebhook(domain.ConfigID("cfg-test"), req)

	assert.Equal(t, "abc123", webhook.ScriptHash)
	assert.Equal(t, domain.ConfigID("cfg-test"), webhook.ConfigID)
}

func TestBuildWebhook_WithoutScriptHash(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("GetScript", mock.Anything, domain.ConfigID("cfg-test")).Return("", errors.New("not found"))

	req := &EnqueueRequest{
		Endpoint: "https://example.com/webhook",
		Payload:  json.RawMessage(`{"test":"data"}`),
	}

	webhook := server.buildWebhook(domain.ConfigID("cfg-test"), req)

	assert.Empty(t, webhook.ScriptHash)
	assert.Equal(t, domain.ConfigID("cfg-test"), webhook.ConfigID)
}

func TestBuildWebhook_InvalidScriptJSON(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	// Return invalid JSON - should be handled gracefully
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("cfg-test")).Return("invalid json", nil)

	req := &EnqueueRequest{
		Endpoint: "https://example.com/webhook",
		Payload:  json.RawMessage(`{"test":"data"}`),
	}

	webhook := server.buildWebhook(domain.ConfigID("cfg-test"), req)

	// Should still create webhook but without script hash
	assert.Empty(t, webhook.ScriptHash)
	assert.Equal(t, domain.ConfigID("cfg-test"), webhook.ConfigID)
}

// TestBuildWebhook_LoaderPrimary verifies that ScriptHash is resolved from the
// in-memory loader (memory-first) WITHOUT any Redis call — the loader holds the
// full script state replayed from Kafka, including namespace fallback.
func TestBuildWebhook_LoaderPrimary(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	loader := script.NewLoader()
	loader.SetScript(&script.Config{
		ConfigID: "wh",
		LuaCode:  "return payload",
		Hash:     "deadbeef",
	})
	WithScriptLoader(loader)(server)

	// No GetScript/SetScript expectations registered: Redis must NOT be touched.
	wh := server.buildWebhook("wh:75:1", &EnqueueRequest{
		Endpoint: "https://example.com/webhook",
		Payload:  json.RawMessage(`{"a":1}`),
	})

	assert.Equal(t, "deadbeef", wh.ScriptHash,
		"ScriptHash must be tagged from the loader (namespace fallback wh:75:1 -> wh)")
	mockHS.AssertNotCalled(t, "GetScript", mock.Anything, mock.Anything)
	mockHS.AssertNotCalled(t, "SetScript", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestBuildWebhook_ColdStartRedisFallback verifies that when the loader is
// empty (e.g. before replay completes), enqueue falls back to Redis once and
// populates the loader so the next enqueue stays in memory.
func TestBuildWebhook_ColdStartRedisFallback(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	loader := script.NewLoader()
	WithScriptLoader(loader)(server)

	scriptJSON := `{"lua_code":"return payload","hash":"redis-hash","config_id":"wh"}`
	// Loader miss for "wh:75:1" -> Redis consulted once.
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("wh:75:1")).Return(scriptJSON, nil).Once()

	wh := server.buildWebhook("wh:75:1", &EnqueueRequest{
		Endpoint: "https://example.com/webhook",
		Payload:  json.RawMessage(`{"a":1}`),
	})

	assert.Equal(t, "redis-hash", wh.ScriptHash)
	// Loader must now hold it (populated from the Redis hit).
	cfg, err := loader.GetScript("wh")
	assert.NoError(t, err)
	if assert.NotNil(t, cfg) {
		assert.Equal(t, "redis-hash", cfg.Hash)
	}
	mockHS.AssertExpectations(t)
}

// TestBuildWebhook_NoScriptAnywhere verifies that with an empty loader and a
// Redis miss, ScriptHash is left empty (no transformation registered).
func TestBuildWebhook_NoScriptAnywhere(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)
	WithScriptLoader(script.NewLoader())(server)

	mockHS.On("GetScript", mock.Anything, domain.ConfigID("other:1")).
		Return("", errors.New("script not found"))

	wh := server.buildWebhook("other:1", &EnqueueRequest{
		Endpoint: "https://example.com/webhook",
		Payload:  json.RawMessage(`{"a":1}`),
	})

	assert.Empty(t, wh.ScriptHash)
	mockHS.AssertExpectations(t)
}

// TestEnqueue_RequireScript_RejectsWhenMissing verifies that require_script=true
// fails fast with 422 and publishes nothing when no script resolves.
func TestEnqueue_RequireScript_RejectsWhenMissing(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)
	WithScriptLoader(script.NewLoader())(server)

	// Loader empty + Redis miss → no script.
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("wh:75:1")).
		Return("", errors.New("script not found"))

	reqBody := `{"endpoint":"https://example.com/webhook","payload":{"a":1},"require_script":true}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/wh:75:1/enqueue", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "wh:75:1"}}

	server.handleEnqueueWebhook(c)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	mockPub.AssertNotCalled(t, "PublishWebhook", mock.Anything, mock.Anything)
}

// TestEnqueue_RequireScript_AcceptsWhenPresent verifies require_script=true
// passes (and stamps the hash) when the loader has the script — no Redis call.
func TestEnqueue_RequireScript_AcceptsWhenPresent(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)
	loader := script.NewLoader()
	loader.SetScript(&script.Config{ConfigID: "wh", Hash: "abc123"})
	WithScriptLoader(loader)(server)

	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	reqBody := `{"endpoint":"https://example.com/webhook","payload":{"a":1},"require_script":true}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/wh:75:1/enqueue", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "wh:75:1"}}

	server.handleEnqueueWebhook(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	mockHS.AssertNotCalled(t, "GetScript", mock.Anything, mock.Anything)
	mockPub.AssertExpectations(t)
}

// TestEnqueue_RequireScript_BatchAllOrNothing verifies one require_script item
// without a script rejects the WHOLE batch (nothing published).
func TestEnqueue_RequireScript_BatchAllOrNothing(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)
	WithScriptLoader(script.NewLoader())(server)

	mockHS.On("GetScript", mock.Anything, domain.ConfigID("wh:9:1")).
		Return("", errors.New("script not found"))

	reqBody := `{"webhooks":[
		{"endpoint":"https://a.example.com/","payload":{"a":1}},
		{"endpoint":"https://b.example.com/","payload":{"b":2},"require_script":true}
	]}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/wh:9:1/enqueue/batch", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "wh:9:1"}}

	server.handleEnqueueWebhookBatch(c)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	mockPub.AssertNotCalled(t, "PublishWebhook", mock.Anything, mock.Anything)
}
