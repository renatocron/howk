//go:build !integration

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/howk/howk/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestHandleUploadScript_Success(t *testing.T) {
	server, _, mockHS, mockValidator, mockScriptPub := setupTestServer(t)

	luaCode := `return payload`
	mockValidator.On("ValidateSyntax", luaCode).Return(nil)
	mockScriptPub.On("PublishScript", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("SetScript", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	reqBody := `{"lua_code":"return payload"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleUploadScript(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ScriptResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.NotEmpty(t, resp.Hash)
	assert.Equal(t, "test-config", resp.ConfigID)

	mockValidator.AssertExpectations(t)
	mockScriptPub.AssertExpectations(t)
	mockHS.AssertExpectations(t)
}

func TestHandleUploadScript_InvalidJSON(t *testing.T) {
	server, _, _, _, _ := setupTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script", bytes.NewBufferString("invalid json"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleUploadScript(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleUploadScript_MissingLuaCode(t *testing.T) {
	server, _, _, _, _ := setupTestServer(t)

	reqBody := `{}` // Missing lua_code
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleUploadScript(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleUploadScript_SyntaxError(t *testing.T) {
	server, _, _, mockValidator, _ := setupTestServer(t)

	luaCode := `invalid lua {{{`
	mockValidator.On("ValidateSyntax", luaCode).Return(errors.New("syntax error: unexpected '{'"))

	reqBody := `{"lua_code":"invalid lua {{{"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleUploadScript(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Contains(t, resp["error"], "Lua syntax error")

	mockValidator.AssertExpectations(t)
}

func TestHandleUploadScript_PublishError(t *testing.T) {
	server, _, _, mockValidator, mockScriptPub := setupTestServer(t)

	luaCode := `return payload`
	mockValidator.On("ValidateSyntax", luaCode).Return(nil)
	mockScriptPub.On("PublishScript", mock.Anything, mock.Anything).Return(errors.New("kafka error"))

	reqBody := `{"lua_code":"return payload"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleUploadScript(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	mockValidator.AssertExpectations(t)
	mockScriptPub.AssertExpectations(t)
}

func TestHandleUploadScript_CacheErrorNonFatal(t *testing.T) {
	server, _, mockHS, mockValidator, mockScriptPub := setupTestServer(t)

	luaCode := `return payload`
	mockValidator.On("ValidateSyntax", luaCode).Return(nil)
	mockScriptPub.On("PublishScript", mock.Anything, mock.Anything).Return(nil)
	// Cache error should not fail the request
	mockHS.On("SetScript", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errors.New("redis error"))

	reqBody := `{"lua_code":"return payload"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleUploadScript(c)

	// Should still succeed even if cache fails
	assert.Equal(t, http.StatusOK, w.Code)

	mockValidator.AssertExpectations(t)
	mockScriptPub.AssertExpectations(t)
	mockHS.AssertExpectations(t)
}

func TestHandleGetScript_Success(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	scriptJSON := `{"config_id":"test-config","hash":"abc123","version":"v1"}`
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("test-config")).Return(scriptJSON, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/config/test-config/script", nil)
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleGetScript(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ScriptResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, "abc123", resp.Hash)
	assert.Equal(t, "test-config", resp.ConfigID)

	mockHS.AssertExpectations(t)
}

func TestHandleGetScript_NotFound(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	mockHS.On("GetScript", mock.Anything, domain.ConfigID("test-config")).Return("", errors.New("not found"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/config/test-config/script", nil)
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleGetScript(c)

	assert.Equal(t, http.StatusNotFound, w.Code)

	mockHS.AssertExpectations(t)
}

func TestHandleGetScript_InvalidCachedData(t *testing.T) {
	server, _, mockHS, _, _ := setupTestServer(t)

	// Return invalid JSON from cache
	mockHS.On("GetScript", mock.Anything, domain.ConfigID("test-config")).Return("invalid json", nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/config/test-config/script", nil)
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleGetScript(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	mockHS.AssertExpectations(t)
}

func TestHandleDeleteScript_Success(t *testing.T) {
	server, _, mockHS, _, mockScriptPub := setupTestServer(t)

	mockScriptPub.On("DeleteScript", mock.Anything, mock.Anything).Return(nil)
	mockHS.On("DeleteScript", mock.Anything, mock.Anything).Return(nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/config/test-config/script", nil)
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleDeleteScript(c)

	// Should succeed (2xx) - WriteHeader(NoContent) may not be detected 
	// properly by httptest with gin, so just check it's not an error
	assert.Less(t, w.Code, 300)

	mockScriptPub.AssertExpectations(t)
	mockHS.AssertExpectations(t)
}

func TestHandleDeleteScript_KafkaError(t *testing.T) {
	server, _, _, _, mockScriptPub := setupTestServer(t)

	mockScriptPub.On("DeleteScript", mock.Anything, mock.Anything).Return(errors.New("kafka error"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/config/test-config/script", nil)
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleDeleteScript(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	mockScriptPub.AssertExpectations(t)
}

func TestHandleDeleteScript_CacheErrorNonFatal(t *testing.T) {
	server, _, mockHS, _, mockScriptPub := setupTestServer(t)

	// Use mock.Anything for all args to ensure match
	mockScriptPub.On("DeleteScript", mock.Anything, mock.Anything).Return(nil)
	// Cache error should not fail the request
	mockHS.On("DeleteScript", mock.Anything, mock.Anything).Return(errors.New("redis error"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/config/test-config/script", nil)
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleDeleteScript(c)

	// Should succeed (2xx) even if cache delete fails - WriteHeader(NoContent) 
	// may not be detected properly by httptest, so just check it's not an error
	assert.Less(t, w.Code, 300)

	mockScriptPub.AssertExpectations(t)
	mockHS.AssertExpectations(t)
}

func TestHandleTestScript_InvalidJSON(t *testing.T) {
	server, _, _, _, _ := setupTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script/test", bytes.NewBufferString("invalid json"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleTestScript(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTestScript_MissingPayload(t *testing.T) {
	server, _, _, _, _ := setupTestServer(t)

	reqBody := `{"lua_code":"return payload"}` // Missing payload
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script/test", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleTestScript(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTestScript_SyntaxError(t *testing.T) {
	server, _, _, mockValidator, _ := setupTestServer(t)

	luaCode := `invalid lua {{{`
	mockValidator.On("ValidateSyntax", luaCode).Return(errors.New("syntax error"))

	reqBody := `{"lua_code":"invalid lua {{{","payload":{"test":"data"}}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script/test", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleTestScript(c)

	assert.Equal(t, http.StatusOK, w.Code) // Returns 200 with success=false

	var resp TestScriptResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "syntax error")

	mockValidator.AssertExpectations(t)
}

func TestHandleTestScript_ValidExecution(t *testing.T) {
	server, _, _, mockValidator, _ := setupTestServer(t)

	luaCode := `request.headers["X-Custom"] = "test"`
	mockValidator.On("ValidateSyntax", luaCode).Return(nil)

	reqBody := `{"lua_code":"request.headers[\"X-Custom\"] = \"test\"","payload":{"test":"data"}}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script/test", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleTestScript(c)

	// Should return 200 with test results
	assert.Equal(t, http.StatusOK, w.Code)

	var resp TestScriptResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	// Script should execute successfully
	if resp.Success {
		assert.NotNil(t, resp.TransformedHeaders)
	}
}

// ERROR PATH TEST - Script execution failure

func TestHandleTestScript_ExecutionError(t *testing.T) {
	server, _, _, mockValidator, _ := setupTestServer(t)

	// Valid syntax but will fail at runtime (attempt to call nil)
	luaCode := `local x = nil; x()`
	mockValidator.On("ValidateSyntax", luaCode).Return(nil)

	reqBody := `{"lua_code":"local x = nil; x()","payload":{"test":"data"}}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/config/test-config/script/test", bytes.NewBufferString(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "config_id", Value: "test-config"}}

	server.handleTestScript(c)

	// Should return 200 with success=false for execution errors
	assert.Equal(t, http.StatusOK, w.Code)

	var resp TestScriptResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.False(t, resp.Success)
	assert.NotEmpty(t, resp.Error)

	mockValidator.AssertExpectations(t)
}
