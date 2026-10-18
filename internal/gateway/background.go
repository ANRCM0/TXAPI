package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"path"
	"strings"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	bg "minigateway/internal/foundation/contracts/background/v1"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

type BackgroundRemoteOptions struct {
	Registrations []bg.Registration
	Channels      map[uint]string
}
type BackgroundRemote struct {
	gateway       *Gateway
	registrations map[string]bg.Registration
	channels      map[uint]string
	epoch         string
}

func (g *Gateway) BillingMode() string { return string(g.billingMode) }
func NewBackgroundRemote(g *Gateway, o BackgroundRemoteOptions) (*BackgroundRemote, error) {
	if g == nil || g.billingMode != billing.ModeDisabled {
		return nil, bg.ErrUnsupportedBilling
	}
	if len(o.Registrations) == 0 || len(o.Channels) == 0 {
		return nil, bg.ErrUnsupportedCapability
	}
	var nonce [32]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		return nil, bg.ErrUnavailable
	}
	r := &BackgroundRemote{gateway: g, registrations: map[string]bg.Registration{}, channels: map[uint]string{}, epoch: hex.EncodeToString(nonce[:])}
	for _, v := range o.Registrations {
		if v.ID == "" || v.Version != bg.Version || v.Operation == "" || nilInterface(v.Parse) || nilInterface(v.Codec) {
			return nil, bg.ErrUnsupportedCapability
		}
		if _, exists := r.registrations[v.ID]; exists {
			return nil, bg.ErrConflict
		}
		r.registrations[v.ID] = v
	}
	for id, v := range o.Channels {
		if id == 0 {
			return nil, bg.ErrUnsupportedCapability
		}
		if _, ok := r.registrations[v]; !ok {
			return nil, bg.ErrUnsupportedCapability
		}
		r.channels[id] = v
	}
	for id := range r.channels {
		found := false
		for _, ch := range g.reg.allChannels() {
			if uint(ch.id) == id {
				found = true
				break
			}
		}
		if !found {
			return nil, bg.ErrUnsupportedCapability
		}
	}
	return r, nil
}
func (r *BackgroundRemote) Execute(ctx context.Context, call bs.RemoteCall, progress bs.Progress) error {
	if r == nil || r.gateway.billingMode != billing.ModeDisabled {
		return bg.ErrUnsupportedBilling
	}
	if nilInterface(progress) || progress.Validate(ctx, call) != nil {
		return bs.ErrClaimLost
	}
	g := r.gateway
	var req execution.Request
	requests := make(map[int]execution.Request)
	var candidates []*channel
	if call.Action == bg.RemoteSubmit {
		if call.Target != nil || call.RemoteID != "" {
			return bg.ErrConflict
		}
		p, ok := access.PrincipalFromContext(ctx)
		if !ok || !p.Policy.Allows(call.Model) {
			return bg.ErrUnauthorized
		}
		for _, ch := range g.pickChannels(call.Model, nil) {
			registration, ok := r.registrations[r.channels[uint(ch.id)]]
			if !ok || registration.Operation != call.Operation {
				continue
			}
			parsed, e := registration.Parse.ParseRequest(call.Operation, call.Body)
			if e != nil || nilInterface(parsed) || parsed.Model() != call.Model || parsed.Operation() != call.Operation || parsed.Stream() != call.Stream || !bytes.Equal(parsed.Body(), call.Body) || parsed.Requirements().Transport != execution.Background || parsed.Requirements().Operation != call.Operation || parsed.Requirements().Stream != parsed.Stream() {
				continue
			}
			requests[ch.id] = ownedRequest{operation: parsed.Operation(), body: bytes.Clone(call.Body), model: parsed.Model(), stream: parsed.Stream(), requirements: parsed.Requirements(), maxTokens: parsed.LegacyMaxTokens(), prompt: parsed.PromptText(), affinity: parsed.AffinityText(), completion: func([]byte) string { return "" }}
			candidates = append(candidates, ch)
		}
		if len(candidates) == 0 {
			return bg.ErrUnsupportedCapability
		}
		release, e := backgroundAdmission(ctx, g.admission, AdmissionScope{Level: RequestAdmission, UserID: p.UserID, TokenID: p.TokenID, Model: call.Model, Operation: call.Operation})
		if e != nil {
			return e
		}
		defer release()
	} else {
		if call.Target == nil || call.RemoteID == "" || call.Action != bg.RemoteInspect && call.Action != bg.RemoteCancel && call.Action != bg.RemoteResume {
			return bg.ErrConflict
		}
		candidates = g.responseChannels(g.reg.allChannels(), call.Model, call.Target)
	}
	var ch *channel
	var credentialID uint
	var target execution.Target
	var credential model.Credential
	if call.Action == bg.RemoteSubmit {
		selected, cred := g.selectTargetContext(ctx, candidates, map[uint]bool{}, call.PublicID, time.Now())
		if selected == nil {
			return bg.ErrUnavailable
		}
		ch = selected
		req = requests[ch.id]
		credentialID = cred.ID
		credential = *cred
		target = *buildMeta(ch, cred, req, call.PublicID)
	} else {
		for _, v := range candidates {
			if _, ok := r.registrations[r.channels[uint(v.id)]]; !ok {
				continue
			}
			if len(v.credentials) != 1 {
				continue
			}
			ch = v
			cred := v.credentials[0]
			credentialID = cred.ID
			credential = cred
			target = execution.Target{ChannelID: ch.id, ChannelName: ch.name, ChannelType: ch.typ, BaseURL: ch.baseURL, APIKey: cred.APIKey, OriginModel: call.Model, ActualModel: call.Target.ActualModel, Operation: call.Operation, ResponseID: call.PublicID, CredentialIDValue: cred.ID}
			break
		}
		if ch == nil {
			return bg.ErrUnavailable
		}
	}
	registration := r.registrations[r.channels[uint(ch.id)]]
	if registration.Operation != call.Operation {
		return bg.ErrUnsupportedCapability
	}
	prepared, problem := registration.Codec.Prepare(bg.RemoteCommand{Action: call.Action, Body: call.Body, RemoteID: call.RemoteID, After: call.After}, target)
	stream := call.Action == bg.RemoteResume || call.Action == bg.RemoteSubmit && call.Stream
	if problem != nil || nilInterface(prepared.Progress) || nilInterface(prepared.Input.Decoder) || prepared.Input.Replayable || prepared.Input.Stream != stream || !backgroundWireBound(prepared.Input, target, call.Action == bg.RemoteResume) {
		return bg.ErrUnsupportedCapability
	}
	var release func()
	var e error
	if call.Action == bg.RemoteSubmit {
		p, _ := access.PrincipalFromContext(ctx)
		release, e = backgroundAdmission(ctx, g.admission, AdmissionScope{Level: AttemptAdmission, Operation: call.Operation, UserID: p.UserID, TokenID: p.TokenID, Model: call.Model, ChannelID: uint(ch.id), CredentialID: credentialID, Attempt: 1})
	} else if g.admission == nil {
		release = func() {}
	} else if control, ok := g.admission.(interface {
		acquireBackgroundControl(context.Context, uint) (func(), error)
	}); ok {
		release, e = control.acquireBackgroundControl(ctx, uint(ch.id))
	} else {
		return bg.ErrUnavailable
	}
	if e != nil {
		if release != nil {
			release()
		}
		return e
	}
	if release == nil {
		return bg.ErrUnavailable
	}
	defer release()
	if e = progress.Validate(ctx, call); e != nil {
		return e
	}
	if call.Action == bg.RemoteSubmit {
		var rateError *core.Error
		var estimate int
		ctx, estimate, _, rateError = g.admitGenerationRate(ctx, req)
		if rateError != nil {
			return bg.ErrCapacity
		}
		binding := responseTarget(ch, &credential, call.Model)
		rate := bs.RateReservation{Estimate: estimate}
		if p, ok := access.PrincipalFromContext(ctx); ok && g.limiter != nil && p.TPMLimit > 0 {
			rate.Epoch = r.epoch
		}
		if e = progress.Dispatch(ctx, binding, rate); e != nil {
			access.FinishTokens(ctx, g.limiter, 0)
			return e
		}
	}
	prepared.Input.Timeout = 10 * time.Second
	tracking := &backgroundDecoderTracking{decoder: prepared.Input.Decoder}
	prepared.Input.Decoder = tracking
	out := &backgroundOutput{progress: progress, observer: prepared.Progress}
	result := g.executor.ExecuteTo(ctx, out, ToCoreInput(prepared.Input))
	if result.Error == nil && out.observed {
		g.reportCredentialHealth(ch.name, &credential, 200, "")
	}
	if result.Error != nil && result.Termination == core.UpstreamError && ctx.Err() == nil {
		g.reportCredentialHealth(ch.name, &credential, result.StatusCode, "background upstream RPC failed")
	}
	if out.persistError != nil {
		for _, known := range []error{bg.ErrCursorUnavailable, bg.ErrCapacity, bg.ErrConflict, bs.ErrClaimLost} {
			if errors.Is(out.persistError, known) {
				return known
			}
		}
		return bg.ErrUnavailable
	}
	if tracking.err != nil && stream {
		return bg.ErrCursorUnavailable
	}
	if call.Action == bg.RemoteResume && (result.UpstreamStatus == 400 || result.UpstreamStatus == 404 || result.UpstreamStatus == 410 || result.UpstreamStatus == 422) {
		return bg.ErrCursorUnavailable
	}
	if result.Error != nil || !out.observed && call.Action != bg.RemoteResume {
		return bg.ErrUnavailable
	}
	return nil
}

