package responses

import (
	"encoding/json"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
)

func enrichedUsageObservation(usage *usageWire, base *v2.UsageObservation) (*v3.UsageObservation, error) {
	u := &v3.UsageObservation{Counts: base.Counts, Completeness: base.Completeness, Source: base.Source}
	if usage != nil {
		cached, err := nestedNativeCount(usage.InputDetails, "cached_tokens")
		if err != nil {
			return nil, err
		}
		written, err := nestedNativeCount(usage.InputDetails, "cache_write_tokens")
		if err != nil {
			return nil, err
		}
		reasoning, err := nestedNativeCount(usage.OutputDetails, "reasoning_tokens")
		if err != nil {
			return nil, err
		}
		u.Breakdown = v3.TokenBreakdown{CachedInputTokens: cached, CacheWriteInputTokens: written, ReasoningOutputTokens: reasoning}
		if cached != nil || written != nil || reasoning != nil {
			if u.Completeness == v2.Unknown {
				u.Completeness = v2.Partial
			}
			u.Source = v2.ProviderReported
		}
	}
	if u.Source == v2.ProviderReported {
		u.Profile = "responses_native_tokens_v2"
	}
	if err := u.Validate(); err != nil {
		return nil, err
	}
	return u, nil
}

// Optional field lookup adapted from codex-proxy-rs events.rs number_field and
// nested_number_field at 591f95cc18ce95eb6fc87cf46a3526c5b3ecba36, Apache-2.0.
// Preserve unknowns and exact native names, reject non-int64 values; see THIRD_PARTY_NOTICES.md.
func nestedNativeCount(details json.RawMessage, name string) (*int64, error) {
	if !present(details) {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(details, &fields); err != nil {
		return nil, err
	}
	raw := fields[name]
	if !present(raw) {
		return nil, nil
	}
	var count int64
	if err := json.Unmarshal(raw, &count); err != nil {
		return nil, err
	}
	return &count, nil
}

func (d *Decoder) DecodeUsageEnvelope(body []byte) (v3.Envelope, error) {
	envelope, _, err := d.decodeUsage(body, true, true)
	return envelope, err
}

func (d *Decoder) NewUsageEnvelopeStream() v3.StreamDecoder {
	d.observe(Observation{})
	return usageEnvelopeStream{stream: &streamDecoder{lastSequence: -1, owner: d, captureFacts: true, captureUsage: true}}
}

type usageEnvelopeStream struct{ stream *streamDecoder }

func (s usageEnvelopeStream) envelopes(frames []v1.Frame, err error) ([]v3.Envelope, error) {
	base, err := (envelopeStream{stream: s.stream}).envelopes(frames, err)
	if err != nil {
		return nil, err
	}
	result := make([]v3.Envelope, len(base))
	for i, envelope := range base {
		result[i] = v3.Envelope{Execution: envelope, Usage: s.stream.enrichedUsage}
	}
	return result, nil
}

func (s usageEnvelopeStream) reset() {
	s.stream.factUsage, s.stream.factNative, s.stream.enrichedUsage = nil, nil, nil
}

func (s usageEnvelopeStream) DecodeUsageEnvelopes(line []byte) ([]v3.Envelope, error) {
	s.reset()
	return s.envelopes(s.stream.DecodeLine(line))
}

func (s usageEnvelopeStream) EndUsageEnvelopes() ([]v3.Envelope, error) {
	s.reset()
	return s.envelopes(s.stream.End())
}

var _ v3.Decoder = (*Decoder)(nil)
