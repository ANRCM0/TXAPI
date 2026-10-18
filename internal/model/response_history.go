package model

import "errors"

var (
	ErrHistoryNotFound = errors.New("history not found")
	ErrHistoryConflict = errors.New("history version or execution conflict")
	ErrHistoryCapacity = errors.New("history capacity exceeded")
	ErrHistoryContext  = errors.New("history context is not self-contained")
)

const ResponseHistoryMaxBytes = 2 << 20

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

type HistoryGuard struct {
	ID      string
	Version uint64
}
type HistoryChange struct {
	Expected uint64
	Record   ResponseHistory
}
type HistoryLimits struct{ InstanceRecords, OwnerRecords, InstanceBytes, OwnerBytes int64 }
