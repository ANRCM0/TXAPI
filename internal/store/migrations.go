package store

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/model"
)

// Integrator assigns each version once. Append new steps; never edit a released
// step, reorder it, or let feature packages run independent startup migrations.
const latestMigrationVersion = 15

type dataMigration struct {
	Version int
	Apply   func(*gorm.DB) error
}

var dataMigrations = []dataMigration{
	{Version: 2, Apply: migrateAccessV2},
	{Version: 3, Apply: func(tx *gorm.DB) error {
		return tx.Exec("UPDATE credentials SET health_generation = 0 WHERE health_generation IS NULL").Error
	}},
	{Version: 4, Apply: func(tx *gorm.DB) error {
		return tx.Exec("UPDATE credentials SET health_observed_at = 0 WHERE health_observed_at IS NULL").Error
	}},
	{Version: 5, Apply: func(tx *gorm.DB) error {
		// Historical logs cannot be assigned a price version retrospectively.
		return tx.Exec("UPDATE logs SET price_version = '' WHERE price_version IS NULL").Error
	}},
	{Version: 6, Apply: func(tx *gorm.DB) error {
		// Existing decisions are never inferred from best-effort access logs.
		return nil
	}},
	{Version: 7, Apply: func(tx *gorm.DB) error {
		// No cost contracts can be inferred for historical attempts.
		return nil
	}},
	{Version: 8, Apply: func(tx *gorm.DB) error {
		// The encrypted output archive has no historical content to backfill.
		return nil
	}},
	{Version: 9, Apply: func(tx *gorm.DB) error {
		// No task admission or budget hold can be inferred from old output logs.
		return nil
	}},
	{Version: 10, Apply: func(tx *gorm.DB) error {
		// Existing intents retain their original holds and an empty policy marker.
		// They cannot silently adopt a newly configured provider policy.
		if err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_intent_output ON generation_intents(output_record_id) WHERE output_record_id <> ''").Error; err != nil {
			return err
		}
		return tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_key_hash ON credentials(channel_id, key_hash) WHERE key_hash <> ''").Error
	}},
	{Version: 11, Apply: func(tx *gorm.DB) error {
		// Foreground receipts start empty; historical logs are not deduplication evidence.
		return nil
	}},
	{Version: 12, Apply: func(tx *gorm.DB) error {
		// No durable user history can be inferred from old receipts or replay caches.
		return nil
	}},
	{Version: 13, Apply: func(tx *gorm.DB) error { return nil }},
	{Version: 14, Apply: func(tx *gorm.DB) error {
		// AutoMigrate adds terminal deletion and shared namespace metadata;
		// retained v13 digests, bindings and ciphertext are never rewritten.
		return nil
	}},
	{Version: 15, Apply: func(tx *gorm.DB) error {
		// Native completion and event-log completion are separate durable facts.
		return nil
	}},
}

func checkMigrationVersion(db *gorm.DB, supported int) error {
	if !db.Migrator().HasTable(&model.SchemaMigration{}) {
		return nil
	}
	var newest int
	if err := db.Model(&model.SchemaMigration{}).Select("COALESCE(MAX(version),0)").Scan(&newest).Error; err != nil {
		return err
	}
	if newest > supported {
		return fmt.Errorf("database schema version %d is newer than supported version %d; restore a compatible backup instead of downgrading in place", newest, supported)
	}
	return nil
}
func applyDataMigrations(db *gorm.DB, steps []dataMigration) error {
	// Validate the entire registry before executing even its first step.
	previous := 0
	for _, step := range steps {
		if step.Version <= previous || step.Apply == nil {
			return fmt.Errorf("migration versions must be positive and strictly increasing")
		}
		previous = step.Version
	}
	if err := checkMigrationVersion(db, previous); err != nil {
		return err
	}
	for _, step := range steps {
		err := db.Transaction(func(tx *gorm.DB) error {
			var count int64
			if err := tx.Model(&model.SchemaMigration{}).Where("version = ?", step.Version).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
			if err := step.Apply(tx); err != nil {
				return err
			}
			return tx.Create(&model.SchemaMigration{Version: step.Version, AppliedAt: time.Now()}).Error
		})
		if err != nil {
			return fmt.Errorf("data migration v%d failed: %w", step.Version, err)
		}
	}
	return nil
}
