package generation

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/core"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
)

var (
	ErrArchiveUnavailable = errors.New("generation output archive is unavailable")
	ErrArchiveIntegrity   = errors.New("generation output archive cannot be authenticated")
	ErrArchiveIncomplete  = errors.New("generation output archive is incomplete; execution may be unknown")
	ErrArchiveLimit       = errors.New("generation output archive limit exceeded")
)

// ArchiveStore stores only ciphertext and bounded scheduling/ownership metadata.
// It must atomically reserve quotas, append with sequence CAS and close a record.
type ArchiveStore interface {
	CreateGenerationRecord(context.Context, model.GenerationRecord, model.GenerationArchiveLimits) error
	AppendGenerationSegment(context.Context, model.GenerationRecord, model.GenerationSegment) error
	ReadGenerationRecord(context.Context, access.Binding, string) (model.GenerationRecord, error)
	DeleteGenerationRecord(context.Context, access.Binding, string) error
}

type ArchiveOptions struct {
	Store          ArchiveStore
	Resolve        func(context.Context, access.Binding) (access.Principal, error)
	ActiveKey      string
	Keys           map[string][]byte
	Retention      time.Duration
	MaxRecordBytes int64
	Limits         model.GenerationArchiveLimits
}

type Archive struct {
	store     ArchiveStore
	resolve   func(context.Context, access.Binding) (access.Principal, error)
	keys      map[string]cipher.AEAD
	activeKey string
	retention time.Duration
	maxBytes  int64
	limits    model.GenerationArchiveLimits
}

// RecordedResult deliberately excludes raw errors, caller keys and policies.
// Sealed means an authenticated observation exists, not protocol/billing success.
type RecordedResult struct {
	Operation        protocol.Operation
	Model            string
	StatusCode       int
	Phase            core.Phase
	Termination      core.Termination
	Committed        bool
	ProtocolComplete bool
	BillingState     string
	Reason           string
}

type Recording struct {
	Metadata core.ResponseMetadata
	Body     []byte
	Result   RecordedResult
}

func NewArchive(opts ArchiveOptions) (*Archive, error) {
	if opts.Store == nil || (reflect.ValueOf(opts.Store).Kind() == reflect.Pointer && reflect.ValueOf(opts.Store).IsNil()) || opts.Resolve == nil || opts.Retention < time.Minute || opts.Retention > 7*24*time.Hour ||
		opts.MaxRecordBytes < 16384 || opts.MaxRecordBytes > 8<<20 || len(opts.Keys) == 0 || len(opts.Keys) > 8 ||
		opts.Limits.OwnerBytes < opts.MaxRecordBytes || opts.Limits.InstanceBytes < opts.Limits.OwnerBytes ||
		opts.Limits.OwnerRecords < 1 || opts.Limits.InstanceRecords < opts.Limits.OwnerRecords {
		return nil, fmt.Errorf("invalid generation archive configuration")
	}
	a := &Archive{store: opts.Store, resolve: opts.Resolve, activeKey: opts.ActiveKey, retention: opts.Retention,
		maxBytes: opts.MaxRecordBytes, limits: opts.Limits, keys: make(map[string]cipher.AEAD)}
	keys, err := compileContentKeys(opts.ActiveKey, opts.Keys)
	if err != nil {
		return nil, err
	}
	a.keys = keys

	return a, nil
}

func (a *Archive) authorize(ctx context.Context, binding access.Binding) error {
	_, err := a.principal(ctx, binding)
	return err
}

func (a *Archive) principal(ctx context.Context, binding access.Binding) (access.Principal, error) {
	return currentPrincipal(ctx, a.resolve, binding)
}

func currentPrincipal(ctx context.Context, resolve func(context.Context, access.Binding) (access.Principal, error), binding access.Binding) (principal access.Principal, err error) {
	defer func() {
		if recover() != nil {
			principal, err = access.Principal{}, ErrAuthorityUnavailable
		}
	}()
	if binding.UserID == 0 || binding.TokenID == 0 {
		return access.Principal{}, access.ErrUnauthorized
	}
	p, err := resolve(ctx, binding)
	if errors.Is(err, access.ErrUnauthorized) {
		return access.Principal{}, access.ErrUnauthorized
	}
	if err != nil || p.UserID != binding.UserID || p.TokenID != binding.TokenID || p.Policy.Validate() != nil {
		return access.Principal{}, ErrAuthorityUnavailable
	}
	return p, ctx.Err()
}

