package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	authv3 "minigateway/internal/foundation/contracts/upstreamauth/v3"
	"minigateway/internal/model"
)

func eligibilityFailure(t *testing.T, f *managedFixture) authv3.FailureWrite {
	t.Helper()
	issued, err := f.repo.AcquireAccess(context.Background(), f.request)
	if err != nil || issued.GateEpoch <= 0 {
		t.Fatalf("no issued epoch: %v", err)
	}
	return authv3.FailureWrite{Issued: f.request, GateEpoch: issued.GateEpoch, Signal: authv3.FailureSignal{Reason: authv3.UsageLimited, Scope: authv3.Grant, ProviderCode: "subscription_sharing_usage_limit_exceeded", HTTPStatus: 429, Source: authv3.ResponsesHTTP}}
}
func eligibilityResume(g authv3.Eligibility) authv3.ResumeRequest {
	return authv3.ResumeRequest{CredentialID: g.CredentialID, ExpectedBindingRevision: g.BindingRevision, ExpectedGateRevision: g.Revision, ExpectedGateEpoch: g.GateEpoch}
}

func TestInferenceEligibilityBlocksPersistsAndResumesWithEpochFence(t *testing.T) {
	f := newManagedFixture(t)
	repo := f.store.InferenceEligibility()
	write := eligibilityFailure(t, f)
	before := upstreamRaw(t, f.store, f.account.ID)
	result, err := repo.RecordFailure(context.Background(), write)
	if err != nil || !result.Applied || !result.Eligibility.Blocked || result.Eligibility.ResetKnown || result.Eligibility.ResetAt != nil {
		t.Fatalf("failure not recorded: %+v %v", result, err)
	}
	if after := upstreamRaw(t, f.store, f.account.ID); !reflect.DeepEqual(before, after) {
		t.Fatal("eligibility changed account/tokens")
	}
	if out, err := f.repo.AcquireAccess(context.Background(), f.request); !errors.Is(err, authv3.ErrBlocked) || out.AccessToken.Value() != "" {
		t.Fatalf("blocked gate issued: %v", err)
	}
	other := f.open(t, f.key)
	otherRepo := other.InferenceEligibility()
	read, err := otherRepo.ReadEligibility(context.Background(), write.Issued.CredentialID)
	if err != nil || !reflect.DeepEqual(read, result.Eligibility) {
		t.Fatalf("gate lost across process: %+v %v", read, err)
	}
	duplicate, err := otherRepo.RecordFailure(context.Background(), write)
	if err != nil || duplicate.Applied || !reflect.DeepEqual(duplicate.Eligibility, read) {
		t.Fatal("duplicate changed gate")
	}
	resumed, err := otherRepo.ResumeEligibility(context.Background(), eligibilityResume(read))
	if err != nil || resumed.Blocked || resumed.GateEpoch != read.GateEpoch+1 || resumed.Revision != read.Revision+1 {
		t.Fatalf("resume failed: %+v %v", resumed, err)
	}
	if _, err := repo.ResumeEligibility(context.Background(), eligibilityResume(read)); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale resume accepted")
	}
	late, err := repo.RecordFailure(context.Background(), write)
	if err != nil || late.Applied || late.Eligibility.Blocked {
		t.Fatal("late old response blocked resumed same token")
	}
	issued, err := f.repo.AcquireAccess(context.Background(), f.request)
	if err != nil || issued.GateEpoch != resumed.GateEpoch {
		t.Fatal("new issuance did not carry resumed epoch")
	}
	write.GateEpoch = issued.GateEpoch
	if next, err := repo.RecordFailure(context.Background(), write); err != nil || !next.Applied {
		t.Fatal("new epoch response could not block")
	}
}

