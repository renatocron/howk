//go:build !integration

package worker

import (
	"context"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/domain"
)

// ProcessMessage exports the internal processMessage for testing
// This is only available in test builds (when integration tag is not set)
// Defaults to fast lane (isSlowLane=false).
func (w *Worker) ProcessMessage(ctx context.Context, msg *broker.Message) error {
	return w.processMessage(ctx, msg, false)
}

// RunConcurrencyGate exports the private runConcurrencyGate for unit testing.
// Using a capitalised alias keeps the internal concurrencyGate type unexported
// while allowing tests to exercise the helper in isolation.
type ConcurrencyGate = concurrencyGate

func (w *Worker) RunConcurrencyGate(
	ctx context.Context,
	webhook *domain.Webhook,
	isSlowLane bool,
	gate ConcurrencyGate,
) (acquired bool, err error) {
	return w.runConcurrencyGate(ctx, webhook, isSlowLane, gate)
}
