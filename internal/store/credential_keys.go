package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/model"
	"minigateway/internal/secrets"
)

var ErrCredentialEncryption = errors.New("upstream credential encryption requires the original valid key configuration")

const credentialEncryptionSetting = "credentials.encryption_version"

// CredentialEncryptionReady reports configured and persisted activation, not
// merely a key-file path. Runtime assembly must check the store it actually uses.
func (s *Store) CredentialEncryptionReady(ctx context.Context) bool {
	return s != nil && s.vault != nil && s.checkCredentialMode(s.db.WithContext(ctx)) == nil
}

type credentialVault struct {
	ring    *secrets.Ring
	digest  []byte
	version string
}

func loadCredentialVault(path string) (*credentialVault, error) {
	if path == "" {
		return nil, nil
	}
	material, err := secrets.ReadKeyFile(path)
	if err != nil {
		return nil, ErrCredentialEncryption
	}
	defer material.Clear()
	ring, err := secrets.NewRing(material.Active, material.Keys)
	if err != nil {
		return nil, ErrCredentialEncryption
	}
	digest := sha256.Sum256(material.Digest)
	return &credentialVault{ring: ring, digest: bytes.Clone(material.Digest), version: "v1:" + hex.EncodeToString(digest[:])}, nil
}

func credentialAAD(credential model.Credential) []byte {
	return []byte(fmt.Sprintf("TXAPI/upstream-credential/v1/%d/%d/", credential.ID, credential.ChannelID))
}

func (v *credentialVault) fingerprint(channelID uint, raw string) string {
	mac := hmac.New(sha256.New, v.digest)
	_, _ = fmt.Fprintf(mac, "TXAPI/upstream-key/v1/%d/", channelID)
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

// initializeCredentialVault is feature activation, not another schema runner.
// The unified migration adds the columns/index. Explicit key-file configuration
// atomically converts existing rows and pins the stable digest-key fingerprint.
func (s *Store) initializeCredentialVault() error {
	if s.vault != nil {
		if err := s.db.Exec("PRAGMA secure_delete=ON").Error; err != nil {
			return ErrCredentialEncryption
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var setting model.Setting
		found := tx.Where("key = ?", credentialEncryptionSetting).Limit(1).Find(&setting)
		if found.Error != nil {
			return found.Error
		}
		enabled := found.RowsAffected == 1
		if s.vault == nil {
			if enabled {
				return ErrCredentialEncryption
			}
			var encrypted int64
			if err := tx.Model(&model.Credential{}).Where("length(key_ciphertext) > 0 OR key_hash <> ''").Count(&encrypted).Error; err != nil {
				return err
			}
			if encrypted != 0 {
				return ErrCredentialEncryption
			}
			return nil
		}
		if enabled && setting.Value != s.vault.version {
			return ErrCredentialEncryption
		}
		var invalid int64
		if err := tx.Model(&model.Credential{}).Where("id <= 0 OR channel_id <= 0").Count(&invalid).Error; err != nil {
			return err
		}
		if invalid != 0 {
			return ErrCredentialEncryption
		}
		var after uint
		for {
			var rows []model.Credential
			if err := tx.Where("id > ?", after).Order("id").Limit(200).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				after = row.ID
				original := row
				if enabled {
					if err := s.openCredential(&row); err != nil {
						return err
					}
				} else if len(row.KeyCiphertext) != 0 || row.KeyHash != "" || row.APIKey == "" || row.ChannelID == 0 {
					return ErrCredentialEncryption
				}
				header, headerErr := secrets.Header(original.KeyCiphertext)
				if enabled && headerErr == nil && string(header[2:]) == s.vault.ring.Active() {
					continue
				}
				sealed, err := s.vault.ring.Seal([]byte(row.APIKey), credentialAAD(row))
				if err != nil {
					return ErrCredentialEncryption
				}
				res := tx.Model(&model.Credential{}).Where("id = ? AND channel_id = ?", row.ID, row.ChannelID).
					UpdateColumns(map[string]any{"api_key": "", "key_ciphertext": sealed, "key_hash": s.vault.fingerprint(row.ChannelID, row.APIKey)})
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected != 1 {
					return ErrCredentialEncryption
				}
			}
		}
		if !enabled {
			return tx.Create(&model.Setting{Key: credentialEncryptionSetting, Value: s.vault.version}).Error
		}
		return nil
	})
}

