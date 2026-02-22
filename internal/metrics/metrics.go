package metrics

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
)

var (
	// WebhooksReceived counts webhooks accepted by the API.
	WebhooksReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "howk_webhooks_received_total",
		Help: "Total number of webhooks accepted by the API.",
	})

	// DeliveriesTotal counts delivery outcomes by result.
	DeliveriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "howk_deliveries_total",
		Help: "Total delivery attempts by outcome.",
	}, []string{"outcome"})

	// DeliveryDuration observes HTTP delivery latency.
	DeliveryDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "howk_delivery_duration_seconds",
		Help:    "HTTP delivery latency in seconds.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	})

	// RetryQueueDepth reports the current retry backlog size.
	RetryQueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "howk_retry_queue_depth",
		Help: "Current number of webhooks waiting for retry.",
	})
)

func init() {
	prometheus.MustRegister(WebhooksReceived)
	prometheus.MustRegister(DeliveriesTotal)
	prometheus.MustRegister(DeliveryDuration)
	prometheus.MustRegister(RetryQueueDepth)
}

// ListenAndServe starts a Prometheus /metrics HTTP server on the given port.
// It shuts down gracefully when ctx is cancelled.
func ListenAndServe(ctx context.Context, port int) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Info().Int("port", port).Msg("Metrics server starting")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error().Err(err).Msg("Metrics server error")
	}
}
