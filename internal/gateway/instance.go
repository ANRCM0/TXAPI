package gateway

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"time"

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
	BillingMode    billing.Mode
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
	billingMode    billing.Mode
	limiter        access.Limiter
	metrics        *metrics.Registry
	logRecorder    func(*dbmodel.RequestLog)
	picker         credentialPicker
	healthReporter credentialHealthReporter
}

func New(opts Options) (*Gateway, error) {
	if opts.BillingMode == "" {
		opts.BillingMode = billing.ModeLegacy
	}
	if err := opts.BillingMode.Validate(); err != nil {
		return nil, err
	}
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
		billing: opts.Billing, billingMode: opts.BillingMode, limiter: opts.Limiter, metrics: opts.Metrics, logRecorder: opts.LogRecorder, picker: opts.Picker, healthReporter: opts.HealthReporter}
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
	reservation   *billing.PreDeduct
	strictRequest *billing.StrictRequest
	BillingState  string
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
