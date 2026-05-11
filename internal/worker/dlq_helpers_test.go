//go:build !integration

package worker

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

// White-box tests for the DLQ redaction / response-body helpers. These exercise
// the config-driven policy without spinning up a full Worker pipeline.

func newDLQWorker(t *testing.T, dlq config.DLQConfig) *Worker {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.DLQ = dlq
	return &Worker{config: cfg}
}

func TestBuildDeadLetterWebhook_DefaultsRedactAuthorization(t *testing.T) {
	w := newDLQWorker(t, config.DLQConfig{IncludeResponseBody: true})
	wh := &domain.Webhook{
		ID: "wh_1",
		Headers: map[string]string{
			"Authorization": "Bearer secret",
			"X-Trace-Id":    "abc",
		},
	}
	got := w.BuildDeadLetterWebhook(wh)

	assert.Equal(t, "[REDACTED]", got.Headers["Authorization"])
	assert.Equal(t, "abc", got.Headers["X-Trace-Id"])
	// Original untouched.
	assert.Equal(t, "Bearer secret", wh.Headers["Authorization"])
}

func TestBuildDeadLetterWebhook_DisableRedaction(t *testing.T) {
	w := newDLQWorker(t, config.DLQConfig{DisableRedaction: true})
	wh := &domain.Webhook{
		ID:      "wh_1",
		Headers: map[string]string{"Authorization": "Bearer secret"},
	}
	got := w.BuildDeadLetterWebhook(wh)

	// Same pointer + raw header value preserved.
	assert.Same(t, wh, got)
	assert.Equal(t, "Bearer secret", got.Headers["Authorization"])
}

func TestBuildDeadLetterWebhook_CustomListFullyOverridesDefaults(t *testing.T) {
	w := newDLQWorker(t, config.DLQConfig{
		RedactHeaders: []string{"X-Tenant-Secret"},
	})
	wh := &domain.Webhook{
		ID: "wh_1",
		Headers: map[string]string{
			"Authorization":   "Bearer secret",
			"X-Tenant-Secret": "tenant-123",
		},
	}
	got := w.BuildDeadLetterWebhook(wh)

	// Custom list does NOT include Authorization → it leaks. This is the
	// explicit footgun acknowledged by the "full override" semantics.
	assert.Equal(t, "Bearer secret", got.Headers["Authorization"])
	assert.Equal(t, "[REDACTED]", got.Headers["X-Tenant-Secret"])
}

func TestDLQResponseBody_IncludedByDefault(t *testing.T) {
	w := newDLQWorker(t, config.DLQConfig{IncludeResponseBody: true})
	assert.Equal(t, "error: validation failed", w.DLQResponseBody("error: validation failed"))
}

func TestDLQResponseBody_OmittedWhenDisabled(t *testing.T) {
	w := newDLQWorker(t, config.DLQConfig{IncludeResponseBody: false})
	assert.Empty(t, w.DLQResponseBody("error: validation failed"))
}
