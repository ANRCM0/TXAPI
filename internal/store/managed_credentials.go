package store

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	cfgv1 "minigateway/internal/foundation/contracts/configuration/v1"
	cfgv2 "minigateway/internal/foundation/contracts/configuration/v2"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	authv2 "minigateway/internal/foundation/contracts/upstreamauth/v2"
	"minigateway/internal/model"
)

const (
	managedSIWCSource      = "openai_siwc_v1"
	managedSIWCResource    = "https://api.openai.com/v1"
	managedChannelProtocol = "openai_siwc"
)

type managedChannels struct{ store *Store }

func (s *Store) ManagedChannels() cfgv2.Repository { return &managedChannels{store: s} }
func (s *Store) InferenceCredentials() authv2.InferenceCredentials {
	return &upstreamAccounts{store: s, now: time.Now}
}

func managedCredentialValid(c model.Credential) bool {
	return c.ID != 0 && c.ChannelID != 0 && c.Source == model.CredentialSourceManaged &&
		auth.ValidAccountID(c.AccountID) && c.BindingOwnerID != 0 && c.BindingTokenID != 0 &&
		managedSlugValid(c.BindingModel) && c.BindingRevision > 0 &&
		(c.Status == model.CredentialActive || c.Status == model.CredentialDisabled)
}

func managedSlugValid(value string) bool {
	return len(value) > 0 && len(value) <= 128 && strings.TrimSpace(value) == value &&
		auth.ValidWireText(value, 128) && !strings.ContainsAny(value, "* /\\?#")
}

func managedView(c model.Credential) *cfgv2.ManagedCredential {
	return &cfgv2.ManagedCredential{ChannelID: c.ChannelID, CredentialID: c.ID, Source: c.Source,
		Binding:  cfgv2.ManagedBindingInput{AccountID: c.AccountID, OwnerID: c.BindingOwnerID, TokenID: c.BindingTokenID, Model: c.BindingModel},
		Revision: c.BindingRevision, Enabled: c.Status == model.CredentialActive}
}

func managedFind(tx *gorm.DB, id uint) (model.Credential, error) {
	var c model.Credential
	if err := tx.First(&c, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c, cfgv2.ErrNotFound
		}
		return c, err
	}
	if c.Source != model.CredentialSourceManaged {
		return c, cfgv2.ErrConflict
	}
	if !managedCredentialValid(c) {
		return c, cfgv2.ErrConflict
	}
	return c, nil
}

// Static management cannot turn a managed grant into an ordinary key channel.
func staticChannelOnly(tx *gorm.DB, channelID uint) error {
	var count int64
	if err := tx.Model(&model.Credential{}).Where("channel_id = ? AND source = ?", channelID, model.CredentialSourceManaged).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return cfgv2.ErrConflict
	}
	return nil
}

func managedBindingValidate(tx *gorm.DB, in *cfgv2.ManagedBindingInput) error {
	if in == nil || !auth.ValidAccountID(in.AccountID) || in.OwnerID == 0 || in.TokenID == 0 || !managedSlugValid(in.Model) {
		return auth.ErrInvalid
	}
	var account model.ManagedUpstreamAccount
	if err := tx.Where("id = ?", in.AccountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return cfgv2.ErrNotFound
		}
		return err
	}
	if account.Provider != "openai" || account.AuthSource != managedSIWCSource || account.Resource != managedSIWCResource {
		return cfgv2.ErrConflict
	}
	view, err := resolveTokenView(tx.Where("id = ? AND user_id = ?", in.TokenID, in.OwnerID))
	if err != nil || !view.Policy.Allows(in.Model) {
		return cfgv2.ErrConflict
	}
	return nil
}

func (r *managedChannels) write(ctx context.Context, fn func(*gorm.DB) error) error {
	var semantic error
	err := (&upstreamAccounts{store: r.store, now: time.Now}).write(ctx, true, func(tx *gorm.DB) error {
		err := fn(tx)
		// Unlike refresh recovery, no managed binding mutation is committed on
		// an error. Keep semantic errors out of the upstream fixed-error filter.
		if errors.Is(err, cfgv2.ErrConflict) || errors.Is(err, cfgv2.ErrNotFound) || errors.Is(err, auth.ErrInvalid) {
			semantic = err
			return err
		}
		return err
	})
	if semantic != nil {
		return semantic
	}
	return err
}

