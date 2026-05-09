package delivery

import (
	"maps"
	"net/url"
	"os"

	"github.com/howk/howk/internal/domain"
)

// Reserved keys read from a script's ScriptConfig map. Values under these keys
// are not exposed to Lua as part of the user-facing config; they describe
// late-bound mutations applied by the worker just before HTTP send so that
// secrets (API keys, tokens) never enter the Webhook record stored on Kafka.
const (
	OverrideQueryParamsKey = "_delivery_query_params"
	OverrideHeadersKey     = "_delivery_headers"
)

// Overrides carries delivery-time mutations that are applied to a transient
// copy of the Webhook just before HTTP send. Both maps hold values already
// resolved against the worker's process environment.
type Overrides struct {
	QueryParams map[string]string
	Headers     map[string]string
}

// IsEmpty reports whether the override has nothing to apply.
func (o *Overrides) IsEmpty() bool {
	return o == nil || (len(o.QueryParams) == 0 && len(o.Headers) == 0)
}

// ExtractTemplates reads the reserved override keys from a script_config map
// and returns the raw ${VAR_NAME} templates UNRESOLVED. Used by the
// transformer when attaching templates to an emitted Webhook so the worker
// resolves env vars at HTTP-send time (templates are not secrets and may
// safely be persisted on Kafka).
//
// Non-string values under the override keys are silently skipped.
func ExtractTemplates(scriptConfig map[string]any) (queryParams, headers map[string]string) {
	queryParams = templateMap(scriptConfig[OverrideQueryParamsKey])
	headers = templateMap(scriptConfig[OverrideHeadersKey])
	return
}

// ResolveTemplates env-expands raw templates into a ready-to-apply Overrides.
// Empty resolutions are dropped so a missing env var does not produce e.g.
// "?key=" on the outbound URL. Returns nil when nothing resolves.
func ResolveTemplates(qp, hd map[string]string) *Overrides {
	rqp := resolveStringValues(qp)
	rhd := resolveStringValues(hd)
	if len(rqp) == 0 && len(rhd) == 0 {
		return nil
	}
	return &Overrides{QueryParams: rqp, Headers: rhd}
}

// ExtractOverrides combines ExtractTemplates + ResolveTemplates for callers
// that hold script_config in the wire shape (map[string]any), e.g. the
// loader-based fallback path in the worker.
func ExtractOverrides(scriptConfig map[string]any) *Overrides {
	qp, hd := ExtractTemplates(scriptConfig)
	return ResolveTemplates(qp, hd)
}

func templateMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, raw := range m {
		if s, ok := raw.(string); ok {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func resolveStringValues(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		resolved := os.Expand(v, os.Getenv)
		if resolved == "" {
			continue
		}
		out[k] = resolved
	}
	return out
}

// ApplyOverrides returns a shallow copy of webhook with the override query
// params merged into its Endpoint URL and the override headers merged into a
// freshly-allocated Headers map. The input webhook is not mutated, so callers
// can safely keep using it for storage operations (DeliveryResult, retry data,
// state snapshots) — those continue to carry the bare endpoint that was
// persisted on Kafka.
//
// If the endpoint URL cannot be parsed, ApplyOverrides returns the webhook
// copy with override headers applied but the endpoint unchanged; the caller's
// HTTP layer will surface the malformed URL on its own.
func ApplyOverrides(webhook *domain.Webhook, ov *Overrides) domain.Webhook {
	deliverable := *webhook
	if ov.IsEmpty() {
		return deliverable
	}

	if len(ov.QueryParams) > 0 {
		if u, err := url.Parse(deliverable.Endpoint); err == nil {
			q := u.Query()
			for k, v := range ov.QueryParams {
				q.Set(k, v)
			}
			u.RawQuery = q.Encode()
			deliverable.Endpoint = u.String()
		}
	}

	if len(ov.Headers) > 0 {
		merged := make(map[string]string, len(webhook.Headers)+len(ov.Headers))
		maps.Copy(merged, webhook.Headers)
		maps.Copy(merged, ov.Headers)
		deliverable.Headers = merged
	}

	return deliverable
}
