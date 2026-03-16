package domain_test

import (
    "encoding/json"
    "strings"
    "testing"
    "time"

    "github.com/howk/howk/internal/domain"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestHashEndpoint(t *testing.T) {
    endpoint1 := "https://example.com/webhook1"
    endpoint2 := "https://example.com/webhook2"

    hash1 := domain.HashEndpoint(endpoint1)
    hash1_again := domain.HashEndpoint(endpoint1)
    hash2 := domain.HashEndpoint(endpoint2)

    assert.Equal(t, hash1, hash1_again)
    assert.NotEqual(t, hash1, hash2)
    assert.Len(t, string(hash1), 32)
}

func TestIsRetryable(t *testing.T) {
    tests := []struct {
        name       string
        statusCode int
        want       bool
    }{
        {"Timeout", 408, true},
        {"Rate Limit", 429, true},
        {"Server Error 500", 500, true},
        {"Server Error 502", 502, true},
        {"Server Error 599", 599, true},
        {"Success 200", 200, false},
        {"Success 299", 299, false},
        {"Client Error 400", 400, false},
        {"Client Error 499", 499, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := domain.IsRetryable(tt.statusCode)
            assert.Equal(t, tt.want, got)
        })
    }
}

func TestIsSuccess(t *testing.T) {
    tests := []struct {
        name       string
        statusCode int
        want       bool
    }{
        {"Success 200", 200, true},
        {"Success 201", 201, true},
        {"Success 299", 299, true},
        {"Redirect 302", 302, false},
        {"Client Error 404", 404, false},
        {"Server Error 500", 500, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := domain.IsSuccess(tt.statusCode)
            assert.Equal(t, tt.want, got)
        })
    }
}

func TestIsRetryable_Boundary(t *testing.T) {
    tests := []struct {
        name       string
        statusCode int
        want       bool
    }{
        // Boundary cases
        {"Boundary 500", 500, true},
        {"Boundary 501", 501, true},
        {"Boundary 502", 502, true},
        {"Boundary 599", 599, true},
        {"Just below 500", 499, false},
        {"Above 599", 600, true}, // >= 500 is retryable
        {"Zero", 0, false},
        {"Negative", -1, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := domain.IsRetryable(tt.statusCode)
            assert.Equal(t, tt.want, got)
        })
    }
}

func TestHashEndpoint_Consistency(t *testing.T) {
    // Test that hashing is consistent across multiple calls
    endpoint := "https://api.example.com/webhooks/receive"
    
    hashes := make([]domain.EndpointHash, 10)
    for i := 0; i < 10; i++ {
        hashes[i] = domain.HashEndpoint(endpoint)
    }
    
    // All hashes should be identical
    for i := 1; i < 10; i++ {
        assert.Equal(t, hashes[0], hashes[i], "Hash should be consistent")
    }
}

func TestHashEndpoint_Sensitivity(t *testing.T) {
    // Test that small changes produce different hashes
    tests := []struct {
        name string
        url1 string
        url2 string
    }{
        {"HTTPS vs HTTP", "https://example.com", "http://example.com"},
        {"Trailing slash", "https://example.com/", "https://example.com"},
        {"Case sensitive", "https://Example.com", "https://example.com"},
        {"Different paths", "https://example.com/a", "https://example.com/b"},
        {"Query params", "https://example.com?a=1", "https://example.com?a=2"},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            hash1 := domain.HashEndpoint(tt.url1)
            hash2 := domain.HashEndpoint(tt.url2)
            assert.NotEqual(t, hash1, hash2, "Different URLs should have different hashes")
        })
    }
}

func TestWebhookID_String(t *testing.T) {
    id := domain.WebhookID("test-webhook-id-123")
    assert.Equal(t, "test-webhook-id-123", string(id))
}

func TestEndpointHash_String(t *testing.T) {
    hash := domain.EndpointHash("abc123hash")
    assert.Equal(t, "abc123hash", string(hash))
}

// --- NewWebhook ---

func TestNewWebhook_DefaultMaxAttempts(t *testing.T) {
    wh := domain.NewWebhook(domain.NewWebhookOpts{
        ConfigID:  "cfg-1",
        Endpoint:  "https://example.com/hook",
        Payload:   json.RawMessage(`{}`),
        // MaxAttempts omitted → should default to 20
    })

    require.NotNil(t, wh)
    assert.Equal(t, 20, wh.MaxAttempts)
}

func TestNewWebhook_ExplicitMaxAttempts(t *testing.T) {
    wh := domain.NewWebhook(domain.NewWebhookOpts{
        ConfigID:    "cfg-1",
        Endpoint:    "https://example.com/hook",
        Payload:     json.RawMessage(`{}`),
        MaxAttempts: 5,
    })

    assert.Equal(t, 5, wh.MaxAttempts)
}

