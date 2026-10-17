package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"minigateway/internal/config"
	"minigateway/internal/model"
	"minigateway/internal/secrets"
)

func credentialKeyFile(t *testing.T, active string, keys map[string]byte, digest byte) string {
	t.Helper()
	encoded := make(map[string]string)
	for id, value := range keys {
		encoded[id] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
	}
	raw, err := json.Marshal(map[string]any{"active_key": active, "keys": encoded, "digest_key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{digest}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "credential-keys.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func encryptedCredentialStore(t *testing.T, path, keys string) *Store {
	t.Helper()
	st, err := Open(config.DatabaseConfig{Path: path, CredentialKeysFile: keys})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if !st.CredentialEncryptionReady(context.Background()) {
		t.Fatal("encrypted store did not pin activation")
	}
	return st
}

func encryptedChannel(t *testing.T, st *Store, name, key string) *model.Channel {
	t.Helper()
	channel, err := st.CreateChannel(&ChannelInput{Name: name, BaseURL: "http://127.0.0.1:9911", Models: []string{"m"}, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func TestCredentialEncryptionCRUDDoesNotPersistOrLogPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "encrypted.db")
	keys := credentialKeyFile(t, "key-1", map[string]byte{"key-1": 21}, 72)
	st := encryptedCredentialStore(t, path, keys)
	var sql bytes.Buffer
	st.db = st.db.Session(&gorm.Session{Logger: logger.New(log.New(&sql, "", 0), logger.Config{LogLevel: logger.Info})})
	first, second := "credential-secret-fixture-first-77bd53", "credential-secret-fixture-second-1c38a9"
	channel := encryptedChannel(t, st, "encrypted", first)
	id := channel.Credentials[0].ID
	created, err := st.CreateCredential(channel.ID, &CredentialInput{APIKey: second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateCredential(channel.ID, &CredentialInput{APIKey: first}); err == nil {
		t.Fatal("encrypted duplicate admitted")
	}
	if _, err := st.UpdateCredential(created.ID, &CredentialInput{APIKey: first}); err == nil {
		t.Fatal("update bypassed encrypted duplicate check")
	}
	if _, err := st.UpdateCredential(id, &CredentialInput{Name: "renamed", Weight: 4}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCredential(id)
	if err != nil || got.APIKey != first || got.Weight != 4 || got.KeyHash != "" || len(got.KeyCiphertext) != 0 {
		t.Fatalf("runtime credential incorrect: %v", err)
	}
	for _, c := range st.ListCredentials(channel.ID) {
		if c.APIKey == "" || c.KeyHash != "" || len(c.KeyCiphertext) != 0 {
			t.Fatal("cache did not expose only authenticated runtime copy")
		}
	}
	var rows []model.Credential
	if err := st.DB().Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatal("stored credential count changed", err)
	}
	for _, row := range rows {
		if row.APIKey != "" || len(row.KeyCiphertext) < 30 || len(row.KeyHash) != 64 {
			t.Fatal("plaintext or missing encrypted key in SQL row")
		}
		encoded, err := json.Marshal(row)
		if err != nil || bytes.Contains(encoded, []byte("key_ciphertext")) || bytes.Contains(encoded, []byte(row.KeyHash)) {
			t.Fatal("encryption fields leaked through JSON")
		}
	}
	if strings.Contains(sql.String(), first) || strings.Contains(sql.String(), second) {
		t.Fatal("plaintext key entered SQL logging")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(first)) || bytes.Contains(data, []byte(second)) {
			t.Fatal("fresh encrypted database persisted plaintext")
		}
	}
	reopened := encryptedCredentialStore(t, path, keys)
	got, err = reopened.GetCredential(created.ID)
	if err != nil || got.APIKey != second {
		t.Fatal("reopen lost credential", err)
	}
}

func TestCredentialActivationRotationAndMissingKeysFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activation.db")
	plain, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	channel := encryptedChannel(t, plain, "legacy", "legacy-activation-fixture-d680")
	id := channel.Credentials[0].ID
	if err := plain.DB().Model(&model.Credential{}).Where("id = ?", id).Updates(map[string]any{"health_generation": 7, "health_observed_at": 41, "fail_count": 2}).Error; err != nil {
		t.Fatal(err)
	}
	var before model.Credential
	if err := plain.DB().First(&before, id).Error; err != nil {
		t.Fatal(err)
	}
	_ = plain.Close()
	old := credentialKeyFile(t, "key-1", map[string]byte{"key-1": 21}, 72)
	st := encryptedCredentialStore(t, path, old)
	var activated model.Credential
	if err := st.DB().First(&activated, id).Error; err != nil || activated.APIKey != "" || activated.HealthGeneration != 7 || activated.HealthObservedAt != 41 || activated.FailCount != 2 || !activated.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("activation changed identity/health/config revision", err)
	}
	_ = st.Close()
	missingOld := credentialKeyFile(t, "key-2", map[string]byte{"key-2": 43}, 72)
	wrongKey := credentialKeyFile(t, "key-1", map[string]byte{"key-1": 43}, 72)
	wrongDigest := credentialKeyFile(t, "key-1", map[string]byte{"key-1": 21}, 73)
	for _, keyfile := range []string{"", missingOld, wrongKey, wrongDigest} {
		if bad, err := Open(config.DatabaseConfig{Path: path, CredentialKeysFile: keyfile}); !errors.Is(err, ErrCredentialEncryption) {
			if bad != nil {
				_ = bad.Close()
			}
			t.Fatalf("missing/wrong original key accepted: %v", err)
		}
	}
	rotation := credentialKeyFile(t, "key-2", map[string]byte{"key-1": 21, "key-2": 43}, 72)
	rotated := encryptedCredentialStore(t, path, rotation)
	var after model.Credential
	if err := rotated.DB().First(&after, id).Error; err != nil {
		t.Fatal(err)
	}
	header, err := secrets.Header(after.KeyCiphertext)
	if err != nil || string(header[2:]) != "key-2" || after.KeyHash != activated.KeyHash || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("rotation changed stable authorization binding or did not reseal")
	}
	_ = rotated.Close()
	retired := encryptedCredentialStore(t, path, missingOld)
	got, err := retired.GetCredential(id)
	if err != nil || got.APIKey != before.APIKey {
		t.Fatal("retired-key restart lost migrated credential", err)
	}
}

func TestCredentialActivationFailureRollsBackAllRowsAndMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	encryptedChannel(t, st, "first", "legacy-first-fixture")
	second := encryptedChannel(t, st, "second", "legacy-second-fixture")
	if err := st.DB().Exec(fmt.Sprintf("CREATE TRIGGER reject_key_conversion BEFORE UPDATE OF key_ciphertext ON credentials WHEN OLD.id = %d BEGIN SELECT RAISE(ABORT, 'controlled conversion failure'); END", second.Credentials[0].ID)).Error; err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	keys := credentialKeyFile(t, "key-1", map[string]byte{"key-1": 21}, 72)
	if bad, err := Open(config.DatabaseConfig{Path: path, CredentialKeysFile: keys}); err == nil {
		_ = bad.Close()
		t.Fatal("partial activation reported success")
	}
	plain, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	var count int64
	if err := plain.DB().Model(&model.Credential{}).Where("api_key = '' OR length(key_ciphertext) > 0 OR key_hash <> ''").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("conversion rollback incomplete", err)
	}
	if err := plain.DB().Model(&model.Setting{}).Where("key = ?", credentialEncryptionSetting).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed conversion pinned encryption marker", err)
	}
}

