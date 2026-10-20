package protocol

import (
	"bytes"
	"encoding/json"
	"errors"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	v4 "minigateway/internal/foundation/contracts/execution/v4"
	"minigateway/internal/protocol/jsonutil"
)

// Native Chat exposes its own observation profile. Converted Chat keeps its
// existing decoder and provider-specific usage semantics.
type nativeChatDecoder struct{ *decoder }

func (d nativeChatDecoder) DecodeUsageEnvelope(body []byte) (v3.Envelope, error) {
	base, usage, _, err := nativeChatUsage(body)
	if err != nil {
		return v3.Envelope{}, err
	}
	return v3.Envelope{Execution: v2.Envelope{Data: body, Usage: base, ProtocolTerminal: true}, Usage: usage}, nil
}

func (d nativeChatDecoder) NewUsageEnvelopeStream() v3.StreamDecoder {
	s := d.NewStream().(*streamDecoder)
	s.captureChatUsage = true
	return nativeChatUsageStream{stream: s}
}

func nativeChatUsage(body []byte) (*v2.UsageObservation, *v3.UsageObservation, bool, error) {
	if err := jsonutil.Validate(body); err != nil {
		return nil, nil, false, err
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, nil, false, err
	}
	var eventType string
	if raw := response["type"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &eventType); err != nil {
			return nil, nil, false, err
		}
	}
	if response == nil || nativeChatPresent(response["error"]) || eventType == "error" {
		return nil, nil, false, errors.New("invalid native Chat response")
	}
	base := &v2.UsageObservation{Completeness: v2.Unknown, Source: v2.SourceUnknown}
	usage := &v3.UsageObservation{Completeness: v2.Unknown, Source: v2.SourceUnknown}
	raw, present := response["usage"]
	if !nativeChatPresent(raw) {
		return base, usage, present, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, nil, false, err
	}
	counts := []*int64{}
	for _, name := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		count, err := nativeChatCount(fields[name])
		if err != nil {
			return nil, nil, false, err
		}
		counts = append(counts, count)
	}
	base.Counts = v2.TokenCounts{InputTokens: counts[0], OutputTokens: counts[1], TotalTokens: counts[2]}
	known := 0
	for _, count := range counts {
		if count != nil {
			known++
		}
	}
	if known != 0 {
		base.Completeness, base.Source, base.Profile = v2.Partial, v2.ProviderReported, "chat_native_tokens_v1"
		if known == 3 {
			base.Completeness = v2.Complete
		}
	}
	usage.Counts, usage.Completeness, usage.Source, usage.Profile = base.Counts, base.Completeness, base.Source, base.Profile
	var err error
	usage.Breakdown.CachedInputTokens, err = nativeChatNestedCount(fields["prompt_tokens_details"], "cached_tokens")
	if err != nil {
		return nil, nil, false, err
	}
	usage.Breakdown.ReasoningOutputTokens, err = nativeChatNestedCount(fields["completion_tokens_details"], "reasoning_tokens")
	if err != nil {
		return nil, nil, false, err
	}
	if usage.Breakdown.CachedInputTokens != nil || usage.Breakdown.ReasoningOutputTokens != nil {
		usage.Source, usage.Profile = v2.ProviderReported, "chat_native_tokens_v1"
		if usage.Completeness == v2.Unknown {
			usage.Completeness = v2.Partial
		}
	}
	if err := usage.Validate(); err != nil {
		return nil, nil, false, err
	}
	return base, usage, true, nil
}

func nativeChatPresent(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func nativeChatCount(raw json.RawMessage) (*int64, error) {
	if !nativeChatPresent(raw) {
		return nil, nil
	}
	var count int64
	if err := json.Unmarshal(raw, &count); err != nil {
		return nil, err
	}
	return &count, nil
}

func nativeChatNestedCount(raw json.RawMessage, name string) (*int64, error) {
	if !nativeChatPresent(raw) {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	return nativeChatCount(fields[name])
}

type nativeChatUsageStream struct{ stream *streamDecoder }

func (s nativeChatUsageStream) Progress() v4.Progress { return s.stream.Progress() }

func (s nativeChatUsageStream) DecodeUsageEnvelopes(line []byte) ([]v3.Envelope, error) {
	s.stream.chatUsageChanged = false
	frames, err := s.stream.DecodeLine(line)
	if err != nil {
		return nil, err
	}
	out := make([]v3.Envelope, len(frames))
	for i, frame := range frames {
		envelope := v3.Envelope{Execution: v2.Envelope{Data: frame.Data, ProtocolTerminal: frame.Complete}}
		if s.stream.chatUsageChanged || frame.Complete {
			envelope.Execution.Usage, envelope.Usage = s.stream.chatBaseUsage, s.stream.chatUsage
			if envelope.Usage == nil {
				envelope.Execution.Usage = &v2.UsageObservation{Completeness: v2.Unknown, Source: v2.SourceUnknown}
				envelope.Usage = &v3.UsageObservation{Completeness: v2.Unknown, Source: v2.SourceUnknown}
			}
		}
		if frame.Complete {
			// The existing parser has already validated finish + DONE. Deliver
			// its retained terminal in this envelope, never again from End.
			envelope.Execution.Data = s.stream.chat.terminal
		}
		out[i] = envelope
	}
	return out, nil
}

func (s nativeChatUsageStream) EndUsageEnvelopes() ([]v3.Envelope, error) {
	_, err := s.stream.End()
	return nil, err
}

var _ v3.Decoder = nativeChatDecoder{}
var _ v3.StreamDecoder = nativeChatUsageStream{}
var _ v1.Decoder = nativeChatDecoder{}
