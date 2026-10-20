package responsehistory

import "encoding/json"

// BackgroundCompiler constructs bounded, self-contained background input from
// history already authorized by its owner. It performs no I/O or publication;
// callers separately bind the target and persist the complete private envelope.
type BackgroundCompiler interface {
	CompileBackground(json.RawMessage, []json.RawMessage) (json.RawMessage, error)
}
