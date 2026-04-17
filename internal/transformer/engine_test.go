//go:build !integration

package transformer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/mocks"
	lua "github.com/yuin/gopher-lua"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------
// validateDomain
// --------------------------------------------------------------------

func newTestEngine() *Engine {
	return NewEngine(
		config.TransformerConfig{
			Timeout:       500 * time.Millisecond,
			MemoryLimitMB: 10,
		},
		config.LuaConfig{},
		nil, // publisher — not needed for domain-only tests
		nil, // hotstate
		nil, // redis
		nil, // httpMod
		nil, // cryptoMod
	)
}

func TestValidateDomain_AllowedExactMatch(t *testing.T) {
	e := newTestEngine()
	cfg := map[string]any{
		"allowed_domains": []any{"example.com"},
	}
	err := e.validateDomain("https://example.com/hook", cfg)
	assert.NoError(t, err)
}

func TestValidateDomain_WildcardSubdomainMatch(t *testing.T) {
	e := newTestEngine()
	cfg := map[string]any{
		"allowed_domains": []any{"*.example.com"},
	}
	err := e.validateDomain("https://api.example.com/hook", cfg)
	assert.NoError(t, err)
}

func TestValidateDomain_BlockedDomain(t *testing.T) {
	e := newTestEngine()
	cfg := map[string]any{
		"allowed_domains": []any{"allowed.com"},
	}
	err := e.validateDomain("https://blocked.com/hook", cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed_domains")
}

func TestValidateDomain_MissingHostname(t *testing.T) {
	e := newTestEngine()
	cfg := map[string]any{
		"allowed_domains": []any{"example.com"},
	}
	// A URL without a hostname
	err := e.validateDomain("not-a-url-at-all", cfg)
	require.Error(t, err)
	// url.Parse won't fail on this but hostname will be empty or the host won't
	// match — we just verify an error is returned.
}

func TestValidateDomain_NilConfigAllowsAll(t *testing.T) {
	e := newTestEngine()
	err := e.validateDomain("https://anything.example.com/hook", nil)
	assert.NoError(t, err)
}

func TestValidateDomain_EmptyAllowedDomainsAllowsAll(t *testing.T) {
	e := newTestEngine()
	cfg := map[string]any{}
	// No "allowed_domains" key at all → allow all
	err := e.validateDomain("https://anything.com/hook", cfg)
	assert.NoError(t, err)
}

func TestValidateDomain_NonArrayAllowedDomains(t *testing.T) {
	e := newTestEngine()
	cfg := map[string]any{
		"allowed_domains": "example.com", // string, not array
	}
	err := e.validateDomain("https://example.com/hook", cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be an array")
}

// --------------------------------------------------------------------
// Engine.Execute
// --------------------------------------------------------------------

func newEngineWithMocks(t *testing.T, timeout time.Duration) (*Engine, *mocks.MockWebhookPublisher, *mocks.MockHotState) {
	t.Helper()

	pub := &mocks.MockWebhookPublisher{}
	hs := &mocks.MockHotState{}

	e := NewEngine(
		config.TransformerConfig{
			Timeout:       timeout,
			MemoryLimitMB: 10,
		},
		config.LuaConfig{},
		pub,
		hs,
		nil, // no real Redis needed; KV module is skipped when rdb==nil
		nil,
		nil,
	)
	return e, pub, hs
}

func TestEngine_Execute_PostCreatesWebhook(t *testing.T) {
	e, pub, hs := newEngineWithMocks(t, 2*time.Second)

	pub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil)
	hs.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	script := &TransformerScript{
		Name:    "fanout",
		LuaCode: `howk.post("https://dest.example.com/hook", {event = "test"})`,
		Config: map[string]any{
			"allowed_domains": []any{"dest.example.com"},
		},
	}

	result, err := e.Execute(context.Background(), script, []byte(`{}`), nil)
	require.NoError(t, err)
	require.Len(t, result.Webhooks, 1)
	assert.Equal(t, "https://dest.example.com/hook", result.Webhooks[0].Endpoint)
	assert.NotEmpty(t, result.Webhooks[0].ID)

	pub.AssertExpectations(t)
	hs.AssertExpectations(t)
}

func TestEngine_Execute_SyntaxError(t *testing.T) {
	e, _, _ := newEngineWithMocks(t, 2*time.Second)

	script := &TransformerScript{
		Name:    "bad",
		LuaCode: `this is not valid lua !!!`,
	}

	_, err := e.Execute(context.Background(), script, []byte(`{}`), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lua syntax error")
}

func TestEngine_Execute_Timeout(t *testing.T) {
	e, _, _ := newEngineWithMocks(t, 50*time.Millisecond)

	script := &TransformerScript{
		Name: "loop",
		LuaCode: `
			while true do
				local x = 1 + 1
			end
		`,
	}

	_, err := e.Execute(context.Background(), script, []byte(`{}`), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestEngine_Execute_ConfigTablePopulated(t *testing.T) {
	e, pub, hs := newEngineWithMocks(t, 2*time.Second)

	pub.On("PublishWebhook", mock.Anything, mock.Anything).Return(nil)
	hs.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	script := &TransformerScript{
		Name: "cfg",
		LuaCode: `
			local endpoint = config.target_url
			howk.post(endpoint, {msg = "hi"})
		`,
		Config: map[string]any{
			"target_url":      "https://target.example.com/hook",
			"allowed_domains": []any{"target.example.com"},
		},
	}

	result, err := e.Execute(context.Background(), script, []byte(`{}`), nil)
	require.NoError(t, err)
	require.Len(t, result.Webhooks, 1)
	assert.Equal(t, "https://target.example.com/hook", result.Webhooks[0].Endpoint)

	pub.AssertExpectations(t)
}

func TestEngine_Execute_ConfigEnvExpansion(t *testing.T) {
	t.Setenv("HOWK_TEST_TARGET_URL", "https://env-target.example.com/hook")
	t.Setenv("HOWK_TEST_SECRET", "s3cret-abc")

	e, pub, hs := newEngineWithMocks(t, 2*time.Second)

	var captured *domain.Webhook
	pub.On("PublishWebhook", mock.Anything, mock.AnythingOfType("*domain.Webhook")).
		Run(func(args mock.Arguments) {
			captured = args.Get(1).(*domain.Webhook)
		}).Return(nil)
	hs.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	script := &TransformerScript{
		Name: "envcfg",
		LuaCode: `
			howk.post(config.target_url, {
				token = config.nested.secret,
				missing = config.nested.missing,
			})
		`,
		Config: map[string]any{
			"target_url":      "${HOWK_TEST_TARGET_URL}",
			"allowed_domains": []any{"env-target.example.com"},
			"nested": map[string]any{
				"secret":  "${HOWK_TEST_SECRET}",
				"missing": "${HOWK_TEST_UNSET_VAR}", // unset env → empty string
			},
		},
	}

	result, err := e.Execute(context.Background(), script, []byte(`{}`), nil)
	require.NoError(t, err)
	require.Len(t, result.Webhooks, 1)
	assert.Equal(t, "https://env-target.example.com/hook", result.Webhooks[0].Endpoint,
		"string config values should expand ${VAR} from process env")

	require.NotNil(t, captured, "publisher should have received the webhook")
	var body map[string]any
	require.NoError(t, json.Unmarshal(captured.Payload, &body))
	assert.Equal(t, "s3cret-abc", body["token"],
		"nested string config values should also expand ${VAR}")
	assert.Equal(t, "", body["missing"],
		"unset env vars should expand to empty string (os.Expand default)")

	pub.AssertExpectations(t)
}

func TestEngine_Execute_HeadersGlobalSet(t *testing.T) {
	e, pub, hs := newEngineWithMocks(t, 2*time.Second)

	pub.On("PublishWebhook", mock.Anything, mock.AnythingOfType("*domain.Webhook")).Return(nil)
	hs.On("SetStatus", mock.Anything, mock.Anything).Return(nil)

	script := &TransformerScript{
		Name: "hdr",
		LuaCode: `
			local token = headers["X-Auth-Token"]
			howk.post("https://dest.example.com/hook", {token = token})
		`,
		Config: map[string]any{
			"allowed_domains": []any{"dest.example.com"},
		},
	}

	inputHeaders := map[string]string{"X-Auth-Token": "bearer-abc"}
	result, err := e.Execute(context.Background(), script, []byte(`{}`), inputHeaders)
	require.NoError(t, err)
	require.Len(t, result.Webhooks, 1)

	pub.AssertExpectations(t)
}

// --------------------------------------------------------------------
// parsePostOptions
// --------------------------------------------------------------------

func TestParsePostOptions_AllFields(t *testing.T) {
	e := newTestEngine()

	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	tbl.RawSetString("signing_secret", lua.LString("mysecret"))
	tbl.RawSetString("config_id", lua.LString("cfg-123"))
	tbl.RawSetString("max_attempts", lua.LNumber(5))

	headersTbl := L.NewTable()
	headersTbl.RawSetString("X-Foo", lua.LString("bar"))
	tbl.RawSetString("headers", headersTbl)

	opts := &postOptions{
		Headers: make(map[string]string),
	}
	e.parsePostOptions(tbl, opts)

	assert.Equal(t, "mysecret", opts.SigningSecret)
	assert.Equal(t, "cfg-123", opts.ConfigID)
	assert.Equal(t, 5, opts.MaxAttempts)
	assert.Equal(t, "bar", opts.Headers["X-Foo"])
}

func TestParsePostOptions_EmptyTable(t *testing.T) {
	e := newTestEngine()

	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	opts := &postOptions{
		Headers:     make(map[string]string),
		MaxAttempts: 20,
	}
	e.parsePostOptions(tbl, opts)

	// Defaults unchanged
	assert.Equal(t, "", opts.SigningSecret)
	assert.Equal(t, "", opts.ConfigID)
	assert.Equal(t, 20, opts.MaxAttempts)
	assert.Empty(t, opts.Headers)
}

// --------------------------------------------------------------------
// setLuaValue
// --------------------------------------------------------------------

func TestSetLuaValue_String(t *testing.T) {
	e := newTestEngine()
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	e.setLuaValue(L, tbl, "key", "hello")

	v := tbl.RawGetString("key")
	assert.Equal(t, lua.LString("hello"), v)
}

func TestSetLuaValue_Float64(t *testing.T) {
	e := newTestEngine()
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	e.setLuaValue(L, tbl, "num", float64(3.14))

	v := tbl.RawGetString("num")
	assert.Equal(t, lua.LNumber(3.14), v)
}

func TestSetLuaValue_Bool(t *testing.T) {
	e := newTestEngine()
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	e.setLuaValue(L, tbl, "flag", true)

	v := tbl.RawGetString("flag")
	assert.Equal(t, lua.LBool(true), v)
}

func TestSetLuaValue_Nil(t *testing.T) {
	e := newTestEngine()
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	e.setLuaValue(L, tbl, "nothing", nil)

	v := tbl.RawGetString("nothing")
	assert.Equal(t, lua.LNil, v)
}

func TestSetLuaValue_NestedMap(t *testing.T) {
	e := newTestEngine()
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	nested := map[string]any{"inner": "value"}
	e.setLuaValue(L, tbl, "obj", nested)

	subTbl, ok := tbl.RawGetString("obj").(*lua.LTable)
	require.True(t, ok, "expected nested table")
	assert.Equal(t, lua.LString("value"), subTbl.RawGetString("inner"))
}

func TestSetLuaValue_Slice(t *testing.T) {
	e := newTestEngine()
	L := lua.NewState()
	defer L.Close()

	tbl := L.NewTable()
	e.setLuaValue(L, tbl, "arr", []any{"a", "b"})

	arrTbl, ok := tbl.RawGetString("arr").(*lua.LTable)
	require.True(t, ok, "expected array table")

	// setLuaValue uses "1", "2" as keys (string indices)
	assert.Equal(t, lua.LString("a"), arrTbl.RawGetString("1"))
	assert.Equal(t, lua.LString("b"), arrTbl.RawGetString("2"))
}

func TestSetLuaValue_UnknownTypeFallsBackToString(t *testing.T) {
	e := newTestEngine()
	L := lua.NewState()
	defer L.Close()

	type custom struct{ X int }
	tbl := L.NewTable()
	e.setLuaValue(L, tbl, "custom", custom{X: 42})

	v := tbl.RawGetString("custom")
	_, ok := v.(lua.LString)
	assert.True(t, ok, "unknown type should be serialized as LString via fmt.Sprintf")
}
