package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"gorm.io/gorm"
	quotation "minigateway/internal/foundation/contracts/quotation/v1"
	"minigateway/internal/model"
)

// QuotationHistory reads bounded immutable metadata. It is unrelated to request
// logs and never changes a reservation, balance or the old price reader.
func (s *Store) QuotationHistory(ctx context.Context, version string) (string, error) {
	digest, err := hex.DecodeString(version)
	if ctx == nil || err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != version {
		return "", quotation.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var row struct {
		Payload string
		Size    int64
	}
	// Reject large corrupted/unrelated rows without first allocating their body.
	err = s.db.WithContext(ctx).Model(&model.BillingPriceVersion{}).
		Select("CASE WHEN length(CAST(payload AS BLOB)) <= ? THEN payload ELSE '' END AS payload, length(CAST(payload AS BLOB)) AS size", 256*1024).
		Where("version = ?", version).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", quotation.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	actual := sha256.Sum256([]byte(row.Payload))
	if row.Size <= 0 || row.Size > 256*1024 || len(row.Payload) > 256*1024 || hex.EncodeToString(actual[:]) != version {
		return "", quotation.ErrInvalid
	}
	return row.Payload, nil
}
