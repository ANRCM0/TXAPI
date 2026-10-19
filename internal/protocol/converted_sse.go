package protocol

import (
	"bytes"
	"encoding/json"
	"errors"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
)

// Converted streams need complete SSE events too. Count the normalized wire
// and the joined observation together, including ignored fields and comments.
const maxConvertedSSEEventBytes = 1024 * 1024

type convertedSSEEvent struct {
	data       []byte
	wireBytes  int
	hasData    bool
	errorEvent bool
	started    bool
}

func (s *streamDecoder) decodeConvertedLine(line []byte) ([]execv1.Frame, error) {
	if s.done {
		return nil, errors.New("event after stream terminal")
	}
	e := &s.converted
	parse := line
	if !e.started {
		parse = bytes.TrimPrefix(parse, []byte{0xef, 0xbb, 0xbf})
		e.started = true
	}
	field, value, _ := bytes.Cut(parse, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	extra := 0
	if bytes.Equal(field, []byte("data")) {
		extra = len(value) + 1
	}
	if len(line)+1 > maxConvertedSSEEventBytes-e.wireBytes-len(e.data)-extra {
		return nil, errors.New("upstream SSE event exceeds limit")
	}
	e.wireBytes += len(line) + 1
	if len(line) == 0 {
		return s.dispatchConvertedEvent()
	}
	switch string(field) {
	case "data":
		e.data = append(e.data, value...)
		e.data = append(e.data, '\n')
		e.hasData = true
	case "event":
		e.errorEvent = bytes.Equal(value, []byte("error"))
	}
	return nil, nil
}

func (s *streamDecoder) dispatchConvertedEvent() ([]execv1.Frame, error) {
	e := &s.converted
	data, hasData, errorEvent := e.data, e.hasData, e.errorEvent
	*e = convertedSSEEvent{started: e.started}
	if errorEvent {
		return nil, errors.New("upstream reported stream error")
	}
	if !hasData {
		return nil, nil
	}
	data = data[:len(data)-1]
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		if s.name == "anthropic" {
			return nil, errors.New("missing native Anthropic stream terminal")
		}
		s.done = true
		return []execv1.Frame{{Complete: true}}, nil
	}
	var envelope struct {
		Error *json.RawMessage `json:"error"`
		Type  string           `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.Error != nil || envelope.Type == "error" {
		return nil, errors.New("upstream reported stream error")
	}
	line := append([]byte("data: "), data...)
	lines, err := ConvertStreamLine(s.decoder.a, line, s.session, &s.decoder.target)
	if err != nil {
		return nil, err
	}
	frames := make([]execv1.Frame, 0, len(lines)+1)
	for _, ln := range lines {
		if hasFinishReason(ln) {
			s.finish = true
		}
		frames = append(frames, execv1.Frame{Data: append(ln, '\n', '\n'), Usage: usageFromLine(ln)})
	}
	if s.name == "anthropic" && envelope.Type == "message_stop" {
		s.done = true
		frames = append(frames, execv1.Frame{Complete: true})
	}
	return frames, nil
}
