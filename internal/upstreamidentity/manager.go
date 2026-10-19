// Package upstreamidentity coordinates operator-owned account credentials.
package upstreamidentity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

type manager struct {
	repository auth.Repository
	provider   auth.Provider
	caller     auth.HTTPCaller
	receiver   auth.Receiver
	now        func() time.Time
}

func New(repository auth.Repository, provider auth.Provider, caller auth.HTTPCaller, receiver auth.Receiver) (auth.Manager, error) {
	if repository == nil || provider == nil || caller == nil || receiver == nil || provider.Name() != "openai" {
		return nil, auth.ErrInvalid
	}
	return &manager{repository: repository, provider: provider, caller: caller, receiver: receiver, now: time.Now}, nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return auth.ErrInvalid
	}
	return ctx.Err()
}

func fixedError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{auth.ErrInvalid, auth.ErrUnavailable, auth.ErrNotFound, auth.ErrConflict,
		auth.ErrEncryptionRequired, auth.ErrIdentityMismatch, auth.ErrBusy, auth.ErrBackoff, auth.ErrReauthorize,
		auth.ErrUncertain, auth.ErrRejected, auth.ErrCallbackRejected, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	return auth.ErrUnavailable
}

func finishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func accountID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", auth.ErrUnavailable
	}
	return "ua_" + hex.EncodeToString(data[:]), nil
}

func (m *manager) Authorize(ctx context.Context, name, id string, revision int64, present auth.PresentAuthorization) (out auth.Account, err error) {
	if err := checkContext(ctx); err != nil {
		return out, err
	}
	if present == nil || revision < 0 || id == "" && revision != 0 || id != "" && (!auth.ValidAccountID(id) || revision == 0) {
		return out, auth.ErrInvalid
	}
	// A consumed authorization code is never replayed, including panic paths.
	exchangeMayHaveRun := false
	defer func() {
		if recover() != nil {
			out, err = auth.Account{}, auth.ErrUnavailable
			if exchangeMayHaveRun {
				err = auth.ErrUncertain
			}
		}
	}()
	var previous auth.Account
	if id != "" {
		previous, err = m.repository.Read(ctx, id)
		if err != nil {
			return out, fixedError(err)
		}
		if previous.Revision != revision {
			return out, auth.ErrConflict
		}
		if previous.Provider != m.provider.Name() {
			return out, auth.ErrIdentityMismatch
		}
		if name == "" {
			name = previous.Name
		}
	}
	if !auth.ValidName(name) {
		return out, auth.ErrInvalid
	}
	host, err := m.repository.HostID(ctx)
	if err != nil {
		return out, fixedError(err)
	}
	if id == "" {
		id, err = accountID()
		if err != nil {
			return out, err
		}
	} else {
		previous, err = m.repository.BeginAuthorization(ctx, id, revision)
		if err != nil {
			return out, fixedError(err)
		}
		revision = previous.Revision
	}
	session, err := m.receiver.Open(ctx)
	if err != nil {
		return out, fixedError(err)
	}
	defer session.Close()
	flow, err := m.provider.Begin(previous.ClientID, host, session.RedirectURI(), m.now().UTC())
	if err != nil {
		return out, fixedError(err)
	}
	if err := session.Bind(flow); err != nil {
		return out, fixedError(err)
	}
	if err := present(flow.URL); err != nil {
		return out, auth.ErrUnavailable
	}
	code, err := session.Wait(ctx)
	if err != nil {
		return out, fixedError(err)
	}
	request, err := m.provider.CodeRequest(flow, code)
	if err != nil {
		return out, fixedError(err)
	}
	work, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	exchangeMayHaveRun = true
	response := m.caller.Call(work, request)
	exchangeMayHaveRun = response.Phase != execution.NotDispatched
	tokens, err := m.provider.ReadTokens(response, m.now().UTC())
	if err != nil {
		if errors.Is(err, auth.ErrRejected) {
			return out, auth.ErrRejected
		}
		if exchangeMayHaveRun {
			return out, auth.ErrUncertain
		}
		if work.Err() != nil {
			return out, work.Err()
		}
		return out, auth.ErrUnavailable
	}
	if tokens.IDToken.Value() == "" {
		return out, auth.ErrUncertain
	}
	identity, err := m.verify(work, tokens, auth.IdentityExpectation{ClientID: code.ClientID,
		Subject: previous.Subject, Nonce: flow.Nonce, AccessToken: tokens.AccessToken, RequireNonce: true, Now: m.now().UTC()})
	if err != nil {
		return out, fixedError(err)
	}
	tokens.Nonce = flow.Nonce
	commit, finish := finishContext(ctx)
	defer finish()
	account, err := m.repository.SaveAuthorization(commit, auth.AuthorizationWrite{ID: id, ExpectedRevision: revision,
		Provider: m.provider.Name(), Name: name, ClientID: code.ClientID, Identity: identity, Tokens: tokens})
	if err != nil {
		if errors.Is(err, auth.ErrConflict) || errors.Is(err, auth.ErrIdentityMismatch) {
			return out, fixedError(err)
		}
		return out, auth.ErrUncertain
	}
	return account, nil
}

