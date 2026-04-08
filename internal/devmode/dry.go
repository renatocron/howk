package devmode

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
)

// DryDeliverer simulates webhook delivery without making HTTP calls.
// Always returns 200 OK.
type DryDeliverer struct {
	logger zerolog.Logger
}

var _ delivery.Deliverer = (*DryDeliverer)(nil)

func NewDryDeliverer(logger zerolog.Logger) *DryDeliverer {
	return &DryDeliverer{logger: logger}
}

func (d *DryDeliverer) Deliver(ctx context.Context, webhook *domain.Webhook) *delivery.Result {
	d.logger.Info().
		Str("webhook_id", string(webhook.ID)).
		Str("endpoint", webhook.Endpoint).
		Str("config_id", string(webhook.ConfigID)).
		Int("attempt", webhook.Attempt).
		Int("payload_bytes", len(webhook.Payload)).
		Msg("DRY: would deliver")

	return &delivery.Result{
		StatusCode:   200,
		Duration:     time.Millisecond,
		ResponseBody: `{"dry":true}`,
	}
}
