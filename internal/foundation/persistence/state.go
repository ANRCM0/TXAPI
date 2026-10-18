// Package persistence bridges public values to the existing transaction owners.
package persistence

import (
	"bytes"
	"context"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	"minigateway/internal/model"
	"reflect"
	"time"
)

func absent(v any) bool {
	if v == nil {
		return true
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(v).IsNil()
	}
	return false
}

func GenerationIntentSnapshot(in model.GenerationIntent) state.GenerationIntent {
	out := state.GenerationIntent{
		ID:                    in.ID,
		UserID:                in.UserID,
		TokenID:               in.TokenID,
		AdmissionKey:          in.AdmissionKey,
		RequestDigest:         in.RequestDigest,
		DigestVersion:         in.DigestVersion,
		Operation:             in.Operation,
		Model:                 in.Model,
		BillingID:             in.BillingID,
		State:                 in.State,
		Ciphertext:            bytes.Clone(in.Ciphertext),
		CreatedAt:             in.CreatedAt,
		Deadline:              in.Deadline,
		ExpiresAt:             in.ExpiresAt,
		ProviderPolicyVersion: in.ProviderPolicyVersion,
		ClaimEpoch:            in.ClaimEpoch,
		ClaimHash:             in.ClaimHash,
		ClaimUntil:            in.ClaimUntil,
		CancelRequested:       in.CancelRequested,
		OutputRecordID:        in.OutputRecordID,
	}
	return out
}

func generationIntentModel(in state.GenerationIntent) model.GenerationIntent {
	out := model.GenerationIntent{
		ID:                    in.ID,
		UserID:                in.UserID,
		TokenID:               in.TokenID,
		AdmissionKey:          in.AdmissionKey,
		RequestDigest:         in.RequestDigest,
		DigestVersion:         in.DigestVersion,
		Operation:             in.Operation,
		Model:                 in.Model,
		BillingID:             in.BillingID,
		State:                 in.State,
		Ciphertext:            bytes.Clone(in.Ciphertext),
		CreatedAt:             in.CreatedAt,
		Deadline:              in.Deadline,
		ExpiresAt:             in.ExpiresAt,
		ProviderPolicyVersion: in.ProviderPolicyVersion,
		ClaimEpoch:            in.ClaimEpoch,
		ClaimHash:             in.ClaimHash,
		ClaimUntil:            in.ClaimUntil,
		CancelRequested:       in.CancelRequested,
		OutputRecordID:        in.OutputRecordID,
	}
	return out
}

func GenerationRecordSnapshot(in model.GenerationRecord) state.GenerationRecord {
	out := state.GenerationRecord{
		ID:           in.ID,
		UserID:       in.UserID,
		TokenID:      in.TokenID,
		State:        in.State,
		MaxBytes:     in.MaxBytes,
		StoredBytes:  in.StoredBytes,
		NextSequence: in.NextSequence,
		Committed:    in.Committed,
		CreatedAt:    in.CreatedAt,
		ExpiresAt:    in.ExpiresAt,
	}
	if in.Segments != nil {
		out.Segments = make([]state.GenerationSegment, len(in.Segments))
		for i, s := range in.Segments {
			out.Segments[i] = GenerationSegmentSnapshot(s)
		}
	}
	return out
}

func generationRecordModel(in state.GenerationRecord) model.GenerationRecord {
	out := model.GenerationRecord{
		ID:           in.ID,
		UserID:       in.UserID,
		TokenID:      in.TokenID,
		State:        in.State,
		MaxBytes:     in.MaxBytes,
		StoredBytes:  in.StoredBytes,
		NextSequence: in.NextSequence,
		Committed:    in.Committed,
		CreatedAt:    in.CreatedAt,
		ExpiresAt:    in.ExpiresAt,
	}
	if in.Segments != nil {
		out.Segments = make([]model.GenerationSegment, len(in.Segments))
		for i, s := range in.Segments {
			out.Segments[i] = generationSegmentModel(s)
		}
	}
	return out
}

func GenerationSegmentSnapshot(in model.GenerationSegment) state.GenerationSegment {
	out := state.GenerationSegment{
		RecordID:   in.RecordID,
		Sequence:   in.Sequence,
		Kind:       in.Kind,
		Ciphertext: bytes.Clone(in.Ciphertext),
	}
	return out
}

func generationSegmentModel(in state.GenerationSegment) model.GenerationSegment {
	out := model.GenerationSegment{
		RecordID:   in.RecordID,
		Sequence:   in.Sequence,
		Kind:       in.Kind,
		Ciphertext: bytes.Clone(in.Ciphertext),
	}
	return out
}

func ResponseRequestSnapshot(in model.ResponseRequest) state.ResponseRequest {
	out := state.ResponseRequest{
		ID:                 in.ID,
		UserID:             in.UserID,
		TokenID:            in.TokenID,
		Operation:          in.Operation,
		KeyHash:            in.KeyHash,
		RequestHash:        in.RequestHash,
		DigestVersion:      in.DigestVersion,
		ResponseID:         in.ResponseID,
		UpstreamResponseID: in.UpstreamResponseID,
		Model:              in.Model,
		Stream:             in.Stream,
		Store:              in.Store,
		State:              in.State,
		NativeStatus:       in.NativeStatus,
		OutputRecordID:     in.OutputRecordID,
		StatusCode:         in.StatusCode,
		HTTPStatus:         in.HTTPStatus,
		BillingID:          in.BillingID,
		ErrorType:          in.ErrorType,
		CreatedAt:          in.CreatedAt,
		ExpiresAt:          in.ExpiresAt,
	}
	return out
}