func TestCredentialCiphertextCannotMoveBetweenRowsOrChannels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tamper.db")
	keys := credentialKeyFile(t, "key-1", map[string]byte{"key-1": 21}, 72)
	st := encryptedCredentialStore(t, path, keys)
	first := encryptedChannel(t, st, "first", "private-first-fixture")
	second := encryptedChannel(t, st, "second", "private-second-fixture")
	var original model.Credential
	if err := st.DB().First(&original, first.Credentials[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&model.Credential{}).Where("id = ?", second.Credentials[0].ID).UpdateColumn("key_ciphertext", original.KeyCiphertext).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCredential(second.Credentials[0].ID); !errors.Is(err, ErrCredentialEncryption) {
		t.Fatal("row substitution accepted", err)
	}
	_ = st.Close()
	if bad, err := Open(config.DatabaseConfig{Path: path, CredentialKeysFile: keys}); !errors.Is(err, ErrCredentialEncryption) {
		if bad != nil {
			_ = bad.Close()
		}
		t.Fatal("tampered store opened", err)
	}
}

func TestCredentialEncryptionModeMismatchDoesNotWritePlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mode.db")
	keys := credentialKeyFile(t, "key-1", map[string]byte{"key-1": 21}, 72)
	st := encryptedCredentialStore(t, path, keys)
	channel := encryptedChannel(t, st, "encrypted", "first-fixture")
	if err := st.DB().Model(&model.Setting{}).Where("key = ?", credentialEncryptionSetting).Update("value", "unexpected-mode").Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if st.CredentialEncryptionReady(ctx) {
		t.Fatal("mode mismatch reported ready")
	}
	if _, err := st.CreateCredential(channel.ID, &CredentialInput{APIKey: "never-write-this-fixture"}); !errors.Is(err, ErrCredentialEncryption) {
		t.Fatal("mode mismatch allowed credential insertion", err)
	}
}

func TestEncryptedSeedAndChannelKeyReplacementUseTheSameProtectedWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "encrypted-seed.db")
	keys := credentialKeyFile(t, "key-1", map[string]byte{"key-1": 21}, 72)
	st := encryptedCredentialStore(t, path, keys)
	cfg := &config.Config{Channels: []config.ChannelConfig{{ID: 9, Name: "encrypted-seed", Type: "openai", BaseURL: "http://127.0.0.1:9911", Key: "private-seed-fixture-11a3", Models: []string{"m"}, Weight: 1}}}
	for iteration := range 2 {
		imported, err := st.SeedFromConfig(cfg)
		if err != nil || imported != (iteration == 0) {
			t.Fatal("encrypted seeding lost its original idempotency", err)
		}
	}
	first, err := st.GetChannel(9)
	if err != nil || len(first.Credentials) != 1 || first.Credentials[0].APIKey != cfg.Channels[0].Key {
		t.Fatal("seeded runtime credential unreadable", err)
	}
	updated, err := st.UpdateChannel(9, &ChannelInput{Name: "encrypted-seed", Type: "openai", BaseURL: "http://127.0.0.1:9911", Models: []string{"m"}, APIKey: "private-replacement-fixture-cc39"})
	if err != nil || len(updated.Credentials) != 1 || updated.Credentials[0].ID == first.Credentials[0].ID || updated.Credentials[0].APIKey != "private-replacement-fixture-cc39" {
		t.Fatal("channel replacement bypassed protected writer", err)
	}
	var rows []model.Credential
	if err := st.DB().Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].APIKey != "" || len(rows[0].KeyCiphertext) == 0 || rows[0].KeyHash == "" {
		t.Fatal("seed/replacement persisted plaintext or retained old pool", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("private-seed-fixture-11a3")) || bytes.Contains(data, []byte("private-replacement-fixture-cc39")) {
			t.Fatal("seed or replacement leaked plaintext to a fresh encrypted database")
		}
	}
}
