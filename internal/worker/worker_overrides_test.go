//go:build !integration

package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/script"
)

// TestDeliveryOverrides_AppliedAtDeliveryNotStored asserts the gondola case:
// a webhook arrives with a bare endpoint, the script_config carries
// _delivery_query_params with secret values, and:
//   - the URL passed to delivery.Deliver carries the resolved secrets
//   - the DeliveryResult published to Kafka carries only the bare URL
//
// This is the storage invariant that makes the topic safe to read.
func TestDeliveryOverrides_AppliedAtDeliveryNotStored(t *testing.T) {
	t.Setenv("TEST_GCHAT_KEY", "leaked-key-value")
	t.Setenv("TEST_GCHAT_TOKEN", "leaked-token-value")

	w, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, _, loader :=
		setupWorkerForScriptTest(t, true)

	const bareEndpoint = "https://chat.googleapis.com/v1/spaces/X/messages?messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"

	wh := &domain.Webhook{
		ID:           "wh-override-001",
		ConfigID:     "gondola-google-chat:unknown",
		EndpointHash: "hash-override-001",
		Endpoint:     bareEndpoint,
		Payload:      json.RawMessage(`{"text":"hi"}`),
		Attempt:      0,
		MaxAttempts:  5,
		// Note: no ScriptHash — gondola-style transformer-emitted webhook
	}
	msg := makeWebhookMsg(t, wh)

	// Register a delivery-only config under the namespace "gondola-google-chat".
	// loader.GetScript performs namespace fallback ("gondola-google-chat:unknown"
	// → "gondola-google-chat") so this matches without exact-id storage.
	loader.SetScript(&script.Config{
		ConfigID: "gondola-google-chat",
		LuaCode:  "", // no Lua execution; only delivery overrides
		Hash:     "",
		ScriptConfig: map[string]any{
			delivery.OverrideQueryParamsKey: map[string]any{
				"key":   "${TEST_GCHAT_KEY}",
				"token": "${TEST_GCHAT_TOKEN}",
			},
		},
	})

	// Standard mocks for a successful delivery path.
	mockHotState.On("CheckAndSetProcessed", mock.Anything, wh.ID, wh.Attempt, mock.Anything).
		Return(true, nil)
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, wh.EndpointHash).Return(true, false, nil)
	mockHotState.On("IncrInflight", mock.Anything, wh.EndpointHash, mock.Anything).Return(int64(1), nil)
	mockHotState.On("DecrInflight", mock.Anything, wh.EndpointHash).Return(nil)
	mockHotState.On("GetScript", mock.Anything, mock.Anything).Return("", errors.New("not found")).Maybe()

	// Capture the webhook handed to Deliver — the URL here SHOULD have the
	// resolved secrets (deliverable copy).
	var deliveredEndpoint string
	var deliveredHeaders map[string]string
	mockDelivery.On("Deliver", mock.Anything, mock.AnythingOfType("*domain.Webhook")).
		Run(func(args mock.Arguments) {
			delivered := args.Get(1).(*domain.Webhook)
			deliveredEndpoint = delivered.Endpoint
			deliveredHeaders = delivered.Headers
		}).
		Return(&delivery.Result{StatusCode: 200, Duration: 30 * time.Millisecond})

	mockCircuitBreaker.On("RecordSuccess", mock.Anything, wh.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	// Capture the DeliveryResult — its Endpoint and nested Webhook.Endpoint
	// MUST be the bare URL, never the resolved one.
	var publishedResult *domain.DeliveryResult
	mockPublisher.On("PublishResult", mock.Anything, mock.AnythingOfType("*domain.DeliveryResult")).
		Run(func(args mock.Arguments) {
			publishedResult = args.Get(1).(*domain.DeliveryResult)
		}).
		Return(nil)
	mockHotState.On("DeleteRetryData", mock.Anything, wh.ID).Return(nil)

	stdHotStateExpectations(mockHotState)

	err := w.ProcessMessage(context.Background(), msg)
	require.NoError(t, err)

	// Deliver received the URL with the resolved secrets.
	require.NotEmpty(t, deliveredEndpoint)
	u, err := url.Parse(deliveredEndpoint)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "leaked-key-value", q.Get("key"))
	assert.Equal(t, "leaked-token-value", q.Get("token"))
	assert.Equal(t, "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD", q.Get("messageReplyOption"),
		"existing query params must be preserved")
	_ = deliveredHeaders

	// Storage invariant: the published DeliveryResult never sees the secrets.
	require.NotNil(t, publishedResult)
	assert.Equal(t, bareEndpoint, publishedResult.Endpoint,
		"DeliveryResult.Endpoint must be the bare URL stored on Kafka")
	require.NotNil(t, publishedResult.Webhook)
	assert.Equal(t, bareEndpoint, publishedResult.Webhook.Endpoint,
		"DeliveryResult.Webhook.Endpoint must be the bare URL stored on Kafka")
	assert.NotContains(t, publishedResult.Endpoint, "leaked-key-value")
	assert.NotContains(t, publishedResult.Webhook.Endpoint, "leaked-token-value")
}

