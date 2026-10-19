package upstreamidentity_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	exec "minigateway/internal/foundation/contracts/execution/v1"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/upstreamidentity"
)

const fixtureID = "ua_0123456789abcdef0123456789abcdef"
const fakeSecret = "fake-private-token-must-never-escape"

type managerFixture struct {
	t                                                                                 *testing.T
	events                                                                            []string
	repo                                                                              *managerRepo
	provider                                                                          *managerProvider
	caller                                                                            *managerCaller
	receiver                                                                          *managerReceiver
	manager                                                                           auth.Manager
	credential                                                                        auth.Credential
	reply                                                                             auth.Tokens
	response                                                                          auth.Response
	keys                                                                              auth.Response
	hostErr, claimErr, startErr, prepareErr, readErr, verifyErr, completeErr, saveErr error
	preparePanic, httpPanic                                                           bool
	cancelAfterReply                                                                  context.CancelFunc
	write                                                                             auth.AuthorizationWrite
	completed                                                                         auth.Tokens
	failures                                                                          []auth.Failure
	finishContexts                                                                    []context.Context
	finishValid                                                                       []bool
	beginClient, beginHost, beginRedirect                                             string
	expected                                                                          auth.IdentityExpectation
	bound, opened, verified                                                           bool
}

func (f *managerFixture) event(s string) { f.events = append(f.events, s) }
func newManagerFixture(t *testing.T) *managerFixture {
	t.Helper()
	f := &managerFixture{t: t}
	f.credential = auth.Credential{Account: auth.Account{ID: fixtureID, Provider: "openai", Name: "operator", ClientID: "issued-client", Subject: "fixed-sub", Issuer: "issuer", Revision: 7, CredentialRevision: 3, State: auth.Active},
		Tokens: auth.Tokens{AccessToken: auth.NewSecret(fakeSecret), RefreshToken: auth.NewSecret("old-refresh"), IDToken: auth.NewSecret("old-id"), Nonce: auth.NewSecret("old-nonce"), ScopesKnown: true, Scopes: []string{"resource.invoke"}}}
	f.reply = auth.Tokens{AccessToken: auth.NewSecret("new-access"), ExpiresAt: time.Now().Add(time.Hour)}
	f.response = auth.Response{Phase: exec.ResponseReceived, Status: 200, Complete: true, Body: auth.NewSecret("token-reply")}
	f.keys = auth.Response{Phase: exec.ResponseReceived, Status: 200, Complete: true, Body: auth.NewSecret("jwks")}
	f.repo = &managerRepo{f: f}
	f.provider = &managerProvider{f: f}
	f.caller = &managerCaller{f: f}
	f.receiver = &managerReceiver{f: f}
	var err error
	f.manager, err = upstreamidentity.New(f.repo, f.provider, f.caller, f.receiver)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *managerFixture) requireEvents(want ...string) {
	f.t.Helper()
	if !reflect.DeepEqual(f.events, want) {
		f.t.Fatalf("events = %v, want %v", f.events, want)
	}
}
func (f *managerFixture) requireFailure(out auth.Account, err, want error, failure auth.Failure) {
	f.t.Helper()
	if !reflect.DeepEqual(out, auth.Account{}) || !errors.Is(err, want) {
		f.t.Fatalf("failure out=%+v err=%v want=%v", out, err, want)
	}
	if strings.Contains(err.Error(), fakeSecret) {
		f.t.Fatal("secret leaked")
	}
	if !reflect.DeepEqual(f.failures, []auth.Failure{failure}) {
		f.t.Fatalf("finish failures %v want %v", f.failures, failure)
	}
	for _, valid := range f.finishValid {
		if !valid {
			f.t.Fatal("finish context was cancelled or unbounded")
		}
	}
}

type managerRepo struct {
	auth.Repository
	f *managerFixture
}