func (a *Archive) start(ctx context.Context, binding access.Binding) (output *recordOutput, err error) {
	defer func() {
		if recover() != nil {
			output, err = nil, ErrArchiveUnavailable
		}
	}()
	if err := a.authorize(ctx, binding); err != nil {
		return nil, err
	}
	var id [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, ErrArchiveUnavailable
	}
	now := time.Now()
	r := model.GenerationRecord{ID: hex.EncodeToString(id[:]), UserID: binding.UserID, TokenID: binding.TokenID,
		State: "open", MaxBytes: a.maxBytes, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(a.retention).UnixMilli()}
	if err := a.store.CreateGenerationRecord(ctx, r, a.limits); err != nil {
		return nil, ErrArchiveUnavailable
	}
	return &recordOutput{archive: a, record: r}, nil
}

func archiveAAD(r model.GenerationRecord, seq uint64, kind string, header []byte) []byte {
	return append([]byte(fmt.Sprintf("TXAPI/generation/v1/%s/%d/%d/%d/%d/%d/%d/%s/", r.ID, r.UserID, r.TokenID, r.CreatedAt, r.ExpiresAt, r.MaxBytes, seq, kind)), header...)
}

func (a *Archive) seal(r model.GenerationRecord, kind string, data []byte) ([]byte, error) {
	key := a.keys[a.activeKey]
	header := append([]byte{1, byte(len(a.activeKey))}, a.activeKey...)
	nonce := make([]byte, key.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrArchiveUnavailable
	}
	return key.Seal(append(header, nonce...), nonce, data, archiveAAD(r, r.NextSequence, kind, header)), nil
}

func (a *Archive) open(r model.GenerationRecord, s model.GenerationSegment) ([]byte, error) {
	data := s.Ciphertext
	if len(data) < 2 || data[0] != 1 || data[1] < 1 || data[1] > 32 || len(data) < 2+int(data[1]) {
		return nil, ErrArchiveIntegrity
	}
	header := data[:2+int(data[1])]
	key := a.keys[string(header[2:])]
	if key == nil || len(data) < len(header)+key.NonceSize()+key.Overhead() {
		return nil, ErrArchiveIntegrity
	}
	nonce := data[len(header) : len(header)+key.NonceSize()]
	plain, err := key.Open(nil, nonce, data[len(header)+len(nonce):], archiveAAD(r, s.Sequence, s.Kind, header))
	if err != nil {
		return nil, ErrArchiveIntegrity
	}
	return plain, nil
}

func (a *Archive) Read(ctx context.Context, binding access.Binding, id string) (Recording, error) {
	principal, err := a.principal(ctx, binding)
	if err != nil {
		return Recording{}, err
	}
	if len(id) != 64 {
		return Recording{}, ErrArchiveUnavailable
	}
	r, err := a.store.ReadGenerationRecord(ctx, binding, id)
	if err != nil {
		return Recording{}, ErrArchiveUnavailable
	}
	if r.ID != id || r.UserID != binding.UserID || r.TokenID != binding.TokenID || r.ExpiresAt <= time.Now().UnixMilli() ||
		r.MaxBytes > a.maxBytes || r.StoredBytes < 0 || r.StoredBytes > r.MaxBytes || len(r.Segments) > 256 || uint64(len(r.Segments)) != r.NextSequence {
		return Recording{}, ErrArchiveIntegrity
	}
	if r.State != "sealed" {
		return Recording{}, ErrArchiveIncomplete
	}
	var out Recording
	var size int64
	metadata, result := false, false
	for i, segment := range r.Segments {
		if err := ctx.Err(); err != nil {
			return Recording{}, err
		}
		if segment.RecordID != id || segment.Sequence != uint64(i) {
			return Recording{}, ErrArchiveIntegrity
		}
		size += int64(len(segment.Ciphertext))
		if size > r.MaxBytes {
			return Recording{}, ErrArchiveIntegrity
		}
		plain, err := a.open(r, segment)
		if err != nil {
			return Recording{}, err
		}
		switch segment.Kind {
		case "metadata":
			if i != 0 || metadata || json.Unmarshal(plain, &out.Metadata) != nil {
				return Recording{}, ErrArchiveIntegrity
			}
			metadata = true
		case "body":
			if !metadata || result {
				return Recording{}, ErrArchiveIntegrity
			}
			out.Body = append(out.Body, plain...)
		case "result":
			if i != len(r.Segments)-1 || result || json.Unmarshal(plain, &out.Result) != nil {
				return Recording{}, ErrArchiveIntegrity
			}
			result = true
		default:
			return Recording{}, ErrArchiveIntegrity
		}
	}
	if !result || size != r.StoredBytes || metadata != r.Committed || out.Result.Committed != metadata {
		return Recording{}, ErrArchiveIntegrity
	}
	if out.Result.Model == "" || !principal.Policy.Allows(out.Result.Model) {
		return Recording{}, access.ErrUnauthorized
	}
	return out, nil
}

