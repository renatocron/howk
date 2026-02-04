package domain_test

import (
    "testing"

    "github.com/howk/howk/internal/domain"
    "github.com/stretchr/testify/assert"
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