func (r *managerRepo) HostID(context.Context) (string, error) {
	r.f.event("host")
	return "01234567-89ab-4cde-8fab-0123456789ab", r.f.hostErr
}
func (r *managerRepo) Read(context.Context, string) (auth.Account, error) {
	r.f.event("read")
	return r.f.credential.Account, nil
}
func (r *managerRepo) BeginAuthorization(_ context.Context, id string, rev int64) (auth.Account, error) {
	r.f.event("fence")
	if id != fixtureID || rev != 7 {
		r.f.t.Fatal("wrong authorization fence", id, rev)
	}
	a := r.f.credential.Account
	a.Revision = 8
	return a, nil
}
func (r *managerRepo) SaveAuthorization(ctx context.Context, w auth.AuthorizationWrite) (auth.Account, error) {
	r.f.event("save")
	if !r.f.verified {
		r.f.t.Fatal("saved before identity verification")
	}
	r.f.write = w
	r.f.checkFinishContext(ctx)
	return r.f.credential.Account, r.f.saveErr
}
func (r *managerRepo) ClaimRefresh(_ context.Context, id string, rev int64) (auth.Claim, error) {
	r.f.event("claim")
	if id != fixtureID || rev != 7 {
		r.f.t.Fatal("wrong refresh claim")
	}
	return auth.Claim{AccountID: id, Revision: rev, Fence: 4, Proof: auth.NewSecret(fakeSecret)}, r.f.claimErr
}
func (r *managerRepo) StartRefresh(_ context.Context, c auth.Claim) (auth.Credential, error) {
	r.f.event("start")
	if c.Fence != 4 {
		r.f.t.Fatal("start lost claim")
	}
	return r.f.credential, r.f.startErr
}
func (r *managerRepo) CompleteRefresh(ctx context.Context, c auth.Claim, tokens auth.Tokens) (auth.Account, error) {
	r.f.event("complete")
	if c.Fence != 4 {
		r.f.t.Fatal("complete lost fence")
	}
	r.f.checkFinishContext(ctx)
	r.f.completed = tokens
	return r.f.credential.Account, r.f.completeErr
}
func (r *managerRepo) FinishRefresh(ctx context.Context, c auth.Claim, failure auth.Failure) (auth.Account, error) {
	r.f.event("finish")
	if c.Fence != 4 {
		r.f.t.Fatal("finish lost fence")
	}
	r.f.checkFinishContext(ctx)
	r.f.failures = append(r.f.failures, failure)
	return auth.Account{}, nil
}
func (r *managerRepo) Recover(_ context.Context, n int) (int, error) {
	r.f.event("recover")
	if n != 1000 {
		r.f.t.Fatal("unbounded recover", n)
	}
	return 0, nil
}
func (r *managerRepo) List(_ context.Context, limit, offset int) (auth.Page, error) {
	r.f.event("list")
	return auth.Page{Data: []auth.Account{r.f.credential.Account}}, nil
}
func (r *managerRepo) Disable(_ context.Context, id string, revision int64) (auth.Account, error) {
	r.f.event("disable")
	return r.f.credential.Account, nil
}
func (f *managerFixture) checkFinishContext(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	f.finishValid = append(f.finishValid, ok && ctx.Err() == nil && time.Until(deadline) > 0 && time.Until(deadline) <= 5*time.Second)
}

type managerProvider struct {
	auth.Provider
	f *managerFixture
}

