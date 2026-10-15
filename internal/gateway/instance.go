package gateway

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	"minigateway/internal/metrics"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol/adaptor"
)

// Options fixes instance dependencies before it starts serving requests.
type Options struct {
	Admission Admission
	// Adaptors extends the built-in protocols during construction, not at runtime.
	Adaptors       map[string]adaptor.Adaptor
	Channels       []dbmodel.Channel
	Client         *http.Client
	RetryTimes     int
	AttemptTimeout time.Duration
	TotalTimeout   time.Duration
	Billing        *billing.Engine
	Limiter        access.Limiter
	Metrics        *metrics.Registry
	LogRecorder    func(*dbmodel.RequestLog)
	Picker         func([]dbmodel.Credential, map[uint]bool, string, time.Time) (*dbmodel.Credential, bool)
	HealthReporter func(*dbmodel.Credential, int, string)
}

type Gateway struct {
	admission      Admission
	adaptors       map[string]adaptor.Adaptor
	reg            *registry
	executor       *core.Executor
	retryTimes     int
	attemptTimeout time.Duration
	totalTimeout   time.Duration
	billing        *billing.Engine
	limiter        access.Limiter
	metrics        *metrics.Registry
	logRecorder    func(*dbmodel.RequestLog)
	picker         credentialPicker
	healthReporter credentialHealthReporter
}

func New(opts Options) (*Gateway, error) {
	if opts.RetryTimes < 0 || opts.RetryTimes > 10 || opts.AttemptTimeout < 0 || opts.TotalTimeout < 0 {
		return nil, fmt.Errorf("invalid gateway retry or timeout configuration")
	}
	if opts.AttemptTimeout <= 0 {
		opts.AttemptTimeout = 120 * time.Second
	}
	if opts.TotalTimeout <= 0 {
		opts.TotalTimeout = opts.AttemptTimeout
	}
	for name, a := range opts.Adaptors {
		if name == "" || a == nil {
			return nil, fmt.Errorf("invalid adaptor registration %q", name)
		}
	}
	if opts.Metrics == nil {
		opts.Metrics = metrics.New()
	}
	g := &Gateway{admission: opts.Admission, adaptors: maps.Clone(opts.Adaptors), executor: core.New(core.Options{Client: opts.Client}), retryTimes: opts.RetryTimes, attemptTimeout: opts.AttemptTimeout, totalTimeout: opts.TotalTimeout,
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
	Policy        *access.Policy
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
	identity := Identity{RequestID: access.RequestID(c.Request.Context())}
	if p, ok := access.PrincipalFromContext(c.Request.Context()); ok {
		identity.UserID = p.UserID
		identity.TokenID = p.TokenID
		identity.TokenName = p.TokenName
		identity.AllowedModels = append([]string(nil), p.Policy.Models...)
		policy := p.Policy
		identity.Policy = &policy
	}
	result := &RequestResult{Result: core.Result{StatusCode: 500}, Identity: identity}

	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), resultContextKey{}, result))
	return result
}