func (a *Archive) Delete(ctx context.Context, binding access.Binding, id string) error {
	if err := a.authorize(ctx, binding); err != nil {
		return err
	}
	if len(id) != 64 {
		return ErrArchiveUnavailable
	}
	if err := a.store.DeleteGenerationRecord(ctx, binding, id); err != nil {
		return ErrArchiveUnavailable
	}
	return nil
}

type recordOutput struct {
	mu      sync.Mutex
	archive *Archive
	record  model.GenerationRecord
	state   core.OutputState
	failed  bool
	closed  bool
	model   string
}

func (o *recordOutput) append(ctx context.Context, kind string, data []byte) error {
	if o.closed || o.failed {
		return ErrArchiveUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.record.ExpiresAt <= time.Now().UnixMilli() {
		return ErrArchiveUnavailable
	}
	ciphertext, err := o.archive.seal(o.record, kind, data)
	if err != nil {
		o.failed = true
		return err
	}
	// Always leave one segment and 4 KiB for the bounded final observation.
	maxBytes, maxSequence := o.record.MaxBytes, uint64(256)
	if kind != "result" {
		maxBytes -= 4096
		maxSequence--
	}
	if int64(len(ciphertext)) > maxBytes-o.record.StoredBytes || o.record.NextSequence >= maxSequence {
		return ErrArchiveLimit
	}
	segment := model.GenerationSegment{RecordID: o.record.ID, Sequence: o.record.NextSequence, Kind: kind, Ciphertext: ciphertext}
	if err := o.archive.store.AppendGenerationSegment(ctx, o.record, segment); err != nil {
		// An ambiguous commit cannot be retried with a new nonce/sequence.
		o.failed = true
		return ErrArchiveUnavailable
	}
	o.record.NextSequence++
	o.record.StoredBytes += int64(len(ciphertext))
	if kind == "result" {
		o.closed = true
	}
	return nil
}

func (o *recordOutput) Commit(ctx context.Context, metadata core.ResponseMetadata) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state.Committed {
		return ErrArchiveUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(metadata.ContentType) > 128 || len(metadata.Values) > 32 {
		return ErrArchiveLimit
	}
	for key, value := range metadata.Values {
		if len(key) > 128 || len(value) > 1024 {
			return ErrArchiveLimit
		}
	}
	data, err := json.Marshal(metadata)
	if err != nil || len(data) > 8192 {
		return ErrArchiveLimit
	}
	// Conservative even on an ambiguous storage failure: core must not retry.
	o.state.Committed = true
	err = o.append(ctx, "metadata", data)
	if err != nil {
		o.failed = true
	}
	return err
}

func (o *recordOutput) Write(ctx context.Context, data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.state.Committed {
		return 0, ErrArchiveUnavailable
	}
	n := 0
	for len(data) > 0 {
		size := min(len(data), 32<<10)
		if err := o.append(ctx, "body", data[:size]); err != nil {
			return n, err
		}
		n += size
		data = data[size:]
	}
	return n, nil
}

func (o *recordOutput) Flush(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failed || o.closed {
		return ErrArchiveUnavailable
	}
	return ctx.Err()
}

func (o *recordOutput) State() core.OutputState {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state
}

func (o *recordOutput) finish(result gateway.RequestResult, executionErr error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrArchiveUnavailable
		}
	}()
	o.mu.Lock()
	defer o.mu.Unlock()
	reason := "observed"
	switch {
	case errors.Is(executionErr, ErrExecutionPanic):
		reason = "execution_panic"
	case errors.Is(executionErr, access.ErrUnauthorized):
		reason = "unauthorized"
	case errors.Is(executionErr, ErrAuthorityUnavailable):
		reason = "authority_unavailable"
	case errors.Is(executionErr, context.DeadlineExceeded):
		reason = "deadline"
	case errors.Is(executionErr, context.Canceled):
		reason = "canceled"
	case executionErr != nil:
		reason = "execution_error"
	}
	if len(result.Operation) > 64 || len(result.Phase) > 64 || len(result.Termination) > 64 || len(result.BillingState) > 64 {
		return ErrArchiveLimit
	}
	data, err := json.Marshal(RecordedResult{Operation: result.Operation, Model: o.model, StatusCode: result.StatusCode, Phase: result.Phase,
		Termination: result.Termination, Committed: o.state.Committed, ProtocolComplete: result.ProtocolComplete,
		BillingState: result.BillingState, Reason: reason})
	if err != nil || len(data) > 2048 {
		return ErrArchiveLimit
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return o.append(ctx, "result", data)
}
