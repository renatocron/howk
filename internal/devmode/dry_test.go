package devmode

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/howk/howk/internal/domain"
)

func TestDryDeliverer_Returns200(t *testing.T) {
	d := NewDryDeliverer(zerolog.Nop())

	wh := &domain.Webhook{
		ID:       "wh_dry1",
		ConfigID: "cfg1",
		Endpoint: "https://example.com/hook",
		Payload:  json.RawMessage(`{"test":true}`),
		Attempt:  2,
	}

	result := d.Deliver(context.Background(), wh)

	assert.Equal(t, 200, result.StatusCode)
	assert.Nil(t, result.Error)
	assert.Contains(t, result.ResponseBody, "dry")
	assert.Greater(t, result.Duration.Nanoseconds(), int64(0))
}
