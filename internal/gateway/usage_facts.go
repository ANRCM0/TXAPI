package gateway

import (
	"bytes"
	"context"
	"errors"
	"math"

	"minigateway/internal/core"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
)

type usageFactsExecutor struct{ gateway *Gateway }

func NewUsageFactsExecutor(g *Gateway) v3.Executor { return &usageFactsExecutor{gateway: g} }

func (e *usageFactsExecutor) Execute(ctx context.Context, output v1.Output, in v1.Invocation) v3.Outcome {
	result := e.gateway.executeTo(ctx, ToCoreOutput(output), invocation(in), nil, false)
	usage := append([]v3.AttemptUsage(nil), result.usageFacts...)
	for i := range usage {
		usage[i].Usage = cloneDetailedUsageFact(usage[i].Usage)
	}
	return v3.Outcome{Execution: factsOutcome(result), Usage: usage}
}

func unknownDetailedUsageFact() v3.UsageObservation {
	return v3.UsageObservation{Completeness: v2.Unknown, Source: v2.SourceUnknown}
}

func cloneDetailedUsageFact(in v3.UsageObservation) v3.UsageObservation {
	copyCount := func(value *int64) *int64 {
		if value == nil {
			return nil
		}
		copied := *value
		return &copied
	}
	in.Counts = cloneUsageFact(v2.UsageObservation{Counts: in.Counts}).Counts
	in.Breakdown = v3.TokenBreakdown{CachedInputTokens: copyCount(in.Breakdown.CachedInputTokens),
		CacheWriteInputTokens: copyCount(in.Breakdown.CacheWriteInputTokens), ReasoningOutputTokens: copyCount(in.Breakdown.ReasoningOutputTokens)}
	return in
}

// High-water marks outlive missing snapshots; the observable usage does not.
type usageFacts struct {
	usage                v3.UsageObservation
	high                 [6]int64
	known                [6]bool
	profile, baseProfile string
}

func usageCounts(in v3.UsageObservation) [6]*int64 {
	return [6]*int64{in.Counts.InputTokens, in.Counts.OutputTokens, in.Counts.TotalTokens,
		in.Breakdown.CachedInputTokens, in.Breakdown.CacheWriteInputTokens, in.Breakdown.ReasoningOutputTokens}
}

func (f *usageFacts) accept(base *protocolFacts, in v3.Envelope) (core.Frame, error) {
	next, nextBase := *f, *base
	if (in.Usage == nil) != (in.Execution.Usage == nil) {
		return core.Frame{}, errors.New("protocol usage projections disagree")
	}
	if in.Usage != nil {
		if err := in.Usage.Validate(); err != nil {
			return core.Frame{}, err
		}
		legacy := in.Execution.Usage
		pairs := [][2]*int64{{in.Usage.Counts.InputTokens, legacy.Counts.InputTokens},
			{in.Usage.Counts.OutputTokens, legacy.Counts.OutputTokens}, {in.Usage.Counts.TotalTokens, legacy.Counts.TotalTokens}}
		for _, pair := range pairs {
			if (pair[0] == nil) != (pair[1] == nil) || pair[0] != nil && *pair[0] != *pair[1] {
				return core.Frame{}, errors.New("protocol base quantities disagree")
			}
		}
		for _, profiles := range [][2]string{{next.profile, in.Usage.Profile}, {next.baseProfile, legacy.Profile}} {
			if profiles[0] != "" && profiles[1] != "" && profiles[0] != profiles[1] {
				return core.Frame{}, errors.New("protocol usage profile changed")
			}
		}
		for i, count := range usageCounts(*in.Usage) {
			if count != nil {
				if next.known[i] && *count < next.high[i] {
					return core.Frame{}, errors.New("cumulative protocol usage decreased")
				}
				next.high[i], next.known[i] = *count, true
			}
		}
		// Historical children are lower bounds on a later explicit parent. A
		// missing current parent is not bounded above by its old high-water mark.
		if next.high[3] > math.MaxInt64-next.high[4] || in.Usage.Counts.InputTokens != nil && next.high[3]+next.high[4] > *in.Usage.Counts.InputTokens {
			return core.Frame{}, errors.New("cumulative cache lower bound exceeds input")
		}
		if in.Usage.Counts.OutputTokens != nil && next.high[5] > *in.Usage.Counts.OutputTokens {
			return core.Frame{}, errors.New("cumulative reasoning lower bound exceeds output")
		}
		inputLower := max(next.high[0], next.high[3]+next.high[4])
		outputLower := max(next.high[1], next.high[5])
		if inputLower > math.MaxInt64-outputLower || in.Usage.Counts.TotalTokens != nil && inputLower+outputLower > *in.Usage.Counts.TotalTokens {
			return core.Frame{}, errors.New("cumulative base lower bound exceeds total")
		}
		if in.Usage.Profile != "" {
			next.profile = in.Usage.Profile
		}
		if legacy.Profile != "" {
			next.baseProfile = legacy.Profile
		}
		next.usage = cloneDetailedUsageFact(*in.Usage)
	}
	frame, err := nextBase.accept(in.Execution)
	if err != nil {
		return core.Frame{}, err
	}
	*f, *base = next, nextBase
	return frame, nil
}

type usageDecoderBridge struct {
	decoder v3.Decoder
	facts   *protocolFacts
	usage   *usageFacts
}

func (d usageDecoderBridge) Decode(body []byte) ([]byte, *core.Usage, error) {
	envelope, err := d.decoder.DecodeUsageEnvelope(bytes.Clone(body))
	if err != nil {
		return nil, nil, err
	}
	if !envelope.Execution.ProtocolTerminal {
		return nil, nil, errors.New("synchronous protocol response is not terminal")
	}
	frame, err := d.usage.accept(d.facts, envelope)
	return frame.Data, frame.Usage, err
}

func (d usageDecoderBridge) NewStream() core.StreamDecoder {
	stream := d.decoder.NewUsageEnvelopeStream()
	if nilInterface(stream) {
		return nil
	}
	return progressStream(usageStreamBridge{decoder: stream, facts: d.facts, usage: d.usage}, stream)
}

type usageStreamBridge struct {
	decoder v3.StreamDecoder
	facts   *protocolFacts
	usage   *usageFacts
}

func (s usageStreamBridge) frames(envelopes []v3.Envelope, err error) ([]core.Frame, error) {
	if err != nil {
		return nil, err
	}
	frames := make([]core.Frame, len(envelopes))
	for i, envelope := range envelopes {
		frames[i], err = s.usage.accept(s.facts, envelope)
		if err != nil {
			return nil, err
		}
	}
	return frames, nil
}

func (s usageStreamBridge) DecodeLine(line []byte) ([]core.Frame, error) {
	return s.frames(s.decoder.DecodeUsageEnvelopes(bytes.Clone(line)))
}

func (s usageStreamBridge) End() ([]core.Frame, error) {
	frames, err := s.frames(s.decoder.EndUsageEnvelopes())
	if err == nil && !s.facts.terminal {
		err = errors.New("protocol stream ended without terminal facts")
	}
	return frames, err
}
