package store

import (
	"context"

	"minigateway/internal/model"
)

// GenerationKeyRequirements checks retained ciphertext too: deleting a key
// must not make still-retained user content silently unreadable after restart.
func (s *Store) GenerationKeyRequirements(ctx context.Context) ([]model.GenerationKeyRequirement, error) {
	var out []model.GenerationKeyRequirement
	// Key IDs are at most 32 ASCII bytes. Clamp even corrupted format headers;
	// at most 8 input + 8 output key/fingerprint pairs are valid for one ring.
	err := s.db.WithContext(ctx).Raw(`
		SELECT DISTINCT substr(ciphertext,1,MIN(34,2+COALESCE(unicode(substr(ciphertext,2,1)),0))) AS header, digest_version
		FROM generation_intents WHERE length(ciphertext) > 0
		UNION
		SELECT DISTINCT substr(ciphertext,1,MIN(34,2+COALESCE(unicode(substr(ciphertext,2,1)),0))) AS header, '' AS digest_version
		FROM generation_segments WHERE length(ciphertext) > 0
		UNION
		SELECT DISTINCT substr(ciphertext,1,MIN(34,2+COALESCE(unicode(substr(ciphertext,2,1)),0))) AS header, '' AS digest_version
		FROM response_histories WHERE length(ciphertext) > 0
		UNION
		SELECT DISTINCT substr(ciphertext,1,MIN(34,2+COALESCE(unicode(substr(ciphertext,2,1)),0))) AS header, digest_version
		FROM background_jobs WHERE length(ciphertext) > 0
		UNION
		SELECT DISTINCT substr(snapshot,1,MIN(34,2+COALESCE(unicode(substr(snapshot,2,1)),0))) AS header, digest_version
		FROM background_jobs WHERE length(snapshot) > 0
		UNION
		SELECT DISTINCT substr(ciphertext,1,MIN(34,2+COALESCE(unicode(substr(ciphertext,2,1)),0))) AS header, '' AS digest_version
		FROM background_events WHERE length(ciphertext) > 0 LIMIT 17`).Scan(&out).Error
	return out, err
}
