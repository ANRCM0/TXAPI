package gateway

import (
	"errors"
	"net/http"
	"time"

	"minigateway/internal/core"
	v4 "minigateway/internal/foundation/contracts/execution/v4"
)

func validHTTPStageTimeouts(t HTTPStageTimeouts) bool {
	return t.Headers >= 0 && t.FirstEffectiveOutput >= 0 && t.StreamIdle >= 0 && t.DownstreamWrite >= 0
}

// Stages belong to the foreground source, never a prepared protocol request.
func (g *Gateway) prepareHTTPStageTimeouts(in *core.Input, output core.Output) *core.Error {
	t := g.httpStageTimeouts
	if t.DownstreamWrite > 0 {
		deadline, ok := output.(interface{ SetWriteDeadline(time.Time) error })
		if !ok || deadline.SetWriteDeadline(time.Time{}) != nil {
			return &core.Error{StatusCode: http.StatusBadRequest, Error: core.ErrorDetail{Message: "output does not support HTTP write deadlines", Type: "unsupported_operation"}}
		}
	}
	in.HTTPStageTimeouts = core.HTTPStageTimeouts{Headers: t.Headers, DownstreamWrite: t.DownstreamWrite}
	if !in.Stream || (t.FirstEffectiveOutput == 0 && t.StreamIdle == 0) {
		return nil
	}
	stream := in.Decoder.NewStream()
	progress, ok := stream.(*httpProgressStream)
	if !ok || progress.reporter == nil {
		return &core.Error{StatusCode: http.StatusBadRequest, Error: core.ErrorDetail{Message: "stream decoder does not support HTTP progress timeouts", Type: "unsupported_operation"}}
	}
	in.Decoder = cachedHTTPStreamDecoder{Decoder: in.Decoder, stream: stream}
	in.HTTPStageTimeouts.FirstEffectiveOutput = t.FirstEffectiveOutput
	in.HTTPStageTimeouts.StreamIdle = t.StreamIdle
	return nil
}

type cachedHTTPStreamDecoder struct {
	core.Decoder
	stream core.StreamDecoder
}

func (d cachedHTTPStreamDecoder) NewStream() core.StreamDecoder { return d.stream }

// Each bridge wraps exactly the stream that already decodes its delivery.
// No second parser or stream is created to obtain progress observations.
func progressStream(stream core.StreamDecoder, source any) core.StreamDecoder {
	reporter, ok := source.(v4.StreamProgress)
	if !ok || nilInterface(reporter) {
		return stream
	}
	return &httpProgressStream{StreamDecoder: stream, reporter: reporter}
}

type httpProgressStream struct {
	core.StreamDecoder
	reporter v4.StreamProgress
	previous v4.Progress
}

func (s *httpProgressStream) observe(frames []core.Frame, err error) ([]core.Frame, error) {
	if err != nil {
		return nil, err
	}
	p := s.reporter.Progress()
	if p.CompleteEvents < s.previous.CompleteEvents || (s.previous.EffectiveOutput && !p.EffectiveOutput) {
		return nil, errors.New("stream progress decreased")
	}
	effective, event := p.EffectiveOutput && !s.previous.EffectiveOutput, p.CompleteEvents > s.previous.CompleteEvents
	s.previous = p
	if effective || event {
		if len(frames) == 0 {
			frames = append(frames, core.Frame{})
		}
		frames[len(frames)-1].EffectiveOutput = effective
		frames[len(frames)-1].EventComplete = event
	}
	return frames, nil
}

func (s *httpProgressStream) DecodeLine(line []byte) ([]core.Frame, error) {
	return s.observe(s.StreamDecoder.DecodeLine(line))
}

func (s *httpProgressStream) End() ([]core.Frame, error) {
	return s.observe(s.StreamDecoder.End())
}

func attachHTTPTimeoutCode(result *core.Result) {
	if result.TimeoutStage != "" && result.Error != nil {
		copy := *result.Error
		copy.Error.Code = struct {
			Stage string `json:"timeout_stage"`
		}{Stage: string(result.TimeoutStage)}
		result.Error = &copy
	}
}
