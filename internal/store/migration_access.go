package store

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
	"minigateway/internal/access"
	"minigateway/internal/model"
)

// Version 2 hashes legacy client keys. Ownership is never guessed. A user_id=0
// token stays quarantined even when its digest was successfully migrated.
// migrateAccessV2 is an append-only data step. The runner owns the version
// record and transaction, so later teams must not reimplement bookkeeping here.
func migrateAccessV2(tx *gorm.DB) error {
	if tx.Migrator().HasIndex(&model.Token{}, "idx_tokens_key") {
		if err := tx.Migrator().DropIndex(&model.Token{}, "idx_tokens_key"); err != nil {
			return err
		}
	}
	var tokens []model.Token
	if err := tx.Unscoped().Where("key <> ''").Find(&tokens).Error; err != nil {
		return err
	}
	for _, token := range tokens {
		policy := access.Legacy([]string(token.Models))
		if err := tx.Unscoped().Model(&model.Token{}).Where("id = ?", token.ID).Updates(map[string]any{
			"key": "", "key_hash": access.Hash(token.Key), "key_prefix": keyPrefix(token.Key), "model_mode": policy.Mode, "models": model.StringList(policy.Models),
		}).Error; err != nil {
			return fmt.Errorf("migrating token %d failed", token.ID)
		}
	}
	if err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_tokens_key_digest ON tokens(key_hash) WHERE key_hash <> ''").Error; err != nil {
		return errors.New("creating token digest index failed")
	}
	return nil
}

func keyPrefix(key string) string {
	if len(key) > 12 {
		return key[:12]
	}
	return "legacy-key"
}
func (s *Store) ClaimToken(id, owner uint) (*model.Token, error) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, owner).Error; err != nil || user.Status != model.UserStatusActive {
			return errors.New("owner must be an active user")
		}
		result := tx.Model(&model.Token{}).Where("id = ? AND user_id = 0", id).Update("user_id", owner)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("token is missing or already claimed")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err = s.reloadCache(); err != nil {
		return nil, err
	}
	return s.GetToken(id)
}
