package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/model"
)

func TestLegacyKeyMigrationRequiresExplicitOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	old, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Independent v1 schema; no new Go model is used to construct the input.
	for _, sql := range []string{
		`CREATE TABLE tokens(id integer PRIMARY KEY,name text,key text NOT NULL,user_id integer NOT NULL DEFAULT 0,models text,status text,created_at datetime,updated_at datetime,deleted_at datetime)`,
		`CREATE UNIQUE INDEX idx_tokens_key ON tokens(key)`,
		`INSERT INTO tokens(id,name,key,models,status) VALUES(1,'old-key','sk-legacy-secret-123456','["m1"]','active')`,
	} {
		if err := old.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := old.DB()
	_ = raw.Close()
	st, err := Open(config.DatabaseConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := st.BootstrapAdmin("first-admin", "admin-password-123!")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.LookupToken("sk-legacy-secret-123456"); ok {
		t.Fatal("unowned legacy key authenticated")
	}
	token, err := st.GetToken(1)
	if err != nil {
		t.Fatal(err)
	}
	if token.Key != "" || token.KeyHash != access.Hash("sk-legacy-secret-123456") || token.UserID != 0 || token.ModelMode != access.Allowlist {
		t.Fatalf("unsafe migrated row=%+v", token)
	}
	if _, err := st.ClaimToken(1, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimToken(1, admin.ID); err == nil {
		t.Fatal("claimed key reassigned")
	}
	view, ok := st.LookupToken("sk-legacy-secret-123456")
	if !ok || view.UserID != admin.ID || !view.Policy.Allows("m1") || view.Policy.Allows("m2") {
		t.Fatalf("claim lost restrictions: %+v ok=%v", view, ok)
	}
	if _, err := st.IssueToken(admin.ID, &TokenInput{Name: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(config.DatabaseConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var versions int64
	st.DB().Model(&model.SchemaMigration{}).Where("version = 2").Count(&versions)
	if versions != 1 {
		t.Fatalf("migration count=%d", versions)
	}
	if _, ok := st.LookupToken("sk-legacy-secret-123456"); !ok {
		t.Fatal("claimed key failed after restart")
	}
}
func TestSessionsKeysAndOwnerChangesAreImmediate(t *testing.T) {
	st := openTestStore(t, "access")
	defer st.Close()
	admin, err := st.BootstrapAdmin("admin", "admin-password-123!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BootstrapAdmin("other-admin", "another-password-123"); err == nil {
		t.Fatal("bootstrap ran twice")
	}
	if _, err := st.UpdateUser(admin.ID, &UserInput{Username: "admin", Role: model.RoleUser, ModelMode: access.All}); err == nil {
		t.Fatal("last active admin demoted")
	}
	user, err := st.CreateUser(&UserInput{Username: "alice", Password: "alice-password-123", ModelMode: access.Allowlist, Models: []string{"m1", "m2"}})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := st.Login("alice", "alice-password-123")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &TokenInput{ModelMode: access.Allowlist, Models: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := st.GetToken(key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if key.Key == "" || persisted.Key != "" || persisted.KeyHash != access.Hash(key.Key) {
		t.Fatal("API key stored or not returned once")
	}
	if _, err := st.SessionUser(key.Key); err == nil {
		t.Fatal("call key authenticated a session")
	}
	if _, ok := st.LookupToken(session); ok {
		t.Fatal("session authenticated a call")
	}
	if _, err := st.IssueToken(user.ID, &TokenInput{ModelMode: access.All}); err == nil {
		t.Fatal("API key widened owner permissions")
	}
	if err := st.SetSystemPolicy(access.Policy{Mode: access.Allowlist, Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	view, ok := st.LookupToken(key.Key)
	if !ok || view.Policy.Allows("m1") || view.Policy.Allows("m2") {
		t.Fatalf("empty intersection must deny: %+v", view)
	}
	if err := st.SetSystemPolicy(access.Policy{Mode: access.All}); err != nil {
		t.Fatal(err)
	}
	st.DB().Model(&model.Token{}).Where("id = ?", key.ID).Update("expires_at", time.Now().Add(-time.Second).Unix())
	if _, ok := st.LookupToken(key.Key); ok {
		t.Fatal("expired key remained active")
	}
	st.DB().Model(&model.Token{}).Where("id = ?", key.ID).Update("expires_at", 0)
	_, err = st.UpdateUser(user.ID, &UserInput{Username: "alice", Role: model.RoleUser, Status: model.UserStatusDisabled, ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.LookupToken(key.Key); ok {
		t.Fatal("disabled owner authenticated")
	}
	if _, err := st.SessionUser(session); err == nil {
		t.Fatal("disabled user kept management session")
	}
}

func TestTokenRestrictionsRevocationAndPermissionChanges(t *testing.T) {
	st := openTestStore(t, "key-changes")
	defer st.Close()
	user, err := st.CreateUser(&UserInput{Username: "owner", Password: "owner-password-123", ModelMode: access.Allowlist, Models: []string{"m1", "m2"}, RPMLimit: 12, TPMLimit: 120})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueToken(user.ID, &TokenInput{Models: []string{"ungranted"}, RPMLimit: 6, TPMLimit: 60}); err == nil {
		t.Fatal("omitting model_mode silently ignored supplied models")
	}
	if _, err := st.IssueToken(user.ID, &TokenInput{Models: []string{"m1"}, RPMLimit: 13, TPMLimit: 60}); err == nil {
		t.Fatal("key rate limit widened owner limit")
	}
	key, err := st.IssueToken(user.ID, &TokenInput{Models: []string{"m1", "m2"}, RPMLimit: 6, TPMLimit: 60})
	if err != nil {
		t.Fatal(err)
	}
	view, ok := st.LookupToken(key.Key)
	if !ok || view.RPMLimit != 6 || !view.Policy.Allows("m2") {
		t.Fatalf("bad key view=%+v", view)
	}
	_, err = st.UpdateUser(user.ID, &UserInput{Username: "owner", Role: model.RoleUser, ModelMode: access.Allowlist, Models: []string{"m1"}, RPMLimit: 3, TPMLimit: 30})
	if err != nil {
		t.Fatal(err)
	}
	view, ok = st.LookupToken(key.Key)
	if !ok || view.Policy.Allows("m2") || view.RPMLimit != 3 {
		t.Fatalf("owner edit did not apply: %+v", view)
	}
	if _, err = st.UpdateOwnedToken(user.ID, key.ID, &TokenInput{Models: []string{"m1"}, RPMLimit: 2, TPMLimit: 20, Status: model.TokenStatusDisabled}); err != nil {
		t.Fatal(err)
	}
	if _, ok = st.LookupToken(key.Key); ok {
		t.Fatal("disabled key authenticated")
	}
	if err = st.DeleteOwnedToken(user.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok = st.LookupToken(key.Key); ok {
		t.Fatal("revoked key authenticated")
	}
}
func TestLoginSessionDigestExpiryAndLogout(t *testing.T) {
	st := openTestStore(t, "sessions")
	defer st.Close()
	_, err := st.BootstrapAdmin("admin", "admin-password-123!")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := st.Login("admin", "admin-password-123!")
	if err != nil {
		t.Fatal(err)
	}
	var row model.Session
	if err := st.DB().First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Hash == secret || row.Hash != access.Hash(secret) {
		t.Fatal("session persisted without a digest")
	}
	if err = st.Logout(secret); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SessionUser(secret); err == nil {
		t.Fatal("logged-out session reused")
	}
	secret, _, err = st.Login("admin", "admin-password-123!")
	if err != nil {
		t.Fatal(err)
	}
	st.DB().Model(&model.Session{}).Where("hash = ?", access.Hash(secret)).Update("expires_at", time.Now().Add(-time.Second).Unix())
	if _, err = st.SessionUser(secret); err == nil {
		t.Fatal("expired session reused")
	}
}

func TestEmptyKeyAllowlistCannotBecomeAll(t *testing.T) {
	st := openTestStore(t, "explicit-key-mode")
	defer st.Close()
	user, err := st.BootstrapAdmin("admin", "admin-password-123!")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &TokenInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateToken(key.ID, &TokenInput{Models: []string{}}); err != nil {
		t.Fatal(err)
	}
	view, ok := st.LookupToken(key.Key)
	if !ok || view.Policy.Mode != access.Allowlist || view.Policy.Allows("anything") {
		t.Fatalf("empty list became all: %+v", view)
	}
}
