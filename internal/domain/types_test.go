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
