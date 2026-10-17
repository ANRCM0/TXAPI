package generation

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"sync"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/core"
	"minigateway/internal/gateway"
)

// Foreground persists encoded output before delivering it to its one observer.
// The caller owns execution, cancellation and Finish; no worker is started.
type Foreground struct {
	mu       sync.Mutex
	record   *recordOutput
	target   core.Output
	failed   bool
	finished bool
}

func (a *Archive) BeginForeground(ctx context.Context, binding access.Binding, model string, target core.Output) (foreground *Foreground, err error) {
	defer func() {
		if recover() != nil {
			foreground, err = nil, ErrArchiveUnavailable
		}
	}()
	if target == nil || model == "" {
		return nil, ErrArchiveUnavailable
	}
	v := reflect.ValueOf(target)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		if v.IsNil() {
			return nil, ErrArchiveUnavailable
		}
	}
	if target.State().Committed {
		return nil, ErrArchiveUnavailable
	}
	principal, err := a.principal(ctx, binding)
	if err != nil {
		return nil, err
	}
	if !principal.Policy.Allows(model) {
		return nil, access.ErrUnauthorized
	}
	record, err := a.start(ctx, binding)
	if err != nil {
		return nil, err
	}
	record.model = model
	return &Foreground{record: record, target: target}, nil
}

func (f *Foreground) ID() string { return f.record.record.ID }

func (f *Foreground) invalidate() {
	f.failed = true
	f.record.mu.Lock()
	f.record.failed = true
	f.record.mu.Unlock()
}

func (f *Foreground) Commit(ctx context.Context, metadata core.ResponseMetadata) (err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() {
		if recover() != nil {
			f.invalidate()
			err = ErrArchiveUnavailable
		}
	}()
	if f.failed || f.finished {
		return ErrArchiveUnavailable
	}
	if err = f.record.Commit(ctx, metadata); err != nil {
		f.invalidate()
		return err
	}
	if err = f.target.Commit(ctx, metadata); err != nil {
		f.invalidate()
		return err
	}
	if !f.target.State().Committed {
		f.invalidate()
		return ErrArchiveUnavailable
	}
	return nil
}

func (f *Foreground) Write(ctx context.Context, data []byte) (n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() {
		if recover() != nil {
			f.invalidate()
			n, err = 0, ErrArchiveUnavailable
		}
	}()
	if f.failed || f.finished {
		return 0, ErrArchiveUnavailable
	}
	if _, err = f.record.Write(ctx, data); err != nil {
		f.invalidate()
		return 0, err
	}
	n, err = f.target.Write(ctx, data)
	if n < 0 || n > len(data) {
		n, err = 0, io.ErrShortWrite
	}
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		f.invalidate()
	}
	return n, err
}

func (f *Foreground) Flush(ctx context.Context) (err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() {
		if recover() != nil {
			f.invalidate()
			err = ErrArchiveUnavailable
		}
	}()
	if f.failed || f.finished {
		return ErrArchiveUnavailable
	}
	if err = f.record.Flush(ctx); err != nil {
		f.invalidate()
		return err
	}
	if err = f.target.Flush(ctx); err != nil {
		f.invalidate()
	}
	return err
}

func (f *Foreground) State() (state core.OutputState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() {
		if recover() != nil {
			f.invalidate()
			state.Committed = true
		}
	}()
	state = f.target.State()
	state.Committed = state.Committed || f.record.State().Committed
	return state
}

func (f *Foreground) SetWriteDeadline(deadline time.Time) error {
	if target, ok := f.target.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return target.SetWriteDeadline(deadline)
	}
	return http.ErrNotSupported
}

// A complete observed response may include a native failed terminal state.
// Storage/output failures never acquire a sealed replayable record.
func (f *Foreground) Finish(result gateway.RequestResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed || f.finished || result.Termination == core.ClientWriteError || result.Phase != core.NotDispatched && !result.ProtocolComplete {
		f.invalidate()
		return ErrArchiveIncomplete
	}
	f.finished = true
	if err := f.record.finish(result, nil); err != nil {
		f.invalidate()
		return err
	}
	return nil
}

var _ core.Output = (*Foreground)(nil)
