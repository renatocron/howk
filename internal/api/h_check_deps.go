package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// DepHealth represents status for a dependency
type DepHealth struct {
	Status string `json:"status"` // "healthy", "degraded", "unhealthy"
	Error  string `json:"error,omitempty"`
}

// dependenciesCheck checks health of external dependencies like Redis and Kafka
func (s *Server) dependenciesCheck(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	health := gin.H{
		"redis": DepHealth{Status: "healthy"},
		"kafka": DepHealth{Status: "healthy"},
	}
	overallStatus := http.StatusOK

	// Check Redis
	if err := s.hotstate.Ping(ctx); err != nil {
		health["redis"] = DepHealth{
			Status: "unhealthy",
			Error:  err.Error(),
		}
		overallStatus = http.StatusServiceUnavailable
	}

	// Check Kafka (try to get metadata)
	if kafkaHealth := s.checkKafkaHealth(ctx); kafkaHealth.Error != "" {
		health["kafka"] = kafkaHealth
		overallStatus = http.StatusServiceUnavailable
	}

	c.JSON(overallStatus, gin.H{
		"status":       map[int]string{200: "healthy", 503: "unhealthy"}[overallStatus],
		"dependencies": health,
		"timestamp":    time.Now().Format(time.RFC3339),
	})
}

func (s *Server) checkKafkaHealth(ctx context.Context) DepHealth {
	// We'll need to add a Ping() method to KafkaBroker and expose via publisher
	if s.publisher == nil {
		return DepHealth{Status: "unhealthy", Error: "publisher not configured"}
	}

	if pinger, ok := interface{}(s.publisher).(interface {
		Ping(ctx context.Context) error
	}); ok {
		if err := pinger.Ping(ctx); err != nil {
			return DepHealth{Status: "unhealthy", Error: err.Error()}
		}
		return DepHealth{Status: "healthy"}
	}

	// If publisher doesn't implement Ping, assume healthy (no direct check available)
	return DepHealth{Status: "healthy"}
}
