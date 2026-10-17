package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"time"

	"minigateway/internal/core"
)

type executionOutput struct {
	target       core.Output
	metadata     map[string]string
	capture      bytes.Buffer
	captureLimit int
}

func (o *executionOutput) setMetadata(name, value string) {
	if o.State().Committed {
		return
	}
	if o.metadata == nil {
		o.metadata = make(map[string]string)
	}
	o.metadata[name] = value
}

func (o *executionOutput) Commit(ctx context.Context, metadata core.ResponseMetadata) error {
	values := maps.Clone(metadata.Values)
	if values == nil {
		values = make(map[string]string)
	}
	for name, value := range o.metadata {
		values[name] = value
	}
	metadata.Values = values
	return o.target.Commit(ctx, metadata)
}

func (o *executionOutput) Write(ctx context.Context, data []byte) (int, error) {
	n, err := o.target.Write(ctx, data)
	if n < 0 || n > len(data) {
		return 0, errors.New("output returned an invalid write count")
	}
	if room := o.captureLimit - o.capture.Len(); room > 0 {
		if room > n {
			room = n
		}
		_, _ = o.capture.Write(data[:room])
	}
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (o *executionOutput) Flush(ctx context.Context) error { return o.target.Flush(ctx) }
func (o *executionOutput) State() core.OutputState         { return o.target.State() }

func (o *executionOutput) SetWriteDeadline(deadline time.Time) error {
	if target, ok := o.target.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return target.SetWriteDeadline(deadline)
	}
	return http.ErrNotSupported
}
