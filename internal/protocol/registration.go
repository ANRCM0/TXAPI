package protocol

import (
	"reflect"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/responses"
)

// BuiltinParser adapts the existing pure parser for isolated consumers.
// Production assembly injects the same registered parser used by execution.
type BuiltinParser struct{}

func (BuiltinParser) ParseRequest(id execution.Operation, body []byte) (execution.Request, error) {
	return ParseRequest(id, body)
}

func Operations() []execution.OperationRegistration {
	var registrations []execution.OperationRegistration
	for _, id := range []Operation{Chat, Embeddings, Responses} {
		registrations = append(registrations, execution.OperationRegistration{
			ID: id, Version: execution.Version,
			Parse:         func(body []byte) (execution.Request, error) { return ParseRequest(id, body) },
			NonReplayable: id == Responses, OpaqueErrors: id == Responses, Continuity: id == Responses,
			Errors: execution.FailureMessages{Upstream: "Responses upstream request failed", Canceled: "Responses request canceled",
				Deadline: "Responses request time budget exhausted", Output: "Responses output delivery failed", Generation: "Responses generation did not complete successfully",
				StrictDenied: "Responses has no verified strict billing dimension"},
		})
	}
	return registrations
}

func Protocols(overrides map[string]adaptor.Adaptor) []execution.ProtocolRegistration {
	adaptors := map[string]adaptor.Adaptor{}
	for _, id := range []string{"openai", "anthropic", "gemini"} {
		adaptors[id], _ = Resolve(id)
	}
	// SIWC uses the existing native Responses codec, while the gateway owns
	// its distinct source, authorization and fixed-target policy.
	adaptors["openai_siwc"], _ = Resolve("openai")
	for id, implementation := range overrides {
		adaptors[id] = implementation
	}
	var registrations []execution.ProtocolRegistration
	for id, implementation := range adaptors {
		var port execution.Adapter
		if !absentAdaptor(implementation) {
			port = &adapterPort{implementation}
			if id == "openai_siwc" {
				port = &siwcAdapterPort{adapterPort: &adapterPort{implementation}}
			}
		}
		registrations = append(registrations, execution.ProtocolRegistration{ID: id, Version: execution.Version, Adapter: port})
	}
	return registrations
}

type siwcAdapterPort struct{ *adapterPort }

func (p *siwcAdapterPort) Supports(r execution.Request) bool {
	// Tool kinds remain the managed source owner's closed permission policy.
	return r.Operation() == execution.Responses && r.Stream() && r.Requirements().Transport == execution.HTTP && p.adapterPort.Supports(r)
}

func (p *siwcAdapterPort) Prepare(r execution.Request, t execution.Target) (execution.Input, *execution.Error) {
	if !p.Supports(r) || t.ChannelType != "openai_siwc" || t.BaseURL != "https://api.openai.com/v1" || t.APIKey != "" || t.ActualModel != r.Model() {
		return execution.Input{}, unsupportedOperation()
	}
	// Existing OpenAI codec appends /v1/responses to its root. This dedicated
	// adapter fixes the official root without accepting configurable destinations.
	t.BaseURL = "https://api.openai.com"
	in, failure := p.adapterPort.Prepare(r, t)
	if failure == nil {
		// The managed grant forbids model mapping. Preserve the exact request
		// bytes so its owner can verify that preparation changed no field.
		in.Body = append([]byte(nil), r.Body()...)
	}
	return in, failure
}

func absentAdaptor(implementation adaptor.Adaptor) bool {
	if implementation == nil {
		return true
	}
	v := reflect.ValueOf(implementation)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return v.IsNil()
	}
	return false
}

type adapterPort struct{ adaptor.Adaptor }

func (p *adapterPort) Name() string {
	if named, ok := p.Adaptor.(interface{ Name() string }); ok {
		return named.Name()
	}
	return ""
}
func (p *adapterPort) Supports(r execution.Request) bool {
	return Supports(p.Adaptor, r.Requirements())
}
func (p *adapterPort) CheckOutputLimit(body []byte, limit int, field string) error {
	return CheckOutputLimit(p.Adaptor, body, limit, field)
}
func (p *adapterPort) Prepare(r execution.Request, t execution.Target) (execution.Input, *execution.Error) {
	native, err := ParseRequest(r.Operation(), r.Body())
	if err != nil {
		return execution.Input{}, unsupportedOperation()
	}
	mode, ok := modeForOperation(r.Operation())
	if !ok {
		return execution.Input{}, unsupportedOperation()
	}
	in, failure := PrepareRequest(p.Adaptor, native, &meta.Meta{ChannelID: t.ChannelID, ChannelName: t.ChannelName,
		ChannelType: t.ChannelType, BaseURL: t.BaseURL, APIKey: t.APIKey, OriginModel: t.OriginModel,
		ActualModel: t.ActualModel, IsStream: r.Stream(), Mode: mode, ResponseID: t.ResponseID, CredentialIDValue: t.CredentialIDValue})
	if failure == nil {
		if decoder, ok := in.Decoder.(*responses.Decoder); ok {
			in.Decoder = &observedDecoder{decoder}
		}
	}
	return in, failure
}

type observedDecoder struct{ *responses.Decoder }

func (d *observedDecoder) Observation() execution.Observation {
	o := d.Decoder.Observation()
	return execution.Observation{Native: execution.NativeCompletion{Status: o.Status, ID: o.ID}, Usage: o.Usage,
		Failed: o.Status == "failed" || o.Status == "cancelled"}
}

func (r Request) PromptText() string {
	if r.operation == Responses {
		return r.responses.PromptText()
	}
	return PromptTextFromBody(r.Body())
}
func (r Request) CompletionText(body []byte) string {
	if r.operation == Responses {
		return responsesCompletionText(r.Stream(), body)
	}
	return CompletionTextFromResponse(body)
}
