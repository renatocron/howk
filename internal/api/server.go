package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/script"
	"github.com/howk/howk/internal/transformer"
)

// Server is the HTTP API server
type Server struct {
	config              config.APIConfig
	publisher           broker.WebhookPublisher
	hotstate            hotstate.HotState
	scriptValidator     script.ValidatorInterface
	scriptPublisher     script.PublisherInterface
	transformerRegistry *transformer.Registry
	transformerEngine   *transformer.Engine
	router              *gin.Engine
	logger              zerolog.Logger
}

// ServerOption is a functional option for Server
type ServerOption func(*Server)

// NewServer creates a new API server
func NewServer(
	cfg config.APIConfig,
	pub broker.WebhookPublisher,
	hs hotstate.HotState,
	scriptValidator script.ValidatorInterface,
	scriptPublisher script.PublisherInterface,
	opts ...ServerOption,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(requestLogger())

	s := &Server{
		config:          cfg,
		publisher:       pub,
		hotstate:        hs,
		scriptValidator: scriptValidator,
		scriptPublisher: scriptPublisher,
		router:          router,
		logger:          log.With().Str("component", "api").Logger(),
	}

	// Apply functional options
	for _, opt := range opts {
		opt(s)
	}

	s.setupRoutes()
	return s
}

// WithTransformers sets the transformer registry and engine
func WithTransformers(reg *transformer.Registry, eng *transformer.Engine) ServerOption {
	return func(s *Server) {
		s.transformerRegistry = reg
		s.transformerEngine = eng
	}
}

func (s *Server) setupRoutes() {
	// Health endpoints
	s.router.GET("/health", s.handleHealth)
	s.router.GET("/ready", s.readyCheck)
	s.router.GET("/health/dependencies", s.dependenciesCheck)

	// Webhook endpoints
	webhooks := s.router.Group("/webhooks")
	{
		webhooks.POST("/:config/enqueue", s.enqueueWebhook)
		webhooks.POST("/:config/enqueue/batch", s.enqueueWebhookBatch)
		webhooks.GET("/:webhook_id/status", s.getStatus)
	}

	// Script endpoints
	scripts := s.router.Group("/config")
	{
		scripts.PUT("/:config_id/script", s.handleUploadScript)
		scripts.GET("/:config_id/script", s.handleGetScript)
		scripts.DELETE("/:config_id/script", s.handleDeleteScript)
		scripts.POST("/:config_id/script/test", s.handleTestScript)
	}

	// Stats endpoint
	s.router.GET("/stats", s.getStats)

	// Transformer endpoint (only if transformer feature is enabled)
	if s.transformerRegistry != nil {
		s.router.POST("/incoming/:script_name", s.handleIncoming)
	}
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

	webhook := &domain.Webhook{
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

	// Check if script exists for this config_id and set ScriptHash
	ctx := context.Background()
	scriptJSON, err := s.hotstate.GetScript(ctx, configID)
	if err == nil && scriptJSON != "" {
		// Parse script to get hash
		var scriptConfig script.ScriptConfig
		if err := json.Unmarshal([]byte(scriptJSON), &scriptConfig); err == nil {
			webhook.ScriptHash = scriptConfig.Hash
			log.Debug().
				Str("config_id", string(configID)).
				Str("script_hash", scriptConfig.Hash).
				Msg("Auto-applied script hash to webhook")
		}
	}

	return webhook
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
