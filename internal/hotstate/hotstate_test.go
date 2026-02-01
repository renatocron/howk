package hotstate

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
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


// --- Retry Scheduling Tests ---

func TestRedisHotState_ScheduleRetry(t *testing.T) {
	ctx := context.Background()
	msg := &RetryMessage{
		Webhook: &domain.Webhook{
			ID:       domain.WebhookID("wh-123"),
			ConfigID: domain.ConfigID("config-1"),
			Endpoint: "https://example.com/webhook",
		},
		ScheduledAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Reason:      "circuit_open",
	}

	payload, _ := json.Marshal(msg)
	state, mockClient := newMockedHotState(t, defaultCircuitConfig)

	mockClient.ExpectZAdd("retries", redis.Z{
		Score:  float64(msg.ScheduledAt.Unix()),
		Member: payload,
	}).SetVal(1)

	err := state.ScheduleRetry(ctx, msg)
	require.NoError(t, err)
	require.NoError(t, mockClient.ExpectationsWereMet())
}


// --- AddToHLL_Empty Test ---

func TestRedisHotState_AddToHLL_Empty(t *testing.T) {
	ctx := context.Background()
	state, _ := newMockedHotState(t, defaultCircuitConfig)

	err := state.AddToHLL(ctx, "endpoints:2026010112")
	require.NoError(t, err)
}

// --- Script Operations Tests ---

func TestRedisHotState_GetScript_Found(t *testing.T) {
	ctx := context.Background()
	configID := domain.ConfigID("config-1")
	scriptJSON := `{"lua_code":"return payload","hash":"abc123"}`

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectGet("script:config-1").SetVal(scriptJSON)

	result, err := state.GetScript(ctx, configID)
	require.NoError(t, err)
	assert.Equal(t, scriptJSON, result)
}

func TestRedisHotState_GetScript_NotFound(t *testing.T) {
	ctx := context.Background()
	configID := domain.ConfigID("config-1")

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectGet("script:config-1").RedisNil()

	result, err := state.GetScript(ctx, configID)
	require.Error(t, err)
	assert.Equal(t, "", result)
}

func TestRedisHotState_SetScript(t *testing.T) {
	ctx := context.Background()
	configID := domain.ConfigID("config-1")
	scriptJSON := `{"lua_code":"return payload","hash":"abc123"}`
	ttl := 24 * time.Hour

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectSet("script:config-1", scriptJSON, ttl).SetVal("OK")

	err := state.SetScript(ctx, configID, scriptJSON, ttl)
	require.NoError(t, err)
}

func TestRedisHotState_DeleteScript(t *testing.T) {
	ctx := context.Background()
	configID := domain.ConfigID("config-1")

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectDel("script:config-1").SetVal(1)

	err := state.DeleteScript(ctx, configID)
	require.NoError(t, err)
}

// --- FlushForRebuild Test ---

func TestRedisHotState_FlushForRebuild(t *testing.T) {
	ctx := context.Background()
	state, mockClient := newMockedHotState(t, defaultCircuitConfig)

	// For pattern matching scans
	mockClient.ExpectScan(0, "status:*", 1000).SetVal([]string{"status:wh-123"}, 0)
	mockClient.ExpectDel("status:wh-123").SetVal(1)

	mockClient.ExpectDel("retries").SetVal(0)

	mockClient.ExpectScan(0, "processed:*", 1000).SetVal([]string{"processed:wh-123:1"}, 0)
	mockClient.ExpectDel("processed:wh-123:1").SetVal(1)

	mockClient.ExpectScan(0, "stats:*", 1000).SetVal([]string{}, 0)

	mockClient.ExpectScan(0, "hll:*", 1000).SetVal([]string{}, 0)

	mockClient.ExpectScan(0, "circuit:*", 1000).SetVal([]string{}, 0)

	err := state.FlushForRebuild(ctx)
	require.NoError(t, err)
}
