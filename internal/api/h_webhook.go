package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/howk/howk/internal/domain"
	"github.com/rs/zerolog/log"
)

func (s *Server) enqueueWebhook(c *gin.Context) {
	configID := c.Param("config")

	var req EnqueueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	s.processEnqueue(c, configID, []EnqueueRequest{req})
}

func (s *Server) enqueueWebhookBatch(c *gin.Context) {
	configID := c.Param("config")

	var req BatchEnqueueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	s.processEnqueue(c, configID, req.Webhooks)
}

// processEnqueue is the unified helper for enqueueing webhooks.
// It handles both single and batch enqueue operations.
func (s *Server) processEnqueue(c *gin.Context, configID string, reqs []EnqueueRequest) {
	ctx := c.Request.Context()
	responses := make([]EnqueueResponse, 0, len(reqs))
	var accepted, failed int
	var firstPublishError error
	var firstEndpointHash string

	for i, req := range reqs {
		webhook := s.buildWebhook(domain.ConfigID(configID), &req)

		// Capture the first endpoint hash for stats
		if i == 0 {
			firstEndpointHash = string(webhook.EndpointHash)
		}

		// Publish to Kafka
		if err := s.publisher.PublishWebhook(ctx, webhook); err != nil {
			log.Error().Err(err).Str("webhook_id", string(webhook.ID)).Msg("Failed to enqueue webhook")
			failed++
			if firstPublishError == nil {
				firstPublishError = err
			}
			continue
		}

		// Set initial status
		status := &domain.WebhookStatus{
			WebhookID: webhook.ID,
			State:     domain.StatePending,
			Attempts:  0,
		}
		s.hotstate.SetStatus(ctx, status)

		responses = append(responses, EnqueueResponse{
			WebhookID: string(webhook.ID),
			Status:    "pending",
		})
		accepted++
	}

	// Record stats
	bucket := time.Now().Format("2006010215")
	s.hotstate.IncrStats(ctx, bucket, map[string]int64{"enqueued": int64(accepted)})
	if firstEndpointHash != "" {
		s.hotstate.AddToHLL(ctx, "endpoints:"+bucket, firstEndpointHash)
	}

	// Return appropriate response based on request type
	if len(reqs) == 1 {
		// For single webhook: maintain backward compatibility
		// If the only webhook failed, return 500
		if len(responses) == 0 && firstPublishError != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to enqueue"})
			return
		}
		c.JSON(http.StatusAccepted, responses[0])
	} else {
		c.JSON(http.StatusAccepted, BatchEnqueueResponse{
			Webhooks: responses,
			Accepted: accepted,
			Failed:   failed,
		})
	}
}

func (s *Server) getStatus(c *gin.Context) {
	webhookID := c.Param("webhook_id")

	status, err := s.hotstate.GetStatus(c.Request.Context(), domain.WebhookID(webhookID))
	if err != nil {
		log.Error().Err(err).Str("webhook_id", webhookID).Msg("Failed to get status")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get status"})
		return
	}

	if status == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "webhook not found"})
		return
	}

	c.JSON(http.StatusOK, StatusResponse{
		WebhookID:      string(status.WebhookID),
		State:          status.State,
		Attempts:       status.Attempts,
		LastAttemptAt:  status.LastAttemptAt,
		LastStatusCode: status.LastStatusCode,
		LastError:      status.LastError,
		NextRetryAt:    status.NextRetryAt,
		DeliveredAt:    status.DeliveredAt,
	})
}
