// Package state defines bounded persistence and observation ports.
package state

import (
	"context"
	"errors"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	"time"
)

const Version = "1"

var (
	ErrArchiveUnavailable = errors.New("generation output archive is unavailable")
	ErrArchiveIntegrity   = errors.New("generation output archive cannot be authenticated")
	ErrArchiveIncomplete  = errors.New("generation output archive is incomplete; execution may be unknown")
	ErrArchiveLimit       = errors.New("generation output archive limit exceeded")
)

const GenerationArchiveMaxSegments = 8192

var (
	ErrIntentConflict = errors.New("generation intent conflicts with its idempotency scope")
	ErrIntentCapacity = errors.New("generation intent capacity exceeded")
	ErrIntentNotFound = errors.New("generation intent was not found")
	ErrIntentUnsafe   = errors.New("generation intent cannot be safely released")
	ErrClaimLost      = errors.New("generation claim is no longer owned")
)

const (
	IntentQueued     = "queued"
	IntentClaimed    = "claimed"
	IntentRunning    = "running"
	IntentFinalizing = "finalizing"
	IntentSucceeded  = "succeeded"
	IntentFailed     = "failed"
	IntentCancelled  = "cancelled"
	IntentExpired    = "expired"
	IntentUncertain  = "uncertain"
)

var (
	ErrResponseConflict    = errors.New("response request conflicts with its idempotency scope")
	ErrResponseCapacity    = errors.New("response request capacity exceeded")
	ErrResponseUnavailable = errors.New("response request persistence is unavailable")
)

const (
	ResponseDispatching = "dispatching"
	ResponseCompleted   = "completed"
	ResponseFailed      = "failed"
	ResponseUncertain   = "uncertain"
	ResponseRejected    = "rejected"
)

var (
	ErrHistoryNotFound = errors.New("history not found")
	ErrHistoryConflict = errors.New("history version or execution conflict")
	ErrHistoryCapacity = errors.New("history capacity exceeded")
	ErrHistoryContext  = errors.New("history context is not self-contained")
)

const ResponseHistoryMaxBytes = 2 << 20

type GenerationRecord struct {
	ID           string
	UserID       uint
	TokenID      uint
	State        string
	MaxBytes     int64
	StoredBytes  int64
	NextSequence uint64
	Committed    bool
	CreatedAt    int64
	ExpiresAt    int64
	Segments     []GenerationSegment
}

type GenerationSegment struct {
	RecordID   string
	Sequence   uint64
	Kind       string
	Ciphertext []byte
}

type GenerationArchiveLimits struct {
	InstanceBytes   int64
	OwnerBytes      int64
	InstanceRecords int64
	OwnerRecords    int64
}

type GenerationIntent struct {
	ID                    string
	UserID                uint
	TokenID               uint
	AdmissionKey          string
	RequestDigest         string
	DigestVersion         string
	Operation             string
	Model                 string
	BillingID             string
	State                 string
	Ciphertext            []byte
	CreatedAt             int64
	Deadline              int64
	ExpiresAt             int64
	ProviderPolicyVersion string
	ClaimEpoch            int64
	ClaimHash             string
	ClaimUntil            int64
	CancelRequested       bool
	OutputRecordID        string
}

type GenerationClaim struct {
	ID    string
	Epoch int64
	Token string
}

type GenerationKeyRequirement struct {
	Header        []byte
	DigestVersion string
}

type GenerationIntentLimits struct {
	InstanceRecords int64
	OwnerRecords    int64
	InstanceBytes   int64
	OwnerBytes      int64
}

type ResponseRequest struct {
	ID                 string
	UserID             uint
	TokenID            uint
	Operation          string
	KeyHash            string
	RequestHash        string
	DigestVersion      string
	ResponseID         string
	UpstreamResponseID string
	Model              string
	Stream             bool
	Store              bool
	State              string
	NativeStatus       string
	OutputRecordID     string
	StatusCode         int
	HTTPStatus         int
	BillingID          string
	ErrorType          string
	CreatedAt          int64
	ExpiresAt          int64
}

