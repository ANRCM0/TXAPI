package store

import (
	"bytes"
	bg "minigateway/internal/foundation/contracts/background/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

func backgroundSnapshot(in model.BackgroundJob) bs.Record {
	return bs.Record{
		BillingID: in.BillingID, BillingMode: in.BillingMode, ControlCalls: in.ControlCalls,
		ID:               in.ID,
		PublicID:         in.PublicID,
		UserID:           in.UserID,
		TokenID:          in.TokenID,
		Operation:        in.Operation,
		Model:            in.Model,
		KeyHash:          in.KeyHash,
		NamespaceHash:    in.NamespaceHash,
		RequestHash:      in.RequestHash,
		DigestVersion:    in.DigestVersion,
		Stage:            in.Stage,
		Stream:           in.Stream,
		Store:            in.Store,
		Revision:         in.Revision,
		CreatedAt:        in.CreatedAt,
		Deadline:         in.Deadline,
		ContentExpiresAt: in.ContentExpiresAt,
		ExpiresAt:        in.ExpiresAt,
		ReservedBytes:    in.ReservedBytes,
		Ciphertext:       bytes.Clone(in.Ciphertext),
		Snapshot:         bytes.Clone(in.Snapshot),
		ClaimEpoch:       in.ClaimEpoch,
		ClaimHash:        in.ClaimHash,
		ClaimUntil:       in.ClaimUntil,
		CancelRequested:  in.CancelRequested,
		DueAt:            in.DueAt,
		LastSequence:     in.LastSequence,
		StoredBytes:      in.StoredBytes,
		EventCount:       in.EventCount,
		Deleted:          in.Deleted,
		ReplayPending:    in.ReplayPending,
		UsageMetadata:    bs.UsageMetadata{NativeState: bg.RemoteState(in.NativeState), Usage: backgroundUsage(in.Usage)},
	}
}

func backgroundRow(in bs.Record) model.BackgroundJob {
	return model.BackgroundJob{
		BillingID: in.BillingID, BillingMode: in.BillingMode, ControlCalls: in.ControlCalls,
		ID:               in.ID,
		PublicID:         in.PublicID,
		UserID:           in.UserID,
		TokenID:          in.TokenID,
		Operation:        in.Operation,
		Model:            in.Model,
		KeyHash:          in.KeyHash,
		NamespaceHash:    in.NamespaceHash,
		RequestHash:      in.RequestHash,
		DigestVersion:    in.DigestVersion,
		Stage:            in.Stage,
		Stream:           in.Stream,
		Store:            in.Store,
		Revision:         in.Revision,
		CreatedAt:        in.CreatedAt,
		Deadline:         in.Deadline,
		ContentExpiresAt: in.ContentExpiresAt,
		ExpiresAt:        in.ExpiresAt,
		ReservedBytes:    in.ReservedBytes,
		Ciphertext:       bytes.Clone(in.Ciphertext),
		Snapshot:         bytes.Clone(in.Snapshot),
		ClaimEpoch:       in.ClaimEpoch,
		ClaimHash:        in.ClaimHash,
		ClaimUntil:       in.ClaimUntil,
		CancelRequested:  in.CancelRequested,
		DueAt:            in.DueAt,
		LastSequence:     in.LastSequence,
		StoredBytes:      in.StoredBytes,
		EventCount:       in.EventCount,
		Deleted:          in.Deleted,
		ReplayPending:    in.ReplayPending,
		NativeState:      string(in.UsageMetadata.NativeState),
		Usage:            backgroundUsageRow(in.UsageMetadata.Usage),
	}
}
