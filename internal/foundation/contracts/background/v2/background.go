// Package background adds detailed observations without changing frozen v1 ports.
package background

import (
	v1 "minigateway/internal/foundation/contracts/background/v1"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
)

const Version = "2"

// Observation describes one cumulative snapshot, never another generation.
// A nil Usage means that this observation supplies no detailed quantities.
type Observation struct {
	v1.Observation
	Usage *v3.UsageObservation
}

// Observer is optional on the Progress returned by an existing v1 Codec.
// DrainUsage and Drain consume the same observation; callers choose one.
type Observer interface{ DrainUsage() Observation }