func TestInferenceEligibilityStaleTupleCannotPunishCurrentGrant(t *testing.T) {
	for _, field := range []string{"binding", "account", "revision", "credential revision", "owner", "key", "model", "epoch"} {
		t.Run(field, func(t *testing.T) {
			f := newManagedFixture(t)
			write := eligibilityFailure(t, f)
			switch field {
			case "binding":
				write.Issued.ExpectedBindingRevision++
			case "account":
				write.Issued.AccountID = upstreamFixtureID(999)
			case "revision":
				write.Issued.ExpectedRevision++
			case "credential revision":
				write.Issued.ExpectedCredentialRevision++
			case "owner":
				write.Issued.OwnerID++
			case "key":
				write.Issued.TokenID++
			case "model":
				write.Issued.Model = "other"
			case "epoch":
				write.GateEpoch++
			}
			result, err := f.store.InferenceEligibility().RecordFailure(context.Background(), write)
			if err != nil || result.Applied || result.Eligibility.Blocked {
				t.Fatalf("stale tuple applied: %+v %v", result, err)
			}
			if _, err := f.repo.AcquireAccess(context.Background(), f.request); err != nil {
				t.Fatal("stale tuple prevented current issuance", err)
			}
		})
	}
}

func TestInferenceEligibilityRefreshAndReauthorizationOrdering(t *testing.T) {
	for _, action := range []string{"claim first", "refresh first", "reauthorize first", "disable first", "block then refresh", "block then reauthorize"} {
		t.Run(action, func(t *testing.T) {
			f := newManagedFixture(t)
			repo := f.store.InferenceEligibility()
			write := eligibilityFailure(t, f)
			blocked := strings.HasPrefix(action, "block then")
			if blocked {
				if _, err := repo.RecordFailure(context.Background(), write); err != nil {
					t.Fatal(err)
				}
			}
			switch action {
			case "claim first", "refresh first", "block then refresh":
				claim := f.claim(t, f.account)
				if action != "claim first" {
					if _, err := f.repo.StartRefresh(context.Background(), claim); err != nil {
						t.Fatal(err)
					}
					tokens := f.write(1).Tokens
					tokens.Scopes = []string{"chatgpt.tokens.use.direct", "openid", "resource.invoke"}
					tokens.ExpiresAt = f.now.Add(2 * time.Hour)
					changed, err := f.repo.CompleteRefresh(context.Background(), claim, tokens)
					if err != nil {
						t.Fatal(err)
					}
					f.request.ExpectedRevision, f.request.ExpectedCredentialRevision = changed.Revision, changed.CredentialRevision
				}
			case "reauthorize first", "block then reauthorize":
				pending, err := f.repo.BeginAuthorization(context.Background(), f.account.ID, f.account.Revision)
				if err != nil {
					t.Fatal(err)
				}
				if blocked {
					save := f.write(1)
					save.ExpectedRevision = pending.Revision
					save.Tokens.Scopes = []string{"chatgpt.tokens.use.direct", "openid", "resource.invoke"}
					changed, err := f.repo.SaveAuthorization(context.Background(), save)
					if err != nil {
						t.Fatal(err)
					}
					f.request.ExpectedRevision, f.request.ExpectedCredentialRevision = changed.Revision, changed.CredentialRevision
				}
			case "disable first":
				if _, err := f.repo.Disable(context.Background(), f.account.ID, f.account.Revision); err != nil {
					t.Fatal(err)
				}
			}
			result, err := repo.RecordFailure(context.Background(), write)
			if err != nil || result.Applied || result.Eligibility.Blocked != blocked {
				t.Fatalf("ordering changed eligibility: %+v %v", result, err)
			}
			if blocked {
				if _, err := f.repo.AcquireAccess(context.Background(), f.request); !errors.Is(err, authv3.ErrBlocked) {
					t.Fatal("rotation erased gate", err)
				}
			}
		})
	}
}

