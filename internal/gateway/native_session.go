package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"minigateway/internal/access"
	admission "minigateway/internal/foundation/contracts/admission/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	"minigateway/internal/model"
)

// These assembly capabilities are not public feature contracts. Only the
// composition root supplies codecs and transports; L3 sees contract.Service.
type NativeWSCreate struct {
	Model, PreviousID, PromptText string
	Store                         bool
	Initial                       v2.Frame
}
type NativeWSResource interface {
	Dial(context.Context, string) (v2.Connection, error)
	Close(context.Context) error
}

// BoundNativeWSResource dials the exact immutable selected target.
type BoundNativeWSResource interface {
	DialTarget(context.Context, string, string) (v2.Connection, error)
}
type NativeWSOptions struct {
	Authority     identity.Authority
	Gate          admission.RequestGate
	Models        []string
	Limits        contract.Limits
	Prepare       func(context.Context) (NativeWSResource, error)
	Parse         func(v2.Frame) (NativeWSCreate, error)
	Decoder       func(string) v2.FrameDecoder
	UsageDecoder  func(string) func(v2.Frame) (v3.Envelope, error)
	Encode        func(v2.Frame, string) (v2.Frame, error)
	StoredHistory history.StoredRounds
	EncodeStored  func(v2.Frame, string) (v2.Frame, error)
}

type NativeWebSocketService struct {
	gateway   *Gateway
	options   NativeWSOptions
	allowed   map[string]bool
	resources *websocketResources
	capacity  *access.ConnectionCapacity
	ctx       context.Context
	cancel    context.CancelFunc
	publishMu sync.Mutex
	mu        sync.Mutex
	stopping  bool
	sessions  map[v2.ConnectionID]*nativeSession
}

func NewNativeWebSocketService(ctx context.Context, g *Gateway, opts NativeWSOptions) (*NativeWebSocketService, error) {
	if ctx == nil || g == nil || !nativeBillingAllowed(g) || nilInterface(g.admission) || nilInterface(opts.Authority) || nilInterface(opts.Gate) || opts.Prepare == nil || opts.Parse == nil || opts.Decoder == nil || opts.Encode == nil {
		return nil, errors.New("native websocket dependencies unavailable")
	}
	if !nilInterface(opts.StoredHistory) && opts.EncodeStored == nil {
		return nil, errors.New("stored native websocket requires its registered encoder")
	}
	limits := opts.Limits
	if limits.MaxMessageBytes < 1 || limits.MaxMessageBytes > 256<<10 || limits.IdleTimeout <= 0 || limits.IdleTimeout > 60*time.Second || limits.RoundTimeout <= 0 || limits.RoundTimeout > 120*time.Second || limits.MaxLifetime < limits.RoundTimeout || limits.MaxLifetime > 55*time.Minute {
		return nil, errors.New("invalid native websocket limits")
	}
	allowed := map[string]bool{}
	if len(opts.Models) == 0 || len(opts.Models) > 64 {
		return nil, errors.New("native websocket requires explicit models")
	}
	for _, m := range opts.Models {
		if m == "" || strings.TrimSpace(m) != m || len(m) > 200 || strings.ContainsAny(m, "*\r\n") || allowed[m] {
			return nil, errors.New("native websocket requires distinct explicit models")
		}
		allowed[m] = true
	}
	capacity, err := access.NewConnectionCapacity(access.ConnectionLimits{Instance: 64, User: 4, Key: 2})
	if err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(ctx)
	opts.Models = append([]string(nil), opts.Models...)
	return &NativeWebSocketService{gateway: g, options: opts, allowed: allowed, resources: newWebSocketResources(), capacity: capacity, ctx: root, cancel: cancel, sessions: make(map[v2.ConnectionID]*nativeSession)}, nil
}
func (s *NativeWebSocketService) Limits() contract.Limits { return s.options.Limits }
func (s *NativeWebSocketService) Open(ctx context.Context, in contract.Open) (contract.Session, *contract.Problem) {
	if ctx == nil || in.Binding.UserID == 0 || in.Binding.TokenID == 0 || in.ConnectionID == "" {
		return nil, nativeProblem("unauthorized", 1008)
	}
	current, trusted := s.options.Authority.Current(ctx)
	if !trusted || current.UserID != in.Binding.UserID || current.TokenID != in.Binding.TokenID {
		return nil, nativeProblem("unauthorized", 1008)
	}
	_, principal, err := s.options.Authority.Refresh(ctx, in.Binding)
	if err != nil || principal.UserID != in.Binding.UserID || principal.TokenID != in.Binding.TokenID {
		return nil, nativeProblem("unauthorized", 1008)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || s.ctx.Err() != nil {
		return nil, nativeProblem("draining", 1001)
	}
	if !nativeBillingAllowed(s.gateway) {
		return nil, nativeProblem("unavailable", 1013)
	}
	if s.sessions[in.ConnectionID] != nil {
		return nil, nativeProblem("invalid_request", 1008)
	}
	release, err := s.capacity.Acquire(ctx, in.Binding)
	if err != nil {
		return nil, nativeProblem("rate_limited", 1013)
	}
	lifetime, cancel := context.WithTimeout(s.ctx, s.options.Limits.MaxLifetime)
	n := &nativeSession{service: s, binding: in.Binding, id: in.ConnectionID, ctx: lifetime, cancel: cancel, releaseConnection: release, done: make(chan struct{}), closeGate: make(chan struct{}, 1)}
	s.sessions[in.ConnectionID] = n
	go func() { <-lifetime.Done(); _ = n.Close(context.Background()) }()
	return n, nil
}
func (s *NativeWebSocketService) Stop() {
	s.mu.Lock()
	s.stopping = true
	s.resources.mu.Lock()
	s.resources.draining = true
	s.resources.signalLocked()
	s.resources.mu.Unlock()
	for _, n := range s.sessions {
		n.mu.Lock()
		if n.problem == nil {
			n.problem = nativeProblem("draining", 1001)
		}
		n.mu.Unlock()
	}
	s.mu.Unlock()
	s.capacity.Close()
	s.cancel()
}
func (s *NativeWebSocketService) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("missing websocket drain context")
	}
	s.Stop()
	s.mu.Lock()
	sessions := make([]*nativeSession, 0, len(s.sessions))
	for _, n := range s.sessions {
		sessions = append(sessions, n)
	}
	s.mu.Unlock()
	for _, n := range sessions {
		if err := n.Close(ctx); err != nil {
			return err
		}
	}
	return s.resources.drain(ctx)
}

