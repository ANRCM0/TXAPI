package gateway

import (
	"bytes"
	"context"
	"errors"
	"math"

	"minigateway/internal/core"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

type factsExecutor struct{ gateway *Gateway }

func NewFactsExecutor(g *Gateway) v2.Executor { return &factsExecutor{gateway: g} }

func (e *factsExecutor) Execute(ctx context.Context, output v1.Output, in v1.Invocation) v2.Outcome {
	result := e.gateway.executeTo(ctx, ToCoreOutput(output), invocation(in), nil, false)
	facts := append([]v2.AttemptFacts(nil), result.protocolFacts...)
	for i := range facts {
		facts[i].Usage = cloneUsageFact(facts[i].Usage)
	}
	return v2.Outcome{Legacy: FromGateway(result), Attempts: facts}
}

func unknownUsageFact() v2.UsageObservation {
	return v2.UsageObservation{Completeness: v2.Unknown, Source: v2.SourceUnknown}
}

func cloneUsageFact(in v2.UsageObservation) v2.UsageObservation {
	copyCount := func(value *int64) *int64 {
		if value == nil {
			return nil
		}
		copied := *value
		return &copied
	}
	in.Counts = v2.TokenCounts{InputTokens: copyCount(in.Counts.InputTokens), OutputTokens: copyCount(in.Counts.OutputTokens), TotalTokens: copyCount(in.Counts.TotalTokens)}
	return in
}

func validateUsageFact(in v2.UsageObservation) error {
	known := 0
	for _, count := range []*int64{in.Counts.InputTokens, in.Counts.OutputTokens, in.Counts.TotalTokens} {
		if count != nil {
			if *count < 0 {
				return errors.New("negative protocol usage")
			}
			known++
		}
	}
	if in.Completeness == v2.Unknown && known == 0 && in.Source == v2.SourceUnknown && in.Profile == "" {
		return nil
	}
	if in.Source != v2.ProviderReported || in.Profile == "" {
		return errors.New("protocol usage has no declared provider source")
	}
	if in.Completeness == v2.Partial && known > 0 && known < 3 {
		return nil
	}
	if in.Completeness != v2.Complete || known != 3 {
		return errors.New("protocol usage completeness does not match known counts")
	}
	a, b, total := *in.Counts.InputTokens, *in.Counts.OutputTokens, *in.Counts.TotalTokens
	if a > math.MaxInt64-b || a+b != total || total > math.MaxInt {
		return errors.New("protocol usage is inconsistent or exceeds the legacy count range")
	}
	return nil
}

type protocolFacts struct {
	usage    v2.UsageObservation
	native   v1.NativeCompletion
	terminal bool
	failed   bool
}

func (f *protocolFacts) accept(in v2.Envelope) (core.Frame, error) {
	next := *f
	frame, err := next.acceptOwned(in)
	if err == nil {
		*f = next
	}
	return frame, err
}

func (f *protocolFacts) acceptOwned(in v2.Envelope) (core.Frame, error) {
	if f.terminal {
		return core.Frame{}, errors.New("protocol envelope follows terminal state")
	}
	if in.Failed && !in.ProtocolTerminal {
		return core.Frame{}, errors.New("protocol failure is not terminal")
	}
	if in.Usage != nil {
		if err := validateUsageFact(*in.Usage); err != nil {
			return core.Frame{}, err
		}
		if f.usage.Profile != "" && in.Usage.Profile != "" && f.usage.Profile != in.Usage.Profile {
			return core.Frame{}, errors.New("protocol usage profile changed")
		}
		pairs := [][2]*int64{{f.usage.Counts.InputTokens, in.Usage.Counts.InputTokens}, {f.usage.Counts.OutputTokens, in.Usage.Counts.OutputTokens}, {f.usage.Counts.TotalTokens, in.Usage.Counts.TotalTokens}}
		for _, pair := range pairs {
			if pair[0] != nil && pair[1] != nil && *pair[1] < *pair[0] {
				return core.Frame{}, errors.New("cumulative protocol usage decreased")
			}
		}
		f.usage = cloneUsageFact(*in.Usage)
	}
	if in.Native != nil {
		if f.native.ID != "" && in.Native.ID != "" && f.native.ID != in.Native.ID {
			return core.Frame{}, errors.New("protocol response identity changed")
		}
		f.native = *in.Native
	}
	f.terminal, f.failed = in.ProtocolTerminal, in.Failed
	frame := core.Frame{Data: bytes.Clone(in.Data), Complete: in.ProtocolTerminal}
	// Only a terminal provider snapshot may feed the existing accounting path.
	if in.ProtocolTerminal && in.Usage != nil && in.Usage.Completeness == v2.Complete {
		counts := in.Usage.Counts
		frame.Usage = &core.Usage{PromptTokens: int(*counts.InputTokens), CompletionTokens: int(*counts.OutputTokens), TotalTokens: int(*counts.TotalTokens)}
	}
	return frame, nil
}

type factsDecoderBridge struct {
	decoder v2.Decoder
	facts   *protocolFacts
}

func (d factsDecoderBridge) Decode(body []byte) ([]byte, *core.Usage, error) {
	envelope, err := d.decoder.DecodeEnvelope(bytes.Clone(body))
	if err != nil {
		return nil, nil, err
	}
	if !envelope.ProtocolTerminal {
		return nil, nil, errors.New("synchronous protocol response is not terminal")
	}
	frame, err := d.facts.accept(envelope)
	return frame.Data, frame.Usage, err
}

func (d factsDecoderBridge) NewStream() core.StreamDecoder {
	stream := d.decoder.NewEnvelopeStream()
	if nilInterface(stream) {
		return nil
	}
	return factsStreamBridge{decoder: stream, facts: d.facts}
}

type factsStreamBridge struct {
	decoder v2.StreamDecoder
	facts   *protocolFacts
}

func (s factsStreamBridge) frames(envelopes []v2.Envelope, err error) ([]core.Frame, error) {
	if err != nil {
		return nil, err
	}
	frames := make([]core.Frame, len(envelopes))
	for i, envelope := range envelopes {
		frames[i], err = s.facts.accept(envelope)
		if err != nil {
			return nil, err
		}
	}
	return frames, nil
}

func (s factsStreamBridge) DecodeLine(line []byte) ([]core.Frame, error) {
	return s.frames(s.decoder.DecodeEnvelopes(bytes.Clone(line)))
}

func (s factsStreamBridge) End() ([]core.Frame, error) {
	frames, err := s.frames(s.decoder.EndEnvelopes())
	if err == nil && !s.facts.terminal {
		err = errors.New("protocol stream ended without terminal facts")
	}
	return frames, err
}
