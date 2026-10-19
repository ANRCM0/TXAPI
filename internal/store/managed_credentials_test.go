package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	cfgv1 "minigateway/internal/foundation/contracts/configuration/v1"
	cfgv2 "minigateway/internal/foundation/contracts/configuration/v2"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	authv2 "minigateway/internal/foundation/contracts/upstreamauth/v2"
	"minigateway/internal/model"
)

type managedFixture struct {
	*upstreamFixture
	account auth.Account
	input   cfgv2.ManagedChannelInput
	created *cfgv2.ManagedChannel
	request authv2.AccessRequest
}

func newManagedFixture(t *testing.T) *managedFixture {
	t.Helper()
	f := &managedFixture{upstreamFixture: newUpstreamFixture(t)}
	write := f.write(1)
	write.Tokens.Scopes = []string{"chatgpt.tokens.use.direct", "openid", "resource.invoke"}
	var err error
	f.account, err = f.repo.SaveAuthorization(context.Background(), write)
	if err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "managed-owner", PasswordHash: "not-used", Role: model.RoleUser, Status: model.UserStatusActive, ModelMode: access.All}
	if err = f.store.DB().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserID: user.ID, Name: "managed-key", KeyHash: access.Hash("controlled-managed-key"), Status: model.TokenStatusActive, ModelMode: access.All}
	if err = f.store.DB().Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	f.input = cfgv2.ManagedChannelInput{Channel: cfgv1.ChannelInput{Name: "managed", Type: managedChannelProtocol, BaseURL: managedSIWCResource, Models: []string{"m"}},
		Binding: cfgv2.ManagedBindingInput{AccountID: f.account.ID, OwnerID: user.ID, TokenID: token.ID, Model: "m"}}
	f.created, err = f.store.ManagedChannels().CreateManagedChannel(context.Background(), &f.input)
	if err != nil {
		t.Fatal(err)
	}
	f.request = authv2.AccessRequest{CredentialID: f.created.Credential.CredentialID, ExpectedBindingRevision: 1, AccountID: f.account.ID,
		ExpectedRevision: f.account.Revision, ExpectedCredentialRevision: f.account.CredentialRevision, OwnerID: user.ID, TokenID: token.ID, Model: "m"}
	return f
}

