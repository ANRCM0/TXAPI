package protocol

import (
	"bytes"
	"encoding/json"
	"errors"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
)

// Both wire and observation buffers count towards this per-attempt event limit.
// Core's Scanner already bounds individual lines. It normalizes CRLF to LF;
// this preserves field spelling/order and comments, not original network bytes.
const maxChatSSEEventBytes = 1024 * 1024

type chatSSEEvent struct {
	raw        []byte
	data       []byte
	errorEvent bool
	hasData    bool
	started    bool
	terminal   []byte
}

func (s *streamDecoder) decodeChatLine(line []byte) ([]execv1.Frame, error) {
	e := &s.chat
	if s.done {
		return nil, errors.New("event after stream terminal")
	}
	parse := line
	if !e.started {
		parse = bytes.TrimPrefix(parse, []byte{0xef, 0xbb, 0xbf})
		e.started = true
	}
	if len(line) == 0 {
		if len(e.raw)+len(e.data)+1 > maxChatSSEEventBytes {
			return nil, errors.New("upstream SSE event exceeds limit")
		}
		e.raw = append(e.raw, '\n')
		return s.dispatchChatEvent()
	}
	field, value, _ := bytes.Cut(parse, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	extra := 0
	if bytes.Equal(field, []byte("data")) {
		extra = len(value) + 1
	}
	if len(line)+1 > maxChatSSEEventBytes-len(e.raw)-len(e.data)-extra {
		return nil, errors.New("upstream SSE event exceeds limit")
	}
	e.raw = append(e.raw, line...)
	e.raw = append(e.raw, '\n')
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

func (s *streamDecoder) dispatchChatEvent() ([]execv1.Frame, error) {
	e := &s.chat
	raw, data, errorEvent, hasData := e.raw, e.data, e.errorEvent, e.hasData
	*e = chatSSEEvent{started: e.started}
	if errorEvent {
		return nil, errors.New("upstream reported stream error")
	}
	if !hasData {
		if len(raw) > 1 {
			s.progress.CompleteEvents++
		}
		return []execv1.Frame{{Data: raw}}, nil
	}
	data = data[:len(data)-1] // SSE joins data fields with LF, excluding the last LF.
	// Retain the previous Chat decoder's acceptance of whitespace around the
	// terminal marker while forwarding the original event fields unchanged.
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		if !s.finish {
			return nil, errors.New("stream terminal without finish")
		}
		s.progress.CompleteEvents++
		s.done = true
		e.terminal = raw // End releases the actual terminal only after finish validation.
		return []execv1.Frame{{Complete: true}}, nil
	}
	var envelope struct {
		Error   *json.RawMessage `json:"error"`
		Type    string           `json:"type"`
		Usage   *execv1.Usage    `json:"usage"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Delta        struct {
				Content      string `json:"content"`
				FunctionCall *struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function_call"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.Error != nil || envelope.Type == "error" {
		return nil, errors.New("upstream reported stream error")
	}
	for _, choice := range envelope.Choices {
		delta := choice.Delta
		effective := delta.Content != "" || delta.FunctionCall != nil && (delta.FunctionCall.Name != "" || delta.FunctionCall.Arguments != "")
		for _, tool := range delta.ToolCalls {
			effective = effective || tool.Function.Name != "" || tool.Function.Arguments != ""
		}
		s.progress.EffectiveOutput = s.progress.EffectiveOutput || effective
		if choice.FinishReason != "" {
			s.finish = true
		}
	}
	s.progress.CompleteEvents++
	return []execv1.Frame{{Data: raw, Usage: envelope.Usage}}, nil
}