func TestNewWebhook_ZeroMaxAttemptsDefaultsTo20(t *testing.T) {
    wh := domain.NewWebhook(domain.NewWebhookOpts{
        ConfigID:    "cfg-1",
        Endpoint:    "https://example.com/hook",
        Payload:     json.RawMessage(`{}`),
        MaxAttempts: 0, // explicit zero → default
    })

    assert.Equal(t, 20, wh.MaxAttempts)
}

func TestNewWebhook_IDPrefixAndUniqueness(t *testing.T) {
    wh1 := domain.NewWebhook(domain.NewWebhookOpts{Endpoint: "https://a.com"})
    wh2 := domain.NewWebhook(domain.NewWebhookOpts{Endpoint: "https://a.com"})

    assert.True(t, strings.HasPrefix(string(wh1.ID), "wh_"), "ID must start with wh_")
    assert.NotEqual(t, wh1.ID, wh2.ID, "Two consecutive NewWebhook calls must produce unique IDs")
}

func TestNewWebhook_EndpointHashPopulated(t *testing.T) {
    endpoint := "https://example.com/hook"
    wh := domain.NewWebhook(domain.NewWebhookOpts{Endpoint: endpoint})

    assert.Equal(t, domain.HashEndpoint(endpoint), wh.EndpointHash)
}

func TestNewWebhook_AttemptIsZero(t *testing.T) {
    wh := domain.NewWebhook(domain.NewWebhookOpts{Endpoint: "https://example.com"})
    assert.Equal(t, 0, wh.Attempt)
}

func TestNewWebhook_TimestampsAreRecent(t *testing.T) {
    before := time.Now()
    wh := domain.NewWebhook(domain.NewWebhookOpts{Endpoint: "https://example.com"})
    after := time.Now()

    assert.True(t, !wh.CreatedAt.Before(before) && !wh.CreatedAt.After(after),
        "CreatedAt should be between before and after")
    assert.Equal(t, wh.CreatedAt, wh.ScheduledAt,
        "CreatedAt and ScheduledAt should be equal on construction")
}

func TestNewWebhook_OptionalFieldsPreserved(t *testing.T) {
    headers := map[string]string{"X-Custom": "value"}
    wh := domain.NewWebhook(domain.NewWebhookOpts{
        ConfigID:       "cfg-abc",
        Endpoint:       "https://example.com",
        Payload:        json.RawMessage(`{"event":"test"}`),
        Headers:        headers,
        IdempotencyKey: "idem-key-123",
        SigningSecret:  "s3cr3t",
        ScriptHash:     "deadbeef",
    })

    assert.Equal(t, domain.ConfigID("cfg-abc"), wh.ConfigID)
    assert.Equal(t, headers, wh.Headers)
    assert.Equal(t, "idem-key-123", wh.IdempotencyKey)
    assert.Equal(t, "s3cr3t", wh.SigningSecret)
    assert.Equal(t, "deadbeef", wh.ScriptHash)
}

// --- MatchesDomain ---

func TestMatchesDomain_WildcardAll(t *testing.T) {
    assert.True(t, domain.MatchesDomain("any.host.example.com", "*"))
    assert.True(t, domain.MatchesDomain("localhost", "*"))
}

func TestMatchesDomain_ExactMatch(t *testing.T) {
    assert.True(t, domain.MatchesDomain("api.example.com", "api.example.com"))
    assert.False(t, domain.MatchesDomain("api.example.com", "other.example.com"))
}

func TestMatchesDomain_ExactMatchCaseInsensitive(t *testing.T) {
    assert.True(t, domain.MatchesDomain("API.EXAMPLE.COM", "api.example.com"))
    assert.True(t, domain.MatchesDomain("api.example.com", "API.EXAMPLE.COM"))
}

func TestMatchesDomain_WildcardSubdomain(t *testing.T) {
    assert.True(t, domain.MatchesDomain("foo.example.com", "*.example.com"))
    assert.True(t, domain.MatchesDomain("bar.example.com", "*.example.com"))
    // Deep subdomain should also match
    assert.True(t, domain.MatchesDomain("deep.sub.example.com", "*.example.com"))
}

func TestMatchesDomain_WildcardSubdomainNoMatch(t *testing.T) {
    assert.False(t, domain.MatchesDomain("other.com", "*.example.com"))
    assert.False(t, domain.MatchesDomain("notexample.com", "*.example.com"))
}

func TestMatchesDomain_WildcardSubdomainCaseInsensitive(t *testing.T) {
    assert.True(t, domain.MatchesDomain("FOO.EXAMPLE.COM", "*.example.com"))
    assert.True(t, domain.MatchesDomain("foo.example.com", "*.EXAMPLE.COM"))
}
