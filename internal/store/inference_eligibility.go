package store

import (
	"context"
	"errors"
	"math"
	"time"

	"gorm.io/gorm"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	authv2 "minigateway/internal/foundation/contracts/upstreamauth/v2"
	authv3 "minigateway/internal/foundation/contracts/upstreamauth/v3"
	"minigateway/internal/model"
)

const inferenceEligibilityCapacity = 1000

func (s *Store) InferenceEligibility() authv3.EligibilityRepository {
	return &upstreamAccounts{store: s, now: time.Now}
}

func newEligibility(c model.Credential) model.InferenceEligibility {
	return model.InferenceEligibility{CredentialID: c.ID, AccountID: c.AccountID, BindingRevision: c.BindingRevision, Model: c.BindingModel, GateEpoch: 1, Revision: 1}
}

func createEligibility(tx *gorm.DB, c model.Credential) error {
	var count int64
	if err := tx.Model(&model.InferenceEligibility{}).Count(&count).Error; err != nil {
		return err
	}
	if count >= inferenceEligibilityCapacity {
		return authv3.ErrCapacity
	}
	row := newEligibility(c)
	return tx.Omit("Credential").Create(&row).Error
}

func validAccessRequest(in authv2.AccessRequest) bool {
	return in.CredentialID != 0 && in.ExpectedBindingRevision > 0 && auth.ValidAccountID(in.AccountID) && in.ExpectedRevision > 0 && in.ExpectedCredentialRevision > 0 && in.OwnerID != 0 && in.TokenID != 0 && managedSlugValid(in.Model)
}

func validFailureSignal(s authv3.FailureSignal) bool {
	switch s.Source {
	case authv3.ModelsHTTP, authv3.ResponsesHTTP, authv3.ResponsesEvent:
	default:
		return false
	}
	if s.Source == authv3.ResponsesEvent && s.Reason != authv3.UsageLimited {
		return false
	}
	switch s.Reason {
	case authv3.AuthorizationReview:
		return s.Scope == authv3.Grant && (s.HTTPStatus == 401 || s.HTTPStatus == 403) && s.ProviderCode == ""
	case authv3.UsageLimited:
		return s.Scope == authv3.Grant && s.HTTPStatus == 429 && s.ProviderCode == "subscription_sharing_usage_limit_exceeded"
	case authv3.RateLimitUnknown:
		return s.Scope == authv3.Model && s.HTTPStatus == 429 && s.ProviderCode == ""
	}
	return false
}

func eligibilityValid(g model.InferenceEligibility, c model.Credential) bool {
	if g.CredentialID != c.ID || g.AccountID != c.AccountID || g.BindingRevision != c.BindingRevision || g.Model != c.BindingModel || g.GateEpoch <= 0 || g.Revision <= 0 || g.ResetKnown || g.ResetAt != nil {
		return false
	}
	if !g.Blocked {
		return g.Reason == "" && g.Scope == "" && g.ProviderCode == "" && g.HTTPStatus == 0 && g.Source == "" && g.ObservedAt == nil && g.EvidenceAccountRevision == 0 && g.EvidenceCredentialRevision == 0
	}
	return g.ObservedAt != nil && upstreamTimeValid(*g.ObservedAt) && g.EvidenceAccountRevision > 0 && g.EvidenceCredentialRevision > 0 && validFailureSignal(authv3.FailureSignal{Reason: authv3.Reason(g.Reason), Scope: authv3.Scope(g.Scope), ProviderCode: g.ProviderCode, HTTPStatus: g.HTTPStatus, Source: authv3.Source(g.Source)})
}

func findEligibility(tx *gorm.DB, c model.Credential) (model.InferenceEligibility, error) {
	var gate model.InferenceEligibility
	var count int64
	if err := tx.Model(&model.InferenceEligibility{}).Count(&count).Error; err != nil {
		return gate, err
	}
	if count > inferenceEligibilityCapacity {
		return gate, authv3.ErrCapacity
	}
	if err := tx.First(&gate, c.ID).Error; err != nil {
		return gate, auth.ErrUnavailable
	}
	if !eligibilityValid(gate, c) {
		return gate, auth.ErrUnavailable
	}
	return gate, nil
}

func eligibilityView(g model.InferenceEligibility) authv3.Eligibility {
	return authv3.Eligibility{CredentialID: g.CredentialID, AccountID: g.AccountID, BindingRevision: g.BindingRevision, Model: g.Model, GateEpoch: g.GateEpoch, Revision: g.Revision, Blocked: g.Blocked,
		Reason: authv3.Reason(g.Reason), Scope: authv3.Scope(g.Scope), ProviderCode: g.ProviderCode, HTTPStatus: g.HTTPStatus, Source: authv3.Source(g.Source), ObservedAt: g.ObservedAt,
		EvidenceAccountRevision: g.EvidenceAccountRevision, EvidenceCredentialRevision: g.EvidenceCredentialRevision, ResetKnown: g.ResetKnown, ResetAt: g.ResetAt}
}

func eligibilityCredential(tx *gorm.DB, id uint) (model.Credential, error) {
	var c model.Credential
	if err := tx.First(&c, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c, auth.ErrNotFound
		}
		return c, err
	}
	if !managedCredentialValid(c) {
		return c, auth.ErrConflict
	}
	return c, nil
}

