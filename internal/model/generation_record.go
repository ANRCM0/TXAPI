package model

// GenerationArchiveMaxSegments bounds both persistent rows and replay loading.
// The final slot is reserved for the authenticated result observation.
const GenerationArchiveMaxSegments = 8192

// GenerationRecord is an output journal, not a task intent or financial ledger.
// MaxBytes reserves ciphertext capacity until explicit deletion or expiry cleanup.
type GenerationRecord struct {
	ID           string              `gorm:"primaryKey;size:64"`
	UserID       uint                `gorm:"not null;index:idx_generation_owner"`
	TokenID      uint                `gorm:"not null;index:idx_generation_owner"`
	State        string              `gorm:"not null;index"`
	MaxBytes     int64               `gorm:"not null"`
	StoredBytes  int64               `gorm:"not null"`
	NextSequence uint64              `gorm:"not null"`
	Committed    bool                `gorm:"not null"`
	CreatedAt    int64               `gorm:"not null;autoCreateTime:false"`
	ExpiresAt    int64               `gorm:"not null;index"`
	Segments     []GenerationSegment `gorm:"foreignKey:RecordID;constraint:OnDelete:CASCADE"`
}

type GenerationSegment struct {
	RecordID   string `gorm:"primaryKey;size:64;autoIncrement:false"`
	Sequence   uint64 `gorm:"primaryKey;autoIncrement:false"`
	Kind       string `gorm:"not null"`
	Ciphertext []byte `gorm:"not null"`
}

type GenerationArchiveLimits struct {
	InstanceBytes   int64
	OwnerBytes      int64
	InstanceRecords int64
	OwnerRecords    int64
}