func (*managerProvider) Name() string { return "openai" }
func (p *managerProvider) Begin(client, host, redirect string, _ time.Time) (auth.Flow, error) {
	p.f.event("begin")
	if !p.f.opened {
		p.f.t.Fatal("Begin before callback listener")
	}
	p.f.beginClient, p.f.beginHost, p.f.beginRedirect = client, host, redirect
	return auth.Flow{ClientID: client, HostID: host, RedirectURI: redirect, URL: auth.NewSecret("authorization-url"), Nonce: auth.NewSecret("flow-nonce")}, nil
}
func (p *managerProvider) CodeRequest(_ auth.Flow, c auth.Code) (auth.Request, error) {
	p.f.event("code-request")
	if c.ClientID != "issued-client" {
		p.f.t.Fatal("exchange lost issued client")
	}
	return auth.Request{URL: "token"}, nil
}
func (p *managerProvider) RefreshRequest(client string, token auth.Secret) (auth.Request, error) {
	p.f.event("prepare")
	if client != "issued-client" || token.Value() != "old-refresh" {
		p.f.t.Fatal("refresh binding changed")
	}
	if p.f.preparePanic {
		panic(fakeSecret)
	}
	return auth.Request{URL: "token"}, p.f.prepareErr
}
func (p *managerProvider) KeysRequest() auth.Request {
	p.f.event("keys-request")
	return auth.Request{URL: "keys"}
}
func (p *managerProvider) ReadTokens(r auth.Response, _ time.Time) (auth.Tokens, error) {
	p.f.event("tokens")
	if r.Phase != p.f.response.Phase || r.Status != p.f.response.Status {
		p.f.t.Fatal("wrong raw reply")
	}
	if p.f.cancelAfterReply != nil {
		p.f.cancelAfterReply()
	}
	return p.f.reply, p.f.readErr
}
func (p *managerProvider) VerifyIdentity(id, keys auth.Secret, e auth.IdentityExpectation) (auth.Identity, error) {
	p.f.event("verify")
	if id.Value() == "" || keys.Value() != "jwks" {
		p.f.t.Fatal("invalid verification inputs")
	}
	p.f.expected = e
	p.f.verified = true
	return auth.Identity{Issuer: "issuer", Subject: "fixed-sub"}, p.f.verifyErr
}

type managerCaller struct {
	auth.HTTPCaller
	f *managerFixture
}

func (c *managerCaller) Call(_ context.Context, r auth.Request) auth.Response {
	if r.URL == "keys" {
		c.f.event("keys-http")
		return c.f.keys
	}
	if r.URL != "token" {
		c.f.t.Fatal("unexpected HTTP target", r.URL)
	}
	c.f.event("token-http")
	if c.f.httpPanic {
		panic(fakeSecret)
	}
	return c.f.response
}

type managerReceiver struct{ f *managerFixture }

func (r *managerReceiver) Open(context.Context) (auth.CallbackSession, error) {
	r.f.event("open")
	r.f.opened = true
	return &managerSession{f: r.f}, nil
}

type managerSession struct{ f *managerFixture }

func (*managerSession) RedirectURI() string    { return "http://127.0.0.1:32123/auth/callback" }
func (s *managerSession) Bind(auth.Flow) error { s.f.event("bind"); s.f.bound = true; return nil }
func (s *managerSession) Wait(context.Context) (auth.Code, error) {
	s.f.event("wait")
	if !s.f.bound {
		s.f.t.Fatal("wait before bind")
	}
	return auth.Code{ClientID: "issued-client", Value: auth.NewSecret(fakeSecret)}, nil
}
func (s *managerSession) Close() error { s.f.event("close"); return nil }

func TestManagerAuthorizationOrdersListenerBindingVerificationAndFence(t *testing.T) {
	for _, reauth := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "reauth"}[reauth], func(t *testing.T) {
			f := newManagerFixture(t)
			f.reply.IDToken = auth.NewSecret("new-id")
			id, rev, name := "", int64(0), "new account"
			if reauth {
				id, rev, name = fixtureID, 7, ""
			}
			out, err := f.manager.Authorize(context.Background(), name, id, rev, func(url auth.Secret) error {
				f.event("present")
				if !f.opened || !f.bound || url.Value() != "authorization-url" {
					t.Fatal("URL presented before open/bind")
				}
				return nil
			})
			if err != nil || out.ID != fixtureID {
				t.Fatal(out, err)
			}
			want := []string{"host", "open", "begin", "bind", "present", "wait", "code-request", "token-http", "tokens", "keys-request", "keys-http", "verify", "save", "close"}
			if reauth {
				want = append([]string{"read", "host", "fence"}, want[1:]...)
			}
			f.requireEvents(want...)
			if f.write.ClientID != "issued-client" || f.write.Identity.Subject != "fixed-sub" || !f.expected.RequireNonce || f.expected.Nonce.Value() != "flow-nonce" || f.write.Tokens.Nonce.Value() != "flow-nonce" {
				t.Fatal("authorization lost verified binding")
			}
			if reauth && (f.write.ExpectedRevision != 8 || f.write.ID != fixtureID || f.beginClient != "issued-client" || f.expected.Subject != "fixed-sub" || f.write.Name != "operator") {
				t.Fatal("reauthorization lost fenced revision/identity", f.write)
			}
			if !reauth && (!auth.ValidAccountID(f.write.ID) || f.write.ExpectedRevision != 0 || f.beginClient != "") {
				t.Fatal("initial authorization binding")
			}
		})
	}
}

