package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/script"
)

// Script request/response types

type UploadScriptRequest struct {
	LuaCode string `json:"lua_code" binding:"required"`
	Version string `json:"version"`
}

type ScriptResponse struct {
	ConfigID  string    `json:"config_id"`
	Hash      string    `json:"hash"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TestScriptRequest struct {
	LuaCode string            `json:"lua_code" binding:"required"`
	Payload json.RawMessage   `json:"payload" binding:"required"`
	Headers map[string]string `json:"headers"`
}

type TestScriptResponse struct {
	Success             bool              `json:"success"`
	TransformedPayload  json.RawMessage   `json:"transformed_payload,omitempty"`
	TransformedHeaders  map[string]string `json:"transformed_headers,omitempty"`
	Error               string            `json:"error,omitempty"`
	ExecutionTimeMs     float64           `json:"execution_time_ms"`
}

// handleUploadScript handles PUT /config/:config_id/script
func (s *Server) handleUploadScript(c *gin.Context) {
	configID := domain.ConfigID(c.Param("config_id"))

	var req UploadScriptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Validate Lua syntax
	if err := s.scriptValidator.ValidateSyntax(req.LuaCode); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Lua syntax error",
			"details": err.Error(),
		})
		return
	}

	// Calculate script hash
	scriptHash := script.ScriptHash(req.LuaCode)

	// Create script config
	now := time.Now()
	scriptConfig := &script.ScriptConfig{
		ConfigID:  configID,
		LuaCode:   req.LuaCode,
		Hash:      scriptHash,
		Version:   req.Version,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Publish to Kafka
	if err := s.scriptPublisher.PublishScript(c.Request.Context(), scriptConfig); err != nil {
		s.logger.Error().Err(err).Msg("Failed to publish script to Kafka")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save script"})
		return
	}

	// Cache in Redis
	scriptJSON, err := json.Marshal(scriptConfig)
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to marshal script for cache")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cache script"})
		return
	}

	if err := s.hotstate.SetScript(c.Request.Context(), configID, string(scriptJSON), 7*24*time.Hour); err != nil {
		s.logger.Warn().Err(err).Msg("Failed to cache script in Redis (non-fatal)")
		// Continue - Kafka is source of truth
	}

	// Return response
	c.JSON(http.StatusOK, ScriptResponse{
		ConfigID:  string(configID),
		Hash:      scriptHash,
		Version:   req.Version,
		CreatedAt: now,
		UpdatedAt: now,
	})
}

// handleGetScript handles GET /config/:config_id/script
func (s *Server) handleGetScript(c *gin.Context) {
	configID := domain.ConfigID(c.Param("config_id"))

	// Try to get from Redis cache first
	scriptJSON, err := s.hotstate.GetScript(c.Request.Context(), configID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Script not found"})
		return
	}

	// Unmarshal script config
	var scriptConfig script.ScriptConfig
	if err := json.Unmarshal([]byte(scriptJSON), &scriptConfig); err != nil {
		s.logger.Error().Err(err).Msg("Failed to unmarshal cached script")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve script"})
		return
	}

	// Return response with full script code
	c.JSON(http.StatusOK, gin.H{
		"config_id":  string(scriptConfig.ConfigID),
		"lua_code":   scriptConfig.LuaCode,
		"hash":       scriptConfig.Hash,
		"version":    scriptConfig.Version,
		"created_at": scriptConfig.CreatedAt,
		"updated_at": scriptConfig.UpdatedAt,
	})
}

// handleDeleteScript handles DELETE /config/:config_id/script
func (s *Server) handleDeleteScript(c *gin.Context) {
	configID := domain.ConfigID(c.Param("config_id"))

	// Publish tombstone to Kafka
	if err := s.scriptPublisher.DeleteScript(c.Request.Context(), configID); err != nil {
		s.logger.Error().Err(err).Msg("Failed to delete script from Kafka")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete script"})
		return
	}

	// Delete from Redis cache
	if err := s.hotstate.DeleteScript(c.Request.Context(), configID); err != nil {
		s.logger.Warn().Err(err).Msg("Failed to delete script from Redis (non-fatal)")
		// Continue - Kafka is source of truth
	}

	c.Writer.WriteHeader(http.StatusNoContent)
}

// handleTestScript handles POST /config/:config_id/script/test
func (s *Server) handleTestScript(c *gin.Context) {
	var req TestScriptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	start := time.Now()

	// Validate Lua syntax
	if err := s.scriptValidator.ValidateSyntax(req.LuaCode); err != nil {
		c.JSON(http.StatusOK, TestScriptResponse{
			Success:         false,
			Error:           err.Error(),
			ExecutionTimeMs: float64(time.Since(start).Microseconds()) / 1000.0,
		})
		return
	}

	// Create a temporary webhook for testing
	payloadJSON, err := json.Marshal(req.Payload)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload format"})
		return
	}

	testWebhook := &domain.Webhook{
		ID:       domain.WebhookID("test_" + time.Now().Format("20060102150405")),
		ConfigID: domain.ConfigID(c.Param("config_id")),
		Payload:  json.RawMessage(payloadJSON),
		Headers:  req.Headers,
		Attempt:  1,
	}

	// Load script into temporary loader for testing
	tempLoader := script.NewLoader()
	tempLoader.SetScript(&script.ScriptConfig{
		ConfigID: testWebhook.ConfigID,
		LuaCode:  req.LuaCode,
		Hash:     "test",
	})

	// Create temporary engine with test config (use defaults)
	testEngine := script.NewEngine(config.LuaConfig{
		Enabled:       true,
		Timeout:       500 * time.Millisecond,
		MemoryLimitMB: 50,
	}, tempLoader, nil, nil, nil, zerolog.Logger{})
	defer testEngine.Close()

	// Execute script
	transformed, err := testEngine.Execute(c.Request.Context(), testWebhook)
	if err != nil {
		c.JSON(http.StatusOK, TestScriptResponse{
			Success:         false,
			Error:           err.Error(),
			ExecutionTimeMs: float64(time.Since(start).Microseconds()) / 1000.0,
		})
		return
	}

	// Use transformed payload if available, otherwise use original
	transformedPayload := req.Payload
	if len(transformed.Payload) > 0 {
		transformedPayload = transformed.Payload
	}

	c.JSON(http.StatusOK, TestScriptResponse{
		Success:            true,
		TransformedPayload: transformedPayload,
		TransformedHeaders: transformed.Headers,
		ExecutionTimeMs:    float64(time.Since(start).Microseconds()) / 1000.0,
	})
}