func (r *managedChannels) CreateManagedChannel(ctx context.Context, in *cfgv2.ManagedChannelInput) (*cfgv2.ManagedChannel, error) {
	if in == nil {
		return nil, auth.ErrInvalid
	}
	chIn := ChannelInput{Name: in.Channel.Name, Type: in.Channel.Type, BaseURL: in.Channel.BaseURL, Models: append([]string(nil), in.Channel.Models...),
		ModelMapping: in.Channel.ModelMapping, GroupName: in.Channel.GroupName, Priority: in.Channel.Priority, Weight: in.Channel.Weight, Status: in.Channel.Status}
	if in.Channel.APIKey != "" || len(chIn.Models) != 1 || chIn.Models[0] != in.Binding.Model || len(chIn.ModelMapping) != 0 ||
		chIn.Type != managedChannelProtocol || chIn.BaseURL != managedSIWCResource || chIn.validate() != nil {
		return nil, auth.ErrInvalid
	}
	ch := chIn.toModel()
	var cred model.Credential
	err := r.write(ctx, func(tx *gorm.DB) error {
		if err := managedBindingValidate(tx, &in.Binding); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.Credential{}).Where("source = ? AND status = ?", model.CredentialSourceManaged, model.CredentialActive).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return cfgv2.ErrConflict
		}
		if err := tx.Omit("Credentials").Create(ch).Error; err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") || errors.Is(err, gorm.ErrDuplicatedKey) {
				return cfgv2.ErrConflict
			}
			return err
		}
		cred = model.Credential{ChannelID: ch.ID, Name: "managed", Source: model.CredentialSourceManaged, AccountID: in.Binding.AccountID,
			BindingOwnerID: in.Binding.OwnerID, BindingTokenID: in.Binding.TokenID, BindingModel: in.Binding.Model, BindingRevision: 1, Weight: 1, Status: model.CredentialActive}
		if err := tx.Create(&cred).Error; err != nil {
			return err
		}
		return syncAbilities(tx, ch)
	})
	if err != nil {
		return nil, err
	}
	out := &cfgv2.ManagedChannel{Channel: cfgv1.Channel{ID: ch.ID, Name: ch.Name, Type: ch.Type, BaseURL: ch.BaseURL, Models: []string(ch.Models),
		ModelMapping: map[string]string(ch.ModelMapping), GroupName: ch.GroupName, Priority: ch.Priority, Weight: ch.Weight, Status: ch.Status, CreatedAt: ch.CreatedAt, UpdatedAt: ch.UpdatedAt}, Credential: *managedView(cred)}
	if err := r.store.refreshSavedConfiguration(); err != nil {
		return out, &cfgv1.SavedError{Cause: err}
	}
	return out, nil
}

func (r *managedChannels) GetManagedCredential(ctx context.Context, id uint) (*cfgv2.ManagedCredential, error) {
	if id == 0 {
		return nil, auth.ErrInvalid
	}
	c, err := managedFind(r.store.db.WithContext(ctx), id)
	if err != nil {
		return nil, err
	}
	return managedView(c), nil
}

