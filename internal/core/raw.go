package core

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
)

// RawInput is one mechanical HTTP exchange without model-response decoding.
type RawInput struct {
	Method  string
	URL     string
	Header  http.Header
	Body    []byte
	Timeout time.Duration
}

type RawResult struct {
	Phase       Phase
	StatusCode  int
	Header      http.Header
	Body        []byte
	Complete    bool
	Termination Termination
}

// ExecuteRaw uses the existing executor/client owner. Status and bounded error
// bodies remain wire facts; the consumer decides their protocol meaning.
func (e *Executor) ExecuteRaw(parent context.Context, in RawInput) (result RawResult) {
	result.Phase, result.Termination = NotDispatched, UpstreamError
	if e == nil || e.client == nil || parent == nil {
		return
	}
	ctx := parent
	if in.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, in.Timeout)
		defer cancel()
	}
	canceled := func() {
		if parent.Err() == context.Canceled || ctx.Err() == context.Canceled {
			result.Termination = Canceled
		} else if ctx.Err() == context.DeadlineExceeded {
			result.Termination = Deadline
		}
	}
	if ctx.Err() != nil {
		canceled()
		return
	}
	// Do not give the transport a GetBody replay hook for non-idempotent calls.
	req, err := http.NewRequestWithContext(ctx, in.Method, in.URL, io.NopCloser(bytes.NewReader(in.Body)))
	if err != nil {
		return
	}
	req.ContentLength = int64(len(in.Body))
	if len(in.Body) == 0 {
		req.Body = http.NoBody
	}
	req.Header = in.Header.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	result.Phase = Dispatching
	response, err := e.client.Do(req)
	if err != nil {
		canceled()
		return
	}
	defer response.Body.Close()
	result.Phase, result.StatusCode = ResponseReceived, response.StatusCode
	result.Header = response.Header.Clone()
	body, err := readBounded(response.Body, e.maxResponseBytes)
	if err != nil {
		canceled()
		return
	}
	result.Body, result.Complete, result.Termination = body, true, Success
	return
}
