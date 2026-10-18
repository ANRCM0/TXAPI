// Package backgroundstate is the private L1 persistence seam, never an L3 API.
package backgroundstate

import (
	"context"
	"errors"
	"time"

	bg "minigateway/internal/foundation/contracts/background/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
)

var ErrClaimLost = errors.New("background claim lost")

const MaxContent = 2 << 20
const MaxInput = 256 << 10
const MaxEvents = 8192

type Record struct {
	ID               string
	PublicID         string
	UserID           uint
	TokenID          uint
	Operation        string
	Model            string
	KeyHash          string
	NamespaceHash    string
	RequestHash      string
	DigestVersion    string
	Stage            string
	Stream           bool
	Store            bool
	Revision         uint64
	CreatedAt        int64
	Deadline         int64
	ContentExpiresAt int64
	ExpiresAt        int64
	ReservedBytes    int64
	Ciphertext       []byte
	Snapshot         []byte
	ClaimEpoch       int64
	ClaimHash        string
	ClaimUntil       int64
	CancelRequested  bool
	DueAt            int64
	LastSequence     int64
	StoredBytes      int64
	EventCount       int64
	Deleted          bool
	ReplayPending    bool
}
type Event struct {
	TaskID     string
	Sequence   int64
	Ciphertext []byte
}
type Limits struct{ InstanceRecords, OwnerRecords, InstanceBytes, OwnerBytes, InstanceActive, OwnerActive int64 }
type Claim struct {
	ID    string
	Epoch int64
	Token string
}
type Update struct {
	Stage         bg.Stage
	Snapshot      []byte
	Events        []Event
	DueAt         int64
	ReplayPending bool
}
type Repository interface {
	InitializeBackground(context.Context, string, string) ([]Record, error)
	AdmitBackground(context.Context, Record, Limits) (Record, error)
	ReadBackground(context.Context, identity.Binding, string) (Record, error)
	BackgroundKind(context.Context, identity.Binding, string) (bg.ResourceKind, error)
	DeleteBackground(context.Context, identity.Binding, string, *uint64) error
	ReadClaimedBackground(context.Context, Claim) (Record, error)
	ReadClaimedBackgroundEvent(context.Context, Claim, int64) (Event, bool, error)
	RequestBackgroundCancel(context.Context, Claim) (Record, error)
	CancelBackground(context.Context, identity.Binding, string) (Record, error)
	ReadBackgroundEvents(context.Context, identity.Binding, string, int64, int) ([]Event, error)
	ClaimBackground(context.Context, time.Duration) (Record, Claim, error)
	RenewBackground(context.Context, Claim, time.Duration) (bool, error)
	UpdateBackground(context.Context, Claim, uint64, Update) (Record, error)
	ReleaseBackground(context.Context, Claim, int64) error
	CleanupBackground(context.Context, int) error
}
