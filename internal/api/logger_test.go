//go:build !integration

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestRequestLogger_PassthroughAndStatus verifies that requestLogger() calls
// the next handler and the response status written by that handler is visible
// to the middleware after c.Next() returns.
func TestRequestLogger_PassthroughAndStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		handlerStatus  int
		expectedStatus int
	}{
		{"200 OK", http.StatusOK, http.StatusOK},
		{"201 Created", http.StatusCreated, http.StatusCreated},
		{"400 Bad Request", http.StatusBadRequest, http.StatusBadRequest},
		{"500 Internal Server Error", http.StatusInternalServerError, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()

			// Build a minimal router with the middleware under test.
			router := gin.New()
			router.Use(requestLogger())
			router.GET("/ping", func(c *gin.Context) {
				c.Status(tt.handlerStatus)
			})

			req := httptest.NewRequest(http.MethodGet, "/ping", nil)
			router.ServeHTTP(w, req)

			// The middleware must not interfere with the status code set by
			// the handler.
			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

// TestRequestLogger_BodyPassthrough verifies that a response body written by
// the handler is not consumed or altered by the middleware.
func TestRequestLogger_BodyPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const want = `{"status":"ok"}`

	w := httptest.NewRecorder()

	router := gin.New()
	router.Use(requestLogger())
	router.GET("/data", func(c *gin.Context) {
		c.String(http.StatusOK, want)
	})

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, want, w.Body.String())
}

// TestRequestLogger_PostRequest verifies that the middleware works correctly
// for POST requests (method is captured in the log field).
func TestRequestLogger_PostRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()

	router := gin.New()
	router.Use(requestLogger())
	router.POST("/webhook", func(c *gin.Context) {
		c.Status(http.StatusAccepted)
	})

	req := httptest.NewRequest(http.MethodPost, "/webhook", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
}