type ResponseRequestLimits struct {
	InstanceRecords int64
	OwnerRecords    int64
}

type ResponseRequestCompletion struct {
	State              string
	NativeStatus       string
	UpstreamResponseID string
	OutputRecordID     string
	StatusCode         int
	HTTPStatus         int
	BillingID          string
	ErrorType          string
}

type ResponseHistory struct {
	ID            string
	UserID        uint
	TokenID       uint
	Kind          string
	Model         string
	Version       uint64
	Busy          string
	Deleted       bool
	CreatedAt     int64
	ExpiresAt     int64
	ReservedBytes int64
	Ciphertext    []byte
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

type ArchiveStore interface {
	CreateGenerationRecord(context.Context, GenerationRecord, GenerationArchiveLimits) error
	AppendGenerationSegment(context.Context, GenerationRecord, GenerationSegment) error
	ReadGenerationRecord(context.Context, identity.Binding, string) (GenerationRecord, error)
	DeleteGenerationRecord(context.Context, identity.Binding, string) error
}

type IntentAdmission interface {
	AdmitGenerationIntent(context.Context, GenerationIntent, GenerationIntentLimits) (GenerationIntent, error)
	ReadGenerationIntent(context.Context, uint, uint, string) (GenerationIntent, error)
	CancelGenerationIntent(context.Context, uint, uint, string) (GenerationIntent, error)
}

type IntentLeases interface {
	ClaimGenerationIntent(context.Context, time.Duration) (GenerationIntent, GenerationClaim, error)
	RenewGenerationClaim(context.Context, GenerationClaim, time.Duration) (bool, error)
	AttachGenerationOutput(context.Context, GenerationClaim, string) error
	CompleteGenerationOutput(context.Context, GenerationClaim) error
	RejectGenerationClaim(context.Context, GenerationClaim) error
	QuarantineGenerationClaim(context.Context, GenerationClaim) error
}

type ReceiptStore interface {
	InitializeResponseRequests(context.Context, string) error
	BeginResponseRequest(context.Context, ResponseRequest, ResponseRequestLimits) (ResponseRequest, bool, error)
	FinishResponseRequest(context.Context, ResponseRequest, ResponseRequestCompletion) error
}

type HistoryStore interface {
	ReadResponseHistory(context.Context, identity.Binding, string) (ResponseHistory, error)
	ApplyResponseHistory(context.Context, identity.Binding, []HistoryChange, []HistoryGuard, HistoryLimits) error
}

// ContentProtection is injected by assembly. The implementation owns key material;
// the caller supplies the existing domain-separated authenticated identity.
type ContentProtection interface {
	Active() string
	Seal([]byte, []byte) ([]byte, error)
	Open([]byte, []byte) ([]byte, error)
}

type RecordedResult struct {
	Operation        execution.Operation
	Model            string
	StatusCode       int
	Phase            execution.Phase
	Termination      execution.Termination
	Committed        bool
	ProtocolComplete bool
	BillingState     string
	Reason           string
}

type Recording struct {
	Metadata execution.ResponseMetadata
	Body     []byte
	Result   RecordedResult
}

type CursorPage struct {
	Metadata *execution.ResponseMetadata
	Body     []byte
	Result   *RecordedResult
	Cursor   string
	Done     bool
}

type Cursor interface {
	Next(context.Context, int) (CursorPage, error)
	Close() error
}

// Foreground owns one generation's recording and observer output. Finish records
// its observation; it does not assert financial success or release unknown holds.
type Foreground interface {
	execution.DeadlineOutput
	ID() string
	Finish(execution.Outcome) error
}

type Archive interface {
	BeginForeground(context.Context, identity.Binding, string, execution.Output) (Foreground, error)
	Read(context.Context, identity.Binding, string) (Recording, error)
	Delete(context.Context, identity.Binding, string) error
	OpenCursor(context.Context, identity.Binding, string, string) (Cursor, error)
}

type ClaimedExecutor interface {
	ExecuteClaimed(context.Context, execution.Output, execution.Invocation, GenerationClaim) execution.Outcome
}
