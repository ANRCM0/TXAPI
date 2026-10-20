package responsehistory

import "encoding/json"

// BackgroundCompiler constructs bounded, self-contained background input from
// history already authorized by its owner. It performs no I/O or publication;
// callers separately bind the target and persist the complete private envelope.
type BackgroundCompiler interface {
	CompileBackground(json.RawMessage, []json.RawMessage) (json.RawMessage, error)
}

// BackgroundHistoryProjector validates the bounded text/function history subset.
// It does not authorize, dispatch, encrypt, or publish a response.
type BackgroundHistoryProjector interface {
	PrepareBackgroundHistory(json.RawMessage, string) (BackgroundHistoryInput, error)
	ProjectBackgroundHistory(json.RawMessage, json.RawMessage, string, string, string) (BackgroundHistoryResult, error)
}

type BackgroundHistoryInput struct {
	Request json.RawMessage
	Input   []json.RawMessage
}

type BackgroundHistoryResult struct {
	Request  json.RawMessage
	Input    []json.RawMessage
	Items    []json.RawMessage
	Response json.RawMessage
}
