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

	webhook := s.buildWebhook(domain.ConfigID(configID), &req)

	// Publish to Kafka
	if err := s.publisher.PublishWebhook(c.Request.Context(), webhook); err != nil {
		log.Error().Err(err).Str("webhook_id", string(webhook.ID)).Msg("Failed to enqueue webhook")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to enqueue"})
		return
	}

	// Set initial status
	status := &domain.WebhookStatus{
		WebhookID: webhook.ID,
		State:     domain.StatePending,
		Attempts:  0,
	}
	s.hotstate.SetStatus(c.Request.Context(), status)

	// Record stats
	bucket := time.Now().Format("2006010215")
	s.hotstate.IncrStats(c.Request.Context(), bucket, map[string]int64{"enqueued": 1})
	s.hotstate.AddToHLL(c.Request.Context(), "endpoints:"+bucket, string(webhook.EndpointHash))

	c.JSON(http.StatusAccepted, EnqueueResponse{
		WebhookID: string(webhook.ID),
		Status:    "pending",
	})
}

func (s *Server) enqueueWebhookBatch(c *gin.Context) {
	configID := c.Param("config")

	var req BatchEnqueueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	responses := make([]EnqueueResponse, 0, len(req.Webhooks))
	var accepted, failed int

	for _, webhookReq := range req.Webhooks {
		webhook := s.buildWebhook(domain.ConfigID(configID), &webhookReq)

		if err := s.publisher.PublishWebhook(c.Request.Context(), webhook); err != nil {
			log.Error().Err(err).Str("webhook_id", string(webhook.ID)).Msg("Failed to enqueue webhook")
			failed++
			continue
		}

		// Set initial status
		status := &domain.WebhookStatus{
			WebhookID: webhook.ID,
			State:     domain.StatePending,
			Attempts:  0,
		}
		s.hotstate.SetStatus(c.Request.Context(), status)

		responses = append(responses, EnqueueResponse{
			WebhookID: string(webhook.ID),
			Status:    "pending",
		})
		accepted++
	}

	// Record stats
	bucket := time.Now().Format("2006010215")
	s.hotstate.IncrStats(c.Request.Context(), bucket, map[string]int64{"enqueued": int64(accepted)})

	c.JSON(http.StatusAccepted, BatchEnqueueResponse{
		Webhooks: responses,
		Accepted: accepted,
		Failed:   failed,
	})
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
