// Package protocol prepares upstream requests and translates responses. All
// mutable stream state is created by NewStream and owned by one attempt.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"minigateway/internal/core"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/anthropic"
	"minigateway/internal/protocol/adaptor/gemini"
	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/meta"
)

func Resolve(name string) (adaptor.Adaptor, bool) {
	switch name {
	case "openai":
		return &openai.Adaptor{}, true
	case "anthropic":
		return &anthropic.Adaptor{}, true
	case "gemini":
		return &gemini.Adaptor{}, true
	default:
		return nil, false
	}
}

func Prepare(a adaptor.Adaptor, body []byte, m *meta.Meta) (core.Input, *core.Error) {
	url, err := a.GetRequestURL(m)
	if err != nil {
		return core.Input{}, &core.Error{StatusCode: 500, Error: core.ErrorDetail{Message: "invalid upstream URL", Type: "internal_error"}}
	}
	out, err := ConvertRequestBody(a, body, m)
	if err != nil {
		return core.Input{}, &core.Error{StatusCode: 400, Error: core.ErrorDetail{Message: "request conversion failed: " + err.Error(), Type: "convert_error"}}
	}
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return core.Input{}, &core.Error{StatusCode: 500, Error: core.ErrorDetail{Message: "invalid upstream URL", Type: "internal_error"}}
	}
	if err = a.SetupRequestHeader(req, m); err != nil {
		return core.Input{}, &core.Error{StatusCode: 500, Error: core.ErrorDetail{Message: "upstream authentication header failed", Type: "internal_error"}}
	}
	return core.Input{URL: url, Header: req.Header, Body: out, Stream: m.IsStream, Replayable: true, Decoder: NewDecoder(a, m)}, nil
}

type decoder struct {
	a      adaptor.Adaptor
	target meta.Meta
}

func NewDecoder(a adaptor.Adaptor, m *meta.Meta) core.Decoder { return &decoder{a: a, target: *m} }
func (d *decoder) Decode(b []byte) ([]byte, *core.Usage, error) {
	return ConvertResponseBody(d.a, b, &d.target)
}
func (d *decoder) NewStream() core.StreamDecoder {
	s := &streamDecoder{decoder: d, raw: true}
	if pa, ok := d.a.(adaptor.ProtocolAdaptor); ok {
		s.name = pa.Name()
		s.raw = pa.Name() == "openai"
	}
	if pa, ok := d.a.(adaptor.SessionStreamer); ok {
		s.session = pa.NewStreamSession()
	}
	return s
}

type streamDecoder struct {
	decoder *decoder
	session adaptor.StreamSession
	name    string
	raw     bool
	finish  bool
	done    bool
}

func (s *streamDecoder) DecodeLine(line []byte) ([]core.Frame, error) {
	data := strings.TrimSpace(string(line))
	if strings.HasPrefix(data, "data:") {
		data = strings.TrimSpace(strings.TrimPrefix(data, "data:"))
		if data == "[DONE]" {
			s.done = true
			return []core.Frame{{Complete: true}}, nil
		}
		var envelope struct {
			Error *json.RawMessage `json:"error"`
			Type  string           `json:"type"`
		}
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			return nil, err
		}
		if envelope.Error != nil || envelope.Type == "error" {
			return nil, errors.New("upstream reported stream error")
		}
		if envelope.Type == "message_stop" {
			s.done = true
		}
	}
	if s.raw {
		if hasFinishReason(line) {
			s.finish = true
		}
		return []core.Frame{{Data: append(append([]byte(nil), line...), '\n'), Usage: usageFromLine(line)}}, nil
	}
	lines, err := ConvertStreamLine(s.decoder.a, line, s.session, &s.decoder.target)
	if err != nil {
		return nil, err
	}
	frames := make([]core.Frame, 0, len(lines)+1)
	for _, ln := range lines {
		if hasFinishReason(ln) {
			s.finish = true
		}
		frames = append(frames, core.Frame{Data: append(ln, '\n', '\n'), Usage: usageFromLine(ln)})
	}
	if s.done {
		frames = append(frames, core.Frame{Complete: true})
	}
	return frames, nil
}
func (s *streamDecoder) End() ([]core.Frame, error) {
	if !s.finish || ((s.raw || s.name == "anthropic") && !s.done) {
		return nil, fmt.Errorf("missing stream terminal state")
	}
	return []core.Frame{{Data: []byte("data: [DONE]\n\n")}}, nil
}

func usageFromLine(line []byte) *core.Usage {
	data := strings.TrimSpace(string(line))
	if !strings.HasPrefix(data, "data:") {
		return nil
	}
	var raw struct {
		Usage *core.Usage `json:"usage"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(data, "data:"))), &raw) != nil {
		return nil
	}
	return raw.Usage
}
func hasFinishReason(line []byte) bool {
	data := strings.TrimSpace(string(line))
	if !strings.HasPrefix(data, "data:") {
		return false
	}
	var raw struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(data, "data:"))), &raw) != nil {
		return false
	}
	for _, c := range raw.Choices {
		if c.FinishReason != "" {
			return true
		}
	}
	return false
}
