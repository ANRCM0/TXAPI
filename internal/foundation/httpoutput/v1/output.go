package httpoutput

import (
	"context"
	"maps"
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	"net/http"
	"time"
)

const Version = "1"

type output struct{ target *core.HTTPOutput }

func New(w http.ResponseWriter) execution.DeadlineOutput {
	return &output{target: core.NewHTTPOutput(w)}
}
func (o *output) Commit(ctx context.Context, m execution.ResponseMetadata) error {
	return o.target.Commit(ctx, core.ResponseMetadata{StatusCode: m.StatusCode, ContentType: m.ContentType, Values: maps.Clone(m.Values)})
}
func (o *output) Write(ctx context.Context, b []byte) (int, error) { return o.target.Write(ctx, b) }
func (o *output) Flush(ctx context.Context) error                  { return o.target.Flush(ctx) }
func (o *output) State() execution.OutputState {
	s := o.target.State()
	return execution.OutputState{Committed: s.Committed, HTTPStatus: s.HTTPStatus}
}
func (o *output) SetWriteDeadline(t time.Time) error { return o.target.SetWriteDeadline(t) }
