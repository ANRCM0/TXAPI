package store

import (
	"bytes"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

func backgroundSnapshot(in model.BackgroundJob) bs.Record {
	return bs.Record{
		ID:               in.ID,
		PublicID:         in.PublicID,
		UserID:           in.UserID,
		TokenID:          in.TokenID,
		Operation:        in.Operation,
		Model:            in.Model,
		KeyHash:          in.KeyHash,
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
	}
}

func backgroundRow(in bs.Record) model.BackgroundJob {
	return model.BackgroundJob{
		ID:               in.ID,
		PublicID:         in.PublicID,
		UserID:           in.UserID,
		TokenID:          in.TokenID,
		Operation:        in.Operation,
		Model:            in.Model,
		KeyHash:          in.KeyHash,
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
	}
}
