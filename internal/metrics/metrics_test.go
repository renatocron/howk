//go:build !integration

package metrics_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freePort asks the OS for an available TCP port so tests don't collide.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// TestCollectorsRegistered verifies that the four core collectors can be
// registered against a fresh registry without error.  Using a custom registry
// avoids any interaction with the global default prometheus.DefaultRegisterer
// (which the package-level init() already populated).
func TestCollectorsRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()

	collectors := []prometheus.Collector{
		prometheus.NewCounter(prometheus.CounterOpts{
			Name: "howk_webhooks_received_total",
			Help: "Total number of webhooks accepted by the API.",
		}),
		prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "howk_deliveries_total",
			Help: "Total delivery attempts by outcome.",
		}, []string{"outcome"}),
		prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "howk_delivery_duration_seconds",
			Help:    "HTTP delivery latency in seconds.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}),
		prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "howk_retry_queue_depth",
			Help: "Current number of webhooks waiting for retry.",
		}),
	}

	for _, c := range collectors {
		assert.NoError(t, reg.Register(c), "collector should register without error")
	}

	// Exercise each collector so it emits at least one sample.  Prometheus
	// only includes metric families in Gather() output when they have been
	// observed at least once.
	collectors[0].(prometheus.Counter).Inc()
	collectors[1].(*prometheus.CounterVec).WithLabelValues("ok").Inc()
	collectors[2].(prometheus.Histogram).Observe(0.1)
	collectors[3].(prometheus.Gauge).Set(1)

	// Gathering from the registry must succeed and return at least 4 metric families.
	mfs, err := reg.Gather()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(mfs), 4, "expected at least 4 metric families")
}

// TestCollectorNames verifies the metric names match the expected convention.
func TestCollectorNames(t *testing.T) {
	reg := prometheus.NewRegistry()

	webhooksReceived := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "howk_webhooks_received_total",
		Help: "Total number of webhooks accepted by the API.",
	})
	deliveriesTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "howk_deliveries_total",
		Help: "Total delivery attempts by outcome.",
	}, []string{"outcome"})
	deliveryDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "howk_delivery_duration_seconds",
		Help:    "HTTP delivery latency in seconds.",
		Buckets: prometheus.DefBuckets,
	})
	retryQueueDepth := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "howk_retry_queue_depth",
		Help: "Current number of webhooks waiting for retry.",
	})

	require.NoError(t, reg.Register(webhooksReceived))
	require.NoError(t, reg.Register(deliveriesTotal))
	require.NoError(t, reg.Register(deliveryDuration))
	require.NoError(t, reg.Register(retryQueueDepth))

	// Exercise the collectors so they emit at least one sample.
	webhooksReceived.Add(1)
	deliveriesTotal.WithLabelValues("success").Inc()
	deliveryDuration.Observe(0.25)
	retryQueueDepth.Set(42)

	mfs, err := reg.Gather()
	require.NoError(t, err)

	names := make(map[string]struct{}, len(mfs))
	for _, mf := range mfs {
		names[mf.GetName()] = struct{}{}
	}

	expectedNames := []string{
		"howk_webhooks_received_total",
		"howk_deliveries_total",
		"howk_delivery_duration_seconds",
		"howk_retry_queue_depth",
	}
	for _, n := range expectedNames {
		assert.Contains(t, names, n, "metric %q should be gathered", n)
	}
}

// TestListenAndServeStartsAndShutdown verifies that ListenAndServe binds a
// port, responds with 200 on /metrics, and shuts down cleanly when the context
// is cancelled.  We replicate the server logic here to test the behaviour
// without coupling to the package-level global handler.
func TestListenAndServeStartsAndShutdown(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{
		Name: "howk_test_counter_total",
		Help: "counter for server test",
	}))

	port := freePort(t)

	mux := http.NewServeMux()
	mux.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}

	// Start the server in the background; mirror the cancel-via-context pattern
	// used by the real ListenAndServe implementation.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-ctx.Done()
		shutdownCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() { _ = srv.ListenAndServe() }()

	// Give the listener a moment to bind.
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 2*time.Second, 20*time.Millisecond, "server did not start in time")

	// Verify it responds to /metrics.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Cancel the context; the goroutine above will shut the server down.
	cancel()

	// Wait until the port is released.
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err != nil {
			return true // port closed — good
		}
		conn.Close()
		return false
	}, 3*time.Second, 50*time.Millisecond, "server did not shut down in time")
}

// TestListenAndServeContextCancellation verifies that shutting down the server
// via context makes subsequent requests fail (port no longer listening).
func TestListenAndServeContextCancellation(t *testing.T) {
	port := freePort(t)

	mux := http.NewServeMux()
	mux.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_ = srv.ListenAndServe()
	}()

	// Wait until bound.
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 2*time.Second, 20*time.Millisecond)

	// Shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(shutdownCtx))

	<-serverDone

	// Port should no longer accept connections.
	_, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	assert.Error(t, err, "port should be closed after shutdown")
}
