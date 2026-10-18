package model

import state "minigateway/internal/foundation/contracts/state/v1"

var (
	ErrHistoryNotFound = state.ErrHistoryNotFound
	ErrHistoryConflict = state.ErrHistoryConflict
	ErrHistoryCapacity = state.ErrHistoryCapacity
	ErrHistoryContext  = state.ErrHistoryContext
)

const ResponseHistoryMaxBytes = state.ResponseHistoryMaxBytes

// ResponseHistory contains encrypted application snapshots, never a runnable
// intent. Receipt identity and the original financial ledger remain separate.
type ResponseHistory struct {
	ID            string `gorm:"primaryKey;size:80"`
	UserID        uint   `gorm:"not null;index:idx_history_owner"`
	TokenID       uint   `gorm:"not null;index:idx_history_owner"`
	Kind          string `gorm:"not null;size:16"`
	Model         string `gorm:"not null;size:256"`
	Version       uint64 `gorm:"not null"`
	Busy          string `gorm:"not null;size:64"`
	Deleted       bool   `gorm:"not null"`
	CreatedAt     int64  `gorm:"not null;autoCreateTime:false"`
	ExpiresAt     int64  `gorm:"not null;index"`
	ReservedBytes int64  `gorm:"not null"`
	Ciphertext    []byte `gorm:"not null"`
}

type HistoryGuard = state.HistoryGuard
type HistoryChange struct {
	Expected uint64
	Record   ResponseHistory
}
type HistoryLimits = state.HistoryLimits
