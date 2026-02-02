//go:build !integration

package worker

import (
	"context"

	"github.com/howk/howk/internal/broker"
)

// ProcessMessage exports the internal processMessage for testing
// This is only available in test builds (when integration tag is not set)
func (w *Worker) ProcessMessage(ctx context.Context, msg *broker.Message) error {
	return w.processMessage(ctx, msg)
}
