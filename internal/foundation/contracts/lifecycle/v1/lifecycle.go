// Package lifecycle describes ownership at the application composition boundary.
package lifecycle

import "context"

const Version = "1"

// Drainer stops admission and joins owned work. A context error does not transfer
// ownership or prove that shared dependencies can be closed; the owner must keep
// them alive until a subsequent Close succeeds. Close is safe to retry.
type Drainer interface {
	Close(context.Context) error
}
