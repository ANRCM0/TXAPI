package responses

import (
	"errors"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v4 "minigateway/internal/foundation/contracts/execution/v4"
)

func usageObservation(usage *Usage) (*v2.UsageObservation, error) {
	observation := &v2.UsageObservation{Completeness: v2.Unknown, Source: v2.SourceUnknown}
	if usage == nil {
		return observation, nil
	}
	known := 0
	count := func(value *int) *int64 {
		if value == nil {
			return nil
		}
		known++
		converted := int64(*value)
		return &converted
	}
	observation.Counts = v2.TokenCounts{InputTokens: count(usage.Input), OutputTokens: count(usage.Output), TotalTokens: count(usage.Total)}
	if known == 0 {
		return observation, nil
	}
	for _, value := range []*int64{observation.Counts.InputTokens, observation.Counts.OutputTokens, observation.Counts.TotalTokens} {
		if value != nil && *value < 0 {
			return nil, errors.New("invalid partial Responses usage")
		}
	}
	observation.Completeness, observation.Source, observation.Profile = v2.Partial, v2.ProviderReported, "responses_native_tokens_v1"
	if known == 3 {
		// ParseResponse already checked overflow and equality for complete usage.
		observation.Completeness = v2.Complete
	}
	return observation, nil
}

func (d *Decoder) DecodeEnvelope(body []byte) (v2.Envelope, error) {
	envelope, _, err := d.decode(body, true)
	return envelope, err
}

func (d *Decoder) NewEnvelopeStream() v2.StreamDecoder {
	d.observe(Observation{})
	return envelopeStream{stream: &streamDecoder{lastSequence: -1, owner: d, captureFacts: true}}
}

type envelopeStream struct{ stream *streamDecoder }

func (s envelopeStream) Progress() v4.Progress { return s.stream.Progress() }

func (s envelopeStream) envelopes(frames []v1.Frame, err error) ([]v2.Envelope, error) {
	if err != nil {
		return nil, err
	}
	result := make([]v2.Envelope, len(frames))
	for i, frame := range frames {
		result[i] = v2.Envelope{Data: frame.Data, Usage: s.stream.factUsage, Native: s.stream.factNative, ProtocolTerminal: frame.Complete,
			Failed: frame.Complete && s.stream.factNative != nil && (s.stream.factNative.Status == "failed" || s.stream.factNative.Status == "cancelled")}
	}
	return result, nil
}

func (s envelopeStream) DecodeEnvelopes(line []byte) ([]v2.Envelope, error) {
	s.stream.factUsage, s.stream.factNative = nil, nil
	return s.envelopes(s.stream.DecodeLine(line))
}

func (s envelopeStream) EndEnvelopes() ([]v2.Envelope, error) {
	s.stream.factUsage, s.stream.factNative = nil, nil
	return s.envelopes(s.stream.End())
}

var _ v2.Decoder = (*Decoder)(nil)