type nativeSession struct {
	service                   *NativeWebSocketService
	binding                   identity.Binding
	id                        v2.ConnectionID
	ctx                       context.Context
	cancel                    context.CancelFunc
	releaseConnection         func()
	done                      chan struct{}
	closeGate                 chan struct{}
	mu                        sync.Mutex
	closed, finalized, active bool
	activeCancel              context.CancelFunc
	activeDone                chan struct{}
	problem                   *contract.Problem
	connection                v2.Connection
	lease                     v2.Lease
	generation                *nativeGeneration
	target                    ResponseTarget
	credential                model.Credential
	model                     string
	latestID                  string
	previousTotal             *int64
}

func (n *nativeSession) Done() <-chan struct{} { return n.done }
func (n *nativeSession) Problem() *contract.Problem {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.problem == nil {
		return nil
	}
	p := *n.problem
	return &p
}
func (n *nativeSession) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("missing websocket close context")
	}
	select {
	case n.closeGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-n.closeGate }()
	n.mu.Lock()
	if n.finalized {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	if n.problem == nil {
		n.problem = nativeProblem("session_expired", 1000)
	}
	n.cancel()
	if n.activeCancel != nil {
		n.activeCancel()
	}
	active := n.activeDone
	n.mu.Unlock()
	if active != nil {
		select {
		case <-active:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !nilInterface(n.connection) {
		if err := n.connection.Close(); err != nil {
			return err
		}
		n.connection = nil
	}
	if n.lease != nil {
		if err := n.lease.Release(); err != nil {
			return err
		}
		n.lease = nil
	}
	n.releaseConnection()
	n.service.mu.Lock()
	delete(n.service.sessions, n.id)
	n.service.mu.Unlock()
	n.mu.Lock()
	n.finalized = true
	close(n.done)
	n.mu.Unlock()
	return nil
}
func nativeProblem(code string, closeCode uint16) *contract.Problem {
	return &contract.Problem{Code: code, CloseCode: closeCode}
}

var _ contract.Service = (*NativeWebSocketService)(nil)
var _ contract.Session = (*nativeSession)(nil)

func cloneNativeProblem(in *contract.Problem) *contract.Problem {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
