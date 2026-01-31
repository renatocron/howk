package retry

import (
	"math"
	"math/rand"
	"time"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

// Retrier abstracts the retry logic methods used by the worker
type Retrier interface {
	ShouldRetry(webhook *domain.Webhook, statusCode int, err error) bool
	NextRetryAt(attempt int, circuitState domain.CircuitState) time.Time
}

// Strategy calculates retry delays with exponential backoff
type Strategy struct {
	config config.RetryConfig
}

// NewStrategy creates a new retry strategy
func NewStrategy(cfg config.RetryConfig) *Strategy {
	return &Strategy{config: cfg}
}

// ShouldRetry returns whether a webhook should be retried
func (s *Strategy) ShouldRetry(webhook *domain.Webhook, statusCode int, err error) bool {
	// Check if we've exhausted attempts
	if webhook.Attempt >= webhook.MaxAttempts {
		return false
	}

	// Network errors are always retryable
	if err != nil {
		return true
	}

	// Check status code
	return domain.IsRetryable(statusCode)
}

// NextDelay calculates the next retry delay
// circuitState affects the delay:
// - CLOSED: normal exponential backoff
// - OPEN: use recovery timeout (back off significantly)
// - HALF_OPEN: short delay (we're probing)
func (s *Strategy) NextDelay(attempt int, circuitState domain.CircuitState) time.Duration {
	var baseDelay time.Duration

	switch circuitState {
	case domain.CircuitOpen:
		// Circuit is open - back off significantly
		// The circuit breaker will handle the recovery timeout
		// We add extra time on top to avoid thundering herd
		baseDelay = 5 * time.Minute
	case domain.CircuitHalfOpen:
		// We're probing - use a shorter delay
		baseDelay = 30 * time.Second
	default:
		// Normal exponential backoff
		// delay = baseDelay * 2^min(attempt, 10)
		exponent := math.Min(float64(attempt), 10)
		baseDelay = time.Duration(float64(s.config.BaseDelay) * math.Pow(2, exponent))
	}

	// Cap at max delay
	if baseDelay > s.config.MaxDelay {
		baseDelay = s.config.MaxDelay
	}

	// Add jitter to prevent thundering herd
	jitter := s.addJitter(baseDelay)

	return jitter
}

// addJitter adds random jitter to a duration
func (s *Strategy) addJitter(d time.Duration) time.Duration {
	if s.config.Jitter <= 0 {
		return d
	}

	// Calculate jitter range
	jitterRange := float64(d) * s.config.Jitter

	// Random value between -jitterRange and +jitterRange
	jitterValue := (rand.Float64()*2 - 1) * jitterRange

	return time.Duration(float64(d) + jitterValue)
}

// NextRetryAt calculates the absolute time for the next retry
func (s *Strategy) NextRetryAt(attempt int, circuitState domain.CircuitState) time.Time {
	return time.Now().Add(s.NextDelay(attempt, circuitState))
}

// IsExhausted returns whether all retry attempts have been used
func (s *Strategy) IsExhausted(attempt int) bool {
	return attempt >= s.config.MaxAttempts
}

// MaxAttempts returns the configured max attempts
func (s *Strategy) MaxAttempts() int {
	return s.config.MaxAttempts
}

// RetrySchedule returns a human-readable schedule of retry attempts
// Useful for debugging and documentation
func (s *Strategy) RetrySchedule() []string {
	schedule := make([]string, s.config.MaxAttempts)
	totalDelay := time.Duration(0)

	for i := 0; i < s.config.MaxAttempts; i++ {
		delay := s.NextDelay(i, domain.CircuitClosed)
		totalDelay += delay
		schedule[i] = totalDelay.String()
	}

	return schedule
}