func TestManagerEncryptionFailureStopsBeforeCallbackOrHTTP(t *testing.T) {
	f := newManagerFixture(t)
	f.hostErr = auth.ErrEncryptionRequired
	out, err := f.manager.Authorize(context.Background(), "account", "", 0, func(auth.Secret) error { t.Fatal("present called"); return nil })
	if !errors.Is(err, auth.ErrEncryptionRequired) || !reflect.DeepEqual(out, auth.Account{}) {
		t.Fatal(out, err)
	}
	f.requireEvents("host")
}

func TestManagerRefreshFailureClassificationAndNoReplay(t *testing.T) {
	for _, kind := range []string{"not-dispatched", "partial", "malformed", "timeout", "http429", "rejected", "jwks-not-dispatched", "identity-invalid", "prepare-panic", "http-panic", "complete-unavailable", "complete-conflict", "claim-rejected", "prepare-error", "start-error"} {
		t.Run(kind, func(t *testing.T) {
			f := newManagerFixture(t)
			want, failure := error(auth.ErrUncertain), auth.UnknownFailure
			end := []string{"claim", "start", "prepare", "token-http", "tokens", "finish"}
			switch kind {
			case "not-dispatched":
				f.response.Phase = exec.NotDispatched
				want, failure = auth.ErrUnavailable, auth.NotSentFailure
				end = []string{"claim", "start", "prepare", "token-http", "finish"}
			case "partial":
				f.response.Complete = false
				f.readErr = errors.New(fakeSecret)
			case "malformed":
				f.readErr = auth.ErrInvalid
			case "timeout":
				f.response.Phase = exec.Dispatching
				f.readErr = context.DeadlineExceeded
			case "http429":
				f.response.Status = 429
				f.readErr = auth.ErrUnavailable
			case "rejected":
				f.readErr = auth.ErrRejected
				want, failure = auth.ErrReauthorize, auth.RejectedFailure
			case "jwks-not-dispatched", "identity-invalid":
				f.reply.IDToken = auth.NewSecret("rotated-id")
				end = []string{"claim", "start", "prepare", "token-http", "tokens", "keys-request", "keys-http", "finish"}
				if kind == "jwks-not-dispatched" {
					f.keys.Phase = exec.NotDispatched
				} else {
					f.verifyErr = errors.New(fakeSecret)
					end = append(end[:len(end)-1], "verify", "finish")
				}
			case "prepare-panic":
				f.preparePanic = true
				want, failure = auth.ErrUnavailable, auth.NotSentFailure
				end = []string{"claim", "start", "prepare", "finish"}
			case "http-panic":
				f.httpPanic = true
				end = []string{"claim", "start", "prepare", "token-http", "finish"}
			case "complete-unavailable", "complete-conflict":
				f.completeErr = auth.ErrUnavailable
				if kind == "complete-conflict" {
					f.completeErr = auth.ErrConflict
					want = auth.ErrConflict
				}
				end = []string{"claim", "start", "prepare", "token-http", "tokens", "complete", "finish"}
			case "claim-rejected":
				f.claimErr = auth.ErrBusy
				out, err := f.manager.Refresh(context.Background(), fixtureID, 7)
				if !errors.Is(err, auth.ErrBusy) || !reflect.DeepEqual(out, auth.Account{}) {
					t.Fatal(out, err)
				}
				f.requireEvents("claim")
				return
			case "prepare-error":
				f.prepareErr = errors.New(fakeSecret)
				want, failure = auth.ErrUnavailable, auth.NotSentFailure
				end = []string{"claim", "start", "prepare", "finish"}
			case "start-error":
				f.startErr = auth.ErrConflict
				want, failure = auth.ErrConflict, auth.NotSentFailure
				end = []string{"claim", "start", "finish"}
			}
			out, err := f.manager.Refresh(context.Background(), fixtureID, 7)
			f.requireFailure(out, err, want, failure)
			f.requireEvents(end...)
		})
	}
}

