package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"minigateway/internal/model"
)

// PriceData reads both dimensions in one database snapshot, so a concurrent
// control-plane save cannot splice prices and rates from different revisions.
func (s *Store) PriceData() (prices []model.ModelPrice, rates map[string]float64, err error) {
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Order("model ASC").Find(&prices).Error; err != nil {
			return err
		}
		var setting model.Setting
		err := tx.Where("key = ?", GroupRatesSetting).Find(&setting).Error
		if err != nil {
			return err
		}
		rates = map[string]float64{}
		if setting.Value != "" {
			if err := json.Unmarshal([]byte(setting.Value), &rates); err != nil {
				return fmt.Errorf("invalid price group rates: %w", err)
			}
		}
		return nil
	})
	return
}

// RecordPriceSnapshot is deliberately not an upsert: a digest can never be
// reused for different configuration. Persistence precedes in-memory publish.
func (s *Store) RecordPriceSnapshot(version, payload string) error {
	digest := sha256.Sum256([]byte(payload))
	if version != hex.EncodeToString(digest[:]) {
		return errors.New("price snapshot digest does not match content")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		row := model.BillingPriceVersion{Version: version, Payload: payload}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		var existing model.BillingPriceVersion
		if err := tx.First(&existing, "version = ?", version).Error; err != nil {
			return err
		}
		if existing.Payload != payload {
			return errors.New("immutable price snapshot content mismatch")
		}
		return nil
	})
}
