package delivery_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeliver_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.NotEmpty(t, r.Header.Get("X-Webhook-ID"))
		assert.NotEmpty(t, r.Header.Get("X-Webhook-Timestamp"))
		assert.Equal(t, "1", r.Header.Get("X-Webhook-Attempt"))

		// Verify body
		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"test": true}`, string(body))

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := delivery.NewClient(config.DefaultConfig().Delivery)
	webhook := testutil.NewTestWebhook(server.URL)

	result := client.Deliver(context.Background(), webhook)

	assert.NoError(t, result.Error)
	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.Greater(t, result.Duration, time.Duration(0))
}

func TestDeliver_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deliveryConfig := config.DefaultConfig().Delivery
	deliveryConfig.Timeout = 50 * time.Millisecond
	client := delivery.NewClient(deliveryConfig)
	webhook := testutil.NewTestWebhook(server.URL)

	result := client.Deliver(context.Background(), webhook)

	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "context deadline exceeded")
}

func TestDeliver_NetworkError(t *testing.T) {
	client := delivery.NewClient(config.DefaultConfig().Delivery)
	webhook := testutil.NewTestWebhook("http://localhost:9999") // Unreachable

	result := client.Deliver(context.Background(), webhook)

	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "dial tcp")
}

func TestDeliver_SignatureGeneration(t *testing.T) {
	var capturedSig string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSig = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := delivery.NewClient(config.DefaultConfig().Delivery)
	webhook := testutil.NewTestWebhook(server.URL)
	webhook.SigningSecret = "my-secret"

	client.Deliver(context.Background(), webhook)

	require.NotEmpty(t, capturedSig)
	parts := strings.Split(capturedSig, ",")
	require.Len(t, parts, 2)
	assert.True(t, strings.HasPrefix(parts[0], "t="))
	assert.True(t, strings.HasPrefix(parts[1], "v1="))
}

func TestDeliver_CustomHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "my-value", r.Header.Get("X-Custom-Header"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := delivery.NewClient(config.DefaultConfig().Delivery)
	webhook := testutil.NewTestWebhook(server.URL)
	webhook.Headers = map[string]string{
		"X-Custom-Header": "my-value",
	}

	result := client.Deliver(context.Background(), webhook)

	assert.NoError(t, result.Error)
}

func TestDeliver_PayloadSent(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := delivery.NewClient(config.DefaultConfig().Delivery)
	webhook := testutil.NewTestWebhook(server.URL)
	webhook.Payload = []byte(`{"custom": "payload"}`)

	client.Deliver(context.Background(), webhook)

	assert.JSONEq(t, `{"custom": "payload"}`, string(capturedBody))
}

func TestClose(t *testing.T) {
	client := delivery.NewClient(config.DefaultConfig().Delivery)
	
	// Close should not panic
	assert.NotPanics(t, func() {
		client.Close()
	})
}

func TestClose_MultipleCalls(t *testing.T) {
	client := delivery.NewClient(config.DefaultConfig().Delivery)
	
	// Multiple Close calls should not panic
	assert.NotPanics(t, func() {
		client.Close()
		client.Close()
		client.Close()
	})
}

func TestNewClient_WithCustomConfig(t *testing.T) {
	cfg := config.DeliveryConfig{
		Timeout:             30 * time.Second,
		MaxIdleConns:        50,
		MaxConnsPerHost:     20,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		UserAgent:           "custom-agent/1.0",
	}
	
	client := delivery.NewClient(cfg)
	assert.NotNil(t, client)
	
	// Close to clean up
	client.Close()
}

func TestDeliver_InvalidURL(t *testing.T) {
	client := delivery.NewClient(config.DefaultConfig().Delivery)
	defer client.Close()
	
	webhook := testutil.NewTestWebhook("://invalid-url")
	
	result := client.Deliver(context.Background(), webhook)
	
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "build request")
}

func TestDeliver_ResponseBodyTruncation(t *testing.T) {
	// Server returns a large response body
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write more than maxResponseBody (1024 bytes)
		largeBody := make([]byte, 2000)
		for i := range largeBody {
			largeBody[i] = 'x'
		}
		w.Write(largeBody)
	}))
	defer server.Close()
	
	client := delivery.NewClient(config.DefaultConfig().Delivery)
	defer client.Close()
	
	webhook := testutil.NewTestWebhook(server.URL)
	result := client.Deliver(context.Background(), webhook)
	
	assert.NoError(t, result.Error)
	assert.Equal(t, http.StatusOK, result.StatusCode)
	// Response body should be truncated to ~1024 bytes
	assert.LessOrEqual(t, len(result.ResponseBody), 1100)
}

func TestDeliver_NoSignatureWithoutSecret(t *testing.T) {
	var capturedSig string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSig = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	
	client := delivery.NewClient(config.DefaultConfig().Delivery)
	defer client.Close()
	
	webhook := testutil.NewTestWebhook(server.URL)
	webhook.SigningSecret = "" // No secret
	
	client.Deliver(context.Background(), webhook)
	
	assert.Empty(t, capturedSig)
}

func TestDeliver_RedirectNotFollowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/final")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	
	client := delivery.NewClient(config.DefaultConfig().Delivery)
	defer client.Close()
	
	webhook := testutil.NewTestWebhook(server.URL)
	result := client.Deliver(context.Background(), webhook)
	
	// Should NOT follow redirect, should get 302
	assert.NoError(t, result.Error)
	assert.Equal(t, http.StatusFound, result.StatusCode)
}

func TestDeliver_AttemptHeader(t *testing.T) {
	var capturedAttempt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAttempt = r.Header.Get("X-Webhook-Attempt")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	
	client := delivery.NewClient(config.DefaultConfig().Delivery)
	defer client.Close()
	
	webhook := testutil.NewTestWebhook(server.URL)
	webhook.Attempt = 2 // Third attempt (0-indexed + 1)
	
	client.Deliver(context.Background(), webhook)
	
	assert.Equal(t, "3", capturedAttempt)
}
