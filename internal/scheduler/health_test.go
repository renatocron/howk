//go:build !integration

package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/howk/howk/internal/domain"
)

// TestCheckSystemHealth_NilEpoch verifies that a nil epoch (Redis flushed
// or not yet reconciled) is treated as a warning, not a fatal error.
func TestCheckSystemHealth_NilEpoch(t *testing.T) {
	scheduler, mockHotState, _ := setupSchedulerTest()

	mockHotState.On("GetEpoch", context.Background()).Return(nil, nil)

	err := scheduler.checkSystemHealth(context.Background())

	assert.NoError(t, err, "nil epoch is a warning, not an error")
	mockHotState.AssertExpectations(t)
}

// TestCheckSystemHealth_OldEpoch verifies that an epoch older than 24 hours
// is logged as a warning but does not return an error.
func TestCheckSystemHealth_OldEpoch(t *testing.T) {
	scheduler, mockHotState, _ := setupSchedulerTest()

	// An epoch completed more than 24 h ago is "old".
	oldEpoch := &domain.SystemEpoch{
		Epoch:            time.Now().Add(-25 * time.Hour).Unix(),
		ReconcilerHost:   "host-1",
		MessagesReplayed: 100,
		CompletedAt:      time.Now().Add(-25 * time.Hour),
	}

	mockHotState.On("GetEpoch", context.Background()).Return(oldEpoch, nil)

	err := scheduler.checkSystemHealth(context.Background())

	assert.NoError(t, err, "old epoch is a warning, not an error")
	mockHotState.AssertExpectations(t)
}

// TestCheckSystemHealth_FreshEpoch verifies that a recent epoch is accepted
// silently and returns no error.
func TestCheckSystemHealth_FreshEpoch(t *testing.T) {
	scheduler, mockHotState, _ := setupSchedulerTest()

	freshEpoch := &domain.SystemEpoch{
		Epoch:            time.Now().Unix(),
		ReconcilerHost:   "host-1",
		MessagesReplayed: 42,
		CompletedAt:      time.Now().Add(-1 * time.Hour),
	}

	mockHotState.On("GetEpoch", context.Background()).Return(freshEpoch, nil)

	err := scheduler.checkSystemHealth(context.Background())

	assert.NoError(t, err)
	mockHotState.AssertExpectations(t)
}

// TestCheckSystemHealth_GetEpochError verifies that a Redis error is wrapped
// and returned to the caller so it can be logged as a warning.
func TestCheckSystemHealth_GetEpochError(t *testing.T) {
	scheduler, mockHotState, _ := setupSchedulerTest()

	mockHotState.On("GetEpoch", context.Background()).
		Return(nil, errors.New("redis: connection refused"))

	err := scheduler.checkSystemHealth(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redis: connection refused")
	mockHotState.AssertExpectations(t)
}
