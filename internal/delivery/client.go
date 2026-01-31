package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

const (
	maxResponseBody = 1024 // Only read first 1KB of response for logging
)

// Deliverer abstracts the Deliver method
type Deliverer interface {
	Deliver(ctx context.Context, webhook *domain.Webhook) *Result
}

// Client delivers webhooks via HTTP
type Client struct {
	httpClient *http.Client
	config     config.DeliveryConfig
}

// Result contains the delivery result
type Result struct {
	StatusCode   int
	ResponseBody string
	Duration     time.Duration
	Error        error
}

// NewClient creates a new delivery client with connection pooling
func NewClient(cfg config.DeliveryConfig) *Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxConnsPerHost,
		MaxConnsPerHost:       cfg.MaxConnsPerHost,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
			// Don't follow redirects - the webhook endpoint should handle this
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		config: cfg,
	}
}

// Deliver sends a webhook to its endpoint
func (c *Client) Deliver(ctx context.Context, webhook *domain.Webhook) *Result {
	start := time.Now()

	// Build request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook.Endpoint, bytes.NewReader(webhook.Payload))
	if err != nil {
		return &Result{
			Error:    fmt.Errorf("build request: %w", err),
			Duration: time.Since(start),
		}
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.config.UserAgent)
	req.Header.Set("X-Webhook-ID", string(webhook.ID))
	req.Header.Set("X-Webhook-Attempt", strconv.Itoa(webhook.Attempt+1))
	req.Header.Set("X-Webhook-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))

	// Custom headers from webhook
	for k, v := range webhook.Headers {
		req.Header.Set(k, v)
	}

	// Sign payload if secret provided
	if webhook.SigningSecret != "" {
		signature := c.sign(webhook.Payload, webhook.SigningSecret, time.Now().Unix())
		req.Header.Set("X-Webhook-Signature", signature)
	}

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Result{
			Error:    fmt.Errorf("execute request: %w", err),
			Duration: time.Since(start),
		}
	}
	defer resp.Body.Close()

	// Read limited response body
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))

	return &Result{
		StatusCode:   resp.StatusCode,
		ResponseBody: string(bodyBytes),
		Duration:     time.Since(start),
	}
}

// sign creates an HMAC-SHA256 signature
// Format: t=<timestamp>,v1=<signature>
func (c *Client) sign(payload []byte, secret string, timestamp int64) string {
	// Create signed payload: timestamp.payload
	signedPayload := fmt.Sprintf("%d.%s", timestamp, string(payload))

	// Compute HMAC-SHA256
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(signedPayload))
	signature := hex.EncodeToString(h.Sum(nil))

	return fmt.Sprintf("t=%d,v1=%s", timestamp, signature)
}

// Close releases resources
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}
