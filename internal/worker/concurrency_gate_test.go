//go:build !integration

package worker_test

// Tests for the runConcurrencyGate helper.
//
// runConcurrencyGate encapsulates the acquire→divert-or-NACK pattern used by
// both the domain limiter and the inflight counter gates.  These tests exercise
// it in isolation through the exported RunConcurrencyGate test helper to verify
// each branch without needing Kafka or Redis infrastructure.

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/mocks"
	"github.com/howk/howk/internal/script"
	"github.com/howk/howk/internal/worker"
)

// newMinimalWorker creates a Worker with only the dependencies required to call
// RunConcurrencyGate (publisher is needed for PublishToSlow).
func newMinimalWorker(t *testing.T, pub *MockPublisher) *worker.Worker {
	t.Helper()
	cfg := config.DefaultConfig()
	mockBroker := new(MockBroker)
	mockHotState := new(MockHotState)
	mockCircuitBreaker := new(mocks.MockCircuitBreaker)
	mockHotState.On("CircuitBreaker").Return(mockCircuitBreaker)

	loader := script.NewLoader()
	engine := script.NewEngine(config.LuaConfig{Enabled: false}, loader, nil, nil, nil, zerolog.Logger{})

	return worker.NewWorker(
		cfg,
		mockBroker,
		pub,
		mockHotState,
		&MockDeliveryClient{},
		&MockRetryStrategy{},
		engine,
		nil,
	)
}

func minimalWebhook() *domain.Webhook {
	return &domain.Webhook{
		ID:           domain.WebhookID("wh-gate-test"),
		Endpoint:     "https://example.com/hook",
		EndpointHash: domain.EndpointHash("hashXYZ"),
	}
}

// TestRunConcurrencyGate_InactiveGate verifies that an inactive gate is a no-op.
func TestRunConcurrencyGate_InactiveGate(t *testing.T) {
	pub := new(MockPublisher)
	w := newMinimalWorker(t, pub)

	acquired, err := w.RunConcurrencyGate(context.Background(), minimalWebhook(), false, worker.ConcurrencyGate{
		Active: false,
	})

	assert.NoError(t, err)
	assert.False(t, acquired, "inactive gate should never report acquired")
	pub.AssertNotCalled(t, "PublishToSlow", mock.Anything, mock.Anything)
}

// TestRunConcurrencyGate_AcquireSuccess verifies that a successful acquire
// returns acquired=true with no error.
func TestRunConcurrencyGate_AcquireSuccess(t *testing.T) {
	pub := new(MockPublisher)
	w := newMinimalWorker(t, pub)

	acquired, err := w.RunConcurrencyGate(context.Background(), minimalWebhook(), false, worker.ConcurrencyGate{
		Active: true,
		TryAcquire: func() (bool, error) {
			return true, nil // slot available
		},
		Release: func() error { return nil },
		NackErr: errors.New("saturated"),
	})

	require.NoError(t, err)
	assert.True(t, acquired)
}

// TestRunConcurrencyGate_InfraError verifies fail-open: on acquire error the
// gate returns (false, nil) so the caller proceeds without holding a slot.
func TestRunConcurrencyGate_InfraError(t *testing.T) {
	pub := new(MockPublisher)
	w := newMinimalWorker(t, pub)

	acquired, err := w.RunConcurrencyGate(context.Background(), minimalWebhook(), false, worker.ConcurrencyGate{
		Active: true,
		TryAcquire: func() (bool, error) {
			return false, errors.New("redis down")
		},
		NackErr: errors.New("saturated"),
	})

	require.NoError(t, err, "infra errors should be fail-open (no error returned to caller)")
	assert.False(t, acquired)
}

// TestRunConcurrencyGate_DivertSuccess verifies that when at capacity in the
// fast lane and divert succeeds, errDiverted is returned.
func TestRunConcurrencyGate_DivertSuccess(t *testing.T) {
	pub := new(MockPublisher)
	pub.On("PublishToSlow", mock.Anything, mock.Anything).Return(nil)
	w := newMinimalWorker(t, pub)

	acquired, err := w.RunConcurrencyGate(context.Background(), minimalWebhook(), false /* fast lane */, worker.ConcurrencyGate{
		Active: true,
		TryAcquire: func() (bool, error) {
			return false, nil // at capacity
		},
		NackErr: errors.New("saturated"),
	})

	assert.False(t, acquired)
	assert.Equal(t, worker.ErrDiverted, err, "divert success must return the ErrDiverted sentinel")

	pub.AssertExpectations(t)
}

// TestRunConcurrencyGate_NACKInSlowLane verifies that when at capacity in the
// slow lane, the nackErr is returned directly (no divert attempted).
func TestRunConcurrencyGate_NACKInSlowLane(t *testing.T) {
	pub := new(MockPublisher)
	nack := errors.New("endpoint saturated in slow lane")
	w := newMinimalWorker(t, pub)

	acquired, err := w.RunConcurrencyGate(context.Background(), minimalWebhook(), true /* slow lane */, worker.ConcurrencyGate{
		Active: true,
		TryAcquire: func() (bool, error) {
			return false, nil // at capacity
		},
		NackErr: nack,
	})

	assert.False(t, acquired)
	assert.Equal(t, nack, err, "nackErr must be propagated to trigger Kafka NACK")
	pub.AssertNotCalled(t, "PublishToSlow", mock.Anything, mock.Anything)
}

// TestRunConcurrencyGate_DivertFailWithReacquire verifies the fail-open path
// when divert fails and a reacquireOnDivertFail func is provided: the gate
// returns (true, nil) so the deferred release remains balanced.
func TestRunConcurrencyGate_DivertFailWithReacquire(t *testing.T) {
	pub := new(MockPublisher)
	pub.On("PublishToSlow", mock.Anything, mock.Anything).Return(errors.New("kafka down"))
	w := newMinimalWorker(t, pub)

	reacquireCalled := false

	acquired, err := w.RunConcurrencyGate(context.Background(), minimalWebhook(), false, worker.ConcurrencyGate{
		Active: true,
		TryAcquire: func() (bool, error) {
			return false, nil // at capacity
		},
		ReacquireOnDivertFail: func() {
			reacquireCalled = true
		},
		NackErr: errors.New("saturated"),
	})

	require.NoError(t, err, "divert failure with reacquire should be fail-open")
	assert.True(t, acquired, "slot should be reacquired after divert failure")
	assert.True(t, reacquireCalled, "reacquireOnDivertFail must be called")

	pub.AssertExpectations(t)
}

// TestRunConcurrencyGate_DivertFailNoReacquire verifies that when divert fails
// and no reacquireOnDivertFail is provided, the gate returns (false, nil) so
// the caller proceeds without holding a slot (domain limiter semantics).
func TestRunConcurrencyGate_DivertFailNoReacquire(t *testing.T) {
	pub := new(MockPublisher)
	pub.On("PublishToSlow", mock.Anything, mock.Anything).Return(errors.New("kafka down"))
	w := newMinimalWorker(t, pub)

	acquired, err := w.RunConcurrencyGate(context.Background(), minimalWebhook(), false, worker.ConcurrencyGate{
		Active: true,
		TryAcquire: func() (bool, error) {
			return false, nil // at capacity
		},
		ReacquireOnDivertFail: nil, // domain gate: no re-acquire
		NackErr:               errors.New("saturated"),
	})

	require.NoError(t, err, "divert failure without reacquire should be fail-open")
	assert.False(t, acquired)

	pub.AssertExpectations(t)
}
