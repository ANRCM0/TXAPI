package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"minigateway/internal/core"
	"minigateway/internal/protocol/jsonutil"
)

type Response struct {
	ID     string          `json:"id"`
	Object string          `json:"object"`
	Model  string          `json:"model"`
	Status string          `json:"status"`
	Output json.RawMessage `json:"output"`
	Usage  *Usage          `json:"usage"`
}

type Usage struct {
	Input  *int `json:"input_tokens"`
	Output *int `json:"output_tokens"`
	Total  *int `json:"total_tokens"`
}

func ParseResponse(body []byte) (Response, *core.Usage, error) {
	var r Response
	if err := jsonutil.Validate(body); err != nil {
		return r, nil, err
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return r, nil, err
	}
	if r.ID == "" || r.Object != "response" {
		return r, nil, errors.New("invalid Responses object")
	}
	switch r.Status {
	case "completed", "incomplete", "failed", "cancelled", "queued", "in_progress":
	default:
		return r, nil, errors.New("unknown Responses state")
	}
	if r.Usage == nil {
		return r, nil, nil
	}
	u := r.Usage
	if u.Input == nil || u.Output == nil || u.Total == nil {
		return r, nil, nil
	}
	if *u.Input < 0 || *u.Output < 0 || *u.Total < 0 || *u.Input > math.MaxInt-*u.Output || *u.Input+*u.Output != *u.Total {
		return r, nil, errors.New("invalid Responses usage")
	}
	return r, &core.Usage{PromptTokens: *u.Input, CompletionTokens: *u.Output, TotalTokens: *u.Total}, nil
}

type Decoder struct{}

func NewDecoder() *Decoder { return &Decoder{} }
func (*Decoder) Decode(body []byte) ([]byte, *core.Usage, error) {
	_, usage, err := ParseResponse(body)
	if err != nil {
		return nil, nil, err
	}
	return body, usage, nil
}
func (*Decoder) NewStream() core.StreamDecoder { return &streamDecoder{lastSequence: -1} }

// SSE framing and mutable protocol state belong to exactly one attempt.
type streamDecoder struct {
	wire         bytes.Buffer
	data         []string
	event        string
	lastSequence int64
	terminal     bool
}

const maxEventBytes = 1 << 20

func (s *streamDecoder) DecodeLine(line []byte) ([]core.Frame, error) {
	if s.terminal {
		return nil, errors.New("Responses event after terminal state")
	}
	if len(line) == 0 {
		return s.flush()
	}
	if s.wire.Len()+len(line)+1 > maxEventBytes {
		return nil, errors.New("Responses event exceeds limit")
	}
	s.wire.Write(line)
	s.wire.WriteByte('\n')
	field, value, hasValue := strings.Cut(string(line), ":")
	if hasValue {
		value = strings.TrimPrefix(value, " ")
	}
	switch field {
	case "event":
		s.event = value
	case "data":
		s.data = append(s.data, value)
	}
	return nil, nil
}

func (s *streamDecoder) flush() ([]core.Frame, error) {
	defer func() { s.wire.Reset(); s.data = nil; s.event = "" }()
	if s.wire.Len() == 0 {
		return nil, nil
	}
	wire := append(append([]byte(nil), s.wire.Bytes()...), '\n')
	if len(s.data) == 0 {
		return []core.Frame{{Data: wire}}, nil
	}
	var event struct {
		Type     string          `json:"type"`
		Sequence *int64          `json:"sequence_number"`
		Response json.RawMessage `json:"response"`
	}
	data := []byte(strings.Join(s.data, "\n"))
	if err := jsonutil.Validate(data); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, err
	}
	if event.Type == "" || s.event != "" && s.event != event.Type {
		return nil, errors.New("Responses event type mismatch")
	}
	if event.Sequence != nil {
		if *event.Sequence < 0 || *event.Sequence <= s.lastSequence {
			return nil, errors.New("Responses sequence is not increasing")
		}
		s.lastSequence = *event.Sequence
	}
	if event.Type == "error" || event.Type == "response.failed" || event.Type == "response.cancelled" {
		return nil, errors.New("Responses generation failed")
	}
	frame := core.Frame{Data: wire}
	if event.Type == "response.completed" || event.Type == "response.incomplete" {
		response, usage, err := ParseResponse(event.Response)
		if err != nil {
			return nil, err
		}
		if "response."+response.Status != event.Type {
			return nil, errors.New("Responses terminal state mismatch")
		}
		s.terminal = true
		frame.Complete = true
		frame.Usage = usage
	}
	return []core.Frame{frame}, nil
}

func (s *streamDecoder) End() ([]core.Frame, error) {
	frames, err := s.flush()
	if err != nil {
		return nil, err
	}
	if !s.terminal {
		return nil, fmt.Errorf("Responses stream ended without a terminal response")
	}
	return frames, nil
}

var _ core.Decoder = (*Decoder)(nil)
