package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
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
	scriptValidator     script.SyntaxChecker
	scriptPublisher     script.ScriptPublisher
	scriptLoader        *script.Loader
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
	scriptValidator script.SyntaxChecker,
	scriptPublisher script.ScriptPublisher,
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

// WithScriptLoader sets the Kafka-replayed in-memory script loader. When set,
// webhook enqueue falls back to it for ScriptHash tagging whenever the Redis
// script cache misses (TTL expiry, flush, outage), and heals the Redis key on
// a hit. The loader is kept current by a script.Consumer replaying the
// compacted scripts topic.
func WithScriptLoader(loader *script.Loader) ServerOption {
	return func(s *Server) {
		s.scriptLoader = loader
	}
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
	s.router.GET("/ready", s.handleReadyCheck)
	s.router.GET("/health/dependencies", s.handleDependenciesCheck)

	// Webhook endpoints
	webhooks := s.router.Group("/webhooks")
	{
		webhooks.POST("/:config/enqueue", s.handleEnqueueWebhook)
		webhooks.POST("/:config/enqueue/batch", s.handleEnqueueWebhookBatch)
		webhooks.GET("/:webhook_id/status", s.handleGetStatus)
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
	s.router.GET("/stats", s.handleGetStats)

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

	// RequireScript, when true, makes the enqueue fail-fast with 422 if no Lua
	// script resolves for this config_id (memory loader, then Redis fallback).
	// Use it when the payload is meaningless without transformation — e.g. a
	// credential-injecting script — so a missing/expired script surfaces
	// immediately at the producer instead of silently delivering a raw payload.
	// HOWK has no knowledge of what the script does; it only enforces presence.
	RequireScript bool `json:"require_script,omitempty"`
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

func (s *Server) handleReadyCheck(c *gin.Context) {
	// Check Redis connectivity
	if err := s.hotstate.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "error": "redis unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func (s *Server) handleGetStats(c *gin.Context) {
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
	webhook := domain.NewWebhook(domain.NewWebhookOpts{
		ConfigID:       configID,
		Endpoint:       req.Endpoint,
		Payload:        req.Payload,
		Headers:        req.Headers,
		IdempotencyKey: req.IdempotencyKey,
		SigningSecret:  req.SigningSecret,
		MaxAttempts:    req.MaxAttempts,
	})

	// Resolve the script hash for this config_id to mark transformation intent
	// for the worker. The in-memory loader holds the COMPLETE script state
	// (replayed from the compacted Kafka topic and kept current by the tail),
	// so this is the primary source: an O(1) in-process lookup with namespace
	// fallback ("wh:75:1" -> "wh"). No Redis round-trip on the enqueue hot
	// path. Redis is consulted ONLY as a cold-start fallback (below), for the
	// brief window before the first replay populates the loader, or for
	// deployments that wire no loader.
	if s.scriptLoader != nil {
		if cfg, err := s.scriptLoader.GetScript(configID); err == nil && cfg != nil {
			webhook.ScriptHash = cfg.Hash
			return webhook
		}
	}

	// Cold-start / no-loader fallback: consult Redis once. On a hit, populate
	// the loader so subsequent enqueues for this config_id hit memory and
	// never touch Redis again. (This is the path the old code took on EVERY
	// enqueue; it is now the exception, not the rule.)
	scriptJSON, err := s.hotstate.GetScript(context.Background(), configID)
	if err == nil && scriptJSON != "" {
		var scriptConfig script.Config
		if err := json.Unmarshal([]byte(scriptJSON), &scriptConfig); err == nil {
			webhook.ScriptHash = scriptConfig.Hash
			if s.scriptLoader != nil {
				s.scriptLoader.SetScript(&scriptConfig)
			}
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
