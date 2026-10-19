package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"minigateway/internal/access"
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
)

type ownedRequest struct {
	operation    execution.Operation
	body         []byte
	model        string
	stream       bool
	requirements execution.Requirements
	maxTokens    int
	prompt       string
	affinity     string
	completion   func([]byte) string
}

func (r ownedRequest) Operation() execution.Operation       { return r.operation }
func (r ownedRequest) Body() []byte                         { return bytes.Clone(r.body) }
func (r ownedRequest) Model() string                        { return r.model }
func (r ownedRequest) Stream() bool                         { return r.stream }
func (r ownedRequest) Requirements() execution.Requirements { return r.requirements }
func (r ownedRequest) LegacyMaxTokens() int                 { return r.maxTokens }
func (r ownedRequest) PromptText() string                   { return r.prompt }
func (r ownedRequest) AffinityText() string                 { return r.affinity }
func (r ownedRequest) CompletionText(body []byte) string    { return r.completion(bytes.Clone(body)) }

func (g *Gateway) ParseRequest(id execution.Operation, body []byte) (execution.Request, error) {
	registration, known := g.operations[id]
	if !known {
		return nil, errors.New("unsupported operation")
	}
	owned := bytes.Clone(body)
	r, err := registration.Parse(bytes.Clone(owned))
	if err != nil {
		return nil, err
	}
	if nilInterface(r) || r.Operation() != id || r.Model() == "" || !bytes.Equal(r.Body(), owned) ||
		r.Requirements().Operation != id || r.Requirements().Stream != r.Stream() {
		return nil, errors.New("operation payload and routing metadata must match")
	}
	return ownedRequest{operation: id, body: owned, model: r.Model(), stream: r.Stream(), requirements: r.Requirements(),
		maxTokens: r.LegacyMaxTokens(), prompt: r.PromptText(), affinity: r.AffinityText(), completion: r.CompletionText}, nil
}

func (e *PublicExecutor) Models(ctx context.Context) []execution.Model {
	return e.gateway.Models(ctx)
}
func (g *Gateway) Models(ctx context.Context) []execution.Model {
	data := make([]execution.Model, 0)
	p, authenticated := access.PrincipalFromContext(ctx)
	if !authenticated || p.UserID == 0 || p.TokenID == 0 {
		return data
	}
	seen := make(map[string]bool)
	for _, ch := range g.reg.allChannels() {
		if !g.modelGrantVisible(ch, p.UserID, p.TokenID) {
			continue
		}
		models := append([]string(nil), ch.models...)
		if ch.supports("*") {
			models = append(models, p.Policy.Models...)
		}
		for _, name := range models {
			if name == "*" || seen[name] || !p.Policy.Allows(name) {
				continue
			}
			seen[name] = true
			data = append(data, execution.Model{ID: name, Object: "model", OwnedBy: ch.name})
		}
	}
	return data
}

func fromCoreUsage(u *core.Usage) *execution.Usage {
	if u == nil {
		return nil
	}
	return &execution.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
}
func toCoreUsage(u *execution.Usage) *core.Usage {
	if u == nil {
		return nil
	}
	return &core.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
}
func toCoreError(e *execution.Error) *core.Error {
	if e == nil {
		return nil
	}
	result := &core.Error{StatusCode: e.StatusCode, Error: core.ErrorDetail{Message: e.Error.Message, Type: e.Error.Type}}
	if len(e.Error.Code) != 0 {
		result.Error.Code = json.RawMessage(bytes.Clone(e.Error.Code))
	}
	return result
}
func ToCoreInput(in execution.Input) core.Input {
	var decoder core.Decoder
	if !nilInterface(in.Decoder) {
		decoder = decoderBridge{in.Decoder}
	}
	return core.Input{Method: in.Method, URL: in.URL, Header: in.Header.Clone(), Body: bytes.Clone(in.Body), Stream: in.Stream,
		Replayable: in.Replayable, Timeout: in.Timeout, Decoder: decoder}
}

type decoderBridge struct{ decoder execution.Decoder }

func (d decoderBridge) Decode(body []byte) ([]byte, *core.Usage, error) {
	out, usage, err := d.decoder.Decode(bytes.Clone(body))
	return bytes.Clone(out), toCoreUsage(usage), err
}
func (d decoderBridge) NewStream() core.StreamDecoder {
	stream := d.decoder.NewStream()
	if nilInterface(stream) {
		return nil
	}
	return progressStream(streamBridge{stream}, stream)
}

type streamBridge struct{ decoder execution.StreamDecoder }

func bridgeFrames(frames []execution.Frame) []core.Frame {
	out := make([]core.Frame, len(frames))
	for i, frame := range frames {
		out[i] = core.Frame{Data: bytes.Clone(frame.Data), Usage: toCoreUsage(frame.Usage), Complete: frame.Complete}
	}
	return out
}
func (s streamBridge) DecodeLine(line []byte) ([]core.Frame, error) {
	frames, err := s.decoder.DecodeLine(bytes.Clone(line))
	return bridgeFrames(frames), err
}
func (s streamBridge) End() ([]core.Frame, error) {
	frames, err := s.decoder.End()
	return bridgeFrames(frames), err
}
