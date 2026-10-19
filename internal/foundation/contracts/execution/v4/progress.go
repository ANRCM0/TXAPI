// Package execution defines optional per-stream progress facts approved by
// FC-P0-HTTP-001. Registration does not freeze this candidate contract.
package execution

const Version = "4"

// Progress is a monotonic snapshot of validated, complete upstream events.
// EffectiveOutput means actual nonempty supported text/tool output has occurred.
// Role, IDs, usage, heartbeat and a finish reason alone do not set it.
type Progress struct {
	EffectiveOutput bool
	CompleteEvents  uint64
}

// StreamProgress is optionally implemented by the existing stream decoder.
// It performs no I/O and has no authority over execution, admission or billing.
type StreamProgress interface{ Progress() Progress }
