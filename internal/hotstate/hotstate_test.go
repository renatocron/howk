package hotstate

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	redismock "github.com/go-redis/redismock/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

var defaultCircuitConfig = config.CircuitBreakerConfig{
	FailureThreshold: 3,
	FailureWindow:    time.Minute,
	RecoveryTimeout:  5 * time.Minute,
	ProbeInterval:    30 * time.Second,
	SuccessThreshold: 2,
}

var defaultTTLConfig = config.TTLConfig{
	CircuitStateTTL: 24 * time.Hour,
	StatusTTL:       7 * 24 * time.Hour,
	StatsTTL:        48 * time.Hour,
	IdempotencyTTL:  24 * time.Hour,
}

func newMockedHotState(t *testing.T, cfg config.CircuitBreakerConfig) (*RedisHotState, redismock.ClientMock) {
	t.Helper()

	client, mock := redismock.NewClientMock()
	hotState := &RedisHotState{
		rdb:           client,
		circuitConfig: cfg,
		ttlConfig:     defaultTTLConfig,
	}

	return hotState, mock
}

func TestRedisHotState_GetCircuit_NotFound(t *testing.T) {
	ctx := context.Background()
	endpoint := domain.EndpointHash("endpoint-hash")

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectGet("circuit:endpoint-hash").RedisNil()

	cb, err := state.GetCircuit(ctx, endpoint)
	require.NoError(t, err)
	require.NotNil(t, cb)
	assert.Equal(t, domain.CircuitClosed, cb.State)
	assert.Equal(t, endpoint, cb.EndpointHash)
	assert.Equal(t, 0, cb.Failures)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestRedisHotState_GetCircuit_Found(t *testing.T) {
	ctx := context.Background()
	endpoint := domain.EndpointHash("endpoint-hash")
	existing := domain.CircuitBreaker{
		EndpointHash:   endpoint,
		State:          domain.CircuitOpen,
		Failures:       4,
		Successes:      1,
		StateChangedAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Second),
	}

	payload, err := json.Marshal(existing)
	require.NoError(t, err)

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectGet("circuit:endpoint-hash").SetVal(string(payload))

	cb, getErr := state.GetCircuit(ctx, endpoint)
	require.NoError(t, getErr)
	require.NotNil(t, cb)
	assert.Equal(t, existing.EndpointHash, cb.EndpointHash)
	assert.Equal(t, existing.State, cb.State)
	assert.Equal(t, existing.Failures, cb.Failures)
	assert.Equal(t, existing.Successes, cb.Successes)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestRedisHotState_UpdateCircuit_ClosedStateTTL(t *testing.T) {
	ctx := context.Background()
	endpoint := domain.EndpointHash("endpoint-hash")
	cfg := defaultCircuitConfig
	cfg.RecoveryTimeout = 2 * time.Minute

	state, mockClient := newMockedHotState(t, cfg)
	circuit := &domain.CircuitBreaker{
		EndpointHash: endpoint,
		State:        domain.CircuitClosed,
		Failures:     0,
	}

	expectedTTL := 2 * cfg.RecoveryTimeout
	payload, err := json.Marshal(circuit)
	require.NoError(t, err)
	mockClient.ExpectSet("circuit:endpoint-hash", payload, expectedTTL).SetVal("OK")

	updateErr := state.UpdateCircuit(ctx, circuit)
	require.NoError(t, updateErr)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestRedisHotState_UpdateCircuit_OpenStateTTL(t *testing.T) {
	ctx := context.Background()
	endpoint := domain.EndpointHash("endpoint-hash")

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	circuit := &domain.CircuitBreaker{
		EndpointHash: endpoint,
		State:        domain.CircuitOpen,
		Failures:     5,
	}

	payload, err := json.Marshal(circuit)
	require.NoError(t, err)
	mockClient.ExpectSet("circuit:endpoint-hash", payload, 24*time.Hour).SetVal("OK")

	updateErr := state.UpdateCircuit(ctx, circuit)
	require.NoError(t, updateErr)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestRedisHotState_SetStatus(t *testing.T) {
	ctx := context.Background()
	status := &domain.WebhookStatus{
		WebhookID: domain.WebhookID("wh-123"),
		State:     domain.StateDelivered,
		Attempts:  2,
	}

	payload, err := json.Marshal(status)
	require.NoError(t, err)

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectSet("status:wh-123", payload, 7*24*time.Hour).SetVal("OK")

	setErr := state.SetStatus(ctx, status)
	require.NoError(t, setErr)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestRedisHotState_GetStatus(t *testing.T) {
	ctx := context.Background()
	status := &domain.WebhookStatus{
		WebhookID: domain.WebhookID("wh-123"),
		State:     domain.StateFailed,
		Attempts:  3,
	}

	payload, err := json.Marshal(status)
	require.NoError(t, err)

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectGet("status:wh-123").SetVal(string(payload))

	fetched, getErr := state.GetStatus(ctx, status.WebhookID)
	require.NoError(t, getErr)
	require.NotNil(t, fetched)
	assert.Equal(t, status.State, fetched.State)
	assert.Equal(t, status.Attempts, fetched.Attempts)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestRedisHotState_CheckAndSetProcessed(t *testing.T) {
	ctx := context.Background()
	ttl := 12 * time.Hour
	key := "processed:wh-123:1"

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectSetNX(key, "1", ttl).SetVal(true)

	set, err := state.CheckAndSetProcessed(ctx, domain.WebhookID("wh-123"), 1, ttl)
	require.NoError(t, err)
	assert.True(t, set)

	mockClient.ExpectSetNX(key, "1", ttl).SetVal(false)

	setAgain, errAgain := state.CheckAndSetProcessed(ctx, domain.WebhookID("wh-123"), 1, ttl)
	require.NoError(t, errAgain)
	assert.False(t, setAgain)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestRedisHotState_Ping(t *testing.T) {
	ctx := context.Background()
	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectPing().SetVal("PONG")

	err := state.Ping(ctx)
	require.NoError(t, err)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestRedisHotState_Client(t *testing.T) {
	state, _ := newMockedHotState(t, defaultCircuitConfig)
	assert.NotNil(t, state.Client())
}

func TestRedisHotState_Close(t *testing.T) {
	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	err := state.Close()
	require.NoError(t, err)

	require.NoError(t, mockClient.ExpectationsWereMet())
}