func (s *Store) checkCredentialMode(tx *gorm.DB) error {
	var setting model.Setting
	result := tx.Where("key = ?", credentialEncryptionSetting).Limit(1).Find(&setting)
	if result.Error != nil {
		return result.Error
	}
	if s.vault == nil {
		if result.RowsAffected != 0 {
			return ErrCredentialEncryption
		}
	} else if result.RowsAffected != 1 || setting.Value != s.vault.version {
		return ErrCredentialEncryption
	}
	return nil
}

func (s *Store) openCredential(credential *model.Credential) error {
	if s.vault == nil {
		if len(credential.KeyCiphertext) != 0 || credential.KeyHash != "" {
			return ErrCredentialEncryption
		}
		return nil
	}
	if credential.ID == 0 || credential.ChannelID == 0 || credential.APIKey != "" || len(credential.KeyCiphertext) == 0 || credential.KeyHash == "" {
		return ErrCredentialEncryption
	}
	plain, err := s.vault.ring.Open(credential.KeyCiphertext, credentialAAD(*credential))
	if err != nil {
		return ErrCredentialEncryption
	}
	defer clear(plain)
	if len(plain) == 0 || !hmac.Equal([]byte(credential.KeyHash), []byte(s.vault.fingerprint(credential.ChannelID, string(plain)))) {
		return ErrCredentialEncryption
	}
	credential.APIKey = string(plain)
	credential.KeyCiphertext, credential.KeyHash = nil, ""
	return nil
}

func (s *Store) openChannels(channels []model.Channel) error {
	for i := range channels {
		for j := range channels[i].Credentials {
			if channels[i].Credentials[j].ChannelID != channels[i].ID {
				return ErrCredentialEncryption
			}
			if err := s.openCredential(&channels[i].Credentials[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

// The caller owns the transaction, including the ID allocation and encryption.
// Plaintext never enters SQL parameters, even in a failed encrypted insertion.
func (s *Store) createCredential(tx *gorm.DB, credential *model.Credential) error {
	if err := s.checkCredentialMode(tx); err != nil {
		return err
	}
	if s.vault == nil {
		return tx.Create(credential).Error
	}
	if credential.APIKey == "" || credential.ChannelID == 0 {
		return ErrCredentialEncryption
	}
	raw := credential.APIKey
	stored := *credential
	stored.APIKey = ""
	stored.KeyHash = s.vault.fingerprint(stored.ChannelID, raw)
	if err := tx.Create(&stored).Error; err != nil {
		return err
	}
	sealed, err := s.vault.ring.Seal([]byte(raw), credentialAAD(stored))
	if err != nil {
		return ErrCredentialEncryption
	}
	res := tx.Model(&model.Credential{}).Where("id = ?", stored.ID).UpdateColumn("key_ciphertext", sealed)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrCredentialEncryption
	}
	*credential = stored
	credential.APIKey, credential.KeyHash, credential.KeyCiphertext = raw, "", nil
	return nil
}

func (s *Store) credentialKeyUpdates(tx *gorm.DB, existing model.Credential, key string) (map[string]any, error) {
	if err := s.checkCredentialMode(tx); err != nil {
		return nil, err
	}
	if s.vault == nil {
		return map[string]any{"api_key": key}, nil
	}
	sealed, err := s.vault.ring.Seal([]byte(key), credentialAAD(existing))
	if err != nil {
		return nil, ErrCredentialEncryption
	}
	return map[string]any{"api_key": "", "key_ciphertext": sealed, "key_hash": s.vault.fingerprint(existing.ChannelID, key)}, nil
}
