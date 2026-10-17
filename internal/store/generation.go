package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	"minigateway/internal/model"
)

// ResolvePrincipal refreshes a trusted binding at dispatch, without retaining
// the original API Key or trusting a queued policy/limit snapshot.
func (s *Store) ResolvePrincipal(ctx context.Context, binding access.Binding) (access.Principal, error) {
	if binding.UserID == 0 || binding.TokenID == 0 {
		return access.Principal{}, access.ErrUnauthorized
	}
	view, err := resolveTokenView(s.db.WithContext(ctx).Where("id = ? AND user_id = ?", binding.TokenID, binding.UserID))
	if err != nil {
		return access.Principal{}, err
	}
	return view.Principal(), nil
}

// Both credential authentication and binding refresh read one consistent
// owner/Key/system-policy snapshot. No database handle escapes this boundary.
func resolveTokenView(query *gorm.DB) (model.TokenView, error) {
	var view model.TokenView
	err := query.Transaction(func(tx *gorm.DB) error {
		var token model.Token
		if err := tx.First(&token).Error; err != nil {
			return identityReadError(err)
		}
		if token.UserID == 0 || token.KeyHash == "" || !token.IsActive(time.Now()) {
			return access.ErrUnauthorized
		}
		// Reset the token selector before querying other tables in this transaction.
		db := tx.Session(&gorm.Session{NewDB: true})
		var user model.User
		if err := db.First(&user, token.UserID).Error; err != nil {
			return identityReadError(err)
		}
		if user.Status != model.UserStatusActive {
			return access.ErrUnauthorized
		}
		system, err := systemPolicy(db)
		if err != nil {
			return err
		}
		policy := access.Intersect(system, user.Policy(), token.Policy())
		view = token.View()
		view.Policy, view.Models = policy, policy.Models
		view.RPMLimit = access.MinLimit(user.RPMLimit, token.RPMLimit)
		view.TPMLimit = access.MinLimit(user.TPMLimit, token.TPMLimit)
		view.UserRPMLimit = user.RPMLimit
		view.UserConcurrencyLimit = user.ConcurrencyLimit
		view.KeyConcurrencyLimit = token.ConcurrencyLimit
		return nil
	})
	return view, err
}

func identityReadError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return access.ErrUnauthorized
	}
	return err
}