func responseRequestModel(in state.ResponseRequest) model.ResponseRequest {
	out := model.ResponseRequest{
		ID:                 in.ID,
		UserID:             in.UserID,
		TokenID:            in.TokenID,
		Operation:          in.Operation,
		KeyHash:            in.KeyHash,
		RequestHash:        in.RequestHash,
		DigestVersion:      in.DigestVersion,
		ResponseID:         in.ResponseID,
		UpstreamResponseID: in.UpstreamResponseID,
		Model:              in.Model,
		Stream:             in.Stream,
		Store:              in.Store,
		State:              in.State,
		NativeStatus:       in.NativeStatus,
		OutputRecordID:     in.OutputRecordID,
		StatusCode:         in.StatusCode,
		HTTPStatus:         in.HTTPStatus,
		BillingID:          in.BillingID,
		ErrorType:          in.ErrorType,
		CreatedAt:          in.CreatedAt,
		ExpiresAt:          in.ExpiresAt,
	}
	return out
}

func ResponseHistorySnapshot(in model.ResponseHistory) state.ResponseHistory {
	out := state.ResponseHistory{
		ID:            in.ID,
		UserID:        in.UserID,
		TokenID:       in.TokenID,
		Kind:          in.Kind,
		Model:         in.Model,
		Version:       in.Version,
		Busy:          in.Busy,
		Deleted:       in.Deleted,
		CreatedAt:     in.CreatedAt,
		ExpiresAt:     in.ExpiresAt,
		ReservedBytes: in.ReservedBytes,
		Ciphertext:    bytes.Clone(in.Ciphertext),
	}
	return out
}

func responseHistoryModel(in state.ResponseHistory) model.ResponseHistory {
	out := model.ResponseHistory{
		ID:            in.ID,
		UserID:        in.UserID,
		TokenID:       in.TokenID,
		Kind:          in.Kind,
		Model:         in.Model,
		Version:       in.Version,
		Busy:          in.Busy,
		Deleted:       in.Deleted,
		CreatedAt:     in.CreatedAt,
		ExpiresAt:     in.ExpiresAt,
		ReservedBytes: in.ReservedBytes,
		Ciphertext:    bytes.Clone(in.Ciphertext),
	}
	return out
}

type ArchiveBackend interface {
	CreateGenerationRecord(context.Context, model.GenerationRecord, model.GenerationArchiveLimits) error
	AppendGenerationSegment(context.Context, model.GenerationRecord, model.GenerationSegment) error
	ReadGenerationRecord(context.Context, identity.Binding, string) (model.GenerationRecord, error)
	DeleteGenerationRecord(context.Context, identity.Binding, string) error
}
type archivePort struct{ backend ArchiveBackend }

func Archive(backend ArchiveBackend) state.ArchiveStore {
	if absent(backend) {
		return nil
	}
	return &archivePort{backend}
}
func (p *archivePort) CreateGenerationRecord(ctx context.Context, in state.GenerationRecord, limits state.GenerationArchiveLimits) error {
	return p.backend.CreateGenerationRecord(ctx, generationRecordModel(in), limits)
}
func (p *archivePort) AppendGenerationSegment(ctx context.Context, in state.GenerationRecord, segment state.GenerationSegment) error {
	return p.backend.AppendGenerationSegment(ctx, generationRecordModel(in), generationSegmentModel(segment))
}
func (p *archivePort) ReadGenerationRecord(ctx context.Context, b identity.Binding, id string) (state.GenerationRecord, error) {
	in, err := p.backend.ReadGenerationRecord(ctx, b, id)
	return GenerationRecordSnapshot(in), err
}
func (p *archivePort) DeleteGenerationRecord(ctx context.Context, b identity.Binding, id string) error {
	return p.backend.DeleteGenerationRecord(ctx, b, id)
}

type AdmissionBackend interface {
	AdmitGenerationIntent(context.Context, model.GenerationIntent, model.GenerationIntentLimits) (model.GenerationIntent, error)
	ReadGenerationIntent(context.Context, uint, uint, string) (model.GenerationIntent, error)
	CancelGenerationIntent(context.Context, uint, uint, string) (model.GenerationIntent, error)
}
type admissionPort struct{ backend AdmissionBackend }

