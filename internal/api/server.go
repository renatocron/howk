package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
)

// Server is the HTTP API server
type Server struct {
	config    config.APIConfig
	publisher *broker.KafkaWebhookPublisher
	hotstate  *hotstate.RedisHotState
	router    *gin.Engine
}

// NewServer creates a new API server
func NewServer(
	cfg config.APIConfig,
	pub *broker.KafkaWebhookPublisher,
	hs *hotstate.RedisHotState,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(requestLogger())

	s := &Server{
		config:    cfg,
		publisher: pub,
		hotstate:  hs,
		router:    router,
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// Health endpoints
	s.router.GET("/health", s.healthCheck)
	s.router.GET("/ready", s.readyCheck)
	s.router.GET("/health/dependencies", s.dependenciesCheck) // Add this

	// Webhook endpoints
	webhooks := s.router.Group("/webhooks")
	{
		webhooks.POST("/:config/enqueue", s.enqueueWebhook)
		webhooks.POST("/:config/enqueue/batch", s.enqueueWebhookBatch)
		webhooks.GET("/:webhook_id/status", s.getStatus)
	}

	// Stats endpoint
	s.router.GET("/stats", s.getStats)
}

// Run starts the server
func (s *Server) Run(ctx context.Context) error {
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Port),
		Handler:      s.router,
		ReadTimeout:  s.config.ReadTimeout,
		WriteTimeout: s.config.WriteTimeout,
	}

	// Start server in goroutine
	errCh := make(chan error, 1)
	go func() {
		log.Info().Int("port", s.config.Port).Msg("API server starting...")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for shutdown signal
	select {
	case <-ctx.Done():
		log.Info().Msg("API server shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// --- Request/Response Types ---

type EnqueueRequest struct {
	Endpoint       string            `json:"endpoint" binding:"required,url"`
	Payload        json.RawMessage   `json:"payload" binding:"required"`
	Headers        map[string]string `json:"headers,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	SigningSecret  string            `json:"signing_secret,omitempty"`
	MaxAttempts    int               `json:"max_attempts,omitempty"`
}

type EnqueueResponse struct {
	WebhookID string `json:"webhook_id"`
	Status    string `json:"status"`
}

type BatchEnqueueRequest struct {
	Webhooks []EnqueueRequest `json:"webhooks" binding:"required,min=1,max=100"`
}

type BatchEnqueueResponse struct {
	Webhooks []EnqueueResponse `json:"webhooks"`
	Accepted int               `json:"accepted"`
	Failed   int               `json:"failed"`
}

type StatusResponse struct {
	WebhookID      string     `json:"webhook_id"`
	State          string     `json:"state"`
	Attempts       int        `json:"attempts"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastStatusCode int        `json:"last_status_code,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
}

type StatsResponse struct {
	Last1h  domain.Stats `json:"last_1h"`
	Last24h domain.Stats `json:"last_24h"`
}

// --- Handlers ---

func (s *Server) healthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) readyCheck(c *gin.Context) {
	// Check Redis connectivity
	if err := s.hotstate.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "error": "redis unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func (s *Server) getStats(c *gin.Context) {
	now := time.Now()

	// Last 1 hour
	stats1h, err := s.hotstate.GetStats(c.Request.Context(), now.Add(-1*time.Hour), now)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get 1h stats")
		stats1h = &domain.Stats{}
	}
	stats1h.Period = "1h"

	// Last 24 hours
	stats24h, err := s.hotstate.GetStats(c.Request.Context(), now.Add(-24*time.Hour), now)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get 24h stats")
		stats24h = &domain.Stats{}
	}
	stats24h.Period = "24h"

	c.JSON(http.StatusOK, StatsResponse{
		Last1h:  *stats1h,
		Last24h: *stats24h,
	})
}

func (s *Server) buildWebhook(configID domain.ConfigID, req *EnqueueRequest) *domain.Webhook {
	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 20 // Default
	}

	return &domain.Webhook{
		ID:             domain.WebhookID("wh_" + ulid.Make().String()),
		ConfigID:       configID,
		Endpoint:       req.Endpoint,
		EndpointHash:   domain.HashEndpoint(req.Endpoint),
		Payload:        req.Payload,
		Headers:        req.Headers,
		IdempotencyKey: req.IdempotencyKey,
		SigningSecret:  req.SigningSecret,
		Attempt:        0,
		MaxAttempts:    maxAttempts,
		CreatedAt:      time.Now(),
		ScheduledAt:    time.Now(),
	}
}

func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		log.Debug().
			Str("method", c.Request.Method).
			Str("path", path).
			Int("status", c.Writer.Status()).
			Dur("latency", time.Since(start)).
			Str("ip", c.ClientIP()).
			Msg("Request")
	}
}