func saveEligibility(tx *gorm.DB, old, row model.InferenceEligibility) error {
	result := tx.Model(&model.InferenceEligibility{}).Where("credential_id = ? AND gate_epoch = ? AND revision = ?", old.CredentialID, old.GateEpoch, old.Revision).Select("*").Omit("Credential").Updates(&row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return auth.ErrConflict
	}
	return nil
}

func rebindEligibility(tx *gorm.DB, oldCredential, newCredential model.Credential) error {
	gate, err := findEligibility(tx, oldCredential)
	if err != nil {
		return err
	}
	if gate.GateEpoch == math.MaxInt64 || gate.Revision == math.MaxInt64 {
		return auth.ErrConflict
	}
	changed := gate
	if oldCredential.AccountID != newCredential.AccountID || oldCredential.BindingModel != newCredential.BindingModel {
		changed = newEligibility(newCredential)
	}
	changed.GateEpoch, changed.Revision = gate.GateEpoch+1, gate.Revision+1
	changed.BindingRevision = newCredential.BindingRevision
	return saveEligibility(tx, gate, changed)
}

func (r *upstreamAccounts) ReadEligibility(ctx context.Context, id uint) (out authv3.Eligibility, err error) {
	if id == 0 {
		return out, auth.ErrInvalid
	}
	err = r.read(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			c, e := eligibilityCredential(tx, id)
			if e != nil {
				return e
			}
			gate, e := findEligibility(tx, c)
			if e != nil {
				return e
			}
			out = eligibilityView(gate)
			return nil
		})
	})
	if err != nil {
		out = authv3.Eligibility{}
	}
	return
}

func (r *upstreamAccounts) ResumeEligibility(ctx context.Context, in authv3.ResumeRequest) (out authv3.Eligibility, err error) {
	if in.CredentialID == 0 || in.ExpectedBindingRevision <= 0 || in.ExpectedGateRevision <= 0 || in.ExpectedGateEpoch <= 0 {
		return out, auth.ErrInvalid
	}
	err = r.write(ctx, true, func(tx *gorm.DB) error {
		c, e := eligibilityCredential(tx, in.CredentialID)
		if e != nil {
			return e
		}
		gate, e := findEligibility(tx, c)
		if e != nil {
			return e
		}
		if c.BindingRevision != in.ExpectedBindingRevision || gate.Revision != in.ExpectedGateRevision || gate.GateEpoch != in.ExpectedGateEpoch || gate.GateEpoch == math.MaxInt64 || gate.Revision == math.MaxInt64 {
			return auth.ErrConflict
		}
		changed := newEligibility(c)
		changed.GateEpoch, changed.Revision = gate.GateEpoch+1, gate.Revision+1
		if e = saveEligibility(tx, gate, changed); e != nil {
			return e
		}
		out = eligibilityView(changed)
		return nil
	})
	if err != nil {
		out = authv3.Eligibility{}
	}
	return
}

func (r *upstreamAccounts) RecordFailure(ctx context.Context, in authv3.FailureWrite) (out authv3.RecordResult, err error) {
	if !validAccessRequest(in.Issued) || in.GateEpoch <= 0 || !validFailureSignal(in.Signal) {
		return out, auth.ErrInvalid
	}
	err = r.write(ctx, true, func(tx *gorm.DB) error {
		c, e := eligibilityCredential(tx, in.Issued.CredentialID)
		if errors.Is(e, auth.ErrNotFound) || errors.Is(e, auth.ErrConflict) {
			return nil
		}
		if e != nil {
			return e
		}
		gate, e := findEligibility(tx, c)
		if e != nil {
			return e
		}
		out.Eligibility = eligibilityView(gate)
		issued := in.Issued
		if c.Status != model.CredentialActive || c.BindingRevision != issued.ExpectedBindingRevision || c.AccountID != issued.AccountID || c.BindingOwnerID != issued.OwnerID || c.BindingTokenID != issued.TokenID || c.BindingModel != issued.Model || gate.GateEpoch != in.GateEpoch {
			return nil
		}
		account, e := upstreamFind(tx, issued.AccountID)
		if errors.Is(e, auth.ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if account.RowRevision != issued.ExpectedRevision || account.CredentialRevision != issued.ExpectedCredentialRevision || account.State != string(auth.Active) || account.ClaimHash != "" || account.RefreshStarted {
			return nil
		}
		if gate.Blocked {
			return nil
		}
		if gate.Revision == math.MaxInt64 {
			return auth.ErrConflict
		}
		changed := gate
		changed.Revision++
		changed.Blocked = true
		changed.Reason, changed.Scope = string(in.Signal.Reason), string(in.Signal.Scope)
		changed.ProviderCode = in.Signal.ProviderCode
		changed.HTTPStatus = in.Signal.HTTPStatus
		changed.Source = string(in.Signal.Source)
		now := r.timestamp()
		changed.ObservedAt = &now
		changed.EvidenceAccountRevision = issued.ExpectedRevision
		changed.EvidenceCredentialRevision = issued.ExpectedCredentialRevision
		if e = saveEligibility(tx, gate, changed); e != nil {
			return e
		}
		out = authv3.RecordResult{Applied: true, Eligibility: eligibilityView(changed)}
		return nil
	})
	if err != nil {
		out = authv3.RecordResult{}
	}
	return
}

var _ authv3.EligibilityRepository = (*upstreamAccounts)(nil)