func (r *managedChannels) UpdateManagedCredential(ctx context.Context, id uint, revision int64, in *cfgv2.ManagedBindingInput) (*cfgv2.ManagedCredential, error) {
	return r.change(ctx, id, revision, in, false)
}
func (r *managedChannels) DisableManagedCredential(ctx context.Context, id uint, revision int64) (*cfgv2.ManagedCredential, error) {
	return r.change(ctx, id, revision, nil, true)
}
func (r *managedChannels) change(ctx context.Context, id uint, revision int64, in *cfgv2.ManagedBindingInput, disable bool) (*cfgv2.ManagedCredential, error) {
	if id == 0 || revision <= 0 || revision == math.MaxInt64 {
		return nil, auth.ErrInvalid
	}
	var cred model.Credential
	err := r.write(ctx, func(tx *gorm.DB) error {
		var err error
		cred, err = managedFind(tx, id)
		if err != nil {
			return err
		}
		if cred.BindingRevision != revision {
			return cfgv2.ErrConflict
		}
		if !disable {
			if err := managedBindingValidate(tx, in); err != nil {
				return err
			}
			var count int64
			if err := tx.Model(&model.Credential{}).Where("source = ? AND status = ? AND id <> ?", model.CredentialSourceManaged, model.CredentialActive, id).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return cfgv2.ErrConflict
			}
			cred.AccountID, cred.BindingOwnerID, cred.BindingTokenID, cred.BindingModel = in.AccountID, in.OwnerID, in.TokenID, in.Model
			cred.Status = model.CredentialActive
			var ch model.Channel
			if err := tx.First(&ch, cred.ChannelID).Error; err != nil {
				return cfgv2.ErrNotFound
			}
			ch.Models = model.StringList{in.Model}
			if err := tx.Model(&ch).Update("models", ch.Models).Error; err != nil {
				return err
			}
			if err := syncAbilities(tx, &ch); err != nil {
				return err
			}
		} else {
			cred.Status = model.CredentialDisabled
		}
		cred.BindingRevision++
		changed := tx.Model(&model.Credential{}).Where("id = ? AND binding_revision = ?", id, revision).Updates(map[string]any{
			"account_id": cred.AccountID, "binding_owner_id": cred.BindingOwnerID, "binding_token_id": cred.BindingTokenID, "binding_model": cred.BindingModel, "binding_revision": cred.BindingRevision, "status": cred.Status})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return cfgv2.ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := managedView(cred)
	if err := r.store.refreshSavedConfiguration(); err != nil {
		return out, &cfgv1.SavedError{Cause: err}
	}
	return out, nil
}

func (r *upstreamAccounts) AcquireAccess(ctx context.Context, in authv2.AccessRequest) (out authv2.AccessCredential, err error) {
	if in.CredentialID == 0 || in.ExpectedBindingRevision <= 0 || !auth.ValidAccountID(in.AccountID) || in.ExpectedRevision <= 0 || in.ExpectedCredentialRevision <= 0 || in.OwnerID == 0 || in.TokenID == 0 || !managedSlugValid(in.Model) {
		return out, auth.ErrInvalid
	}
	err = r.write(ctx, true, func(tx *gorm.DB) error {
		var c model.Credential
		if e := tx.First(&c, in.CredentialID).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return auth.ErrNotFound
			}
			return e
		}
		if !managedCredentialValid(c) || c.Status != model.CredentialActive || c.BindingRevision != in.ExpectedBindingRevision || c.AccountID != in.AccountID || c.BindingOwnerID != in.OwnerID || c.BindingTokenID != in.TokenID || c.BindingModel != in.Model || c.APIKey != "" || len(c.KeyCiphertext) != 0 || c.KeyHash != "" {
			return auth.ErrConflict
		}
		var ch model.Channel
		if e := tx.First(&ch, c.ChannelID).Error; e != nil {
			return auth.ErrNotFound
		}
		if !ch.IsEnabled() || ch.Type != managedChannelProtocol || ch.BaseURL != managedSIWCResource || len(ch.Models) != 1 || ch.Models[0] != in.Model || len(ch.ModelMapping) != 0 {
			return auth.ErrConflict
		}
		view, e := resolveTokenView(tx.Where("id = ? AND user_id = ?", in.TokenID, in.OwnerID))
		if e != nil || !view.Policy.Allows(in.Model) {
			return auth.ErrConflict
		}
		row, e := upstreamFind(tx, in.AccountID)
		if e != nil {
			return e
		}
		if row.Provider != "openai" || row.Issuer != upstreamIssuer || row.AuthSource != managedSIWCSource || row.Resource != managedSIWCResource {
			return auth.ErrConflict
		}
		if e := upstreamStateError(row); e != nil {
			return e
		}
		if row.RowRevision != in.ExpectedRevision || row.CredentialRevision != in.ExpectedCredentialRevision {
			return auth.ErrConflict
		}
		if row.ClaimHash != "" || row.RefreshStarted {
			return auth.ErrBusy
		}
		if !row.ExpiresAt.After(r.timestamp()) {
			return auth.ErrReauthorize
		}
		scopes, e := upstreamScopes(row.ScopesKnown, row.ScopesJSON)
		if e != nil || !row.ScopesKnown || !slices.Contains(scopes, "resource.invoke") || !slices.Contains(scopes, "chatgpt.tokens.use.direct") {
			return auth.ErrConflict
		}
		tokens, e := r.store.openUpstreamAccount(tx, row)
		if e != nil {
			return e
		}
		out = authv2.AccessCredential{AccountID: row.ID, Revision: row.RowRevision, CredentialRevision: row.CredentialRevision, AccessToken: tokens.AccessToken, ExpiresAt: tokens.ExpiresAt}
		return nil
	})
	if err != nil {
		out = authv2.AccessCredential{}
	}
	return
}

var _ cfgv2.Repository = (*managedChannels)(nil)
var _ authv2.InferenceCredentials = (*upstreamAccounts)(nil)
