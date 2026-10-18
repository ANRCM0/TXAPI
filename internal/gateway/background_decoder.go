package gateway

import execution "minigateway/internal/foundation/contracts/execution/v1"

// Preserve the pure codec's validation cause across the existing core result.
// Transport interruption without a codec error remains resumable, not corrupt.
type backgroundDecoderTracking struct {
	decoder execution.Decoder
	err     error
}

func (d *backgroundDecoderTracking) Decode(b []byte) ([]byte, *execution.Usage, error) {
	out, usage, e := d.decoder.Decode(b)
	if e != nil {
		d.err = e
	}
	return out, usage, e
}
func (d *backgroundDecoderTracking) NewStream() execution.StreamDecoder {
	s := d.decoder.NewStream()
	if nilInterface(s) {
		return nil
	}
	return backgroundStreamTracking{owner: d, stream: s}
}

type backgroundStreamTracking struct {
	owner  *backgroundDecoderTracking
	stream execution.StreamDecoder
}

func (s backgroundStreamTracking) DecodeLine(b []byte) ([]execution.Frame, error) {
	f, e := s.stream.DecodeLine(b)
	if e != nil {
		s.owner.err = e
	}
	return f, e
}
func (s backgroundStreamTracking) End() ([]execution.Frame, error) {
	f, e := s.stream.End()
	if e != nil {
		s.owner.err = e
	}
	return f, e
}