func TestInferenceEligibilityRebindingAndDisableBumpEpoch(t *testing.T) {
	f := newManagedFixture(t)
	repo := f.store.InferenceEligibility()
	write := eligibilityFailure(t, f)
	result, err := repo.RecordFailure(context.Background(), write)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := f.store.ManagedChannels().UpdateManagedCredential(context.Background(), f.request.CredentialID, 1, &f.input.Binding)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := repo.ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil || !gate.Blocked || gate.GateEpoch != result.Eligibility.GateEpoch+1 || gate.BindingRevision != updated.Revision {
		t.Fatal("same grant rebind erased block", err)
	}
	other := f.input.Binding
	other.Model = "other"
	updated, err = f.store.ManagedChannels().UpdateManagedCredential(context.Background(), f.request.CredentialID, updated.Revision, &other)
	if err != nil {
		t.Fatal(err)
	}
	gate, err = repo.ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil || gate.Blocked || gate.Model != "other" || gate.GateEpoch != result.Eligibility.GateEpoch+2 {
		t.Fatal("new scope not reset", err)
	}
	if _, err = f.store.ManagedChannels().DisableManagedCredential(context.Background(), f.request.CredentialID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	gate, err = repo.ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil || gate.GateEpoch != result.Eligibility.GateEpoch+3 {
		t.Fatal("disable did not fence previous issuance", err)
	}
	if late, err := repo.RecordFailure(context.Background(), write); err != nil || late.Applied || late.Eligibility.Blocked {
		t.Fatal("old rebind response applied")
	}
}

func TestInferenceEligibilityDoesNotEscalateModelFailureOrOverwriteBlock(t *testing.T) {
	f := newManagedFixture(t)
	write := eligibilityFailure(t, f)
	write.Signal = authv3.FailureSignal{Reason: authv3.RateLimitUnknown, Scope: authv3.Model, HTTPStatus: 429, Source: authv3.ResponsesHTTP}
	repo := f.store.InferenceEligibility()
	result, err := repo.RecordFailure(context.Background(), write)
	if err != nil || !result.Applied || result.Eligibility.Scope != authv3.Model || result.Eligibility.Reason != authv3.RateLimitUnknown {
		t.Fatal(err)
	}
	before := result.Eligibility
	write.Signal = authv3.FailureSignal{Reason: authv3.AuthorizationReview, Scope: authv3.Grant, HTTPStatus: 403, Source: authv3.ModelsHTTP}
	result, err = repo.RecordFailure(context.Background(), write)
	if err != nil || result.Applied || !reflect.DeepEqual(before, result.Eligibility) {
		t.Fatal("same epoch rejection overwrote first block")
	}
	f.now = f.now.Add(365 * 24 * time.Hour)
	gate, err := repo.ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil || !gate.Blocked || gate.ResetKnown || gate.ResetAt != nil {
		t.Fatal("unknown reset became automatic window")
	}
	if row := upstreamRaw(t, f.store, f.account.ID); row.State != string(auth.Active) {
		t.Fatal("model gate changed account state")
	}
	raw, _ := json.Marshal(gate)
	if bytes.Contains(raw, []byte("private")) || bytes.Contains(raw, []byte("refresh_token")) {
		t.Fatal("eligibility read leaked secrets")
	}
}

func TestInferenceEligibilityNewAccountBindingResetsOnlyItsScope(t *testing.T) {
	f := newManagedFixture(t)
	repo := f.store.InferenceEligibility()
	write := eligibilityFailure(t, f)
	first, err := repo.RecordFailure(context.Background(), write)
	if err != nil {
		t.Fatal(err)
	}
	accountWrite := f.write(2)
	accountWrite.Tokens.Scopes = []string{"chatgpt.tokens.use.direct", "openid", "resource.invoke"}
	account, err := f.repo.SaveAuthorization(context.Background(), accountWrite)
	if err != nil {
		t.Fatal(err)
	}
	binding := f.input.Binding
	binding.AccountID = account.ID
	updated, err := f.store.ManagedChannels().UpdateManagedCredential(context.Background(), f.request.CredentialID, 1, &binding)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := repo.ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil || gate.Blocked || gate.AccountID != account.ID || gate.GateEpoch != first.Eligibility.GateEpoch+1 || gate.BindingRevision != updated.Revision {
		t.Fatal("new account retained old gate scope", err)
	}
	late, err := repo.RecordFailure(context.Background(), write)
	if err != nil || late.Applied || late.Eligibility.Blocked {
		t.Fatal("old account response blocked replacement")
	}
}

func TestInferenceEligibilityStrictSignalsMissingGateAndOverflowFailClosed(t *testing.T) {
	f := newManagedFixture(t)
	repo := f.store.InferenceEligibility()
	write := eligibilityFailure(t, f)
	for _, mutate := range []func(*authv3.FailureWrite){func(w *authv3.FailureWrite) { w.Signal.ProviderCode = "private arbitrary text" }, func(w *authv3.FailureWrite) { w.Signal.HTTPStatus = 503 }, func(w *authv3.FailureWrite) { w.Signal.Scope = authv3.Model }, func(w *authv3.FailureWrite) { w.Signal.Source = "untrusted" }, func(w *authv3.FailureWrite) { w.GateEpoch = 0 }} {
		bad := write
		mutate(&bad)
		if _, err := repo.RecordFailure(context.Background(), bad); !errors.Is(err, auth.ErrInvalid) {
			t.Fatal("invalid signal accepted", err)
		}
	}
	if err := f.store.DB().Model(&model.InferenceEligibility{}).Where("credential_id = ?", f.request.CredentialID).Update("gate_epoch", math.MaxInt64).Error; err != nil {
		t.Fatal(err)
	}
	gate, err := repo.ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ResumeEligibility(context.Background(), eligibilityResume(gate)); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("epoch overflow accepted")
	}
	if err := f.store.DB().Delete(&model.InferenceEligibility{}, f.request.CredentialID).Error; err != nil {
		t.Fatal(err)
	}
	if out, err := f.repo.AcquireAccess(context.Background(), f.request); !errors.Is(err, auth.ErrUnavailable) || out.AccessToken.Value() != "" {
		t.Fatal("deleted gate bypassed refusal")
	}
}