func TestManagedCredentialPersistentBindingAndNoStaticSecrets(t *testing.T) {
	f := newManagedFixture(t)
	out, err := f.repo.AcquireAccess(context.Background(), f.request)
	if err != nil || out.AccessToken.Value() != f.write(1).Tokens.AccessToken.Value() {
		t.Fatalf("credential not issued: %v", err)
	}
	if strings.Contains(out.AccessToken.String(), "fixture-private") {
		t.Fatal("secret formatting exposed token")
	}
	var raw model.Credential
	if err = f.store.DB().First(&raw, f.request.CredentialID).Error; err != nil {
		t.Fatal(err)
	}
	if raw.APIKey != "" || len(raw.KeyCiphertext) != 0 || raw.KeyHash != "" || raw.Source != model.CredentialSourceManaged {
		t.Fatal("managed token copied into static credential")
	}
	_ = f.store.Close()
	restarted := f.open(t, f.key)
	view, err := restarted.ManagedChannels().GetManagedCredential(context.Background(), f.request.CredentialID)
	if err != nil || *view != f.created.Credential {
		t.Fatalf("binding changed after restart: %+v %v", view, err)
	}
	channels, snapshotErr := restarted.ChannelSnapshot()
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if len(channels) != 1 || len(channels[0].Credentials) != 1 || channels[0].Credentials[0].APIKey != "" || channels[0].Credentials[0].AccountRevision != f.account.Revision || channels[0].Credentials[0].CredentialRevision != f.account.CredentialRevision {
		t.Fatalf("unsafe runtime snapshot: %+v", channels)
	}
	if _, err = f.repository(restarted).AcquireAccess(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
	if err = checkMigrationVersion(restarted.DB(), 17); err == nil {
		t.Fatal("schema17 accepted18")
	}
}

func TestManagedCredentialRejectsWrongScopeAndStaleRevisions(t *testing.T) {
	f := newManagedFixture(t)
	for name, mutate := range map[string]func(*authv2.AccessRequest){
		"owner": func(r *authv2.AccessRequest) { r.OwnerID++ }, "key": func(r *authv2.AccessRequest) { r.TokenID++ },
		"model": func(r *authv2.AccessRequest) { r.Model = "other" }, "account": func(r *authv2.AccessRequest) { r.AccountID = upstreamFixtureID(2) },
		"binding revision": func(r *authv2.AccessRequest) { r.ExpectedBindingRevision++ }, "account revision": func(r *authv2.AccessRequest) { r.ExpectedRevision++ },
		"credential revision": func(r *authv2.AccessRequest) { r.ExpectedCredentialRevision++ },
	} {
		t.Run(name, func(t *testing.T) {
			r := f.request
			mutate(&r)
			out, err := f.repo.AcquireAccess(context.Background(), r)
			if err == nil || out.AccessToken.Value() != "" {
				t.Fatalf("invalid scope issued token: %v", err)
			}
		})
	}
	if err := f.store.DB().Model(&model.User{}).Where("id = ?", f.request.OwnerID).Update("model_mode", access.Allowlist).Error; err != nil {
		t.Fatal(err)
	}
	if out, err := f.repo.AcquireAccess(context.Background(), f.request); err == nil || out.AccessToken.Value() != "" {
		t.Fatal("current user policy was not rechecked")
	}
}

func TestManagedCredentialCASUniquenessAndStaticMutationIsolation(t *testing.T) {
	f := newManagedFixture(t)
	in := f.input
	in.Channel.Name = "second"
	if _, err := f.store.ManagedChannels().CreateManagedChannel(context.Background(), &in); !errors.Is(err, cfgv2.ErrConflict) {
		t.Fatalf("second active grant accepted: %v", err)
	}
	var count int64
	f.store.DB().Model(&model.Channel{}).Count(&count)
	if count != 1 {
		t.Fatal("conflicting create left partial channel")
	}
	for name, call := range map[string]func() error{
		"channel update": func() error {
			_, e := f.store.UpdateChannel(f.created.Channel.ID, &ChannelInput{Name: "replace", Type: "openai", BaseURL: "https://example.test", Models: []string{"m"}, APIKey: "fake"})
			return e
		},
		"channel delete": func() error { return f.store.DeleteChannel(f.created.Channel.ID) },
		"credential create": func() error {
			_, e := f.store.CreateCredential(f.created.Channel.ID, &CredentialInput{APIKey: "fake"})
			return e
		},
		"credential update": func() error {
			_, e := f.store.UpdateCredential(f.request.CredentialID, &CredentialInput{APIKey: "fake"})
			return e
		},
		"credential delete": func() error { return f.store.DeleteCredential(f.request.CredentialID) },
		"credential status": func() error {
			_, e := f.store.SetCredentialStatus(f.request.CredentialID, model.CredentialDisabled)
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			if e := call(); !errors.Is(e, cfgv2.ErrConflict) {
				t.Fatalf("static mutation changed managed row: %v", e)
			}
		})
	}
	updated, err := f.store.ManagedChannels().UpdateManagedCredential(context.Background(), f.request.CredentialID, 1, &f.input.Binding)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("CAS update failed: %+v %v", updated, err)
	}
	if _, err = f.store.ManagedChannels().DisableManagedCredential(context.Background(), f.request.CredentialID, 1); !errors.Is(err, cfgv2.ErrConflict) {
		t.Fatal("stale CAS accepted", err)
	}
	if out, err := f.repo.AcquireAccess(context.Background(), f.request); err == nil || out.AccessToken.Value() != "" {
		t.Fatal("stale binding granted token")
	}
	f.request.ExpectedBindingRevision = 2
	if _, err = f.repo.AcquireAccess(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ManagedChannels().DisableManagedCredential(context.Background(), f.request.CredentialID, 2); err != nil {
		t.Fatal(err)
	}
	if out, err := f.repo.AcquireAccess(context.Background(), f.request); err == nil || out.AccessToken.Value() != "" {
		t.Fatal("disabled grant issued token")
	}
}

