package delivery

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/domain"
)

// NullRouter answers webhooks for null-routed hosts without touching the network.
// A host entry matches itself and every subdomain. A path of "/NNN" sets the
// returned status (e.g. https://sink.howk.test/500), otherwise it is 200.
type NullRouter struct {
	inner Deliverer
	hosts []string
}

var _ Deliverer = (*NullRouter)(nil)

func NewNullRouter(inner Deliverer, hostLists ...[]string) *NullRouter {
	var hosts []string
	for _, list := range hostLists {
		for _, h := range list {
			h = strings.Trim(strings.ToLower(strings.TrimSpace(h)), ".")
			if h != "" {
				hosts = append(hosts, h)
			}
		}
	}
	return &NullRouter{inner: inner, hosts: hosts}
}

func (n *NullRouter) Deliver(ctx context.Context, webhook *domain.Webhook) *Result {
	u, err := url.Parse(webhook.Endpoint)
	if err != nil || !n.Matches(u.Hostname()) {
		return n.inner.Deliver(ctx, webhook)
	}

	status := 200
	if code, err := strconv.Atoi(strings.Trim(u.Path, "/")); err == nil && code >= 100 && code <= 599 {
		status = code
	}

	log.Debug().
		Str("webhook_id", string(webhook.ID)).
		Str("endpoint", webhook.Endpoint).
		Int("status_code", status).
		Msg("Null-routed delivery")

	return &Result{
		StatusCode:   status,
		ResponseBody: `{"null_route":true}`,
		ContentType:  "application/json",
	}
}

func (n *NullRouter) Matches(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, h := range n.hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}