func backgroundWireBound(in execution.Input, target execution.Target, allowQuery ...bool) bool {
	base, e := url.Parse(target.BaseURL)
	if e != nil {
		return false
	}
	u, e := url.Parse(in.URL)
	if e != nil || !u.IsAbs() || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.Fragment != "" || u.RawQuery != "" && (len(allowQuery) == 0 || !allowQuery[0] || in.Method != "GET") {
		return false
	}
	root := strings.TrimRight(path.Clean("/"+strings.TrimPrefix(base.Path, "/")), "/") + "/"
	if !strings.HasPrefix(path.Clean(u.Path), root) || target.APIKey == "" {
		return false
	}
	if u.RawQuery != "" {
		query, e := url.ParseQuery(u.RawQuery)
		if e != nil || len(query) > 4 {
			return false
		}
		for name, values := range query {
			if len(name) > 64 || strings.Contains(name, target.APIKey) || len(values) != 1 || len(values[0]) > 256 || strings.Contains(values[0], target.APIKey) {
				return false
			}
		}
	}
	// This first batch explicitly supports standard API-key Bearer transport.
	// A codec cannot silently dispatch under another origin or credential.
	count := 0
	for name, values := range in.Header {
		if strings.EqualFold(name, "Authorization") {
			count++
			if len(values) != 1 || values[0] != "Bearer "+target.APIKey {
				return false
			}
			continue
		}
		if !strings.EqualFold(name, "Content-Type") && !strings.EqualFold(name, "Accept") {
			return false
		}
	}
	return count == 1
}

