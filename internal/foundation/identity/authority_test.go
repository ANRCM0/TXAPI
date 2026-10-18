package identity_test

import (
	"context"
	"errors"
	"testing"

	"minigateway/internal/access"
	contract "minigateway/internal/foundation/contracts/identity/v1"
	"minigateway/internal/foundation/identity"
)

func TestAuthorityRequiresAuthenticatedBindingAndRefreshesCurrentPolicy(t *testing.T) {
	calls := 0
	current := access.Principal{UserID: 4, TokenID: 7, Policy: access.Policy{Mode: access.Allowlist, Models: []string{"allowed"}}}
	authority := identity.New(func(context.Context, access.Binding) (access.Principal, error) { calls++; return current, nil })
	binding := contract.Binding{UserID: 4, TokenID: 7}
	if _, _, err := authority.Refresh(context.Background(), binding); !errors.Is(err, contract.ErrUnauthorized) || calls != 0 {
		t.Fatalf("untrusted binding was resolved: calls=%d err=%v", calls, err)
	}
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 4, TokenID: 7, Policy: access.Policy{Mode: access.All}})
	if _, _, err := authority.Refresh(ctx, contract.Binding{UserID: 4, TokenID: 8}); !errors.Is(err, contract.ErrUnauthorized) || calls != 0 {
		t.Fatalf("binding override: %v", err)
	}
	refreshed, projection, err := authority.Refresh(ctx, binding)
	if err != nil || projection.Policy.Allows("other") {
		t.Fatalf("old policy remained authoritative: %+v %v", projection, err)
	}
	projection.UserID = 999
	projection.Policy.Models[0] = "other"
	if current.Policy.Models[0] != "allowed" {
		t.Fatal("public projection mutated the authority source")
	}
	trusted, ok := access.PrincipalFromContext(refreshed)
	if !ok || trusted.UserID != 4 || !trusted.Policy.Allows("allowed") || trusted.Policy.Allows("other") {
		t.Fatalf("mutable projection changed authentication: %+v", trusted)
	}
	current.Policy = access.Policy{Mode: access.Allowlist, Models: []string{}}
	_, projection, err = authority.Refresh(refreshed, binding)
	if err != nil || projection.Policy.Allows("allowed") {
		t.Fatalf("current revocation was ignored: %+v %v", projection, err)
	}
}

func TestAuthorityRefusesRevocationPanicAndUnavailableState(t *testing.T) {
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 2, Policy: access.Policy{Mode: access.All}})
	for _, tc := range []struct {
		name     string
		resolve  func(context.Context, access.Binding) (access.Principal, error)
		expected error
	}{
		{"revoked", func(context.Context, access.Binding) (access.Principal, error) {
			return access.Principal{}, access.ErrUnauthorized
		}, contract.ErrUnauthorized},
		{"panic", func(context.Context, access.Binding) (access.Principal, error) { panic("fixture") }, contract.ErrUnavailable},
		{"wrong-owner", func(context.Context, access.Binding) (access.Principal, error) {
			return access.Principal{UserID: 9, TokenID: 2, Policy: access.Policy{Mode: access.All}}, nil
		}, contract.ErrUnavailable},
		{"missing", nil, contract.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := identity.New(tc.resolve).Refresh(ctx, contract.Binding{UserID: 1, TokenID: 2})
			if !errors.Is(err, tc.expected) {
				t.Fatalf("%v", err)
			}
		})
	}
}
