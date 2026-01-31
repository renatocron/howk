package retry_test

import (
	"errors"
	"testing"
	"time"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/retry"
	"github.com/howk/howk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldRetry(t *testing.T) {
	strategy := retry.NewStrategy(config.DefaultConfig().Retry)

	t.Run("Exhausted", func(t *testing.T) {
		wh := testutil.NewTestWebhook("http://example.com")
		wh.Attempt = wh.MaxAttempts
		assert.False(t, strategy.ShouldRetry(wh, 500, nil))
	})

	t.Run("NetworkError", func(t *testing.T) {
		wh := testutil.NewTestWebhook("http://example.com")
		assert.True(t, strategy.ShouldRetry(wh, 0, errors.New("network error")))
	})

	t.Run("StatusCodes", func(t *testing.T) {
		tests := []struct {
			name       string
			statusCode int
			want       bool
		}{
			{"Retryable 500", 500, true},
			{"Retryable 429", 429, true},
			{"Non-retryable 404", 404, false},
			{"Success 200", 200, false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				wh := testutil.NewTestWebhook("http://example.com")
				got := strategy.ShouldRetry(wh, tt.statusCode, nil)
				assert.Equal(t, tt.want, got)
			})
		}
	})
}

func TestNextDelay(t *testing.T) {
	retryConfig := config.DefaultConfig().Retry
	strategy := retry.NewStrategy(retryConfig)

	t.Run("CircuitClosed", func(t *testing.T) {
		delay := strategy.NextDelay(2, domain.CircuitClosed)
		baseDelay := time.Duration(float64(retryConfig.BaseDelay) * 4) // 2^2
		minDelay := time.Duration(float64(baseDelay) * 0.8)
		maxDelay := time.Duration(float64(baseDelay) * 1.2)
		assert.GreaterOrEqual(t, delay, minDelay)
		assert.LessOrEqual(t, delay, maxDelay)
	})

	t.Run("CircuitOpen", func(t *testing.T) {
		delay := strategy.NextDelay(2, domain.CircuitOpen)
		minDelay := time.Duration(float64(5*time.Minute) * 0.8)
		maxDelay := time.Duration(float64(5*time.Minute) * 1.2)
		assert.GreaterOrEqual(t, delay, minDelay)
		assert.LessOrEqual(t, delay, maxDelay)
	})

	t.Run("CircuitHalfOpen", func(t *testing.T) {
		delay := strategy.NextDelay(2, domain.CircuitHalfOpen)
		minDelay := time.Duration(float64(30*time.Second) * 0.8)
		maxDelay := time.Duration(float64(30*time.Second) * 1.2)
		assert.GreaterOrEqual(t, delay, minDelay)
		assert.LessOrEqual(t, delay, maxDelay)
	})

	t.Run("MaxCap", func(t *testing.T) {
		retryConfig.MaxDelay = 1 * time.Second
		strategy := retry.NewStrategy(retryConfig)
		delay := strategy.NextDelay(15, domain.CircuitClosed) // high attempt to exceed cap
		assert.LessOrEqual(t, delay, time.Duration(float64(1*time.Second)*1.2))
	})
}

func TestAddJitter_Bounds(t *testing.T) {
	retryConfig := config.DefaultConfig().Retry
	retryConfig.Jitter = 0.2
    retryConfig.BaseDelay = 10 * time.Second
	strategy := retry.NewStrategy(retryConfig)

	baseDelay := 10 * time.Second
	minDelay := baseDelay - time.Duration(float64(baseDelay)*0.2)
	maxDelay := baseDelay + time.Duration(float64(baseDelay)*0.2)

	for i := 0; i < 100; i++ {
		// As `addJitter` is not exported, we test it through `NextDelay`
		delayWithJitter := strategy.NextDelay(0, domain.CircuitClosed)
		assert.True(t, delayWithJitter >= minDelay, "delay should be >= minDelay")
		assert.True(t, delayWithJitter <= maxDelay, "delay should be <= maxDelay")
	}
}

func TestIsExhausted(t *testing.T) {
	retryConfig := config.DefaultConfig().Retry
	strategy := retry.NewStrategy(retryConfig)

	assert.True(t, strategy.IsExhausted(retryConfig.MaxAttempts))
	assert.True(t, strategy.IsExhausted(retryConfig.MaxAttempts+1))
	assert.False(t, strategy.IsExhausted(retryConfig.MaxAttempts-1))
}

func TestRetrySchedule(t *testing.T) {
	retryConfig := config.DefaultConfig().Retry
	retryConfig.MaxAttempts = 5
	strategy := retry.NewStrategy(retryConfig)

	schedule := strategy.RetrySchedule()
	require.Len(t, schedule, 5)

	var previousDelay time.Duration
	for i, delayStr := range schedule {
		delay, err := time.ParseDuration(delayStr)
		require.NoError(t, err)

		// check that delay is increasing
		if i > 0 {
			assert.Greater(t, delay, previousDelay)
		}
		previousDelay = delay
	}
}