// TestDeliveryOverrides_NoConfigIsNoOp ensures that webhooks whose ConfigID
// has no matching script_config in the loader are unaffected — overrides are
// strictly opt-in.
func TestDeliveryOverrides_NoConfigIsNoOp(t *testing.T) {
	w, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, _, _ :=
		setupWorkerForScriptTest(t, true)

	const bareEndpoint = "https://api.example.com/hook"
	wh := &domain.Webhook{
		ID:           "wh-noconfig-001",
		ConfigID:     "no-such-config:1",
		EndpointHash: "hash-noconfig",
		Endpoint:     bareEndpoint,
		Payload:      json.RawMessage(`{}`),
		MaxAttempts:  3,
	}
	msg := makeWebhookMsg(t, wh)

	mockHotState.On("CheckAndSetProcessed", mock.Anything, wh.ID, wh.Attempt, mock.Anything).
		Return(true, nil)
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, wh.EndpointHash).Return(true, false, nil)
	mockHotState.On("IncrInflight", mock.Anything, wh.EndpointHash, mock.Anything).Return(int64(1), nil)
	mockHotState.On("DecrInflight", mock.Anything, wh.EndpointHash).Return(nil)
	mockHotState.On("GetScript", mock.Anything, mock.Anything).Return("", errors.New("not found")).Maybe()

	var deliveredEndpoint string
	mockDelivery.On("Deliver", mock.Anything, mock.AnythingOfType("*domain.Webhook")).
		Run(func(args mock.Arguments) {
			deliveredEndpoint = args.Get(1).(*domain.Webhook).Endpoint
		}).
		Return(&delivery.Result{StatusCode: 200, Duration: 10 * time.Millisecond})
	mockCircuitBreaker.On("RecordSuccess", mock.Anything, wh.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)
	mockPublisher.On("PublishResult", mock.Anything, mock.Anything).Return(nil)
	mockHotState.On("DeleteRetryData", mock.Anything, wh.ID).Return(nil)

	stdHotStateExpectations(mockHotState)

	err := w.ProcessMessage(context.Background(), msg)
	require.NoError(t, err)

	assert.Equal(t, bareEndpoint, deliveredEndpoint, "no overrides registered → URL untouched")
}

// TestDeliveryOverrides_FromWebhookFields exercises the disk-mounted
// transformer flow: the API-side transformer attached unresolved templates
// directly to the Webhook (via gondola-google-chat.json), so the worker has
// no script registered in the loader yet still applies overrides at HTTP
// send. Storage records continue to carry the bare endpoint.
func TestDeliveryOverrides_FromWebhookFields(t *testing.T) {
	t.Setenv("TEST_GCHAT_KEY_FIELDS", "field-key-value")
	t.Setenv("TEST_GCHAT_TOKEN_FIELDS", "field-token-value")

	w, mockPublisher, mockHotState, mockCircuitBreaker, mockDelivery, _, _ :=
		setupWorkerForScriptTest(t, true)

	const bareEndpoint = "https://chat.googleapis.com/v1/spaces/X/messages?messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"

	wh := &domain.Webhook{
		ID:           "wh-fields-001",
		ConfigID:     "gondola-google-chat:unknown",
		EndpointHash: "hash-fields-001",
		Endpoint:     bareEndpoint,
		Payload:      json.RawMessage(`{"text":"hi"}`),
		Attempt:      0,
		MaxAttempts:  5,
		// Templates set by the transformer from gondola-google-chat.json.
		// The loader is intentionally NOT populated for this config_id —
		// proving the Webhook-fields path works on its own.
		DeliveryQueryParams: map[string]string{
			"key":   "${TEST_GCHAT_KEY_FIELDS}",
			"token": "${TEST_GCHAT_TOKEN_FIELDS}",
		},
	}
	msg := makeWebhookMsg(t, wh)

	mockHotState.On("CheckAndSetProcessed", mock.Anything, wh.ID, wh.Attempt, mock.Anything).
		Return(true, nil)
	mockCircuitBreaker.On("ShouldAllow", mock.Anything, wh.EndpointHash).Return(true, false, nil)
	mockHotState.On("IncrInflight", mock.Anything, wh.EndpointHash, mock.Anything).Return(int64(1), nil)
	mockHotState.On("DecrInflight", mock.Anything, wh.EndpointHash).Return(nil)

	var deliveredEndpoint string
	mockDelivery.On("Deliver", mock.Anything, mock.AnythingOfType("*domain.Webhook")).
		Run(func(args mock.Arguments) {
			deliveredEndpoint = args.Get(1).(*domain.Webhook).Endpoint
		}).
		Return(&delivery.Result{StatusCode: 200, Duration: 30 * time.Millisecond})

	mockCircuitBreaker.On("RecordSuccess", mock.Anything, wh.EndpointHash).
		Return(&domain.CircuitBreaker{State: domain.CircuitClosed}, nil)

	var publishedResult *domain.DeliveryResult
	mockPublisher.On("PublishResult", mock.Anything, mock.AnythingOfType("*domain.DeliveryResult")).
		Run(func(args mock.Arguments) {
			publishedResult = args.Get(1).(*domain.DeliveryResult)
		}).
		Return(nil)
	mockHotState.On("DeleteRetryData", mock.Anything, wh.ID).Return(nil)

	stdHotStateExpectations(mockHotState)

	err := w.ProcessMessage(context.Background(), msg)
	require.NoError(t, err)

	// Deliver received the resolved secrets.
	u, err := url.Parse(deliveredEndpoint)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "field-key-value", q.Get("key"))
	assert.Equal(t, "field-token-value", q.Get("token"))

	// Storage invariant: published DeliveryResult sees only the bare URL.
	require.NotNil(t, publishedResult)
	assert.Equal(t, bareEndpoint, publishedResult.Endpoint)
	assert.Equal(t, bareEndpoint, publishedResult.Webhook.Endpoint)
	// Templates (not secrets) DO travel on the persisted webhook so retries
	// and reconciliation can re-apply them.
	assert.Equal(t, "${TEST_GCHAT_KEY_FIELDS}", publishedResult.Webhook.DeliveryQueryParams["key"])
}

// Ensure imports stay used even when a test is gated out.
var _ = broker.Message{}
