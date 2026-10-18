package access

import identity "minigateway/internal/foundation/contracts/identity/v1"

var ErrUnauthorized = identity.ErrUnauthorized

// Binding is an identity reference, never authentication proof.
type Binding = identity.Binding