func (m *manager) verify(ctx context.Context, tokens auth.Tokens, expected auth.IdentityExpectation) (auth.Identity, error) {
	keys := m.caller.Call(ctx, m.provider.KeysRequest())
	if keys.Phase != execution.ResponseReceived || keys.Status != 200 || !keys.Complete || len(keys.Body.Value()) > auth.MaxResponseBytes {
		return auth.Identity{}, auth.ErrUncertain
	}
	identity, err := m.provider.VerifyIdentity(tokens.IDToken, keys.Body, expected)
	if err != nil {
		return auth.Identity{}, fixedError(err)
	}
	return identity, nil
}

func (m *manager) List(ctx context.Context, limit, offset int) (auth.Page, error) {
	if err := checkContext(ctx); err != nil {
		return auth.Page{}, err
	}
	if limit < 0 || limit > 100 || offset < 0 || offset > 10000 {
		return auth.Page{}, auth.ErrInvalid
	}
	if _, err := m.repository.Recover(ctx, 1000); err != nil {
		return auth.Page{}, fixedError(err)
	}
	page, err := m.repository.List(ctx, limit, offset)
	if err != nil {
		return auth.Page{}, fixedError(err)
	}
	return page, nil
}

func (m *manager) Disable(ctx context.Context, id string, revision int64) (auth.Account, error) {
	if err := checkContext(ctx); err != nil {
		return auth.Account{}, err
	}
	if !auth.ValidAccountID(id) || revision <= 0 {
		return auth.Account{}, auth.ErrInvalid
	}
	account, err := m.repository.Disable(ctx, id, revision)
	if err != nil {
		return auth.Account{}, fixedError(err)
	}
	return account, nil
}

func (m *manager) Refresh(ctx context.Context, id string, revision int64) (out auth.Account, err error) {
	if err := checkContext(ctx); err != nil {
		return out, err
	}
	if !auth.ValidAccountID(id) || revision <= 0 {
		return out, auth.ErrInvalid
	}
	var claim *auth.Claim
	possible, finished := false, false
	defer func() {
		if recover() != nil {
			out, err = auth.Account{}, auth.ErrUnavailable
			if possible {
				err = auth.ErrUncertain
			}
			if claim != nil && !finished {
				failure := auth.NotSentFailure
				if possible {
					failure = auth.UnknownFailure
				}
				m.finish(ctx, *claim, failure)
			}
		}
	}()
	claimed, err := m.repository.ClaimRefresh(ctx, id, revision)
	if err != nil {
		return out, fixedError(err)
	}
	claim = &claimed
	credential, err := m.repository.StartRefresh(ctx, claimed)
	if err != nil {
		m.finish(ctx, claimed, auth.NotSentFailure)
		finished = true
		return out, fixedError(err)
	}
	request, err := m.provider.RefreshRequest(credential.Account.ClientID, credential.Tokens.RefreshToken)
	if err != nil {
		m.finish(ctx, claimed, auth.NotSentFailure)
		finished = true
		return out, fixedError(err)
	}
	work, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	possible = true
	response := m.caller.Call(work, request)
	possible = response.Phase != execution.NotDispatched
	if !possible {
		m.finish(ctx, claimed, auth.NotSentFailure)
		finished = true
		if work.Err() != nil {
			return out, work.Err()
		}
		return out, auth.ErrUnavailable
	}
	reply, err := m.provider.ReadTokens(response, m.now().UTC())
	if err != nil {
		failure, returned := auth.UnknownFailure, auth.ErrUncertain
		if errors.Is(err, auth.ErrRejected) {
			failure, returned = auth.RejectedFailure, auth.ErrReauthorize
		}
		m.finish(ctx, claimed, failure)
		finished = true
		return out, returned
	}
	if reply.IDToken.Value() != "" {
		_, err = m.verify(work, reply, auth.IdentityExpectation{ClientID: credential.Account.ClientID,
			Subject: credential.Account.Subject, Nonce: credential.Tokens.Nonce, AccessToken: reply.AccessToken, Now: m.now().UTC()})
		if err != nil {
			m.finish(ctx, claimed, auth.UnknownFailure)
			finished = true
			return out, auth.ErrUncertain
		}
	}
	reply.Nonce = credential.Tokens.Nonce
	if reply.RefreshToken.Value() == "" {
		reply.RefreshToken = credential.Tokens.RefreshToken
	}
	if reply.IDToken.Value() == "" {
		reply.IDToken = credential.Tokens.IDToken
	}
	if !reply.ScopesKnown {
		reply.ScopesKnown, reply.Scopes = credential.Tokens.ScopesKnown, slices.Clone(credential.Tokens.Scopes)
	}
	commit, finish := finishContext(ctx)
	defer finish()
	account, err := m.repository.CompleteRefresh(commit, claimed, reply)
	if err != nil {
		m.finish(ctx, claimed, auth.UnknownFailure)
		finished = true
		if errors.Is(err, auth.ErrConflict) {
			return out, auth.ErrConflict
		}
		return out, auth.ErrUncertain
	}
	finished = true
	return account, nil
}

func (m *manager) finish(ctx context.Context, claim auth.Claim, failure auth.Failure) {
	cleanup, cancel := finishContext(ctx)
	defer cancel()
	_, _ = m.repository.FinishRefresh(cleanup, claim, failure)
}
