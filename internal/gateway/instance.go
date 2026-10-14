package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	"minigateway/internal/metrics"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/server/middleware"
)

// Options fixes instance dependencies before it starts serving requests.
type Options struct {
	Channels       []dbmodel.Channel
	Client         *http.Client
	RetryTimes     int
	AttemptTimeout time.Duration
	TotalTimeout   time.Duration
	Billing        *billing.Engine
	Limiter        middleware.Limiter
	Metrics        *metrics.Registry
	LogRecorder    func(*dbmodel.RequestLog)
	Picker         func([]dbmodel.Credential, map[uint]bool, string, time.Time) (*dbmodel.Credential, bool)
	HealthReporter func(*dbmodel.Credential, int, string)
}

type Gateway struct {
	reg            *registry
	executor       *core.Executor
	retryTimes     int
	attemptTimeout time.Duration
	totalTimeout   time.Duration
	billing        *billing.Engine
	limiter        middleware.Limiter
	metrics        *metrics.Registry
	logRecorder    func(*dbmodel.RequestLog)
	picker         credentialPicker
	healthReporter credentialHealthReporter
}

func New(opts Options) (*Gateway, error) {
	if opts.AttemptTimeout <= 0 {
		opts.AttemptTimeout = 120 * time.Second
	}
	if opts.TotalTimeout <= 0 {
		opts.TotalTimeout = opts.AttemptTimeout
	}
	if opts.Metrics == nil {
		opts.Metrics = metrics.New()
	}
	g := &Gateway{executor: core.New(core.Options{Client: opts.Client}), retryTimes: opts.RetryTimes, attemptTimeout: opts.AttemptTimeout, totalTimeout: opts.TotalTimeout,
		billing: opts.Billing, limiter: opts.Limiter, metrics: opts.Metrics, logRecorder: opts.LogRecorder, picker: opts.Picker, healthReporter: opts.HealthReporter}
	if err := g.initRegistry(opts.Channels); err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}
func (g *Gateway) Close()                     { g.executor.Close() }
func (g *Gateway) Metrics() *metrics.Registry { return g.metrics }

type Identity struct {
	TokenID       uint
	UserID        uint
	TokenName     string
	RequestID     string
	AllowedModels []string
}

type AttemptResult struct {
	ChannelID    uint
	CredentialID uint
	Model        string
	core.Result
}

type RequestResult struct {
	Identity      Identity
	ChannelID     uint
	CredentialID  uint
	ChannelName   string
	ActualModel   string
	ChannelGroup  string
	ErrorMessage  string
	Attempts      []AttemptResult
	TotalDuration time.Duration
	core.Result
}

type resultContextKey struct{}

func ResultFromContext(ctx context.Context) *RequestResult {
	result, _ := ctx.Value(resultContextKey{}).(*RequestResult)
	return result
}

// state is an HTTP boundary helper. Relay bookkeeping lives in a typed context
// value, never in Gin's string map. Authentication is read once at this boundary.
func state(c *gin.Context) *RequestResult {
	if c.Request == nil {
		c.Request = &http.Request{Header: make(http.Header)}
	}
	if result := ResultFromContext(c.Request.Context()); result != nil {
		return result
	}
	allowed, _ := c.Get(middleware.CtxAllowedModel)
	models, _ := allowed.([]string)
	result := &RequestResult{Result: core.Result{StatusCode: 500}, Identity: Identity{
		TokenID: ctxUint(c, middleware.CtxTokenID), UserID: ctxUint(c, middleware.CtxUserID), TokenName: c.GetString(middleware.CtxTokenName), RequestID: c.GetString(middleware.CtxRequestID), AllowedModels: append([]string(nil), models...),
	}}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), resultContextKey{}, result))
	return result
}
