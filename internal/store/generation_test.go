package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/model"
)

func TestGenerationBindingSharesCredentialAuthority(t *testing.T) {
	st := openTestStore(t, "generation-authority")
	defer st.Close()
	user, err := st.CreateUser(&UserInput{Username: "owner", Password: "fixture-password-123", ModelMode: access.All,
		RPMLimit: 20, TPMLimit: 200, ConcurrencyLimit: 3})
	if err != nil {
		t.Fatal(err)
	}
	token, err := st.CreateToken(&TokenInput{UserID: user.ID, Key: "fixture-generation-secret", ModelMode: access.All,
		RPMLimit: 10, TPMLimit: 100, ConcurrencyLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	binding := access.Binding{UserID: user.ID, TokenID: token.ID}
	check := func(want bool) access.Principal {
		t.Helper()
		p, err := st.ResolvePrincipal(context.Background(), binding)
		view, ok := st.LookupToken(token.Key)
		if want && (err != nil || !ok || !reflect.DeepEqual(p, view.Principal())) {
			t.Fatalf("authority disagreement: principal=%+v err=%v view=%+v ok=%v", p, err, view, ok)
		}
		if !want && (!errors.Is(err, access.ErrUnauthorized) || ok) {
			t.Fatalf("revoked identity accepted: principal=%+v err=%v ok=%v", p, err, ok)
		}
		return p
	}
	old := check(true)
	if old.RPMLimit != 10 || old.TPMLimit != 100 || old.UserConcurrencyLimit != 3 || old.KeyConcurrencyLimit != 2 {
		t.Fatalf("lost current limits: %+v", old)
	}
	if err := st.SetSystemPolicy(access.Policy{Mode: access.Allowlist, Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(user).Updates(map[string]any{"rpm_limit": 2, "concurrency_limit": 1}).Error; err != nil {
		t.Fatal(err)
	}
	current := check(true)
	if current.Policy.Allows("other") || !current.Policy.Allows("m") || current.RPMLimit != 2 || current.UserConcurrencyLimit != 1 || !old.Policy.Allows("other") {
		t.Fatalf("binding reused old snapshot: old=%+v current=%+v", old, current)
	}
	for _, bad := range []access.Binding{{}, {UserID: user.ID + 1, TokenID: token.ID}, {UserID: user.ID, TokenID: token.ID + 1}} {
		if _, err := st.ResolvePrincipal(context.Background(), bad); !errors.Is(err, access.ErrUnauthorized) {
			t.Fatalf("untrusted owner/Key accepted: %+v %v", bad, err)
		}
	}
	for _, change := range []struct {
		row    any
		column string
		bad    any
		good   any
	}{
		{token, "status", model.TokenStatusDisabled, model.TokenStatusActive},
		{token, "expires_at", time.Now().Unix() - 1, int64(0)},
		{token, "key_hash", "", token.KeyHash},
		{user, "status", model.UserStatusDisabled, model.UserStatusActive},
	} {
		if err := st.DB().Model(change.row).Update(change.column, change.bad).Error; err != nil {
			t.Fatal(err)
		}
		check(false)
		if err := st.DB().Model(change.row).Update(change.column, change.good).Error; err != nil {
			t.Fatal(err)
		}
		check(true)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.ResolvePrincipal(ctx, binding); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled authority read: %v", err)
	}
	if err := st.DB().Delete(token).Error; err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestGenerationAuthorityDoesNotTurnStorageFailureIntoAuthentication(t *testing.T) {
	st := openTestStore(t, "failed-generation-authority")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := st.ResolvePrincipal(context.Background(), access.Binding{UserID: 1, TokenID: 1})
	if err == nil || errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("storage failure lost its fail-closed distinction: %v", err)
	}
}

func TestGenerationAuthorityReadsOneSnapshotDuringConcurrentPolicyChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.db")
	st, err := Open(config.DatabaseConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(&UserInput{Username: "snapshot-owner", Password: "fixture-password-123", ModelMode: access.All, RPMLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	token, err := st.IssueToken(user.ID, &TokenInput{ModelMode: access.All, RPMLimit: 10, ConcurrencyLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := writer.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	changed := false
	const hook = "generation-snapshot-fixture"
	if err := st.DB().Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if changed || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "tokens" || tx.Error != nil {
			return
		}
		changed = true
		// A separate WAL connection commits between the token and owner reads.
		// The resolver must not combine the old token with the new policy/limits.
		err := writer.Transaction(func(db *gorm.DB) error {
			if err := db.Model(user).Update("rpm_limit", 2).Error; err != nil {
				return err
			}
			if err := db.Model(token).Update("concurrency_limit", 1).Error; err != nil {
				return err
			}
			return db.Create(&model.Setting{Key: systemModelsSetting, Value: `{"mode":"allowlist","models":["new"]}`}).Error
		})
		if err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer st.DB().Callback().Query().Remove(hook)
	binding := access.Binding{UserID: user.ID, TokenID: token.ID}
	p, err := st.ResolvePrincipal(context.Background(), binding)
	if err != nil || !changed || p.RPMLimit != 10 || p.KeyConcurrencyLimit != 2 || !p.Policy.Allows("old") {
		t.Fatalf("mixed authority snapshot: %+v %v changed=%v", p, err, changed)
	}
	p, err = st.ResolvePrincipal(context.Background(), binding)
	if err != nil || p.RPMLimit != 2 || p.KeyConcurrencyLimit != 1 || p.Policy.Allows("old") || !p.Policy.Allows("new") {
		t.Fatalf("next generation missed committed authority: %+v %v", p, err)
	}
}
