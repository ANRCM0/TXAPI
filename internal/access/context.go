package access

import "context"

// Principal is the verified model-calling identity. Only the authentication
// boundary constructs it; HTTP parameters must never supply its IDs or Policy.
// Fields are snapshots for one request, never mutable cached user/Key objects.
type Principal struct {
	UserID    uint
	TokenID   uint
	TokenName string
	Policy    Policy
	RPMLimit  int
	TPMLimit  int
}

type principalKey struct{}
type requestIDKey struct{}

func clonePrincipal(p Principal) Principal {
	p.Policy.Models = append([]string(nil), p.Policy.Models...)
	return p
}
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, clonePrincipal(p))
}
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return clonePrincipal(p), ok
}
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}
func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestIDKey{}).(string); return id }
