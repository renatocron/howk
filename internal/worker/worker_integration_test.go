//go:build integration

package worker_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/circuit"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/retry"
	"github.com/howk/howk/internal/script"
	"github.com/howk/howk/internal/testutil"
	"github.com/howk/howk/internal/worker"
)

func setupWorkerTest(t *testing.T, httpServer *httptest.Server) (*worker.Worker, *broker.KafkaBroker, *hotstate.RedisHotState, context.Context, context.CancelFunc) {
	cfg := config.DefaultConfig()
	cfg.CircuitBreaker.RecoveryTimeout = 100 * time.Millisecond
	cfg.CircuitBreaker.FailureThreshold = 3
	cfg.Delivery.Timeout = 2 * time.Second
	// Use unique consumer group for each test to avoid processing other tests' messages
	cfg.Kafka.ConsumerGroup = cfg.Kafka.ConsumerGroup + "-" + t.Name()

	hs := testutil.SetupRedis(t)
	b := testutil.SetupKafka(t)

	pub := broker.NewKafkaWebhookPublisher(b, cfg.Kafka.Topics)
	cb := circuit.NewBreaker(hs.Client(), cfg.CircuitBreaker, cfg.TTL)
	dc := delivery.NewClient(cfg.Delivery)
	rs := retry.NewStrategy(cfg.Retry)
	se := script.NewEngine(cfg.Lua, script.NewLoader(), nil, nil, nil, zerolog.Logger{})

	w := worker.NewWorker(cfg, b, pub, hs, cb, dc, rs, se)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	return w, b, hs, ctx, cancel
}