func backgroundAdmission(ctx context.Context, a Admission, scope AdmissionScope) (func(), error) {
	if a == nil {
		return func() {}, nil
	}
	release, e := a.Acquire(ctx, scope)
	if e != nil {
		if release != nil {
			release()
		}
		return nil, bg.ErrCapacity
	}
	if release == nil || ctx.Err() != nil {
		if release != nil {
			release()
		}
		return nil, bg.ErrUnavailable
	}
	return release, nil
}

type backgroundOutput struct {
	progress            bs.Progress
	observer            bg.Observer
	committed, observed bool
	persistError        error
}

func (o *backgroundOutput) Commit(ctx context.Context, _ core.ResponseMetadata) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	o.committed = true
	return nil
}
func (o *backgroundOutput) State() core.OutputState         { return core.OutputState{Committed: o.committed} }
func (o *backgroundOutput) Flush(ctx context.Context) error { return ctx.Err() }
func (o *backgroundOutput) Write(ctx context.Context, b []byte) (int, error) {
	observation := o.observer.Drain()
	if observation.RemoteID == "" {
		o.persistError = errors.New("missing native background observation")
		return 0, o.persistError
	}
	if e := o.progress.Observe(ctx, observation); e != nil {
		o.persistError = e
		return 0, e
	}
	o.observed = true
	return len(b), nil
}
func (r *BackgroundRemote) FinishRate(ctx context.Context, tokenID uint, reservation bs.RateReservation, u *execution.Usage) {
	if r == nil || r.gateway.limiter == nil || reservation.Epoch != r.epoch || u == nil {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	r.gateway.limiter.AddTokens(cleanup, access.TokenUsageBucketKey(tokenID), u.TotalTokens-reservation.Estimate, time.Now())
}

var _ bs.Remote = (*BackgroundRemote)(nil)
