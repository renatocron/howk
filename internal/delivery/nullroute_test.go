package delivery_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/testutil"
)

var defaultHosts = config.DefaultConfig().Delivery.NullRouteHosts

type countingDeliverer struct{ calls atomic.Int32 }

func (c *countingDeliverer) Deliver(context.Context, *domain.Webhook) *delivery.Result {
	c.calls.Add(1)
	return &delivery.Result{StatusCode: 202}
}

func TestNullRouter_Matches(t *testing.T) {
	nr := delivery.NewNullRouter(nil, defaultHosts, []string{" Jaskjdakjkdjsa.com ", "teste.qualquercoisa", ""})

	for _, host := range []string{
		"sink.howk.test", "test", "foo.example", "a.b.invalid",
		"example.com", "www.example.org", "EXAMPLE.NET.",
		"jaskjdakjkdjsa.com", "api.jaskjdakjkdjsa.com", "teste.qualquercoisa",
	} {
		assert.True(t, nr.Matches(host), host)
	}
	for _, host := range []string{
		"localhost", "contest", "latest.io", "notexample.com", "example.com.br", "qualquercoisa", "api.grouplinkone.com", "",
	} {
		assert.False(t, nr.Matches(host), host)
	}
}

func TestNullRouter_Deliver(t *testing.T) {
	inner := &countingDeliverer{}
	nr := delivery.NewNullRouter(inner, defaultHosts)
	ctx := context.Background()

	cases := map[string]int{
		"https://sink.howk.test/":      200,
		"https://sink.howk.test":       200,
		"https://sink.howk.test/500":   500,
		"https://sink.howk.test/410/":  410,
		"https://sink.howk.test/999":   200,
		"https://sink.howk.test/a/500": 200,
		"http://foo.invalid:8080/hook": 200,
	}
	for endpoint, want := range cases {
		res := nr.Deliver(ctx, testutil.NewTestWebhook(endpoint))
		assert.Equal(t, want, res.StatusCode, endpoint)
		assert.NoError(t, res.Error, endpoint)
	}
	assert.Zero(t, inner.calls.Load())

	res := nr.Deliver(ctx, testutil.NewTestWebhook("http://localhost:8080/webhooks"))
	assert.Equal(t, 202, res.StatusCode)
	assert.Equal(t, int32(1), inner.calls.Load())
}
