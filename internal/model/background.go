package model

// BackgroundJob persists disabled-mode native tasks, separate from strict intents.
type BackgroundJob struct {
	ID               string `gorm:"primaryKey;size:64"`
	PublicID         string `gorm:"not null;uniqueIndex;size:70"`
	UserID           uint   `gorm:"not null;uniqueIndex:idx_background_key,priority:1;index"`
	TokenID          uint   `gorm:"not null;uniqueIndex:idx_background_key,priority:2"`
	Operation        string `gorm:"not null"`
	Model            string `gorm:"not null"`
	KeyHash          string `gorm:"not null;uniqueIndex:idx_background_key,priority:3;size:64"`
	NamespaceHash    string `gorm:"not null;default:'';size:64;index"`
	RequestHash      string `gorm:"not null"`
	DigestVersion    string `gorm:"not null"`
	Stage            string `gorm:"not null"`
	Stream           bool   `gorm:"not null"`
	Store            bool   `gorm:"not null"`
	Revision         uint64 `gorm:"not null"`
	CreatedAt        int64  `gorm:"not null;autoCreateTime:false"`
	Deadline         int64  `gorm:"not null"`
	ContentExpiresAt int64  `gorm:"not null;index"`
	ExpiresAt        int64  `gorm:"not null;index"`
	ReservedBytes    int64  `gorm:"not null"`
	Ciphertext       []byte `gorm:"not null"`
	Snapshot         []byte `gorm:"not null"`
	ClaimEpoch       int64  `gorm:"not null"`
	ClaimHash        string `gorm:"not null"`
	ClaimUntil       int64  `gorm:"not null;index"`
	CancelRequested  bool   `gorm:"not null"`
	DueAt            int64  `gorm:"not null;index"`
	LastSequence     int64  `gorm:"not null"`
	StoredBytes      int64  `gorm:"not null"`
	EventCount       int64  `gorm:"not null"`
	Deleted          bool   `gorm:"not null;default:false"`
}

type BackgroundEvent struct {
	TaskID     string `gorm:"primaryKey;size:64"`
	Sequence   int64  `gorm:"primaryKey;autoIncrement:false"`
	Ciphertext []byte `gorm:"not null"`
}
