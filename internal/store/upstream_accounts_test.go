package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/config"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/model"
	"minigateway/internal/secrets"
)

type upstreamFixture struct {
	store     *Store
	repo      *upstreamAccounts
	path, key string
	now       time.Time
}

func newUpstreamFixture(t *testing.T) *upstreamFixture {
	t.Helper()
	f := &upstreamFixture{path: filepath.Join(t.TempDir(), "accounts.db"), now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	f.key = credentialKeyFile(t, "key-1", map[string]byte{"key-1": 21}, 72)
	f.store = f.open(t, f.key)
	f.repo = f.repository(f.store)
	return f
}

func (f *upstreamFixture) open(t *testing.T, key string) *Store {
	t.Helper()
	st, err := Open(config.DatabaseConfig{Path: f.path, CredentialKeysFile: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func (f *upstreamFixture) repository(st *Store) *upstreamAccounts {
	r := st.UpstreamAccounts().(*upstreamAccounts)
	r.now = func() time.Time { return f.now }
	return r
}

func upstreamFixtureID(n int) string { return fmt.Sprintf("ua_%032x", n) }

func (f *upstreamFixture) write(n int) auth.AuthorizationWrite {
	return auth.AuthorizationWrite{ID: upstreamFixtureID(n), Provider: "openai", Name: fmt.Sprintf("Account %d", n),
		ClientID: "issued-client", Identity: auth.Identity{Issuer: upstreamIssuer, Subject: fmt.Sprintf("verified-subject-%d", n)},
		Tokens: auth.Tokens{AccessToken: auth.NewSecret(fmt.Sprintf("fixture-private-access-%d", n)), RefreshToken: auth.NewSecret(fmt.Sprintf("fixture-private-refresh-%d", n)),
			IDToken: auth.NewSecret(fmt.Sprintf("fixture-private-id-token-%d", n)), Nonce: auth.NewSecret(fmt.Sprintf("fixture-private-nonce-%d", n)),
			ExpiresAt: f.now.Add(time.Hour), ScopesKnown: true, Scopes: []string{"openid", "profile"}}}
}

func (f *upstreamFixture) save(t *testing.T, n int) auth.Account {
	t.Helper()
	out, err := f.repo.SaveAuthorization(context.Background(), f.write(n))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *upstreamFixture) claim(t *testing.T, account auth.Account) auth.Claim {
	t.Helper()
	f.now = maxTime(f.now, account.NextRefreshAt)
	claim, err := f.repo.ClaimRefresh(context.Background(), account.ID, account.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func maxTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

func upstreamRaw(t *testing.T, st *Store, id string) model.ManagedUpstreamAccount {
	t.Helper()
	var row model.ManagedUpstreamAccount
	if err := st.DB().Where("id = ?", id).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func TestUpstreamAccountsEncryptedLifecycleAndScopes(t *testing.T) {
	f := newUpstreamFixture(t)
	in := f.write(1)
	account, err := f.repo.SaveAuthorization(context.Background(), in)
	if err != nil || account.Revision != 1 || account.CredentialRevision != 1 || account.State != auth.Active || !account.HasRefresh ||
		!account.NextRefreshAt.Equal(f.now.Add(5*time.Second)) || !account.ScopesKnown || !slices.Equal(account.Scopes, []string{"openid", "profile"}) {
		t.Fatal("authorization metadata incorrect", account, err)
	}
	in.Tokens.Scopes[0], account.Scopes[0] = "caller-change", "consumer-change"
	read, err := f.repo.Read(context.Background(), account.ID)
	if err != nil || !slices.Equal(read.Scopes, []string{"openid", "profile"}) {
		t.Fatal("stored scopes share a caller slice", read, err)
	}
	if claim, err := f.repo.ClaimRefresh(context.Background(), account.ID, account.Revision); !errors.Is(err, auth.ErrBackoff) || claim != (auth.Claim{}) {
		t.Fatal("fresh authorization did not retain minimum cooldown", err)
	}
	claim := f.claim(t, read)
	if claim.Revision != 2 || claim.CredentialRevision != 1 || claim.Fence <= 1 || claim.Proof.Value() == "" || !claim.LeaseUntil.Equal(f.now.Add(90*time.Second)) {
		t.Fatal("claim did not freeze authoritative revision and finite lease", claim)
	}
	before := upstreamRaw(t, f.store, account.ID)
	if before.RefreshStarted || strings.Contains(string(before.TokenCiphertext), "fixture-private") || before.ClaimHash == claim.Proof.Value() {
		t.Fatal("claim leaked plaintext or prematurely started")
	}
	credential, err := f.repo.StartRefresh(context.Background(), claim)
	if err != nil || credential.Tokens.AccessToken.Value() != f.write(1).Tokens.AccessToken.Value() || !slices.Equal(credential.Tokens.Scopes, []string{"openid", "profile"}) {
		t.Fatal("authenticated token material missing", err)
	}
	if !upstreamRaw(t, f.store, account.ID).RefreshStarted {
		t.Fatal("token returned before durable dispatch marker")
	}
	if again, err := f.repo.StartRefresh(context.Background(), claim); !errors.Is(err, auth.ErrUncertain) || !reflect.DeepEqual(again, auth.Credential{}) {
		t.Fatal("repeated start exposed refresh token again", err)
	}
	rotated := credential.Tokens
	rotated.AccessToken, rotated.RefreshToken = auth.NewSecret("rotated-private-access"), auth.NewSecret("rotated-private-refresh")
	rotated.ScopesKnown, rotated.Scopes = true, []string{}
	rotated.ExpiresAt = f.now.Add(2 * time.Hour)
	finished, err := f.repo.CompleteRefresh(context.Background(), claim, rotated)
	if err != nil || finished.Revision != claim.Revision+1 || finished.CredentialRevision != claim.CredentialRevision+1 ||
		!finished.ScopesKnown || finished.Scopes == nil || len(finished.Scopes) != 0 || !finished.NextRefreshAt.Equal(f.now.Add(5*time.Second)) {
		t.Fatal("rotation lost exact revisions or explicit empty scopes", finished, err)
	}
	after := upstreamRaw(t, f.store, account.ID)
	if after.RefreshStarted || after.ClaimHash != "" || !after.ClaimUntil.IsZero() || bytes.Equal(before.TokenCiphertext, after.TokenCiphertext) {
		t.Fatal("rotation did not clear claim and replace authenticated ciphertext")
	}
	claim2 := f.claim(t, finished)
	got, err := f.repo.StartRefresh(context.Background(), claim2)
	if err != nil || got.Tokens.RefreshToken.Value() != "rotated-private-refresh" || got.Tokens.Scopes == nil || !got.Tokens.ScopesKnown {
		t.Fatal("new revision did not decrypt rotated material", err)
	}
	if _, err := f.repo.CompleteRefresh(context.Background(), claim, credential.Tokens); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("old success overwrote new material", err)
	}
}

func TestUpstreamAccountsScopeUnknownAndValidation(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			f := newUpstreamFixture(t)
			in := f.write(1)
			in.Tokens.ScopesKnown, in.Tokens.Scopes = known, nil
			out, err := f.repo.SaveAuthorization(context.Background(), in)
			if err != nil || out.ScopesKnown != known || len(out.Scopes) != 0 || known && out.Scopes == nil {
				t.Fatal("unknown and known-empty scopes collapsed", out, err)
			}
			encoded, _ := json.Marshal(out)
			if known && !strings.Contains(string(encoded), `"scopes":[]`) || !known && !strings.Contains(string(encoded), `"scopes_known":false`) {
				t.Fatal("scope JSON lost known-state", string(encoded))
			}
		})
	}
	f := newUpstreamFixture(t)
	for _, test := range []struct {
		name   string
		change func(*auth.AuthorizationWrite)
	}{
		{"empty-id", func(in *auth.AuthorizationWrite) { in.ID = "" }},
		{"provider", func(in *auth.AuthorizationWrite) { in.Provider = "other" }},
		{"issuer", func(in *auth.AuthorizationWrite) { in.Identity.Issuer += "/" }},
		{"subject", func(in *auth.AuthorizationWrite) { in.Identity.Subject = "space subject" }},
		{"client", func(in *auth.AuthorizationWrite) { in.ClientID = strings.Repeat("c", 513) }},
		{"name", func(in *auth.AuthorizationWrite) { in.Name = "bad\nname" }},
		{"missing-at", func(in *auth.AuthorizationWrite) { in.Tokens.AccessToken = auth.NewSecret("") }},
		{"large-token", func(in *auth.AuthorizationWrite) {
			in.Tokens.RefreshToken = auth.NewSecret(strings.Repeat("r", auth.MaxTokenBytes+1))
		}},
		{"large-envelope", func(in *auth.AuthorizationWrite) {
			in.Tokens.AccessToken = auth.NewSecret(strings.Repeat("\"", auth.MaxTokenBytes))
			in.Tokens.RefreshToken = in.Tokens.AccessToken
			in.Tokens.IDToken = in.Tokens.AccessToken
		}},
		{"nonce", func(in *auth.AuthorizationWrite) { in.Tokens.Nonce = auth.NewSecret("") }},
		{"expiry", func(in *auth.AuthorizationWrite) { in.Tokens.ExpiresAt = f.now }},
		{"scope-unknown-nonempty", func(in *auth.AuthorizationWrite) { in.Tokens.ScopesKnown = false }},
		{"scope-duplicate", func(in *auth.AuthorizationWrite) { in.Tokens.Scopes = []string{"openid", "openid"} }},
		{"scope-unsorted", func(in *auth.AuthorizationWrite) { in.Tokens.Scopes = []string{"profile", "openid"} }},
		{"scope-large", func(in *auth.AuthorizationWrite) { in.Tokens.Scopes = []string{strings.Repeat("s", 129)} }},
		{"scope-count", func(in *auth.AuthorizationWrite) {
			in.Tokens.Scopes = make([]string, 33)
			for i := range in.Tokens.Scopes {
				in.Tokens.Scopes[i] = fmt.Sprintf("scope-%02d", i)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := f.write(10)
			test.change(&in)
			if out, err := f.repo.SaveAuthorization(context.Background(), in); !errors.Is(err, auth.ErrInvalid) || !reflect.DeepEqual(out, auth.Account{}) {
				t.Fatal("invalid input was accepted or returned metadata", err)
			}
		})
	}
	page, err := f.repo.List(context.Background(), 100, 0)
	if err != nil || len(page.Data) != 0 {
		t.Fatal("invalid writes leaked rows", page, err)
	}
}

func TestUpstreamAccountsReauthorizationAndDisableWinOverOldRefresh(t *testing.T) {
	for _, action := range []string{"reauthorize", "disable"} {
		t.Run(action, func(t *testing.T) {
			f := newUpstreamFixture(t)
			account := f.save(t, 1)
			claim := f.claim(t, account)
			old, err := f.repo.StartRefresh(context.Background(), claim)
			if err != nil {
				t.Fatal(err)
			}
			var current auth.Account
			if action == "reauthorize" {
				begun, err := f.repo.BeginAuthorization(context.Background(), account.ID, claim.Revision)
				if err != nil || begun.State != auth.Reauthorize {
					t.Fatal(begun, err)
				}
				in := f.write(1)
				in.ExpectedRevision = begun.Revision
				in.Tokens.AccessToken = auth.NewSecret("new-authorization-access")
				current, err = f.repo.SaveAuthorization(context.Background(), in)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				current, err = f.repo.Disable(context.Background(), account.ID, claim.Revision)
				if err != nil || current.State != auth.Disabled {
					t.Fatal(current, err)
				}
			}
			before := upstreamRaw(t, f.store, account.ID)
			if out, err := f.repo.CompleteRefresh(context.Background(), claim, old.Tokens); !errors.Is(err, auth.ErrConflict) || !reflect.DeepEqual(out, auth.Account{}) {
				t.Fatal("stale completion overwrote administrator action", err)
			}
			if _, err := f.repo.FinishRefresh(context.Background(), claim, auth.UnknownFailure); !errors.Is(err, auth.ErrConflict) {
				t.Fatal(err)
			}
			if after := upstreamRaw(t, f.store, account.ID); !reflect.DeepEqual(before, after) {
				t.Fatal("stale failure changed administrator result")
			}
			if action == "reauthorize" {
				next := f.claim(t, current)
				credential, err := f.repo.StartRefresh(context.Background(), next)
				if err != nil || credential.Tokens.AccessToken.Value() != "new-authorization-access" {
					t.Fatal("new authorization material lost", err)
				}
			} else if _, err := f.repo.ClaimRefresh(context.Background(), account.ID, current.Revision); !errors.Is(err, auth.ErrReauthorize) {
				t.Fatal(err)
			}
		})
	}
}

func TestUpstreamAccountsIdentityDedupAndImmutableReauthorizationBinding(t *testing.T) {
	f := newUpstreamFixture(t)
	account := f.save(t, 1)
	duplicate := f.write(1)
	duplicate.ID = upstreamFixtureID(2)
	if _, err := f.repo.SaveAuthorization(context.Background(), duplicate); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("duplicate identity obtained another refresh row", err)
	}
	duplicate.ClientID = "different-issued-client"
	if _, err := f.repo.SaveAuthorization(context.Background(), duplicate); err != nil {
		t.Fatal("independent registered client rejected", err)
	}
	in := f.write(1)
	in.ExpectedRevision = account.Revision
	if _, err := f.repo.SaveAuthorization(context.Background(), in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("replacement skipped BeginAuthorization", err)
	}
	begun, err := f.repo.BeginAuthorization(context.Background(), account.ID, account.Revision)
	if err != nil {
		t.Fatal(err)
	}
	before := upstreamRaw(t, f.store, account.ID)
	for _, part := range []string{"subject", "client"} {
		in := f.write(1)
		in.ExpectedRevision = begun.Revision
		if part == "subject" {
			in.Identity.Subject = "another-subject"
		} else {
			in.ClientID = "another-client"
		}
		if out, err := f.repo.SaveAuthorization(context.Background(), in); !errors.Is(err, auth.ErrIdentityMismatch) || !reflect.DeepEqual(out, auth.Account{}) {
			t.Fatal("identity retargeting accepted", part, err)
		}
	}
	if after := upstreamRaw(t, f.store, account.ID); !reflect.DeepEqual(before, after) {
		t.Fatal("identity mismatch changed authorization state")
	}
}

func TestUpstreamAccountsClaimProofGuardsAndStartFailureReturnsNoSecret(t *testing.T) {
	f := newUpstreamFixture(t)
	account := f.save(t, 1)
	claim := f.claim(t, account)
	before := upstreamRaw(t, f.store, account.ID)
	for _, field := range []string{"row", "credential", "fence", "proof", "lease"} {
		bad := claim
		switch field {
		case "row":
			bad.Revision++
		case "credential":
			bad.CredentialRevision++
		case "fence":
			bad.Fence++
		case "proof":
			bad.Proof = auth.NewSecret(strings.Repeat("a", 64))
		case "lease":
			bad.LeaseUntil = bad.LeaseUntil.Add(time.Millisecond)
		}
		if out, err := f.repo.StartRefresh(context.Background(), bad); !errors.Is(err, auth.ErrConflict) || !reflect.DeepEqual(out, auth.Credential{}) {
			t.Fatal("claim guard bypassed", field, err)
		}
	}
	if after := upstreamRaw(t, f.store, account.ID); !reflect.DeepEqual(before, after) {
		t.Fatal("forged proofs mutated account")
	}
	if err := f.store.DB().Exec("CREATE TRIGGER upstream_reject_started BEFORE UPDATE OF refresh_started ON managed_upstream_accounts WHEN NEW.refresh_started = 1 BEGIN SELECT RAISE(ABORT,'private-start-storage-failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if out, err := f.repo.StartRefresh(context.Background(), claim); !errors.Is(err, auth.ErrUnavailable) || !reflect.DeepEqual(out, auth.Credential{}) || strings.Contains(err.Error(), "private-start") {
		t.Fatal("failed marker commit leaked material or SQL error", err)
	}
	if after := upstreamRaw(t, f.store, account.ID); after.RefreshStarted || after.ClaimHash == "" {
		t.Fatal("failed marker was partially committed")
	}
}

func TestUpstreamAccountsExpiredStartedCommitsUncertainOnSemanticErrors(t *testing.T) {
	for _, operation := range []string{"claim", "start", "complete", "finish"} {
		t.Run(operation, func(t *testing.T) {
			f := newUpstreamFixture(t)
			account := f.save(t, 1)
			claim := f.claim(t, account)
			credential, err := f.repo.StartRefresh(context.Background(), claim)
			if err != nil {
				t.Fatal(err)
			}
			before := upstreamRaw(t, f.store, account.ID)
			f.now = claim.LeaseUntil
			switch operation {
			case "claim":
				_, err = f.repo.ClaimRefresh(context.Background(), account.ID, claim.Revision)
			case "start":
				_, err = f.repo.StartRefresh(context.Background(), claim)
			case "complete":
				_, err = f.repo.CompleteRefresh(context.Background(), claim, credential.Tokens)
			case "finish":
				_, err = f.repo.FinishRefresh(context.Background(), claim, auth.NotSentFailure)
			}
			if !errors.Is(err, auth.ErrUncertain) {
				t.Fatal("expired send did not remain uncertain", operation, err)
			}
			after := upstreamRaw(t, f.store, account.ID)
			if after.State != string(auth.Uncertain) || after.LastFailure != string(auth.UnknownFailure) || after.ClaimHash != "" || after.RefreshStarted ||
				after.RowRevision != before.RowRevision+1 || after.Fence != before.Fence+1 || !bytes.Equal(before.TokenCiphertext, after.TokenCiphertext) {
				t.Fatal("semantic refusal rolled back the safety transition")
			}
			if _, err := f.repo.ClaimRefresh(context.Background(), account.ID, after.RowRevision); !errors.Is(err, auth.ErrUncertain) {
				t.Fatal("old rotating token became refreshable", err)
			}
		})
	}
}

func TestUpstreamAccountsRestartRecoverStartedAndUnstarted(t *testing.T) {
	f := newUpstreamFixture(t)
	first, second := f.save(t, 1), f.save(t, 2)
	started, unstarted := f.claim(t, first), f.claim(t, second)
	if _, err := f.repo.StartRefresh(context.Background(), started); err != nil {
		t.Fatal(err)
	}
	_ = f.store.Close()
	f.now = maxTime(started.LeaseUntil, unstarted.LeaseUntil)
	reopened := f.open(t, f.key)
	f.repo = f.repository(reopened)
	if n, err := f.repo.Recover(context.Background(), 1); err != nil || n != 1 {
		t.Fatal("bounded recovery failed", n, err)
	}
	if n, err := f.repo.Recover(context.Background(), 1); err != nil || n != 1 {
		t.Fatal("second expired claim not recovered", n, err)
	}
	if n, err := f.repo.Recover(context.Background(), 1000); err != nil || n != 0 {
		t.Fatal("recovery replay changed rows", n, err)
	}
	a, err := f.repo.Read(context.Background(), first.ID)
	if err != nil || a.State != auth.Uncertain {
		t.Fatal("started crash did not quarantine old RT", a, err)
	}
	b, err := f.repo.Read(context.Background(), second.ID)
	if err != nil || b.State != auth.Active || b.LastFailure != auth.NotSentFailure || !b.NextRefreshAt.After(f.now) {
		t.Fatal("unstarted crash was not safely released", b, err)
	}
	f.now = b.NextRefreshAt
	if _, err := f.repo.ClaimRefresh(context.Background(), b.ID, b.Revision); err != nil {
		t.Fatal("proved unstarted work could not be reclaimed", err)
	}
}

func TestUpstreamAccountsFailedSuccessStorageNeverReexposesOldRefreshToken(t *testing.T) {
	f := newUpstreamFixture(t)
	account := f.save(t, 1)
	claim := f.claim(t, account)
	credential, err := f.repo.StartRefresh(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Exec("CREATE TRIGGER upstream_reject_rotation BEFORE UPDATE OF token_ciphertext ON managed_upstream_accounts WHEN NEW.credential_revision > OLD.credential_revision BEGIN SELECT RAISE(ABORT,'private-rotation-storage-failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	rotated := credential.Tokens
	rotated.RefreshToken = auth.NewSecret("new-private-rotating-token")
	if out, err := f.repo.CompleteRefresh(context.Background(), claim, rotated); !errors.Is(err, auth.ErrUnavailable) || !reflect.DeepEqual(out, auth.Account{}) {
		t.Fatal("failed success commit was accepted", err)
	}
	if out, err := f.repo.StartRefresh(context.Background(), claim); !errors.Is(err, auth.ErrUncertain) || !reflect.DeepEqual(out, auth.Credential{}) {
		t.Fatal("old token exposed after possible rotation", err)
	}
	f.now = claim.LeaseUntil
	if n, err := f.repo.Recover(context.Background(), 1000); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	current, err := f.repo.Read(context.Background(), account.ID)
	if err != nil || current.State != auth.Uncertain {
		t.Fatal(current, err)
	}
}

func TestUpstreamAccountsFailureClassificationAndPersistentBackoff(t *testing.T) {
	f := newUpstreamFixture(t)
	account := f.save(t, 1)
	for attempt := 1; attempt <= 7; attempt++ {
		claim := f.claim(t, account)
		if attempt%2 == 0 {
			if _, err := f.repo.StartRefresh(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
		}
		finished, err := f.repo.FinishRefresh(context.Background(), claim, auth.NotSentFailure)
		if err != nil || finished.State != auth.Active || finished.LastFailure != auth.NotSentFailure {
			t.Fatal(finished, err)
		}
		delay := finished.NextRefreshAt.Sub(f.now)
		base := int64(5)
		for i := 1; i < attempt; i++ {
			base = min(base*3, 300)
		}
		if delay < time.Duration(base*800)*time.Millisecond || delay > min(time.Duration(base*1200)*time.Millisecond, 300*time.Second) {
			t.Fatal("backoff escaped bounded stable jitter", attempt, delay)
		}
		if _, err := f.repo.ClaimRefresh(context.Background(), finished.ID, finished.Revision); !errors.Is(err, auth.ErrBackoff) {
			t.Fatal("persisted delay ignored", err)
		}
		if attempt == 3 {
			_ = f.store.Close()
			f.store = f.open(t, f.key)
			f.repo = f.repository(f.store)
			read, err := f.repo.Read(context.Background(), finished.ID)
			if err != nil || !read.NextRefreshAt.Equal(finished.NextRefreshAt) {
				t.Fatal("restart lost retry schedule", read, err)
			}
		}
		account = finished
	}
	for i, failure := range []auth.Failure{auth.RejectedFailure, auth.UnknownFailure} {
		account := f.save(t, i+2)
		claim := f.claim(t, account)
		if _, err := f.repo.StartRefresh(context.Background(), claim); err != nil {
			t.Fatal(err)
		}
		out, err := f.repo.FinishRefresh(context.Background(), claim, failure)
		if err != nil {
			t.Fatal(err)
		}
		wantState, wantError := auth.Reauthorize, auth.ErrReauthorize
		if failure == auth.UnknownFailure {
			wantState, wantError = auth.Uncertain, auth.ErrUncertain
		}
		if out.State != wantState || out.LastFailure != failure || !out.NextRefreshAt.IsZero() {
			t.Fatal("unsafe retry state", out)
		}
		f.now = f.now.Add(time.Hour)
		if _, err := f.repo.ClaimRefresh(context.Background(), out.ID, out.Revision); !errors.Is(err, wantError) {
			t.Fatal("terminal/ambiguous failure retried old RT", err)
		}
	}
}

func TestUpstreamAccountsCrossStoreClaimsCapacityAndHostID(t *testing.T) {
	f := newUpstreamFixture(t)
	var accounts [6]auth.Account
	for i := range accounts {
		accounts[i] = f.save(t, i+1)
	}
	f.now = f.now.Add(10 * time.Second)
	second := f.repository(f.open(t, f.key))
	var wg sync.WaitGroup
	hosts := make(chan string, 12)
	failures := make(chan error, 24)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := f.repo
			if i%2 != 0 {
				r = second
			}
			host, err := r.HostID(context.Background())
			if err != nil {
				failures <- err
				return
			}
			hosts <- host
		}(i)
	}
	wg.Wait()
	close(hosts)
	var hostID string
	for host := range hosts {
		if !auth.ValidHostID(host) || hostID != "" && host != hostID {
			t.Fatal("HostID differed across processes", hostID, host)
		}
		hostID = host
	}
	claims := make(chan auth.Claim, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := f.repo
			if i%2 != 0 {
				r = second
			}
			account := accounts[i%len(accounts)]
			claim, err := r.ClaimRefresh(context.Background(), account.ID, account.Revision)
			if err == nil {
				claims <- claim
			} else if !errors.Is(err, auth.ErrBusy) && !errors.Is(err, auth.ErrConflict) {
				failures <- err
			}
		}(i)
	}
	wg.Wait()
	close(claims)
	close(failures)
	for err := range failures {
		t.Fatal("cross-store transaction failed", err)
	}
	var winners []auth.Claim
	seen := map[string]bool{}
	for claim := range claims {
		if seen[claim.AccountID] {
			t.Fatal("same account acquired twice")
		}
		seen[claim.AccountID] = true
		winners = append(winners, claim)
	}
	if len(winners) != 4 {
		t.Fatal("global claim capacity was not enforced", len(winners))
	}
	var granted int
	var grantMu sync.Mutex
	for _, r := range []*upstreamAccounts{f.repo, second} {
		wg.Add(1)
		go func(r *upstreamAccounts) {
			defer wg.Done()
			_, err := r.StartRefresh(context.Background(), winners[0])
			if err == nil {
				grantMu.Lock()
				granted++
				grantMu.Unlock()
			} else if !errors.Is(err, auth.ErrUncertain) {
				t.Error(err)
			}
		}(r)
	}
	wg.Wait()
	if granted != 1 {
		t.Fatal("same RT exposed concurrently", granted)
	}
}

func TestUpstreamAccountsAADMetadataAndKeyRotation(t *testing.T) {
	for _, changed := range []string{"ciphertext", "client", "credential", "subject", "expiry", "refresh", "scopes", "scopes-known"} {
		t.Run(changed, func(t *testing.T) {
			f := newUpstreamFixture(t)
			account := f.save(t, 1)
			other := f.save(t, 2)
			row := upstreamRaw(t, f.store, account.ID)
			column, value := "token_ciphertext", any(upstreamRaw(t, f.store, other.ID).TokenCiphertext)
			switch changed {
			case "client":
				column, value = "client_id", "altered-issued-client"
			case "credential":
				column, value = "credential_revision", row.CredentialRevision+1
			case "subject":
				column, value = "subject", "altered-verified-subject"
			case "expiry":
				column, value = "expires_at", row.ExpiresAt.Add(time.Second)
			case "refresh":
				column, value = "has_refresh", false
			case "scopes":
				column, value = "scopes_json", `["openid"]`
			case "scopes-known":
				column, value = "scopes_known", false
			}
			if err := f.store.DB().Model(&model.ManagedUpstreamAccount{}).Where("id = ?", account.ID).UpdateColumn(column, value).Error; err != nil {
				t.Fatal(err)
			}
			if changed != "refresh" && changed != "scopes-known" {
				claim := f.claim(t, account)
				if out, err := f.repo.StartRefresh(context.Background(), claim); !errors.Is(err, auth.ErrEncryptionRequired) || !reflect.DeepEqual(out, auth.Credential{}) {
					t.Fatal("unauthenticated account binding accepted", changed, err)
				}
				if upstreamRaw(t, f.store, account.ID).RefreshStarted {
					t.Fatal("decryption failure committed started marker")
				}
			}
			if bad, err := Open(config.DatabaseConfig{Path: f.path, CredentialKeysFile: f.key}); err == nil {
				_ = bad.Close()
				t.Fatal("startup accepted tampered envelope metadata", changed)
			}
		})
	}
	t.Run("rotation", func(t *testing.T) {
		f := newUpstreamFixture(t)
		account := f.save(t, 1)
		before := upstreamRaw(t, f.store, account.ID)
		_ = f.store.Close()
		missing := credentialKeyFile(t, "key-2", map[string]byte{"key-2": 43}, 72)
		if bad, err := Open(config.DatabaseConfig{Path: f.path, CredentialKeysFile: missing}); err == nil {
			_ = bad.Close()
			t.Fatal("unknown historical key accepted")
		}
		if bad, err := Open(config.DatabaseConfig{Path: f.path}); err == nil {
			_ = bad.Close()
			t.Fatal("missing vault accepted retained accounts")
		}
		rotation := credentialKeyFile(t, "key-2", map[string]byte{"key-1": 21, "key-2": 43}, 72)
		rotated := f.open(t, rotation)
		after := upstreamRaw(t, rotated, account.ID)
		header, err := secrets.Header(after.TokenCiphertext)
		if err != nil || string(header[2:]) != "key-2" || bytes.Equal(before.TokenCiphertext, after.TokenCiphertext) {
			t.Fatal("active key did not reseal")
		}
		before.TokenCiphertext, after.TokenCiphertext = nil, nil
		if !reflect.DeepEqual(before, after) {
			t.Fatal("key rewrap changed logical account revision or metadata")
		}
		_ = rotated.Close()
		retired := f.open(t, missing)
		if got, err := retired.UpstreamAccounts().Read(context.Background(), account.ID); err != nil || got.Revision != account.Revision {
			t.Fatal("retired-key restart lost account", err)
		}
	})
}

func TestUpstreamAccountsOverflowPreventsTokenExposureAndMutation(t *testing.T) {
	for _, test := range []struct {
		column string
		value  int64
	}{
		{"row_revision", math.MaxInt64}, {"row_revision", math.MaxInt64 - 1}, {"fence", math.MaxInt64}, {"fence", math.MaxInt64 - 1}, {"credential_revision", math.MaxInt64},
	} {
		t.Run(fmt.Sprintf("%s-%d", test.column, test.value), func(t *testing.T) {
			f := newUpstreamFixture(t)
			account := f.save(t, 1)
			if err := f.store.DB().Model(&model.ManagedUpstreamAccount{}).Where("id = ?", account.ID).UpdateColumn(test.column, test.value).Error; err != nil {
				t.Fatal(err)
			}
			before := upstreamRaw(t, f.store, account.ID)
			f.now = f.now.Add(10 * time.Second)
			if out, err := f.repo.ClaimRefresh(context.Background(), account.ID, before.RowRevision); !errors.Is(err, auth.ErrConflict) || out != (auth.Claim{}) {
				t.Fatal("near-overflow claim can expose unsavable token exchange", err)
			}
			if after := upstreamRaw(t, f.store, account.ID); !reflect.DeepEqual(before, after) {
				t.Fatal("overflow partially updated claim")
			}
		})
	}
}

func TestUpstreamAccountsMetadataSQLSecretRedactionAndFinancialIsolation(t *testing.T) {
	f := newUpstreamFixture(t)
	ctx := context.Background()
	if err := f.store.DB().Create(&model.BillingBalance{OwnerType: "user", OwnerID: 71, UsedUnits: 9007199254740993, ReservedUnits: 137}).Error; err != nil {
		t.Fatal(err)
	}
	reservation := model.BillingReservation{ID: strings.Repeat("a", 32), UserID: 71, TokenID: 91, PriceVersion: "historical-immutable", OriginModel: "m", ReservedUnits: 137, State: "pending", CreatedAt: f.now, UpdatedAt: f.now}
	if err := f.store.DB().Create(&reservation).Error; err != nil {
		t.Fatal(err)
	}
	price := model.BillingPriceVersion{Version: "historical-immutable", Payload: "fixed-price-document", CreatedAt: f.now}
	if err := f.store.DB().Create(&price).Error; err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var statements []string
	var leaked bool
	in := f.write(1)
	private := []string{in.Tokens.AccessToken.Value(), in.Tokens.RefreshToken.Value(), in.Tokens.IDToken.Value(), in.Tokens.Nonce.Value()}
	inspect := func(tx *gorm.DB) {
		mu.Lock()
		defer mu.Unlock()
		statements = append(statements, tx.Statement.SQL.String())
		for _, value := range tx.Statement.Vars {
			var raw string
			switch value := value.(type) {
			case string:
				raw = value
			case []byte:
				raw = string(value)
			}
			for _, secret := range private {
				if strings.Contains(raw, secret) {
					leaked = true
				}
			}
		}
	}
	if err := f.store.DB().Callback().Create().After("gorm:create").Register("test:upstream-create-secrets", inspect); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Callback().Update().After("gorm:update").Register("test:upstream-update-secrets", inspect); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Callback().Query().After("gorm:query").Register("test:upstream-query-columns", inspect); err != nil {
		t.Fatal(err)
	}
	account := f.save(t, 1)
	claim := f.claim(t, account)
	credential, err := f.repo.StartRefresh(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.CompleteRefresh(ctx, claim, credential.Tokens); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	statements = nil
	mu.Unlock()
	if _, err := f.repo.List(ctx, 100, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Read(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if leaked {
		t.Error("plaintext token entered SQL parameters")
	}
	for _, statement := range statements {
		lower := strings.ToLower(statement)
		if strings.Contains(lower, "select *") || strings.Contains(lower, "token_ciphertext") || strings.Contains(lower, "claim_hash") {
			t.Error("metadata query loaded secret/private columns", statement)
		}
	}
	mu.Unlock()
	printed, _ := json.Marshal(credential)
	displayed := fmt.Sprintf("%v %+v %#v %s", credential, credential, credential, printed)
	for _, secret := range private {
		if strings.Contains(displayed, secret) {
			t.Fatal("credential wrappers leaked through formatting/JSON")
		}
	}
	var balance model.BillingBalance
	var after model.BillingReservation
	var afterPrice model.BillingPriceVersion
	if err := f.store.DB().Where("owner_type = ? AND owner_id = ?", "user", 71).First(&balance).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Where("id = ?", reservation.ID).First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Where("version = ?", price.Version).First(&afterPrice).Error; err != nil {
		t.Fatal(err)
	}
	if balance.UsedUnits != 9007199254740993 || balance.ReservedUnits != 137 || !reflect.DeepEqual(reservation, after) || !reflect.DeepEqual(price, afterPrice) {
		t.Fatal("account lifecycle changed finance/history")
	}
}

func TestUpstreamAccountsCapacityPaginationAndNoEncryptionFallback(t *testing.T) {
	f := newUpstreamFixture(t)
	// Use the same real transaction/encryption helpers to prepare a full bounded
	// table without turning this boundary test into 1000 independent sync commits.
	err := f.store.DB().Transaction(func(tx *gorm.DB) error {
		for i := 1; i <= upstreamAccountCapacity; i++ {
			in := f.write(i)
			scopesJSON, _ := upstreamScopeJSON(in.Tokens.ScopesKnown, in.Tokens.Scopes)
			row := model.ManagedUpstreamAccount{ID: in.ID, Provider: in.Provider, Name: in.Name, Issuer: in.Identity.Issuer, Subject: in.Identity.Subject,
				ClientID: in.ClientID, RowRevision: 1, CredentialRevision: 1, Fence: 1, State: string(auth.Active), HasRefresh: true,
				ScopesKnown: in.Tokens.ScopesKnown, ScopesJSON: scopesJSON, ExpiresAt: in.Tokens.ExpiresAt, CreatedAt: f.now, UpdatedAt: f.now}
			sealed, err := f.store.sealUpstreamAccount(tx, row, in.Tokens)
			if err != nil {
				return err
			}
			row.TokenCiphertext = sealed
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := f.repo.SaveAuthorization(context.Background(), f.write(1001)); !errors.Is(err, auth.ErrBusy) || !reflect.DeepEqual(out, auth.Account{}) {
		t.Fatal("account capacity not enforced", err)
	}
	first, err := f.repo.List(context.Background(), 100, 0)
	if err != nil || len(first.Data) != 100 || !first.HasMore {
		t.Fatal("bounded first page incorrect", err)
	}
	last, err := f.repo.List(context.Background(), 100, 900)
	if err != nil || len(last.Data) != 100 || last.HasMore || first.Data[0].ID == last.Data[0].ID {
		t.Fatal("last page/order incorrect", err)
	}
	for _, query := range [][2]int{{101, 0}, {-1, 0}, {1, -1}, {1, 10001}} {
		if _, err := f.repo.List(context.Background(), query[0], query[1]); !errors.Is(err, auth.ErrInvalid) {
			t.Fatal("unbounded page accepted", query, err)
		}
	}
	plain, err := Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "plain.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err := plain.UpstreamAccounts().SaveAuthorization(context.Background(), f.write(1)); !errors.Is(err, auth.ErrEncryptionRequired) {
		t.Fatal("secret write fell back to plaintext", err)
	}
	if page, err := plain.UpstreamAccounts().List(context.Background(), 10, 0); err != nil || len(page.Data) != 0 {
		t.Fatal(page, err)
	}
	if out, err := plain.UpstreamAccounts().HostID(context.Background()); !errors.Is(err, auth.ErrEncryptionRequired) || out != "" {
		t.Fatal("authorization bootstrap did not reject missing encryption before network", err)
	}
}

func TestUpstreamAccountsMigration16UpgradeFailureRollbackAndOldGuard(t *testing.T) {
	f := newUpstreamFixture(t)
	channel := encryptedChannel(t, f.store, "retained-static-channel", "fixture-static-key-never-an-oauth-account")
	var original model.Credential
	if err := f.store.DB().Where("id = ?", channel.Credentials[0].ID).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	balance := model.BillingBalance{OwnerType: "user", OwnerID: 71, UsedUnits: 617, ReservedUnits: 913}
	if err := f.store.DB().Create(&balance).Error; err != nil {
		t.Fatal(err)
	}
	// Model the exact old schema boundary: retain released steps through v16,
	// all existing tables/data, and no managed account table or v17 marker.
	if err := f.store.DB().Migrator().DropTable(&model.ManagedUpstreamAccount{}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Where("version = ?", 17).Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	var before []model.SchemaMigration
	if err := f.store.DB().Order("version").Find(&before).Error; err != nil || len(before) != len(dataMigrations)-1 || before[len(before)-1].Version != 16 {
		t.Fatal("not a schema16 fixture", len(before), err)
	}
	if err := f.store.DB().Exec("CREATE TRIGGER upstream_reject_migration17 BEFORE INSERT ON schema_migrations WHEN NEW.version = 17 BEGIN SELECT RAISE(ABORT,'controlled-managed-migration-failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if failed, err := Open(config.DatabaseConfig{Path: f.path, CredentialKeysFile: f.key}); err == nil {
		_ = failed.Close()
		t.Fatal("failed schema17 migration was accepted")
	}
	var failedVersions []model.SchemaMigration
	if err := f.store.DB().Order("version").Find(&failedVersions).Error; err != nil || !reflect.DeepEqual(before, failedVersions) {
		t.Fatal("failed migration registered/replaced version evidence", err)
	}
	if f.store.DB().Migrator().HasIndex(&model.ManagedUpstreamAccount{}, "idx_managed_account_identity") {
		t.Fatal("failed migration committed its index outside its transaction")
	}
	var retained model.Credential
	var retainedBalance model.BillingBalance
	if err := f.store.DB().Where("id = ?", original.ID).First(&retained).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Where("owner_type = ? AND owner_id = ?", "user", 71).First(&retainedBalance).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, retained) || balance != retainedBalance {
		t.Fatal("failed migration damaged old static/financial tables")
	}
	if err := f.store.DB().Exec("DROP TRIGGER upstream_reject_migration17").Error; err != nil {
		t.Fatal(err)
	}
	_ = f.store.Close()
	upgraded := f.open(t, f.key)
	var after []model.SchemaMigration
	if err := upgraded.DB().Order("version").Find(&after).Error; err != nil || len(after) != len(before)+1 || !reflect.DeepEqual(before, after[:len(before)]) {
		t.Fatal("schema16 upgrade rewrote released migration evidence", err)
	}
	if err := checkMigrationVersion(upgraded.DB(), 16); err == nil {
		t.Fatal("the existing schema16 binary guard accepts schema17")
	}
	if !upgraded.DB().Migrator().HasIndex(&model.ManagedUpstreamAccount{}, "idx_managed_account_identity") {
		t.Fatal("schema17 did not commit identity uniqueness")
	}
	page, err := upgraded.UpstreamAccounts().List(context.Background(), 10, 0)
	if err != nil || len(page.Data) != 0 {
		t.Fatal("static credentials were inferred as OAuth accounts", page, err)
	}
	if err := upgraded.DB().Where("id = ?", original.ID).First(&retained).Error; err != nil || !reflect.DeepEqual(original, retained) {
		t.Fatal("upgrade rewrote retained static identity", err)
	}
}

func TestUpstreamAccountsContextAndHostCorruptionFailClosed(t *testing.T) {
	f := newUpstreamFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := f.repo.SaveAuthorization(ctx, f.write(1)); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(out, auth.Account{}) {
		t.Fatal("canceled operation persisted/returned material", err)
	}
	if out, err := f.repo.HostID(nil); !errors.Is(err, auth.ErrInvalid) || out != "" {
		t.Fatal("nil context accepted", err)
	}
	host, err := f.repo.HostID(context.Background())
	if err != nil || !auth.ValidHostID(host) {
		t.Fatal(host, err)
	}
	if err := f.store.DB().Model(&model.Setting{}).Where("key = ?", upstreamHostSetting).UpdateColumn("value", "invalid-private-host-marker").Error; err != nil {
		t.Fatal(err)
	}
	if out, err := f.repo.HostID(context.Background()); !errors.Is(err, auth.ErrUnavailable) || out != "" || strings.Contains(err.Error(), "private") {
		t.Fatal("corrupt host identity replaced or leaked", err)
	}
	for _, limit := range []int{0, -1, 1001} {
		if n, err := f.repo.Recover(context.Background(), limit); !errors.Is(err, auth.ErrInvalid) || n != 0 {
			t.Fatal("unbounded recovery accepted", limit, n, err)
		}
	}
}

func TestUpstreamAccountsStartupRotationFailureRollsBackEveryCredentialOwner(t *testing.T) {
	f := newUpstreamFixture(t)
	first, last := f.save(t, 1), f.save(t, 2)
	channel := encryptedChannel(t, f.store, "static-before-managed-rotation", "fixture-private-static-preserved")
	before := upstreamRaw(t, f.store, first.ID)
	var staticBefore model.Credential
	if err := f.store.DB().Where("id = ?", channel.Credentials[0].ID).First(&staticBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Model(&model.ManagedUpstreamAccount{}).Where("id = ?", last.ID).UpdateColumn("token_ciphertext", before.TokenCiphertext).Error; err != nil {
		t.Fatal(err)
	}
	rotation := credentialKeyFile(t, "key-2", map[string]byte{"key-1": 21, "key-2": 43}, 72)
	if failed, err := Open(config.DatabaseConfig{Path: f.path, CredentialKeysFile: rotation}); err == nil {
		_ = failed.Close()
		t.Fatal("startup accepted the last account's invalid AAD")
	}
	if after := upstreamRaw(t, f.store, first.ID); !reflect.DeepEqual(before, after) {
		t.Fatal("failed startup committed the earlier account's rewrap")
	}
	var staticAfter model.Credential
	if err := f.store.DB().Where("id = ?", staticBefore.ID).First(&staticAfter).Error; err != nil || !reflect.DeepEqual(staticBefore, staticAfter) {
		t.Fatal("managed vault failure partially committed static credential rotation", err)
	}
}
