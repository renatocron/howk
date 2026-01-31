package testutil

import (
    "encoding/json"
    "testing"
    "time"

    "github.com/howk/howk/internal/domain"
    "github.com/oklog/ulid/v2"
)

type WebhookOpts struct {
    TenantID      string
    Endpoint      string
    Payload       json.RawMessage
    MaxAttempts   int
    SigningSecret string
    Headers       map[string]string
}

// NewTestWebhook creates webhook with sensible defaults
func NewTestWebhook(endpoint string) *domain.Webhook {
    return NewTestWebhookWithOpts(WebhookOpts{Endpoint: endpoint})
}

// NewTestWebhookWithOpts creates webhook with custom options
func NewTestWebhookWithOpts(opts WebhookOpts) *domain.Webhook {
    if opts.Endpoint == "" {
        opts.Endpoint = "https://example.com/webhook"
    }
    if opts.TenantID == "" {
        opts.TenantID = "test-tenant"
    }
    if opts.Payload == nil {
        opts.Payload = json.RawMessage(`{"test": true}`)
    }
    if opts.MaxAttempts == 0 {
        opts.MaxAttempts = 3
    }

    return &domain.Webhook{
        ID:            domain.WebhookID("wh_test_" + ulid.Make().String()),
        TenantID:      domain.TenantID(opts.TenantID),
        Endpoint:      opts.Endpoint,
        EndpointHash:  domain.HashEndpoint(opts.Endpoint),
        Payload:       opts.Payload,
        Headers:       opts.Headers,
        SigningSecret: opts.SigningSecret,
        Attempt:       0,
        MaxAttempts:   opts.MaxAttempts,
        CreatedAt:     time.Now(),
        ScheduledAt:   time.Now(),
    }
}


// WaitFor polls condition until true or timeout
func WaitFor(t *testing.T, timeout time.Duration, condition func() bool) {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if condition() {
            return
        }
        time.Sleep(50 * time.Millisecond)
    }
    t.Fatal("timeout waiting for condition")
}