func TestManagerValidRefreshMergesOnlyOmittedFieldsAndFinishesAfterCancellation(t *testing.T) {
	for _, kind := range []string{"omitted", "known-empty-scopes", "new-id", "cancel-after-valid-reply"} {
		t.Run(kind, func(t *testing.T) {
			f := newManagerFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "known-empty-scopes" {
				f.reply.ScopesKnown = true
				f.reply.Scopes = []string{}
			}
			if kind == "new-id" {
				f.reply.IDToken = auth.NewSecret("new-id")
				f.reply.RefreshToken = auth.NewSecret("rotated-refresh")
			}
			if kind == "cancel-after-valid-reply" {
				f.cancelAfterReply = cancel
			}
			out, err := f.manager.Refresh(ctx, fixtureID, 7)
			if err != nil || out.ID != fixtureID || len(f.failures) != 0 {
				t.Fatal(out, err, f.failures)
			}
			want := []string{"claim", "start", "prepare", "token-http", "tokens", "complete"}
			if kind == "new-id" {
				want = []string{"claim", "start", "prepare", "token-http", "tokens", "keys-request", "keys-http", "verify", "complete"}
			}
			f.requireEvents(want...)
			if f.completed.Nonce.Value() != "old-nonce" || f.completed.AccessToken.Value() != "new-access" || !f.completed.ExpiresAt.Equal(f.reply.ExpiresAt) {
				t.Fatal("valid reply fields lost")
			}
			if kind == "known-empty-scopes" {
				if !f.completed.ScopesKnown || len(f.completed.Scopes) != 0 {
					t.Fatal("empty grant widened")
				}
			} else if !reflect.DeepEqual(f.completed.Scopes, []string{"resource.invoke"}) {
				t.Fatal("omitted scope not retained")
			}
			if kind == "new-id" {
				if f.completed.IDToken.Value() != "new-id" || f.completed.RefreshToken.Value() != "rotated-refresh" || f.expected.Subject != "fixed-sub" || f.expected.ClientID != "issued-client" || f.expected.RequireNonce {
					t.Fatal("refresh identity binding lost")
				}
			} else if f.completed.IDToken.Value() != "old-id" || f.completed.RefreshToken.Value() != "old-refresh" {
				t.Fatal("omitted credential fields lost")
			}
			if !reflect.DeepEqual(f.finishValid, []bool{true}) {
				t.Fatal("completion context cancelled/unbounded")
			}
		})
	}
}

func TestManagerListRecoversThenReadsAndInvalidOperationsDoNoIO(t *testing.T) {
	f := newManagerFixture(t)
	page, err := f.manager.List(context.Background(), 10, 0)
	if err != nil || len(page.Data) != 1 {
		t.Fatal(page, err)
	}
	f.requireEvents("recover", "list")
	for _, run := range []func(*managerFixture){
		func(f *managerFixture) {
			p, e := f.manager.List(context.Background(), 101, 0)
			if e == nil || !reflect.DeepEqual(p, auth.Page{}) {
				t.Fatal(p, e)
			}
		},
		func(f *managerFixture) {
			a, e := f.manager.Refresh(context.Background(), "invalid", 7)
			if e == nil || !reflect.DeepEqual(a, auth.Account{}) {
				t.Fatal(a, e)
			}
		},
		func(f *managerFixture) {
			a, e := f.manager.Disable(context.Background(), fixtureID, 0)
			if e == nil || !reflect.DeepEqual(a, auth.Account{}) {
				t.Fatal(a, e)
			}
		},
		func(f *managerFixture) {
			a, e := f.manager.Authorize(context.Background(), "", "", 0, func(auth.Secret) error { return nil })
			if e == nil || !reflect.DeepEqual(a, auth.Account{}) {
				t.Fatal(a, e)
			}
		},
	} {
		f := newManagerFixture(t)
		run(f)
		if len(f.events) != 0 {
			t.Fatal("invalid operation performed IO", f.events)
		}
	}
}
