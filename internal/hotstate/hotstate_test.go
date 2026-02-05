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
	RetryDataTTL:    7 * 24 * time.Hour,
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

func TestCircuitBreaker_Get_NotFound(t *testing.T) {
	ctx := context.Background()
	endpoint := domain.EndpointHash("endpoint-hash")

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	mockClient.ExpectGet("circuit:endpoint-hash").RedisNil()

	cb := state.CircuitBreaker()
	result, err := cb.(*circuitBreaker).Get(ctx, endpoint)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, domain.CircuitClosed, result.State)
	assert.Equal(t, endpoint, result.EndpointHash)
	assert.Equal(t, 0, result.Failures)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestCircuitBreaker_Get_Found(t *testing.T) {
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

	cb := state.CircuitBreaker()
	result, getErr := cb.(*circuitBreaker).Get(ctx, endpoint)
	require.NoError(t, getErr)
	require.NotNil(t, result)
	assert.Equal(t, existing.EndpointHash, result.EndpointHash)
	assert.Equal(t, existing.State, result.State)
	assert.Equal(t, existing.Failures, result.Failures)
	assert.Equal(t, existing.Successes, result.Successes)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestCircuitBreaker_Save_ClosedStateTTL(t *testing.T) {
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

	cb := state.CircuitBreaker()
	saveErr := cb.(*circuitBreaker).save(ctx, circuit)
	require.NoError(t, saveErr)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestCircuitBreaker_Save_OpenStateTTL(t *testing.T) {
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

	cb := state.CircuitBreaker()
	saveErr := cb.(*circuitBreaker).save(ctx, circuit)
	require.NoError(t, saveErr)

	require.NoError(t, mockClient.ExpectationsWereMet())
}

// luaSetStatusLWW is the same script defined in redis.go
// We redefine it here for test expectations
var luaSetStatusLWW = `
	local key = KEYS[1]
	local new_ts = tonumber(ARGV[1])
	local data = ARGV[2]
	local ttl = tonumber(ARGV[3])
	
	local old_ts = tonumber(redis.call('HGET', key, 'ts') or '0')
	if new_ts > old_ts then
		redis.call('HSET', key, 'data', data, 'ts', new_ts)
		redis.call('EXPIRE', key, ttl)
		return 1
	end
	return 0
`

// luaSetStatusLWWHash is the SHA1 hash of the script (cached by go-redis)
const luaSetStatusLWWHash = "3201f48899258101b2f6a6e034d6652cb6760474"

func TestRedisHotState_SetStatus(t *testing.T) {
	ctx := context.Background()
	status := &domain.WebhookStatus{
		WebhookID: domain.WebhookID("wh-123"),
		State:     domain.StateDelivered,
		Attempts:  2,
		UpdatedAtNs: 1234567890,
	}

	payload, err := json.Marshal(status)
	require.NoError(t, err)

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)
	// SetStatus now uses Lua script with LWW semantics
	mockClient.ExpectEvalSha(
		luaSetStatusLWWHash,
		[]string{"status:wh-123"},
		status.UpdatedAtNs,
		string(payload),
		int64(7*24*time.Hour.Seconds()),
	).SetVal(int64(1))

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
	// GetStatus now uses HGET to retrieve from hash (LWW storage format)
	mockClient.ExpectHGet("status:wh-123", "data").SetVal(string(payload))

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
	webhookID := domain.WebhookID("wh-123")
	attempt := 2
	scheduledAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	reason := "circuit_open"

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)

	// The ScheduleRetry uses a pipeline (not transaction)
	// Match any value for the metadata JSON since it's passed as bytes
	mockClient.Regexp().ExpectSet("retry_meta:wh-123:2", `.*`, 7*24*time.Hour).SetVal("OK")
	mockClient.ExpectZAdd("retries", redis.Z{
		Score:  float64(scheduledAt.Unix()),
		Member: "wh-123:2",
	}).SetVal(1)

	err := state.ScheduleRetry(ctx, webhookID, attempt, scheduledAt, reason)
	require.NoError(t, err)
	require.NoError(t, mockClient.ExpectationsWereMet())
}

func TestRedisHotState_EnsureAndGetRetryData(t *testing.T) {
	ctx := context.Background()
	webhook := &domain.Webhook{
		ID:       domain.WebhookID("wh-123"),
		ConfigID: domain.ConfigID("config-1"),
		Endpoint: "https://example.com/webhook",
		Attempt:  2,
	}
	ttl := 7 * 24 * time.Hour

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)

	// Test Case 1: Key doesn't exist - should call EXPIRE (returns false) then SET
	mockClient.ExpectExpire("retry_data:wh-123", ttl).SetVal(false) // Key doesn't exist
	mockClient.Regexp().ExpectSet("retry_data:wh-123", `^.+$`, ttl).SetVal("OK")
	err := state.EnsureRetryData(ctx, webhook, ttl)
	require.NoError(t, err)
	require.NoError(t, mockClient.ExpectationsWereMet())

	// Test Case 2: Key exists - should only call EXPIRE (returns true), no SET
	state2, mockClient2 := newMockedHotState(t, defaultCircuitConfig)
	mockClient2.ExpectExpire("retry_data:wh-123", ttl).SetVal(true) // Key exists
	err = state2.EnsureRetryData(ctx, webhook, ttl)
	require.NoError(t, err)
	require.NoError(t, mockClient2.ExpectationsWereMet())
}

func TestRedisHotState_DeleteRetryData(t *testing.T) {
	ctx := context.Background()
	webhookID := domain.WebhookID("wh-123")

	state, mockClient := newMockedHotState(t, defaultCircuitConfig)

	mockClient.ExpectDel("retry_data:wh-123").SetVal(1)

	err := state.DeleteRetryData(ctx, webhookID)
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
