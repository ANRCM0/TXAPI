// Public execution bridges preserve the existing single coordinator.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"time"

	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
)

type PublicExecutor struct {
	gateway       *Gateway
	httpAdmission bool
}

func NewPublicExecutor(g *Gateway) *PublicExecutor { return &PublicExecutor{gateway: g} }

// NewHTTPExecutor is injected only into the HTTP compatibility entry by assembly.
func NewHTTPExecutor(g *Gateway) *PublicExecutor {
	return &PublicExecutor{gateway: g, httpAdmission: true}
}

func (e *PublicExecutor) Execute(ctx context.Context, output execution.Output, in execution.Invocation) execution.Outcome {
	return FromGateway(e.gateway.executeTo(ctx, ToCoreOutput(output), invocation(in), nil, e.httpAdmission))
}

// ExecuteClaimed is an assembly-only bridge pending the state contract migration.
func (e *PublicExecutor) ExecuteClaimed(ctx context.Context, output execution.Output, in execution.Invocation, claim model.GenerationClaim) execution.Outcome {
	return FromGateway(e.gateway.ExecuteClaimedTo(ctx, ToCoreOutput(output), invocation(in), claim))
}

func invocation(in execution.Invocation) Execution {
	var target *ResponseTarget
	if in.Target != nil {
		copied := *in.Target
		target = &copied
	}
	return Execution{Operation: in.Operation, Body: bytes.Clone(in.Body), SessionID: in.SessionID, ResponseID: in.OutputID, ResponseTarget: target}
}

func FromGateway(in RequestResult) execution.Outcome {
	out := execution.Outcome{Target: in.ResponseTarget, Native: execution.NativeCompletion{Status: in.ResponseStatus, ID: in.UpstreamResponseID},
		Operation: in.Operation, BillingState: in.BillingState, BillingID: in.BillingID, PriceVersion: in.PriceVersion, ChannelID: in.ChannelID, CredentialID: in.CredentialID, ChannelName: in.ChannelName,
		ActualModel: in.ActualModel, ChannelGroup: in.ChannelGroup, ErrorMessage: in.ErrorMessage, TotalDuration: in.TotalDuration, Result: fromCoreResult(in.Result),
		Identity: execution.Identity{UserID: in.Identity.UserID, TokenID: in.Identity.TokenID, TokenName: in.Identity.TokenName, RequestID: in.Identity.RequestID, AllowedModels: append([]string(nil), in.Identity.AllowedModels...)}}
	if in.Identity.Policy != nil {
		p := *in.Identity.Policy
		p.Models = append([]string(nil), p.Models...)
		out.Identity.Policy = &p
	}
	if in.Attempts != nil {
		out.Attempts = make([]execution.AttemptOutcome, len(in.Attempts))
		for i, a := range in.Attempts {
			out.Attempts[i] = execution.AttemptOutcome{Operation: a.Operation, ChannelID: a.ChannelID, CredentialID: a.CredentialID, Model: a.Model, Result: fromCoreResult(a.Result)}
		}
	}
	return out
}

func fromCoreResult(in core.Result) execution.Result {
	out := execution.Result{Phase: execution.Phase(in.Phase), UpstreamStatus: in.UpstreamStatus, ProtocolComplete: in.ProtocolComplete,
		StatusCode: in.StatusCode, HTTPStatus: in.HTTPStatus, Committed: in.Committed, Replayable: in.Replayable, Termination: execution.Termination(in.Termination), Duration: in.Duration, BytesWritten: in.BytesWritten}
	if in.Usage != nil {
		out.Usage = &execution.Usage{PromptTokens: in.Usage.PromptTokens, CompletionTokens: in.Usage.CompletionTokens, TotalTokens: in.Usage.TotalTokens}
	}
	if in.Error != nil {
		out.Error = &execution.Error{StatusCode: in.Error.StatusCode, Error: execution.ErrorDetail{Message: in.Error.Error.Message, Type: in.Error.Error.Type}}
		if in.Error.Error.Code != nil {
			raw, err := json.Marshal(in.Error.Error.Code)
			if err == nil {
				out.Error.Error.Code = raw
			}
		}
	}
	return out
}

func absent(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}

type coreOutput struct{ output execution.Output }

func ToCoreOutput(output execution.Output) core.Output {
	if absent(output) {
		return nil
	}
	return &coreOutput{output: output}
}

func (o *coreOutput) Commit(ctx context.Context, m core.ResponseMetadata) error {
	return o.output.Commit(ctx, execution.ResponseMetadata{StatusCode: m.StatusCode, ContentType: m.ContentType, Values: maps.Clone(m.Values)})
}
func (o *coreOutput) Write(ctx context.Context, b []byte) (int, error) { return o.output.Write(ctx, b) }
func (o *coreOutput) Flush(ctx context.Context) error                  { return o.output.Flush(ctx) }
func (o *coreOutput) State() core.OutputState {
	s := o.output.State()
	return core.OutputState{Committed: s.Committed, HTTPStatus: s.HTTPStatus}
}
func (o *coreOutput) SetWriteDeadline(t time.Time) error {
	if d, ok := o.output.(execution.DeadlineOutput); ok {
		return d.SetWriteDeadline(t)
	}
	return http.ErrNotSupported
}

var _ execution.Executor = (*PublicExecutor)(nil)
