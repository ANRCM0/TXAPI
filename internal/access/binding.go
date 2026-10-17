package access

import "errors"

var ErrUnauthorized = errors.New("generation identity is no longer authorized")

// Binding is a non-secret owner/Key reference from an authenticated boundary or
// trusted task record. IDs supplied by a client are never proof of ownership.
type Binding struct {
	UserID  uint
	TokenID uint
}
