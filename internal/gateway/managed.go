package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"minigateway/internal/billing"
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	jsonvalue "minigateway/internal/foundation/contracts/json/v1"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	authv2 "minigateway/internal/foundation/contracts/upstreamauth/v2"
	"minigateway/internal/model"
)

const managedOrigin = "https://api.openai.com/v1"

type managedWireKey struct{}

func managedWireRequest(r *http.Request) bool {
	marked, _ := r.Context().Value(managedWireKey{}).(bool)
	return marked
}

// Keep the supplied client's static-route behavior. Managed requests alone
// suppress redirects and cookie exchange within the same mechanical executor.
func managedHTTPClient(original *http.Client) *http.Client {
	client := *original
	redirect := original.CheckRedirect
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if managedWireRequest(r) {
			return http.ErrUseLastResponse
		}
		if redirect != nil {
			return redirect(r, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = managedTransport{transport}
	return &client
}

type managedTransport struct{ base http.RoundTripper }

func (t managedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !managedWireRequest(r) {
		return t.base.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	r.Header.Del("Cookie")
	r.Header.Del("Cookie2")
	response, err := t.base.RoundTrip(r)
	if response != nil {
		copy := *response
		copy.Header = response.Header.Clone()
		copy.Header.Del("Set-Cookie")
		copy.Header.Del("Set-Cookie2")
		response = &copy
	}
	return response, err
}

func (t managedTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// ManagedInferenceOptions wires the approved SIWC subset into the existing
// execution owner. ValidateModel is a pure, bounded L2 catalog decoder.
type ManagedInferenceOptions struct {
	Enabled         bool
	Credentials     authv2.InferenceCredentials
	DirectoryCaller auth.HTTPCaller
	ValidateModel   func([]byte, string) error
}

type managedInference struct {
	credentials authv2.InferenceCredentials
	caller      auth.HTTPCaller
	validate    func([]byte, string) error
	mu          sync.Mutex
	cache       managedCatalog
}

// One metadata-only slot bounds cache cardinality across account generations.
type managedCatalog struct {
	account, slug        string
	revision, credential int64
	expires              time.Time
}

func newManagedInference(opts *ManagedInferenceOptions, mode billing.Mode) (*managedInference, error) {
	if opts == nil || !opts.Enabled {
		return nil, nil
	}
	if mode != billing.ModeDisabled || nilInterface(opts.Credentials) || nilInterface(opts.DirectoryCaller) || opts.ValidateModel == nil {
		return nil, errors.New("managed inference requires disabled billing and complete dependencies")
	}
	return &managedInference{credentials: opts.Credentials, caller: opts.DirectoryCaller, validate: opts.ValidateModel}, nil
}

func isManaged(c *model.Credential) bool { return c.Source == model.CredentialSourceManaged }
func isStatic(c *model.Credential) bool {
	return c.Source == "" || c.Source == model.CredentialSourceStatic
}

func availableCredential(c *model.Credential, now time.Time) bool {
	if !c.IsUsable(now) {
		return false
	}
	if isManaged(c) {
		return c.APIKey == "" && c.ID != 0 && c.AccountID != "" && c.BindingRevision > 0 && c.BindingOwnerID != 0 && c.BindingTokenID != 0 && c.BindingModel != "" && c.AccountRevision > 0 && c.CredentialRevision > 0
	}
	return isStatic(c) && c.APIKey != ""
}

func sameManagedBinding(a, b *model.Credential) bool {
	return isManaged(a) && isManaged(b) && a.ID == b.ID && a.ChannelID == b.ChannelID && a.AccountID == b.AccountID && a.BindingRevision == b.BindingRevision &&
		a.BindingOwnerID == b.BindingOwnerID && a.BindingTokenID == b.BindingTokenID && a.BindingModel == b.BindingModel && a.AccountRevision == b.AccountRevision && a.CredentialRevision == b.CredentialRevision
}

func managedFailure(status int, kind string) *core.Error {
	return &core.Error{StatusCode: status, Error: core.ErrorDetail{Message: "managed inference is unavailable for this request", Type: kind}}
}

func managedGrantMatches(cred model.Credential, owner, token uint, modelName string) bool {
	return cred.BindingOwnerID == owner && cred.BindingTokenID == token && cred.BindingModel == modelName
}

// A matching grant reserves its identity/model tuple even while disabled.
// Other transport owners must refuse it rather than selecting a static key.
func (g *Gateway) hasManagedGrant(owner, token uint, modelName string) bool {
	g.reg.mu.RLock()
	defer g.reg.mu.RUnlock()
	for _, cred := range g.reg.managedGrants {
		if managedGrantMatches(cred, owner, token, modelName) {
			return true
		}
	}
	return false
}

// Only an exact trusted grant selects this source. Once selected, no static
// channel, other account or newer binding can replace it within this request.
func (g *Gateway) prepareManagedScope(c *executionScope, req execution.Request, allowed []string) *core.Error {
	if !modelAllowed(req.Model(), allowed) {
		return nil
	}
	g.reg.mu.RLock()
	defer g.reg.mu.RUnlock()
	matched := false
	for _, cred := range g.reg.managedGrants {
		if !managedGrantMatches(cred, c.result.Identity.UserID, c.result.Identity.TokenID, req.Model()) {
			continue
		}
		matched = true
		if cred.Status == model.CredentialDisabled {
			continue
		}
		if c.managedTarget != nil {
			return managedFailure(503, "provider_binding_unavailable")
		}
		copy := cred
		c.managedTarget = &copy
	}
	if !matched {
		return nil
	}
	if c.managedTarget == nil {
		return managedFailure(503, "provider_binding_unavailable")
	}
	if g.managed == nil || g.billingMode != billing.ModeDisabled {
		return managedFailure(503, "provider_inference_disabled")
	}
	if c.claim != nil || c.source == invocationClaimed || c.responseTarget != nil || req.Operation() != execution.Responses || !req.Stream() || req.Requirements().Transport != execution.HTTP || validateManagedBody(req.Body(), req.Model()) != nil {
		return managedFailure(400, "unsupported_operation")
	}
	cred := c.managedTarget
	ch := g.reg.channels[int(cred.ChannelID)]
	if ch == nil || !validManagedChannel(ch, cred) || !availableCredential(cred, time.Now()) {
		return managedFailure(503, "provider_binding_unavailable")
	}
	return nil
}

func (g *Gateway) scopeCandidates(c *executionScope, req execution.Request, allowed []string) []*channel {
	var result []*channel
	for _, ch := range g.capableChannels(req, allowed) {
		copy := *ch
		copy.credentials = nil
		for _, cred := range ch.credentials {
			if c.managedTarget != nil {
				if sameManagedBinding(&cred, c.managedTarget) {
					copy.credentials = append(copy.credentials, cred)
				}
			} else if isStatic(&cred) {
				copy.credentials = append(copy.credentials, cred)
			}
		}
		if len(copy.credentials) != 0 {
			result = append(result, &copy)
		}
	}
	return g.responseChannels(result, req.Model(), c.responseTarget)
}

func validManagedChannel(ch *channel, cred *model.Credential) bool {
	return ch.typ == "openai_siwc" && ch.baseURL == managedOrigin && len(ch.mapping) == 0 && len(ch.models) == 1 && ch.models[0] == cred.BindingModel && len(ch.credentials) == 1 && cred.APIKey == ""
}

func validateCredentialSources(ch model.Channel) error {
	managed := false
	for _, cred := range ch.Credentials {
		if isManaged(&cred) {
			managed = true
			if cred.ID == 0 || cred.AccountID == "" || cred.BindingRevision <= 0 || cred.BindingOwnerID == 0 || cred.BindingTokenID == 0 || cred.BindingModel == "" || cred.APIKey != "" || len(cred.KeyCiphertext) != 0 || cred.KeyHash != "" {
				return configurationError("managed credential has invalid source or binding")
			}
		} else if !isStatic(&cred) || ch.Type == "openai_siwc" {
			return configurationError("credential source does not match channel protocol")
		}
	}
	if ch.Type == "openai_siwc" && !managed {
		return configurationError("managed channel requires a managed credential source")
	}
	if managed && (ch.Type != "openai_siwc" || ch.BaseURL != managedOrigin || len(ch.Credentials) != 1 || len(ch.ModelMapping) != 0 || len(ch.Models) != 1 || ch.Models[0] != ch.Credentials[0].BindingModel) {
		return configurationError("managed channel requires its fixed official target and one model binding")
	}
	return nil
}

func (g *Gateway) modelGrantVisible(ch *channel, owner, token uint) bool {
	if ch.typ != "openai_siwc" {
		return true
	}
	for _, cred := range ch.credentials {
		if isStatic(&cred) {
			return true
		}
		if g.managed != nil && isManaged(&cred) && cred.BindingOwnerID == owner && cred.BindingTokenID == token && availableCredential(&cred, time.Now()) {
			return true
		}
	}
	return false
}

// validateManagedBody is the approved self-contained text-only SIWC subset.
// Unknown fields fail closed; standard API Key Requests keep their own codec.
func validateManagedBody(body []byte, slug string) error {
	invalid := errors.New("unsupported managed inference request")
	if jsonvalue.Validate(body) != nil {
		return invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return invalid
	}
	for field := range fields {
		switch field {
		case "model", "input", "instructions", "store", "stream":
		default:
			return invalid
		}
	}
	var actual string
	if json.Unmarshal(fields["model"], &actual) != nil || actual != slug || !bytes.Equal(bytes.TrimSpace(fields["store"]), []byte("false")) || !bytes.Equal(bytes.TrimSpace(fields["stream"]), []byte("true")) {
		return invalid
	}
	if instructions, ok := fields["instructions"]; ok {
		var text string
		if bytes.Equal(bytes.TrimSpace(instructions), []byte("null")) || json.Unmarshal(instructions, &text) != nil {
			return invalid
		}
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(fields["input"], &items) != nil || len(items) == 0 {
		return invalid
	}
	for _, item := range items {
		for key := range item {
			if key != "role" && key != "type" && key != "content" {
				return invalid
			}
		}
		var role, kind string
		if json.Unmarshal(item["role"], &role) != nil || role != "user" && role != "assistant" && role != "developer" {
			return invalid
		}
		if raw, ok := item["type"]; ok && (json.Unmarshal(raw, &kind) != nil || kind != "message") {
			return invalid
		}
		var text string
		if raw := bytes.TrimSpace(item["content"]); len(raw) != 0 && raw[0] == '"' && json.Unmarshal(raw, &text) == nil {
			continue
		}
		var parts []map[string]json.RawMessage
		if json.Unmarshal(item["content"], &parts) != nil || len(parts) == 0 {
			return invalid
		}
		for _, part := range parts {
			if len(part) != 2 || json.Unmarshal(part["type"], &kind) != nil {
				return invalid
			}
			field := "text"
			if kind == "refusal" && role == "assistant" {
				field = "refusal"
			} else if kind != "input_text" && !(kind == "output_text" && role == "assistant") {
				return invalid
			}
			if json.Unmarshal(part[field], &text) != nil || bytes.Equal(bytes.TrimSpace(part[field]), []byte("null")) {
				return invalid
			}
		}
	}
	return nil
}

func (g *Gateway) acquireManaged(ctx context.Context, c *executionScope, cred *model.Credential) (authv2.AccessCredential, error) {
	if g.managed == nil || ctx.Err() != nil || c.result.Identity.Policy == nil || !c.result.Identity.Policy.Allows(cred.BindingModel) || c.result.Identity.UserID != cred.BindingOwnerID || c.result.Identity.TokenID != cred.BindingTokenID {
		return authv2.AccessCredential{}, auth.ErrRejected
	}
	access, err := g.managed.credentials.AcquireAccess(ctx, authv2.AccessRequest{CredentialID: cred.ID, ExpectedBindingRevision: cred.BindingRevision, AccountID: cred.AccountID,
		ExpectedRevision: cred.AccountRevision, ExpectedCredentialRevision: cred.CredentialRevision, OwnerID: c.result.Identity.UserID, TokenID: c.result.Identity.TokenID, Model: cred.BindingModel})
	if err != nil || access.AccountID != cred.AccountID || access.Revision != cred.AccountRevision || access.CredentialRevision != cred.CredentialRevision || !access.ExpiresAt.After(time.Now()) || !auth.ValidWireText(access.AccessToken.Value(), auth.MaxTokenBytes) || strings.ContainsAny(access.AccessToken.Value(), " \t") {
		return authv2.AccessCredential{}, auth.ErrRejected
	}
	return access, nil
}

func (m *managedInference) visible(ctx context.Context, access authv2.AccessCredential, slug string) (bool, error) {
	m.mu.Lock()
	if ctx.Err() != nil {
		m.mu.Unlock()
		return false, ctx.Err()
	}
	if m.cache.account == access.AccountID && m.cache.revision == access.Revision && m.cache.credential == access.CredentialRevision && m.cache.slug == slug && time.Now().Before(m.cache.expires) {
		m.mu.Unlock()
		return false, nil
	}
	m.cache = managedCatalog{}
	m.mu.Unlock()
	started := time.Now()
	response := m.caller.Call(ctx, auth.Request{Method: http.MethodGet, URL: managedOrigin + "/models", Header: http.Header{"Authorization": {"Bearer " + access.AccessToken.Value()}, "Accept": {"application/json"}}})
	if ctx.Err() != nil || !response.Complete || response.Status != 200 || m.validate([]byte(response.Body.Value()), slug) != nil {
		return true, auth.ErrUnavailable
	}
	expires := started.Add(60 * time.Second)
	if access.ExpiresAt.Before(expires) {
		expires = access.ExpiresAt
	}
	if !time.Now().Before(expires) {
		return true, auth.ErrUnavailable
	}
	m.mu.Lock()
	m.cache = managedCatalog{account: access.AccountID, revision: access.Revision, credential: access.CredentialRevision, slug: slug, expires: expires}
	m.mu.Unlock()
	return true, nil
}

func (g *Gateway) authorizeManagedInput(c *executionScope, target *execution.Target, req execution.Request, input *core.Input) *core.Error {
	if c.managedTarget == nil {
		return nil
	}
	cred := c.managedTarget
	// Recheck the live registry after admission and adapter preparation. SQLite
	// AcquireAccess is the cross-process authority after this snapshot check.
	g.reg.mu.RLock()
	live := g.reg.channels[target.ChannelID]
	valid := live != nil && validManagedChannel(live, cred) && len(live.credentials) == 1 && sameManagedBinding(&live.credentials[0], cred) && availableCredential(&live.credentials[0], time.Now())
	g.reg.mu.RUnlock()
	if !valid || target.CredentialIDValue != cred.ID || target.ActualModel != cred.BindingModel || input.URL != managedOrigin+"/responses" || input.Method != "" && input.Method != http.MethodPost || !input.Stream || input.Replayable || !bytes.Equal(input.Body, req.Body()) || validateManagedBody(input.Body, cred.BindingModel) != nil {
		return managedFailure(503, "provider_binding_unavailable")
	}
	// The adapter gets no secret, and cannot add arbitrary upstream headers.
	if len(input.Header) != 1 || len(input.Header.Values("Authorization")) != 1 || input.Header.Get("Authorization") != "Bearer " {
		return managedFailure(503, "provider_target_unavailable")
	}
	access, err := g.acquireManaged(c.ctx, c, cred)
	if err != nil {
		return managedFailure(503, "provider_credential_unavailable")
	}
	fetched, err := g.managed.visible(c.ctx, access, cred.BindingModel)
	if err != nil {
		return managedFailure(503, "provider_model_unavailable")
	}
	if fetched {
		// Directory I/O is a separate wire request. Re-issue at the authoritative
		// commit before inference, so intervening disable/refresh fails closed.
		access, err = g.acquireManaged(c.ctx, c, cred)
		if err != nil {
			return managedFailure(503, "provider_credential_unavailable")
		}
	}
	input.Method = http.MethodPost
	input.Header = http.Header{"Authorization": {"Bearer " + access.AccessToken.Value()}}
	return nil
}
