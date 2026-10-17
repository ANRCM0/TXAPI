package gateway

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/model"
	"minigateway/internal/server/middleware"
	"net/http"
	"time"
)

// Temporary bridge for legacy tests; normal production attempts use core.Execute.
type responseTransport struct{ response *http.Response }

func (t responseTransport) RoundTrip(*http.Request) (*http.Response, error) { return t.response, nil }
func relayStream(c *gin.Context, a adaptor.Adaptor, resp *http.Response, m *meta.Meta) (*model.Usage, error) {
	e := core.New(core.Options{Client: &http.Client{Transport: responseTransport{response: resp}}})
	ctx := context.Background()
	if c.Request != nil {
		ctx = c.Request.Context()
	}
	result := e.Execute(ctx, c.Writer, core.Input{URL: "http://legacy-response.test", Stream: true, Decoder: protocol.NewDecoder(a, m)})
	if result.Error != nil {
		return result.Usage, errors.New(result.Error.Error.Message)
	}
	return result.Usage, nil
}

func (g *Gateway) SetTimeout(d time.Duration) { g.attemptTimeout = d }
func (g *Gateway) SetBilling(e *billing.Engine) {
	g.billing, g.strictBilling = e, e
	if e == nil {
		g.billing, g.strictBilling = nil, nil
	}
}
func (g *Gateway) SetRateLimiter(l middleware.Limiter)               { g.limiter = l }
func (g *Gateway) SetLogRecorder(fn func(entry *dbmodel.RequestLog)) { g.logRecorder = fn }
func (g *Gateway) SetRetryTimes(n int)                               { g.retryTimes = n }
func (g *Gateway) SetCredentialPicker(fn credentialPicker)           { g.picker = fn }
func (g *Gateway) SetHealthReporter(fn credentialHealthReporter)     { g.healthReporter = fn }
