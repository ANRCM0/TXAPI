package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	executionv2 "minigateway/internal/foundation/contracts/execution/v2"
	executionv3 "minigateway/internal/foundation/contracts/execution/v3"
	quotation "minigateway/internal/foundation/contracts/quotation/v1"
	usagehistory "minigateway/internal/foundation/contracts/usagehistory/v1"
	"minigateway/internal/metrics"
	dbmodel "minigateway/internal/model"
)

// Options fixes instance dependencies before it starts serving requests.
type Options struct {
	Admission Admission
	// Registrations are copied once; an absent registration never falls back.
	Protocols      []execution.ProtocolRegistration
	Operations     []execution.OperationRegistration
	Channels       []dbmodel.Channel
	Client         *http.Client
	RetryTimes     int
	AttemptTimeout time.Duration
	TotalTimeout   time.Duration
	Billing        LegacyBilling
	// StrictBilling can be supplied independently. For existing callers, Billing
	// is also used when it implements StrictBilling. Strict never uses legacy fees.
	StrictBilling      StrictBilling
	BillingMode        billing.Mode
	Limiter            access.Limiter
	Metrics            *metrics.Registry
	LogRecorder        func(*dbmodel.RequestLog)
	RouteDiagnostics   bool
	ShadowRouteScoring bool
	UsageHistory       usagehistory.Recorder
	UsageQuotation     quotation.Reader
	Picker             func([]dbmodel.Credential, map[uint]bool, string, time.Time) (*dbmodel.Credential, bool)
	HealthReporter     func(*dbmodel.Credential, int, string)
}

type Gateway struct {
	shadow          *shadowSampler
	appliedEpoch    atomic.Uint64
	shadowNow       func() time.Time
	shadowLogMu     sync.Mutex
	shadowLastLog   time.Time
	routeHistory    *routeHistory
	admission       Admission
	adaptors        map[string]execution.Adapter
	operations      map[execution.Operation]execution.OperationRegistration
	reg             *registry
	executor        *core.Executor
	retryTimes      int
	attemptTimeout  time.Duration
	totalTimeout    time.Duration
	billing         LegacyBilling
	strictBilling   StrictBilling
	billingMode     billing.Mode
	limiter         access.Limiter
	metrics         *metrics.Registry
	logRecorder     func(*dbmodel.RequestLog)
	picker          credentialPicker
	healthReporter  credentialHealthReporter
	usageHistory    usagehistory.Recorder
	usageQuotation  quotation.Reader
	recordingMu     sync.Mutex
	recordingClosed bool
	recordingActive sync.WaitGroup
}

func New(opts Options) (*Gateway, error) {
	if len(opts.Operations) == 0 || len(opts.Protocols) == 0 {
		return nil, fmt.Errorf("operation and protocol registrations are required")
	}
	if opts.BillingMode == "" {
		opts.BillingMode = billing.ModeLegacy
	}
	if err := opts.BillingMode.Validate(); err != nil {
		return nil, err
	}
	if opts.UsageHistory != nil && nilInterface(opts.UsageHistory) || opts.UsageQuotation != nil && nilInterface(opts.UsageQuotation) {
		return nil, fmt.Errorf("invalid usage history dependency: typed nil")
	}
	if opts.UsageHistory != nil && opts.BillingMode != billing.ModeDisabled {
		return nil, fmt.Errorf("usage history requires disabled billing")
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
	protocols := make(map[string]execution.Adapter, len(opts.Protocols))
	for _, registration := range opts.Protocols {
		if registration.ID == "" || registration.Version != execution.Version || nilInterface(registration.Adapter) {
			return nil, fmt.Errorf("invalid adaptor registration %q", registration.ID)
		}
		if _, duplicate := protocols[registration.ID]; duplicate {
			return nil, fmt.Errorf("duplicate protocol %q", registration.ID)
		}
		protocols[registration.ID] = registration.Adapter
	}
	operations := make(map[execution.Operation]execution.OperationRegistration, len(opts.Operations))
	for _, registration := range opts.Operations {
		if registration.ID == "" || registration.Version != execution.Version || registration.Parse == nil {
			return nil, fmt.Errorf("invalid operation registration %q", registration.ID)
		}
		if registration.Continuity && !registration.NonReplayable || registration.OpaqueErrors && (!registration.NonReplayable ||
			registration.Errors.Upstream == "" || registration.Errors.Canceled == "" || registration.Errors.Deadline == "" || registration.Errors.Output == "" || registration.Errors.Generation == "") {
			return nil, fmt.Errorf("incomplete operation safety registration %q", registration.ID)
		}
		if _, duplicate := operations[registration.ID]; duplicate {
			return nil, fmt.Errorf("duplicate operation %q", registration.ID)
		}
		operations[registration.ID] = registration
	}
	if opts.Metrics == nil {
		opts.Metrics = metrics.New()
	}
	g := &Gateway{admission: opts.Admission, adaptors: protocols, operations: operations, executor: core.New(core.Options{Client: opts.Client}), retryTimes: opts.RetryTimes, attemptTimeout: opts.AttemptTimeout, totalTimeout: opts.TotalTimeout,
		billing: opts.Billing, strictBilling: opts.StrictBilling, billingMode: opts.BillingMode, limiter: opts.Limiter, metrics: opts.Metrics, logRecorder: opts.LogRecorder, picker: opts.Picker, healthReporter: opts.HealthReporter,
		usageHistory: opts.UsageHistory, usageQuotation: opts.UsageQuotation}
	if err := g.initRegistry(opts.Channels); err != nil {
		g.Close()
		return nil, err
	}
	if opts.RouteDiagnostics {
		g.routeHistory = &routeHistory{now: time.Now}
		if opts.ShadowRouteScoring {
			g.shadow = newShadowSampler(&g.appliedEpoch)
			g.shadowNow = time.Now
		}
	}
	return g, nil
}
func (g *Gateway) Close() {
	if g.shadow != nil {
		g.shadow.Close()
	}
	if g.usageHistory != nil {
		g.recordingMu.Lock()
		g.recordingClosed = true
		g.recordingMu.Unlock()
		g.recordingActive.Wait()
	}
	g.executor.Close()
}
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
	Operation    execution.Operation
	ChannelID    uint
	CredentialID uint
	Model        string
	core.Result
}

type RequestResult struct {
	selectedEpoch      uint64
	UsageRecordID      string
	RecordingState     string
	ResponseTarget     ResponseTarget
	ResponseStatus     string
	UpstreamResponseID string
	nativeFailed       bool
	protocolFacts      []executionv2.AttemptFacts
	usageFacts         []executionv3.AttemptUsage
	Operation          execution.Operation
	reservation        *billing.PreDeduct
	strictRequest      *billing.StrictRequest
	BillingState       string
	BillingID          string
	PriceVersion       string
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