func TestManagedCredentialCrossConnectionDisableAndRefreshOrdering(t *testing.T) {
	for _, action := range []string{"account disable", "refresh claim", "binding disable"} {
		t.Run(action, func(t *testing.T) {
			f := newManagedFixture(t)
			other := f.open(t, f.key)
			remote := f.repository(other)
			issued, err := f.repo.AcquireAccess(context.Background(), f.request)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "account disable":
				_, err = remote.Disable(context.Background(), f.account.ID, f.account.Revision)
			case "refresh claim":
				f.now = f.account.NextRefreshAt
				_, err = remote.ClaimRefresh(context.Background(), f.account.ID, f.account.Revision)
			case "binding disable":
				_, err = other.ManagedChannels().DisableManagedCredential(context.Background(), f.request.CredentialID, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			if issued.AccessToken.Value() == "" {
				t.Fatal("later disable retracted previously issued material")
			}
			if out, err := f.repo.AcquireAccess(context.Background(), f.request); err == nil || out.AccessToken.Value() != "" {
				t.Fatal("post-revocation issuance succeeded")
			}
		})
	}
}

func TestManagedCredentialCrossConnectionTransactionLinearization(t *testing.T) {
	for _, first := range []string{"issuance", "disable"} {
		t.Run(first, func(t *testing.T) {
			f := newManagedFixture(t)
			other := f.open(t, f.key)
			remote := f.repository(other)
			entered, release := make(chan struct{}), make(chan struct{})
			var holdOnce, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			lockedStore := f.store
			if first == "disable" {
				lockedStore = other
			}
			if err := lockedStore.DB().Callback().Query().After("gorm:query").Register("managed_hold_account_snapshot", func(tx *gorm.DB) {
				if tx.Statement.Table == "managed_upstream_accounts" {
					holdOnce.Do(func() { close(entered); <-release })
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { lockedStore.DB().Callback().Query().Remove("managed_hold_account_snapshot") })
			type issuance struct {
				credential authv2.AccessCredential
				err        error
			}
			issued, disabled := make(chan issuance, 1), make(chan error, 1)
			issue := func() {
				out, err := f.repo.AcquireAccess(context.Background(), f.request)
				issued <- issuance{out, err}
			}
			disable := func() {
				_, err := remote.Disable(context.Background(), f.account.ID, f.account.Revision)
				disabled <- err
			}
			if first == "issuance" {
				go issue()
			} else {
				go disable()
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("first transaction did not reach locked snapshot")
			}
			if first == "issuance" {
				go disable()
			} else {
				go issue()
			}
			// The opposite writer cannot pass the first writer's SQLite lock.
			if first == "issuance" {
				select {
				case err := <-disabled:
					t.Fatalf("disable crossed issuance transaction: %v", err)
				case <-time.After(30 * time.Millisecond):
				}
			} else {
				select {
				case out := <-issued:
					t.Fatalf("issuance crossed disable transaction: %v", out.err)
				case <-time.After(30 * time.Millisecond):
				}
			}
			unblock()
			select {
			case err := <-disabled:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("disable did not finish")
			}
			select {
			case out := <-issued:
				if first == "issuance" && (out.err != nil || out.credential.AccessToken.Value() == "") {
					t.Fatalf("prior issuance lost: %v", out.err)
				}
				if first == "disable" && (out.err == nil || out.credential.AccessToken.Value() != "") {
					t.Fatal("post-disable issuance succeeded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("issuance did not finish")
			}
		})
	}
}

func TestManagedCredentialProvenanceScopesExpiryAndUncertainFailClosed(t *testing.T) {
	for name, changes := range map[string]map[string]any{
		"unknown provenance": {"auth_source": "", "resource": ""}, "wrong resource": {"resource": "https://other.invalid"},
		"unknown scopes": {"scopes_known": false, "scopes_json": "[]"}, "missing scope": {"scopes_json": "[\"resource.invoke\"]"},
		"uncertain": {"state": string(auth.Uncertain)}, "expired": {"expires_at": time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newManagedFixture(t)
			if err := f.store.DB().Model(&model.ManagedUpstreamAccount{}).Where("id = ?", f.account.ID).Updates(changes).Error; err != nil {
				t.Fatal(err)
			}
			if out, err := f.repo.AcquireAccess(context.Background(), f.request); err == nil || out.AccessToken.Value() != "" {
				t.Fatal("unproven account granted material")
			}
		})
	}
}

func TestManagedCredentialCommitFailureDoesNotReturnToken(t *testing.T) {
	f := newManagedFixture(t)
	// The lock update succeeds and token decryption proceeds; only COMMIT
	// rejects this deliberately deferred constraint. Nothing may escape then.
	for _, statement := range []string{
		"PRAGMA foreign_keys=ON",
		"CREATE TABLE managed_commit_parent (id integer PRIMARY KEY)",
		"CREATE TABLE managed_commit_child (parent_id integer REFERENCES managed_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)",
		fmt.Sprintf("CREATE TRIGGER managed_defer_commit_failure AFTER UPDATE ON schema_migrations WHEN OLD.version = %d BEGIN INSERT INTO managed_commit_child(parent_id) VALUES(99); END", latestMigrationVersion),
	} {
		if err := f.store.DB().Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if out, err := f.repo.AcquireAccess(context.Background(), f.request); err == nil || out.AccessToken.Value() != "" {
		t.Fatal("failed transaction returned token")
	}
}

func TestManagedCredentialHealthCannotMutateManagedSource(t *testing.T) {
	f := newManagedFixture(t)
	if err := f.store.UpdateCredentialHealth(HealthUpdate{CredentialID: f.request.CredentialID, Status: model.CredentialBanned, FailCount: 9}); err != nil {
		t.Fatal(err)
	}
	var row model.Credential
	if err := f.store.DB().First(&row, f.request.CredentialID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != model.CredentialActive || row.FailCount != 0 {
		t.Fatal("static health mutated managed grant")
	}
}

func TestManagedCredentialMutationRollbackIsAtomic(t *testing.T) {
	f := newManagedFixture(t)
	if err := f.store.DB().Exec("CREATE TRIGGER managed_reject_binding BEFORE UPDATE ON credentials WHEN OLD.source = 'managed_account' BEGIN SELECT RAISE(ABORT,'controlled-binding-failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	changed := f.input.Binding
	changed.Model = "other-model"
	if _, err := f.store.ManagedChannels().UpdateManagedCredential(context.Background(), f.request.CredentialID, 1, &changed); err == nil {
		t.Fatal("failed write accepted")
	}
	var ch model.Channel
	if err := f.store.DB().First(&ch, f.created.Channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(ch.Models) != 1 || ch.Models[0] != "m" {
		t.Fatal("partial model/ability update committed")
	}
	if _, err := f.repo.AcquireAccess(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCredentialSchema18DoesNotInferOldAccountProvenance(t *testing.T) {
	f := newUpstreamFixture(t)
	account := f.save(t, 1)
	static := encryptedChannel(t, f.store, "old-static", "controlled-static-key")
	var original model.Credential
	if err := f.store.DB().First(&original, static.Credentials[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	// Reconstruct the v17 authenticated envelope, which contained no provenance.
	if err := f.store.DB().Transaction(func(tx *gorm.DB) error {
		row, err := upstreamFind(tx, account.ID)
		if err != nil {
			return err
		}
		tokens, err := f.store.openUpstreamAccount(tx, row)
		if err != nil {
			return err
		}
		row.AuthSource, row.Resource = "", ""
		sealed, err := f.store.sealUpstreamAccount(tx, row, tokens)
		if err != nil {
			return err
		}
		return tx.Model(&model.ManagedUpstreamAccount{}).Where("id = ?", row.ID).Updates(map[string]any{"auth_source": "", "resource": "", "token_ciphertext": sealed}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Where("version >= ?", 18).Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Exec("DROP INDEX idx_one_active_managed_binding").Error; err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"AuthSource", "Resource"} {
		if err := f.store.DB().Migrator().DropColumn(&model.ManagedUpstreamAccount{}, field); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"Source", "AccountID", "BindingOwnerID", "BindingTokenID", "BindingModel", "BindingRevision"} {
		if err := f.store.DB().Migrator().DropColumn(&model.Credential{}, field); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.store.Close()
	upgraded := f.open(t, f.key)
	row := upstreamRaw(t, upgraded, account.ID)
	if row.AuthSource != "" || row.Resource != "" {
		t.Fatal("migration invented SIWC source proof")
	}
	var retained model.Credential
	if err := upgraded.DB().First(&retained, original.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retained.Source != model.CredentialSourceStatic || retained.AccountID != "" || retained.BindingRevision != 0 || string(retained.KeyCiphertext) != string(original.KeyCiphertext) || retained.KeyHash != original.KeyHash {
		t.Fatal("migration rewrote static identity/material")
	}
	if _, err := f.repository(upgraded).Read(context.Background(), account.ID); err != nil {
		t.Fatal("old account metadata became unreadable", err)
	}
	if err := upgraded.DB().Transaction(func(tx *gorm.DB) error { _, e := upgraded.openUpstreamAccount(tx, row); return e }); err != nil {
		t.Fatal("old account ciphertext damaged", err)
	}
	input := cfgv2.ManagedChannelInput{Channel: cfgv1.ChannelInput{Name: "unproven", Type: managedChannelProtocol, BaseURL: managedSIWCResource, Models: []string{"m"}}, Binding: cfgv2.ManagedBindingInput{AccountID: account.ID, OwnerID: 1, TokenID: 1, Model: "m"}}
	if _, err := upgraded.ManagedChannels().CreateManagedChannel(context.Background(), &input); !errors.Is(err, cfgv2.ErrConflict) {
		t.Fatalf("unknown-provenance account gained inference binding: %v", err)
	}
	if err := checkMigrationVersion(upgraded.DB(), 17); err == nil {
		t.Fatal("old schema17 binary accepted18")
	}
}
