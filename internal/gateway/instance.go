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
	"minigateway/internal/protocol"
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
	Billing        LegacyBilling
	// StrictBilling can be supplied independently. For existing callers, Billing
	// is also used when it implements StrictBilling. Strict never uses legacy fees.
	StrictBilling  StrictBilling
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
	billing        LegacyBilling
	strictBilling  StrictBilling
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
	if opts.Admission != nil && nilInterface(opts.Admission) {
		return nil, fmt.Errorf("invalid gateway admission: typed nil dependency")
	}
	if opts.Limiter != nil && nilInterface(opts.Limiter) {
		return nil, fmt.Errorf("invalid gateway limiter: typed nil dependency")
	}
	if nilBilling(opts.Billing) {
		opts.Billing = nil
	}
	if nilBilling(opts.StrictBilling) {
		opts.StrictBilling = nil
	}
	if opts.StrictBilling == nil {
		opts.StrictBilling, _ = opts.Billing.(StrictBilling)
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
		if name == "" || nilInterface(a) {
			return nil, fmt.Errorf("invalid adaptor registration %q", name)
		}
	}
	if opts.Metrics == nil {
		opts.Metrics = metrics.New()
	}
	g := &Gateway{admission: opts.Admission, adaptors: maps.Clone(opts.Adaptors), executor: core.New(core.Options{Client: opts.Client}), retryTimes: opts.RetryTimes, attemptTimeout: opts.AttemptTimeout, totalTimeout: opts.TotalTimeout,
		billing: opts.Billing, strictBilling: opts.StrictBilling, billingMode: opts.BillingMode, limiter: opts.Limiter, metrics: opts.Metrics, logRecorder: opts.LogRecorder, picker: opts.Picker, healthReporter: opts.HealthReporter}
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
	Operation    protocol.Operation
	ChannelID    uint
	CredentialID uint
	Model        string
	core.Result
}

type RequestResult struct {
	ResponseTarget     ResponseTarget
	ResponseStatus     string
	UpstreamResponseID string
	Operation          protocol.Operation
	reservation        *billing.PreDeduct
	strictRequest      *billing.StrictRequest
	BillingState       string
	Identity           Identity
	ChannelID          uint
	CredentialID       uint
	ChannelName        string
	ActualModel        string
	ChannelGroup       string
	ErrorMessage       string
	Attempts           []AttemptResult
	TotalDuration      time.Duration
	core.Result
}

type resultContextKey struct{}

func ResultFromContext(ctx context.Context) *RequestResult {
	result, _ := ctx.Value(resultContextKey{}).(*RequestResult)
	return result
}
