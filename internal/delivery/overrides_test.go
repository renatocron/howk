//go:build !integration

package delivery

import (
	"maps"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/domain"
)

func TestExtractTemplates_PreservesUnresolvedFormat(t *testing.T) {
	cfg := map[string]any{
		OverrideQueryParamsKey: map[string]any{
			"key": "${SOME_VAR}",
			"raw": "literal",
		},
		OverrideHeadersKey: map[string]any{
			"Authorization": "Bearer ${TOK}",
		},
	}
	qp, hd := ExtractTemplates(cfg)
	assert.Equal(t, "${SOME_VAR}", qp["key"], "templates must remain unresolved")
	assert.Equal(t, "literal", qp["raw"])
	assert.Equal(t, "Bearer ${TOK}", hd["Authorization"])
}

func TestExtractTemplates_NilWhenAbsent(t *testing.T) {
	qp, hd := ExtractTemplates(map[string]any{})
	assert.Nil(t, qp)
	assert.Nil(t, hd)
}

func TestResolveTemplates_DropsEmpty(t *testing.T) {
	t.Setenv("RT_VAR", "ok")
	ov := ResolveTemplates(
		map[string]string{"present": "${RT_VAR}", "missing": "${RT_NOT_SET_XYZ}"},
		nil,
	)
	require.NotNil(t, ov)
	assert.Equal(t, "ok", ov.QueryParams["present"])
	_, hasMissing := ov.QueryParams["missing"]
	assert.False(t, hasMissing)
}

func TestResolveTemplates_NilWhenNothingResolves(t *testing.T) {
	assert.Nil(t, ResolveTemplates(nil, nil))
	assert.Nil(t, ResolveTemplates(
		map[string]string{"k": "${RT_NOT_SET_XYZZY}"},
		nil,
	))
}

func TestExtractOverrides_NilWhenAbsent(t *testing.T) {
	assert.Nil(t, ExtractOverrides(nil))
	assert.Nil(t, ExtractOverrides(map[string]any{}))
	assert.Nil(t, ExtractOverrides(map[string]any{"unrelated": "x"}))
}

func TestExtractOverrides_QueryParamsAndHeaders(t *testing.T) {
	t.Setenv("TEST_GCHAT_KEY", "abc123")
	t.Setenv("TEST_GCHAT_TOKEN", "tok-9")
	t.Setenv("TEST_AUTH_HEADER", "Bearer s3cret")

	cfg := map[string]any{
		OverrideQueryParamsKey: map[string]any{
			"key":   "${TEST_GCHAT_KEY}",
			"token": "${TEST_GCHAT_TOKEN}",
		},
		OverrideHeadersKey: map[string]any{
			"Authorization": "${TEST_AUTH_HEADER}",
			"X-Static":      "literal-value",
		},
	}

	ov := ExtractOverrides(cfg)
	require.NotNil(t, ov)
	assert.Equal(t, "abc123", ov.QueryParams["key"])
	assert.Equal(t, "tok-9", ov.QueryParams["token"])
	assert.Equal(t, "Bearer s3cret", ov.Headers["Authorization"])
	assert.Equal(t, "literal-value", ov.Headers["X-Static"])
}

func TestExtractOverrides_DropsEmptyResolutions(t *testing.T) {
	// TEST_NOT_SET is not in the environment; resolved to "" should be dropped
	// to avoid emitting "?key=" on the outbound URL.
	cfg := map[string]any{
		OverrideQueryParamsKey: map[string]any{
			"present": "literal",
			"missing": "${TEST_NOT_SET_XYZ}",
		},
	}

	ov := ExtractOverrides(cfg)
	require.NotNil(t, ov)
	_, missing := ov.QueryParams["missing"]
	assert.False(t, missing, "missing env var should be dropped, not stored as empty")
	assert.Equal(t, "literal", ov.QueryParams["present"])
}

func TestExtractOverrides_IgnoresNonStringValues(t *testing.T) {
	cfg := map[string]any{
		OverrideQueryParamsKey: map[string]any{
			"good":   "ok",
			"number": 42,         // ignored
			"bool":   true,       // ignored
			"nested": map[string]any{"a": "b"}, // ignored
		},
	}
	ov := ExtractOverrides(cfg)
	require.NotNil(t, ov)
	assert.Equal(t, map[string]string{"good": "ok"}, ov.QueryParams)
}

func TestExtractOverrides_ReturnsNilWhenAllResolveEmpty(t *testing.T) {
	cfg := map[string]any{
		OverrideQueryParamsKey: map[string]any{"k": "${UNSET_VAR_NAME_XYZZY}"},
	}
	assert.Nil(t, ExtractOverrides(cfg))
}

func TestApplyOverrides_DoesNotMutateInput(t *testing.T) {
	original := &domain.Webhook{
		Endpoint: "https://api.example.com/hook",
		Headers:  map[string]string{"X-Original": "keep"},
	}
	originalEndpoint := original.Endpoint
	originalHeaders := map[string]string{}
	maps.Copy(originalHeaders, original.Headers)

	ov := &Overrides{
		QueryParams: map[string]string{"key": "secret"},
		Headers:     map[string]string{"Authorization": "Bearer x"},
	}
	deliverable := ApplyOverrides(original, ov)

	// Original untouched
	assert.Equal(t, originalEndpoint, original.Endpoint)
	assert.Equal(t, originalHeaders, original.Headers)

	// Deliverable has the overrides applied
	assert.Contains(t, deliverable.Endpoint, "key=secret")
	assert.Equal(t, "Bearer x", deliverable.Headers["Authorization"])
	assert.Equal(t, "keep", deliverable.Headers["X-Original"])
}

func TestApplyOverrides_PreservesExistingQueryString(t *testing.T) {
	wh := &domain.Webhook{
		Endpoint: "https://chat.googleapis.com/v1/spaces/X/messages?messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD",
	}
	ov := &Overrides{
		QueryParams: map[string]string{"key": "K", "token": "T"},
	}
	deliverable := ApplyOverrides(wh, ov)

	u, err := url.Parse(deliverable.Endpoint)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD", q.Get("messageReplyOption"))
	assert.Equal(t, "K", q.Get("key"))
	assert.Equal(t, "T", q.Get("token"))
}

func TestApplyOverrides_EmptyOverridesReturnsCopy(t *testing.T) {
	wh := &domain.Webhook{Endpoint: "https://api.example.com/hook"}
	deliverable := ApplyOverrides(wh, nil)
	assert.Equal(t, wh.Endpoint, deliverable.Endpoint)

	deliverable = ApplyOverrides(wh, &Overrides{})
	assert.Equal(t, wh.Endpoint, deliverable.Endpoint)
}

func TestApplyOverrides_NilHeadersOnInput(t *testing.T) {
	wh := &domain.Webhook{
		Endpoint: "https://api.example.com/hook",
		Headers:  nil,
	}
	ov := &Overrides{Headers: map[string]string{"Authorization": "Bearer x"}}
	deliverable := ApplyOverrides(wh, ov)
	assert.Equal(t, "Bearer x", deliverable.Headers["Authorization"])
	// Input map remains nil (no implicit allocation)
	assert.Nil(t, wh.Headers)
}

func TestOverrides_IsEmpty(t *testing.T) {
	var nilOv *Overrides
	assert.True(t, nilOv.IsEmpty())
	assert.True(t, (&Overrides{}).IsEmpty())
	assert.False(t, (&Overrides{QueryParams: map[string]string{"k": "v"}}).IsEmpty())
	assert.False(t, (&Overrides{Headers: map[string]string{"k": "v"}}).IsEmpty())
}
