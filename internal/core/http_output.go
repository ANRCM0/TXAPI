package core

import (
	"context"
	"net/http"
	"time"
)

// HTTPOutput adapts encoded output to HTTP without owning the response writer.
type HTTPOutput struct {
	writer http.ResponseWriter
	state  OutputState
}

func NewHTTPOutput(w http.ResponseWriter) *HTTPOutput { return &HTTPOutput{writer: w} }

func (o *HTTPOutput) Commit(_ context.Context, metadata ResponseMetadata) error {
	if o.state.Committed {
		return nil
	}
	for name, value := range metadata.Values {
		o.writer.Header().Set(name, value)
	}
	o.writer.Header().Set("Content-Type", metadata.ContentType)
	if metadata.ContentType == "text/event-stream" {
		o.writer.Header().Set("Cache-Control", "no-cache")
		o.writer.Header().Set("X-Accel-Buffering", "no")
	}
	o.state = OutputState{Committed: true, HTTPStatus: metadata.StatusCode}
	o.writer.WriteHeader(metadata.StatusCode)
	return nil
}

func (o *HTTPOutput) Write(_ context.Context, data []byte) (int, error) {
	return o.writer.Write(data)
}

func (o *HTTPOutput) Flush(context.Context) error {
	return http.NewResponseController(o.writer).Flush()
}

func (o *HTTPOutput) State() OutputState { return o.state }

func (o *HTTPOutput) SetWriteDeadline(deadline time.Time) error {
	return http.NewResponseController(o.writer).SetWriteDeadline(deadline)
}