func TestInferenceEligibilityCrossProcessResumeVersusLateFeedback(t *testing.T) {
	f := newManagedFixture(t)
	write := eligibilityFailure(t, f)
	other := f.open(t, f.key)
	gate, err := f.store.InferenceEligibility().ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	var resumeErr, recordErr error
	go func() {
		defer wg.Done()
		<-start
		_, resumeErr = f.store.InferenceEligibility().ResumeEligibility(context.Background(), eligibilityResume(gate))
	}()
	go func() {
		defer wg.Done()
		<-start
		_, recordErr = other.InferenceEligibility().RecordFailure(context.Background(), write)
	}()
	close(start)
	wg.Wait()
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	after, err := f.store.InferenceEligibility().ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if resumeErr == nil {
		if after.Blocked || after.GateEpoch != gate.GateEpoch+1 {
			t.Fatal("late feedback undid committed recovery")
		}
	} else if !errors.Is(resumeErr, auth.ErrConflict) || !after.Blocked {
		t.Fatal("concurrent recovery CAS wrong", resumeErr)
	}
}

func TestInferenceEligibilityCommitFailureHasNoAppliedOutput(t *testing.T) {
	f := newManagedFixture(t)
	write := eligibilityFailure(t, f)
	independent := f.open(t, f.key)
	for _, statement := range []string{"CREATE TABLE gate_commit_parent (id integer PRIMARY KEY)", "CREATE TABLE gate_commit_child (parent_id integer REFERENCES gate_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)", fmt.Sprintf("CREATE TRIGGER gate_defer_commit_failure AFTER UPDATE ON schema_migrations WHEN OLD.version = %d BEGIN INSERT INTO gate_commit_child(parent_id) VALUES(99); END", latestMigrationVersion)} {
		if err := f.store.DB().Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	out, err := f.store.InferenceEligibility().RecordFailure(context.Background(), write)
	if err == nil || out.Applied || out.Eligibility.CredentialID != 0 {
		t.Fatal("failed COMMIT exposed applied feedback")
	}
	var raw model.InferenceEligibility
	if err := f.store.DB().First(&raw, f.request.CredentialID).Error; err != nil || raw.Blocked {
		t.Fatal("failed COMMIT persisted gate", err)
	}
	read, err := independent.InferenceEligibility().ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil || read.Blocked {
		t.Fatal("independent connection observed failed COMMIT", err)
	}
	if err := f.store.DB().Exec("DROP TRIGGER gate_defer_commit_failure").Error; err != nil {
		t.Fatal(err)
	}
	retried, err := f.store.InferenceEligibility().RecordFailure(context.Background(), write)
	if err != nil || !retried.Applied {
		t.Fatal("failed COMMIT poisoned subsequent transaction", err)
	}
	read, err = independent.InferenceEligibility().ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil || !read.Blocked {
		t.Fatal("later successful transaction not committed", err)
	}
}

func TestInferenceEligibilityCapacityAndCredentialCascade(t *testing.T) {
	f := newManagedFixture(t)
	if err := f.store.DB().Transaction(func(tx *gorm.DB) error {
		for i := 0; i < inferenceEligibilityCapacity-1; i++ {
			c := model.Credential{ChannelID: uint(f.created.Channel.ID), Source: model.CredentialSourceManaged, AccountID: f.account.ID, BindingOwnerID: f.request.OwnerID, BindingTokenID: f.request.TokenID, BindingModel: "m", BindingRevision: 1, Status: model.CredentialDisabled, Weight: 1}
			if err := tx.Create(&c).Error; err != nil {
				return err
			}
			if err := createEligibility(tx, c); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ManagedChannels().DisableManagedCredential(context.Background(), f.request.CredentialID, 1); err != nil {
		t.Fatal(err)
	}
	f.input.Channel.Name = "managed-over-capacity"
	if _, err := f.store.ManagedChannels().CreateManagedChannel(context.Background(), &f.input); !errors.Is(err, authv3.ErrCapacity) {
		t.Fatal("capacity overflow accepted", err)
	}
	if err := f.store.DB().Delete(&model.Credential{}, f.request.CredentialID).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := f.store.DB().Model(&model.InferenceEligibility{}).Where("credential_id = ?", f.request.CredentialID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("credential delete did not cascade", err)
	}
}

func TestInferenceEligibilitySchema18UpgradePreservesStaticSecretsAndAccount(t *testing.T) {
	f := newManagedFixture(t)
	static := encryptedChannel(t, f.store, "v18-static", "controlled-static-key")
	var staticBefore model.Credential
	if err := f.store.DB().First(&staticBefore, static.Credentials[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	accountBefore := upstreamRaw(t, f.store, f.account.ID)
	var released []model.SchemaMigration
	if err := f.store.DB().Where("version <= ?", 18).Order("version").Find(&released).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Migrator().DropTable(&model.InferenceEligibility{}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Where("version > ?", 18).Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	_ = f.store.Close()
	upgraded := f.open(t, f.key)
	gate, err := upgraded.InferenceEligibility().ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil || gate.Blocked || gate.GateEpoch != 1 || gate.Revision != 1 {
		t.Fatalf("v18 binding gate not initialized: %+v %v", gate, err)
	}
	var staticAfter model.Credential
	if err = upgraded.DB().First(&staticAfter, staticBefore.ID).Error; err != nil || !reflect.DeepEqual(staticBefore, staticAfter) {
		t.Fatal("migration changed static credential ciphertext", err)
	}
	if !reflect.DeepEqual(accountBefore, upstreamRaw(t, upgraded, f.account.ID)) {
		t.Fatal("migration changed account or tokens")
	}
	if err = checkMigrationVersion(upgraded.DB(), 18); err == nil {
		t.Fatal("schema18 accepted schema19")
	}
	var ledger []model.SchemaMigration
	if err = upgraded.DB().Order("version").Find(&ledger).Error; err != nil || len(ledger) != len(released)+2 || ledger[len(ledger)-2].Version != 19 || ledger[len(ledger)-1].Version != 20 {
		t.Fatal("schema19 not appended once", err)
	}
	for i, old := range released {
		if old.Version != ledger[i].Version || !old.AppliedAt.Equal(ledger[i].AppliedAt) {
			t.Fatal("migration rewrote released steps")
		}
	}
	_ = upgraded.Close()
	reopened := f.open(t, f.key)
	read, err := reopened.InferenceEligibility().ReadEligibility(context.Background(), f.request.CredentialID)
	if err != nil || !reflect.DeepEqual(read, gate) {
		t.Fatal("reopen duplicated or changed gate", err)
	}
}
