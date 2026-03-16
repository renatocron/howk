package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/transformer"
)

// transformerErrToHTTP maps a TransformerError to the appropriate HTTP status
// and a safe client-facing message. Internal errors (broker, Redis) are opaque
// to callers; script errors are surfaced to help operators debug their Lua code.
func transformerErrToHTTP(err *transformer.TransformerError) (statusCode int, clientMsg string) {
	switch err.Kind {
	case transformer.ErrorKindSyntax:
		return http.StatusUnprocessableEntity, "script syntax error: " + err.Message
	case transformer.ErrorKindRuntime:
		return http.StatusUnprocessableEntity, "script runtime error: " + err.Message
	case transformer.ErrorKindTimeout:
		return http.StatusGatewayTimeout, "script execution timed out"
	default:
		// ErrorKindInternal and any future kinds: hide implementation details
		return http.StatusInternalServerError, "script execution failed"
	}
}

// IncomingResponse is the response format for the incoming webhook endpoint
type IncomingResponse struct {
	Webhooks []transformer.WebhookRef `json:"webhooks"`
	Count    int                      `json:"count"`
}

// handleIncoming processes POST /incoming/:script_name requests
func (s *Server) handleIncoming(c *gin.Context) {
	scriptName := c.Param("script_name")

	// Check if transformer feature is enabled
	if s.transformerRegistry == nil || s.transformerEngine == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "transformer feature not enabled"})
		return
	}

	// 1. Look up script
	script, found := s.transformerRegistry.Get(scriptName)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "script not found"})
		return
	}

	// 2. Check auth if .passwd exists
	if script.Auth != nil {
		username, password, hasAuth := c.Request.BasicAuth()
		if !hasAuth || !script.Auth.Check(username, password) {
			c.Header("WWW-Authenticate", `Basic realm="transformer"`)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
	}

	// 3. Read body (raw bytes - any content type), enforcing the size limit
	// before reading to prevent unbounded memory allocation.
	maxSize := s.config.MaxRequestSize
	if maxSize > 0 {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSize)
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		// MaxBytesReader returns an error when the limit is exceeded.
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}
	defer c.Request.Body.Close()

	// 5. Collect HTTP headers to pass to script
	headers := make(map[string]string)
	for k, v := range c.Request.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	// 6. Execute transformer
	result, err := s.transformerEngine.Execute(c.Request.Context(), script, body, headers)
	if err != nil {
		log.Error().
			Str("script", scriptName).
			Err(err).
			Msg("Transformer script execution failed")

		// Use the typed error to select the right status code and a safe message.
		// Unrecognised error types fall through to 500 with an opaque message.
		var tErr *transformer.TransformerError
		if errors.As(err, &tErr) {
			statusCode, clientMsg := transformerErrToHTTP(tErr)
			c.JSON(statusCode, gin.H{"error": clientMsg})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "script execution failed"})
		}
		return
	}

	// 7. Return success response
	c.JSON(http.StatusOK, IncomingResponse{
		Webhooks: result.Webhooks,
		Count:    len(result.Webhooks),
	})
}
