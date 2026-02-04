package retry_test

import (
	"errors"
	"fmt"
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

func TestNextRetryAt(t *testing.T) {
	retryConfig := config.DefaultConfig().Retry
	retryConfig.Jitter = 0 // No jitter for predictable results
	strategy := retry.NewStrategy(retryConfig)

	now := time.Now()
	attempt := 2
	circuitState := domain.CircuitClosed

	retryAt := strategy.NextRetryAt(attempt, circuitState)

	// Should be in the future
	assert.True(t, retryAt.After(now), "NextRetryAt should return a future time")

	// Should be approximately baseDelay * 2^attempt from now (with no jitter)
	expectedDelay := strategy.NextDelay(attempt, circuitState)
	expectedTime := now.Add(expectedDelay)
	diff := retryAt.Sub(expectedTime)
	if diff < 0 {
		diff = -diff
	}
	assert.Less(t, diff, 100*time.Millisecond, "NextRetryAt should be consistent with NextDelay")
}

func TestNextRetryAt_DifferentCircuitStates(t *testing.T) {
	retryConfig := config.DefaultConfig().Retry
	retryConfig.Jitter = 0 // No jitter for predictable results
	strategy := retry.NewStrategy(retryConfig)

	now := time.Now()

	tests := []struct {
		name         string
		circuitState domain.CircuitState
		minDuration  time.Duration
		maxDuration  time.Duration
	}{
		{
			name:         "CircuitClosed",
			circuitState: domain.CircuitClosed,
			minDuration:  retryConfig.BaseDelay,
			maxDuration:  retryConfig.BaseDelay * 2, // 1s with no jitter
		},
		{
			name:         "CircuitOpen",
			circuitState: domain.CircuitOpen,
			minDuration:  5 * time.Minute,
			maxDuration:  5*time.Minute + 10*time.Millisecond, // Allow some tolerance for time.Now() overhead
		},
		{
			name:         "CircuitHalfOpen",
			circuitState: domain.CircuitHalfOpen,
			minDuration:  30 * time.Second,
			maxDuration:  30*time.Second + 10*time.Millisecond, // Allow some tolerance
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retryAt := strategy.NextRetryAt(0, tt.circuitState)
			duration := retryAt.Sub(now)
			assert.GreaterOrEqual(t, duration, tt.minDuration)
			assert.LessOrEqual(t, duration, tt.maxDuration)
		})
	}
}

func TestMaxAttempts(t *testing.T) {
	tests := []struct {
		name        string
		maxAttempts int
	}{
		{"Default", 3},
		{"High", 10},
		{"Low", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retryConfig := config.DefaultConfig().Retry
			retryConfig.MaxAttempts = tt.maxAttempts
			strategy := retry.NewStrategy(retryConfig)

			assert.Equal(t, tt.maxAttempts, strategy.MaxAttempts())
		})
	}
}

func TestShouldRetry_MaxAttemptsBoundary(t *testing.T) {
	retryConfig := config.DefaultConfig().Retry
	retryConfig.MaxAttempts = 3 // Set a fixed value
	strategy := retry.NewStrategy(retryConfig)

	tests := []struct {
		name     string
		attempt  int
		expected bool
	}{
		{"At limit", 3, false},  // Attempt >= MaxAttempts
		{"One below limit", 2, true},
		{"One above limit", 4, false},
		{"Zero attempts", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wh := testutil.NewTestWebhook("http://example.com")
			wh.Attempt = tt.attempt
			wh.MaxAttempts = 3 // Match the strategy config
			result := strategy.ShouldRetry(wh, 500, nil)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsExhausted_Boundary(t *testing.T) {
	retryConfig := config.DefaultConfig().Retry
	retryConfig.MaxAttempts = 3
	strategy := retry.NewStrategy(retryConfig)

	tests := []struct {
		name     string
		attempt  int
		expected bool
	}{
		{"At max", 3, true},  // attempt >= maxAttempts
		{"Below max", 2, false},
		{"Zero", 0, false},
		{"Negative", -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := strategy.IsExhausted(tt.attempt)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNextDelay_ExponentialBackoff(t *testing.T) {
	retryConfig := config.RetryConfig{
		BaseDelay:   1 * time.Second,
		MaxDelay:    1 * time.Hour,
		MaxAttempts: 10,
		Jitter:      0, // No jitter for predictable results
	}
	strategy := retry.NewStrategy(retryConfig)

	tests := []struct {
		attempt      int
		expectedBase time.Duration
	}{
		{0, 1 * time.Second},       // 2^0 = 1
		{1, 2 * time.Second},       // 2^1 = 2
		{2, 4 * time.Second},       // 2^2 = 4
		{3, 8 * time.Second},       // 2^3 = 8
		{4, 16 * time.Second},      // 2^4 = 16
		{5, 32 * time.Second},      // 2^5 = 32
		{10, 1024 * time.Second},   // 2^10 = 1024 (capped at 10)
		{15, 1024 * time.Second},   // Still capped at 10
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("Attempt%d", tt.attempt), func(t *testing.T) {
			delay := strategy.NextDelay(tt.attempt, domain.CircuitClosed)
			assert.Equal(t, tt.expectedBase, delay)
		})
	}
}

func TestAddJitter_ZeroJitter(t *testing.T) {
	retryConfig := config.RetryConfig{
		BaseDelay:   10 * time.Second,
		MaxDelay:    1 * time.Hour,
		MaxAttempts: 5,
		Jitter:      0, // No jitter
	}
	strategy := retry.NewStrategy(retryConfig)

	// Without jitter, delay should be exact
	delay := strategy.NextDelay(0, domain.CircuitClosed)
	assert.Equal(t, retryConfig.BaseDelay, delay)
}
