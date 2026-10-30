//go:build !integration

package worker

import (
	"bytes"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
)

func logWithResponseBody(t *testing.T, cfg config.DeliveryConfig, configID domain.ConfigID) string {
	t.Helper()
	full := config.DefaultConfig()
	full.Delivery = cfg
	w := &Worker{config: full}

	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	result := &delivery.Result{StatusCode: 200, ResponseBody: "<html>blocked</html>", ContentType: "text/html"}
	w.withResponseBody(logger.Info(), configID, result).Msg("Delivery succeeded")
	return buf.String()
}

func TestWithResponseBody_DisabledByDefault(t *testing.T) {
	out := logWithResponseBody(t, config.DefaultConfig().Delivery, "wh:acme:1")
	assert.NotContains(t, out, "response_body")
	assert.NotContains(t, out, "response_content_type")
}

func TestWithResponseBody_EnabledNoPrefix(t *testing.T) {
	cfg := config.DefaultConfig().Delivery
	cfg.LogResponseBody = true
	out := logWithResponseBody(t, cfg, "any-config")
	assert.Contains(t, out, `"response_body":"<html>blocked</html>"`)
	assert.Contains(t, out, `"response_content_type":"text/html"`)
}

func TestWithResponseBody_PrefixFilter(t *testing.T) {
	cfg := config.DefaultConfig().Delivery
	cfg.LogResponseBody = true
	cfg.LogResponseBodyConfigPrefix = "wh:acme:"

	assert.Contains(t, logWithResponseBody(t, cfg, "wh:acme:42"), "response_body")
	assert.NotContains(t, logWithResponseBody(t, cfg, "wh:other:42"), "response_body")
}
