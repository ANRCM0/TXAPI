package core

import "context"

// ResponseMetadata describes an encoded response, not a protocol event model.
// Values carries application metadata such as retry and billing identifiers.
type ResponseMetadata struct {
	StatusCode  int
	ContentType string
	Values      map[string]string
}

type OutputState struct {
	Committed bool
	// HTTPStatus is zero for outputs that do not send an HTTP response.
	HTTPStatus int
}

// Output belongs to one execution and is called synchronously. Implementations
// must bound buffering, honor cancellation and report irreversible commitment
// even when Commit or Write fails. State is monotonic; Write never retains data.
// The caller owns the output lifetime; an attempt must not close it.
type Output interface {
	Commit(context.Context, ResponseMetadata) error
	Write(context.Context, []byte) (int, error)
	Flush(context.Context) error
	State() OutputState
}