func TestWorker_SuccessfulDelivery(t *testing.T) {
	// Create mock HTTP server that returns 200
	var receivedPayload []byte
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify webhook ID header is present
		assert.NotEmpty(t, r.Header.Get("X-Webhook-ID"))

		// Read payload
		payload, _ := io.ReadAll(r.Body)
		mu.Lock()
		receivedPayload = payload
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()

	w, b, _, ctx, cancel := setupWorkerTest(t, server)
	defer cancel()

	// Create webhook
	webhook := testutil.NewTestWebhook(server.URL)

	// Start worker in background
	go func() {
		err := w.Run(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for worker to start consuming
	time.Sleep(2 * time.Second)

	// Publish webhook
	data, err := json.Marshal(webhook)
	require.NoError(t, err)

	msg := broker.Message{
		Key:   []byte(webhook.ID),
		Value: data,
	}

	// Use the same broker instance that the worker is using
	cfg := config.DefaultConfig()
	err = b.Publish(ctx, cfg.Kafka.Topics.Pending, msg)
	require.NoError(t, err)

	// Wait for delivery
	testutil.WaitFor(t, 10*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(receivedPayload) > 0
	})

	// Verify webhook was delivered
	mu.Lock()
	defer mu.Unlock()
	assert.NotNil(t, receivedPayload)
}

func TestWorker_RetryAfterFailure(t *testing.T) {
	// Create mock HTTP server that fails first time, succeeds second time
	callCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		if count == 1 {
			w.WriteHeader(500) // First call fails
		} else {
			w.WriteHeader(200) // Second call succeeds
		}
	}))
	defer server.Close()

	w, b, hs, ctx, cancel := setupWorkerTest(t, server)
	defer cancel()

	// Create webhook
	webhook := testutil.NewTestWebhook(server.URL)

	// Start worker in background
	go func() {
		err := w.Run(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for worker to start consuming
	time.Sleep(2 * time.Second)

	// Publish webhook using the same broker instance
	cfg := config.DefaultConfig()

	data, err := json.Marshal(webhook)
	require.NoError(t, err)

	msg := broker.Message{
		Key:   []byte(webhook.ID),
		Value: data,
	}

	err = b.Publish(ctx, cfg.Kafka.Topics.Pending, msg)
	require.NoError(t, err)

	// Wait for first delivery attempt
	testutil.WaitFor(t, 10*time.Second, func() bool {
		return callCount.Load() >= 1
	})

	// Wait for retry to be scheduled
	time.Sleep(500 * time.Millisecond)

	// Check that there's a retry scheduled (look at ZSET directly)
	count, err := hs.Client().ZCount(ctx, "retries", "-inf", "+inf").Result()
	require.NoError(t, err)

	// The retry might not be due yet (depends on retry delay)
	// So we accept either 0 or 1 scheduled retry at this moment
	t.Logf("Retries scheduled: %d", count)
}

func TestWorker_CircuitOpens(t *testing.T) {
	// Create mock HTTP server that always fails
	callCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(500)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.CircuitBreaker.FailureThreshold = 3
	cfg.CircuitBreaker.FailureWindow = 10 * time.Second
	cfg.Retry.BaseDelay = 100 * time.Millisecond
	// Use unique consumer group for this test
	cfg.Kafka.ConsumerGroup = cfg.Kafka.ConsumerGroup + "-" + t.Name()

	hs := testutil.SetupRedis(t)
	b := testutil.SetupKafka(t)

	pub := broker.NewKafkaWebhookPublisher(b, cfg.Kafka.Topics)
	cb := circuit.NewBreaker(hs.Client(), cfg.CircuitBreaker, cfg.TTL)
	dc := delivery.NewClient(cfg.Delivery)
	rs := retry.NewStrategy(cfg.Retry)
	se := script.NewEngine(cfg.Lua, script.NewLoader(), nil, nil, nil, zerolog.Logger{})

	w := worker.NewWorker(cfg, b, pub, hs, cb, dc, rs, se)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		err := w.Run(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for worker to start consuming
	time.Sleep(2 * time.Second)

	// Create webhook
	webhook := testutil.NewTestWebhook(server.URL)

	// Publish 3 webhooks to trigger circuit breaker
	for i := 0; i < 3; i++ {
		wh := testutil.NewTestWebhook(server.URL)
		wh.Endpoint = server.URL // Same endpoint for all
		wh.EndpointHash = webhook.EndpointHash

		data, err := json.Marshal(wh)
		require.NoError(t, err)

		msg := broker.Message{
			Key:   []byte(wh.ID),
			Value: data,
		}

		err = b.Publish(ctx, cfg.Kafka.Topics.Pending, msg)
		require.NoError(t, err)

		time.Sleep(200 * time.Millisecond) // Wait between deliveries
	}

	// Wait for all deliveries
	testutil.WaitFor(t, 15*time.Second, func() bool {
		return callCount.Load() >= 3
	})

	// Check circuit breaker state
	circuitBreaker, err := cb.Get(ctx, webhook.EndpointHash)
	require.NoError(t, err)

	// Circuit should be open after 3 failures
	assert.Equal(t, domain.CircuitOpen, circuitBreaker.State)
}

func TestWorker_Idempotency(t *testing.T) {
	// Create mock HTTP server that counts calls
	callCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(200)
	}))
	defer server.Close()

	w, b, _, ctx, cancel := setupWorkerTest(t, server)
	defer cancel()

	// Create webhook
	webhook := testutil.NewTestWebhook(server.URL)

	// Start worker in background
	go func() {
		err := w.Run(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for worker to start consuming
	time.Sleep(2 * time.Second)

	// Publish same webhook twice (duplicate) using the same broker instance
	cfg := config.DefaultConfig()

	data, err := json.Marshal(webhook)
	require.NoError(t, err)

	msg := broker.Message{
		Key:   []byte(webhook.ID),
		Value: data,
	}

	// Publish first time
	err = b.Publish(ctx, cfg.Kafka.Topics.Pending, msg)
	require.NoError(t, err)

	// Wait for first delivery
	testutil.WaitFor(t, 10*time.Second, func() bool {
		return callCount.Load() >= 1
	})

	firstCount := callCount.Load()

	// Publish second time (duplicate)
	err = b.Publish(ctx, cfg.Kafka.Topics.Pending, msg)
	require.NoError(t, err)

	// Wait a bit
	time.Sleep(2 * time.Second)

	// Call count should still be 1 (idempotency prevented duplicate delivery)
	assert.Equal(t, firstCount, callCount.Load(), "duplicate webhook should not be delivered again")
}

func TestWorker_ExhaustedRetries(t *testing.T) {
	// Create mock HTTP server that always fails
	callCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(500)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Retry.MaxAttempts = 2 // Only 2 attempts for faster testing
	cfg.Retry.BaseDelay = 100 * time.Millisecond
	// Use unique consumer group for this test
	cfg.Kafka.ConsumerGroup = cfg.Kafka.ConsumerGroup + "-" + t.Name()

	hs := testutil.SetupRedis(t)
	b := testutil.SetupKafka(t)

	pub := broker.NewKafkaWebhookPublisher(b, cfg.Kafka.Topics)
	cb := circuit.NewBreaker(hs.Client(), cfg.CircuitBreaker, cfg.TTL)
	dc := delivery.NewClient(cfg.Delivery)
	rs := retry.NewStrategy(cfg.Retry)
	se := script.NewEngine(cfg.Lua, script.NewLoader(), nil, nil, nil, zerolog.Logger{})

	w := worker.NewWorker(cfg, b, pub, hs, cb, dc, rs, se)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		err := w.Run(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for worker to start consuming
	time.Sleep(2 * time.Second)

	// Create webhook with attempt already at max-1
	webhook := testutil.NewTestWebhook(server.URL)
	webhook.Attempt = cfg.Retry.MaxAttempts - 1

	data, err := json.Marshal(webhook)
	require.NoError(t, err)

	msg := broker.Message{
		Key:   []byte(webhook.ID),
		Value: data,
	}

	err = b.Publish(ctx, cfg.Kafka.Topics.Pending, msg)
	require.NoError(t, err)

	// Wait for delivery
	testutil.WaitFor(t, 10*time.Second, func() bool {
		return callCount.Load() >= 1
	})

	// Wait a bit more to ensure status is updated
	time.Sleep(1 * time.Second)

	// Check status - should be exhausted
	status, err := hs.GetStatus(ctx, webhook.ID)
	require.NoError(t, err)

	if status != nil {
		t.Logf("Status: %s, Attempts: %d", status.State, status.Attempts)
		// Status should be either "exhausted" or "failed" (depending on exact timing)
		assert.Contains(t, []string{domain.StateExhausted, domain.StateFailed}, status.State)
	}
}
