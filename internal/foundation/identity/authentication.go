package identity

import (
	"context"
	"minigateway/internal/access"
	contract "minigateway/internal/foundation/contracts/identity/v1"
	"reflect"
)

type TokenLookup interface {
	LookupToken(string) (contract.TokenView, bool)
}
type tokenAuthenticator struct{ lookup TokenLookup }

func NewTokenAuthenticator(lookup TokenLookup) contract.Authenticator {
	if lookup == nil {
		return nil
	}
	v := reflect.ValueOf(lookup)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return nil
		}
	}
	return &tokenAuthenticator{lookup: lookup}
}
func (a *tokenAuthenticator) Authenticate(ctx context.Context, key string) (context.Context, contract.TokenView, bool) {
	view, ok := a.lookup.LookupToken(key)
	if !ok {
		return ctx, contract.TokenView{}, false
	}
	view.Models = append([]string(nil), view.Models...)
	view.Policy.Models = append([]string(nil), view.Policy.Models...)
	return access.WithPrincipal(ctx, view.Principal()), view, true
}
