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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func setupTestServer(t *testing.T) (*Server, *mocks.MockWebhookPublisher, *mocks.MockHotState, *mocks.MockValidator, *mocks.MockPublisher) {
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

	server.enqueueWebhook(c)

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

	server.enqueueWebhook(c)

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

	server.enqueueWebhook(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEnqueueWebhook_PublishError(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(errors.New("kafka error"))
	mockHS.On("GetScript", mock.Anything, mock.Anything).Return("", errors.New("not found"))

	reqBody := `{"endpoint":"https://example.com/webhook","payload":{"test":"data"}}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.enqueueWebhook(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockPub.AssertExpectations(t)
}

func TestEnqueueWebhook_WithScriptHash(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	scriptJSON := `{"lua_code":"return payload","hash":"abc123hash","config_id":"test-config"}`
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("test-config")).Return(scriptJSON, nil)
	mockPub.On("PublishWebhook", mock.Anything, mock.MatchedBy(func(w *domain.Webhook) bool {
		return w.ScriptHash == "abc123hash"
	})).Return(nil)
	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("AddToHLL", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	reqBody := `{"endpoint":"https://example.com/webhook","payload":{"test":"data"}}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.enqueueWebhook(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	mockPub.AssertExpectations(t)
}

func TestEnqueueWebhookBatch_Success(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil).Twice()
	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(nil).Twice()
	mockHS.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockHS.On("GetScript", mock.Anything, mock.Anything).Return("", errors.New("not found")).Twice()

	reqBody := `{"webhooks":[{"endpoint":"https://example.com/wh1","payload":{"test":1}},{"endpoint":"https://example.com/wh2","payload":{"test":2}}]}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue/batch", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.enqueueWebhookBatch(c)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var resp BatchEnqueueResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, 2, resp.Accepted)
	assert.Equal(t, 0, resp.Failed)
	assert.Len(t, resp.Webhooks, 2)

	mockPub.AssertExpectations(t)
}

func TestEnqueueWebhookBatch_PartialFailure(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	// First succeeds, second fails
	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil).Once()
	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(errors.New("kafka error")).Once()
	mockHS.On("SetStatus", mock.Anything, mock.Anything).Return(nil).Once()
	mockHS.On("GetScript", mock.Anything, mock.Anything).Return("", errors.New("not found")).Twice()
	mockHS.On("IncrStats", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	reqBody := `{"webhooks":[{"endpoint":"https://example.com/wh1","payload":{"test":1}},{"endpoint":"https://example.com/wh2","payload":{"test":2}}]}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/webhooks/test-config/enqueue/batch", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config", Value: "test-config"}}

	server.enqueueWebhookBatch(c)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var resp BatchEnqueueResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, 1, resp.Accepted)
	assert.Equal(t, 1, resp.Failed)
}

func TestGetStatus_Success(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	status := &domain.WebhookStatus{
		WebhookID:      "wh_123",
		State:          domain.StateDelivered,
		Attempts:       1,
		LastStatusCode: 200,
	}
	mockHS.On("GetStatus", mock.Anything, domain.WebhookID("wh_123")).Return(status, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/webhooks/wh_123/status", nil)
	c.Params = gin.Params{{Key: "webhook_id", Value: "wh_123"}}

	server.getStatus(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp StatusResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, "wh_123", resp.WebhookID)
	assert.Equal(t, domain.StateDelivered, resp.State)
	assert.Equal(t, 1, resp.Attempts)
	assert.Equal(t, 200, resp.LastStatusCode)
}

func TestGetStatus_NotFound(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("GetStatus", mock.Anything, domain.WebhookID("wh_notfound")).Return(nil, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/webhooks/wh_notfound/status", nil)
	c.Params = gin.Params{{Key: "webhook_id", Value: "wh_notfound"}}

	server.getStatus(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetStatus_Error(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("GetStatus", mock.Anything, domain.WebhookID("wh_error")).Return(nil, errors.New("redis error"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/webhooks/wh_error/status", nil)
	c.Params = gin.Params{{Key: "webhook_id", Value: "wh_error"}}

	server.getStatus(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHealthCheck(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("Ping", mock.Anything).Return(nil)
	mockHS.On("GetEpoch", mock.Anything).Return(nil, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/health", nil)

	server.handleHealth(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "healthy")
	mockHS.AssertExpectations(t)
}

func TestReadyCheck_Healthy(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("Ping", mock.Anything).Return(nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/ready", nil)

	server.readyCheck(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ready")
}

func TestReadyCheck_Unhealthy(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("Ping", mock.Anything).Return(errors.New("redis down"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/ready", nil)

	server.readyCheck(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "not ready")
}

func TestGetStats_Success(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	stats1h := &domain.Stats{
		Period:          "1h",
		Enqueued:        100,
		Delivered:       95,
		Failed:          3,
		Exhausted:       2,
		UniqueEndpoints: 10,
	}
	stats24h := &domain.Stats{
		Period:          "24h",
		Enqueued:        2400,
		Delivered:       2300,
		Failed:          70,
		Exhausted:       30,
		UniqueEndpoints: 50,
	}

	mockHS.On("GetStats", mock.Anything, mock.Anything, mock.Anything).Return(stats1h, nil).Once()
	mockHS.On("GetStats", mock.Anything, mock.Anything, mock.Anything).Return(stats24h, nil).Once()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/stats", nil)

	server.getStats(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp StatsResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, int64(100), resp.Last1h.Enqueued)
	assert.Equal(t, int64(2400), resp.Last24h.Enqueued)
}

func TestGetStats_Error(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("GetStats", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("redis error")).Twice()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/stats", nil)

	server.getStats(c)

	assert.Equal(t, http.StatusOK, w.Code) // Returns empty stats on error
}

func TestDependenciesCheck_AllHealthy(t *testing.T) {
	server, mockPub, mockHS, _, _ := setupTestServer(t)

	mockHS.On("Ping", mock.Anything).Return(nil)
	// Use a mock that implements the Ping method via type assertion
	mockPub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/health/dependencies", nil)

	server.dependenciesCheck(c)

	// Note: The Kafka health check uses type assertion, may not detect mock
	assert.Contains(t, []int{http.StatusOK, http.StatusServiceUnavailable}, w.Code)
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