func Admission(backend AdmissionBackend) state.IntentAdmission {
	if absent(backend) {
		return nil
	}
	return &admissionPort{backend}
}
func (p *admissionPort) AdmitGenerationIntent(ctx context.Context, in state.GenerationIntent, limits state.GenerationIntentLimits) (state.GenerationIntent, error) {
	out, err := p.backend.AdmitGenerationIntent(ctx, generationIntentModel(in), limits)
	return GenerationIntentSnapshot(out), err
}
func (p *admissionPort) ReadGenerationIntent(ctx context.Context, user, token uint, id string) (state.GenerationIntent, error) {
	out, err := p.backend.ReadGenerationIntent(ctx, user, token, id)
	return GenerationIntentSnapshot(out), err
}
func (p *admissionPort) CancelGenerationIntent(ctx context.Context, user, token uint, id string) (state.GenerationIntent, error) {
	out, err := p.backend.CancelGenerationIntent(ctx, user, token, id)
	return GenerationIntentSnapshot(out), err
}

type LeaseBackend interface {
	ClaimGenerationIntent(context.Context, time.Duration) (model.GenerationIntent, model.GenerationClaim, error)
	RenewGenerationClaim(context.Context, model.GenerationClaim, time.Duration) (bool, error)
	AttachGenerationOutput(context.Context, model.GenerationClaim, string) error
	CompleteGenerationOutput(context.Context, model.GenerationClaim) error
	RejectGenerationClaim(context.Context, model.GenerationClaim) error
	QuarantineGenerationClaim(context.Context, model.GenerationClaim) error
}
type leasePort struct{ backend LeaseBackend }

func Leases(backend LeaseBackend) state.IntentLeases {
	if absent(backend) {
		return nil
	}
	return &leasePort{backend}
}
func (p *leasePort) ClaimGenerationIntent(ctx context.Context, d time.Duration) (state.GenerationIntent, state.GenerationClaim, error) {
	out, claim, err := p.backend.ClaimGenerationIntent(ctx, d)
	return GenerationIntentSnapshot(out), claim, err
}
func (p *leasePort) RenewGenerationClaim(ctx context.Context, c state.GenerationClaim, d time.Duration) (bool, error) {
	return p.backend.RenewGenerationClaim(ctx, c, d)
}
func (p *leasePort) AttachGenerationOutput(ctx context.Context, c state.GenerationClaim, id string) error {
	return p.backend.AttachGenerationOutput(ctx, c, id)
}
func (p *leasePort) CompleteGenerationOutput(ctx context.Context, c state.GenerationClaim) error {
	return p.backend.CompleteGenerationOutput(ctx, c)
}
func (p *leasePort) RejectGenerationClaim(ctx context.Context, c state.GenerationClaim) error {
	return p.backend.RejectGenerationClaim(ctx, c)
}
func (p *leasePort) QuarantineGenerationClaim(ctx context.Context, c state.GenerationClaim) error {
	return p.backend.QuarantineGenerationClaim(ctx, c)
}

type ReceiptBackend interface {
	InitializeResponseRequests(context.Context, string) error
	BeginResponseRequest(context.Context, model.ResponseRequest, model.ResponseRequestLimits) (model.ResponseRequest, bool, error)
	FinishResponseRequest(context.Context, model.ResponseRequest, model.ResponseRequestCompletion) error
}
type receiptPort struct{ backend ReceiptBackend }

func Receipts(backend ReceiptBackend) state.ReceiptStore {
	if absent(backend) {
		return nil
	}
	return &receiptPort{backend}
}
func (p *receiptPort) InitializeResponseRequests(ctx context.Context, version string) error {
	return p.backend.InitializeResponseRequests(ctx, version)
}
func (p *receiptPort) BeginResponseRequest(ctx context.Context, in state.ResponseRequest, limits state.ResponseRequestLimits) (state.ResponseRequest, bool, error) {
	out, fresh, err := p.backend.BeginResponseRequest(ctx, responseRequestModel(in), limits)
	return ResponseRequestSnapshot(out), fresh, err
}
func (p *receiptPort) FinishResponseRequest(ctx context.Context, in state.ResponseRequest, completion state.ResponseRequestCompletion) error {
	return p.backend.FinishResponseRequest(ctx, responseRequestModel(in), completion)
}

type HistoryBackend interface {
	ReadResponseHistory(context.Context, identity.Binding, string) (model.ResponseHistory, error)
	ApplyResponseHistory(context.Context, identity.Binding, []model.HistoryChange, []model.HistoryGuard, model.HistoryLimits) error
}
type historyPort struct{ backend HistoryBackend }

func History(backend HistoryBackend) state.HistoryStore {
	if absent(backend) {
		return nil
	}
	return &historyPort{backend}
}
func (p *historyPort) ReadResponseHistory(ctx context.Context, b identity.Binding, id string) (state.ResponseHistory, error) {
	out, err := p.backend.ReadResponseHistory(ctx, b, id)
	return ResponseHistorySnapshot(out), err
}
func (p *historyPort) ApplyResponseHistory(ctx context.Context, b identity.Binding, changes []state.HistoryChange, guards []state.HistoryGuard, limits state.HistoryLimits) error {
	var raw []model.HistoryChange
	if changes != nil {
		raw = make([]model.HistoryChange, len(changes))
		for i, c := range changes {
			raw[i] = model.HistoryChange{Expected: c.Expected, Record: responseHistoryModel(c.Record)}
		}
	}
	return p.backend.ApplyResponseHistory(ctx, b, raw, append([]state.HistoryGuard(nil), guards...), limits)
}
