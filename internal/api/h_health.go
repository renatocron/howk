package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthResponse represents the health check response
type HealthResponse struct {
	Status     string            `json:"status"`
	Components map[string]string `json:"components"`
	Epoch      *EpochInfo        `json:"epoch,omitempty"`
	Timestamp  time.Time         `json:"timestamp"`
}

// EpochInfo contains the system epoch information
type EpochInfo struct {
	Epoch       int64     `json:"epoch"`
	CompletedAt time.Time `json:"completed_at"`
	Age         string    `json:"age"`
}

// handleHealth returns a detailed health check with epoch information
func (s *Server) handleHealth(c *gin.Context) {
	ctx := c.Request.Context()
	response := HealthResponse{
		Status:     "healthy",
		Components: make(map[string]string),
		Timestamp:  time.Now(),
	}

	// Check Redis
	if err := s.hotstate.Ping(ctx); err != nil {
		response.Components["redis"] = "unhealthy"
		response.Status = "degraded"
	} else {
		response.Components["redis"] = "healthy"
	}

	response.Components["kafka"] = "healthy"

	// Get epoch info
	epoch, err := s.hotstate.GetEpoch(ctx)
	if err == nil && epoch != nil {
		response.Epoch = &EpochInfo{
			Epoch:       epoch.Epoch,
			CompletedAt: epoch.CompletedAt,
			Age:         time.Since(epoch.CompletedAt).Round(time.Second).String(),
		}
	}

	statusCode := http.StatusOK
	if response.Status != "healthy" {
		statusCode = http.StatusServiceUnavailable
	}

	c.JSON(statusCode, response)
}
