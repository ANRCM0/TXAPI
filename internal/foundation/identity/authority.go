package identity

import (
	"context"
	"errors"

	"minigateway/internal/access"
	contract "minigateway/internal/foundation/contracts/identity/v1"
)

type Authority struct {
	resolve func(context.Context, access.Binding) (access.Principal, error)
}

func New(resolve func(context.Context, access.Binding) (access.Principal, error)) *Authority {
	return &Authority{resolve: resolve}
}

func (a *Authority) Current(ctx context.Context) (contract.Principal, bool) {
	return access.PrincipalFromContext(ctx)
}

func (a *Authority) Refresh(ctx context.Context, binding contract.Binding) (updated context.Context, principal contract.Principal, err error) {
	updated = ctx
	defer func() {
		if recover() != nil {
			principal, err = contract.Principal{}, contract.ErrUnavailable
		}
	}()
	previous, ok := access.PrincipalFromContext(ctx)
	if !ok || binding.UserID == 0 || binding.TokenID == 0 || previous.UserID != binding.UserID || previous.TokenID != binding.TokenID {
		return ctx, contract.Principal{}, contract.ErrUnauthorized
	}
	if a == nil || a.resolve == nil {
		return ctx, contract.Principal{}, contract.ErrUnavailable
	}
	p, err := a.resolve(ctx, binding)
	if errors.Is(err, contract.ErrUnauthorized) {
		return ctx, contract.Principal{}, contract.ErrUnauthorized
	}
	if err != nil || p.UserID != binding.UserID || p.TokenID != binding.TokenID || p.Policy.Validate() != nil || p.RPMLimit < 0 || p.TPMLimit < 0 || p.UserRPMLimit < 0 || p.UserConcurrencyLimit < 0 || p.KeyConcurrencyLimit < 0 {
		return ctx, contract.Principal{}, contract.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return ctx, contract.Principal{}, err
	}
	p.Policy.Models = append([]string(nil), p.Policy.Models...)
	return access.WithPrincipal(ctx, p), p, nil
}
