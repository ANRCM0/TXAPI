package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

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

type Observation struct {
	ID     string
	Status string
	Usage  *core.Usage
}

type Decoder struct {
	publicID    string
	mu          sync.Mutex
	observation Observation
}

func NewDecoder() *Decoder                      { return NewDecoderWithID("") }
func NewDecoderWithID(publicID string) *Decoder { return &Decoder{publicID: publicID} }
func (d *Decoder) Observation() Observation {
	d.mu.Lock()
	defer d.mu.Unlock()
	o := d.observation
	if o.Usage != nil {
		u := *o.Usage
		o.Usage = &u
	}
	return o
}
func (d *Decoder) observe(o Observation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observation = o
}
func terminalStatus(status string) bool {
	return status == "completed" || status == "incomplete" || status == "failed" || status == "cancelled"
}
func (d *Decoder) Decode(body []byte) ([]byte, *core.Usage, error) {
	d.observe(Observation{})
	r, usage, err := ParseResponse(body)
	if err != nil {
		return nil, nil, err
	}
	if !terminalStatus(r.Status) {
		return nil, nil, errors.New("Responses synchronous response is not terminal")
	}
	out, err := rewrite(body, d.publicID, true, false)
	if err != nil {
		return nil, nil, err
	}
	d.observe(Observation{ID: r.ID, Status: r.Status, Usage: usage})
	return out, usage, nil
}
func (d *Decoder) NewStream() core.StreamDecoder {
	d.observe(Observation{})
	return &streamDecoder{lastSequence: -1, owner: d}
}

// SSE framing and mutable protocol state belong to exactly one attempt.
type streamDecoder struct {
	owner        *Decoder
	upstreamID   string
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
	frame := core.Frame{Data: wire}
	var response Response
	var usage *core.Usage
	if present(event.Response) {
		var err error
		response, usage, err = ParseResponse(event.Response)
		if err != nil {
			return nil, err
		}
		if s.upstreamID != "" && s.upstreamID != response.ID {
			return nil, errors.New("Responses upstream identity changed")
		}
		s.upstreamID = response.ID
		s.owner.observe(Observation{ID: s.upstreamID})
	}
	var identity struct {
		ResponseID string `json:"response_id"`
	}
	if json.Unmarshal(data, &identity) != nil {
		return nil, errors.New("invalid Responses event identity")
	}
	if identity.ResponseID != "" {
		if s.upstreamID != "" && s.upstreamID != identity.ResponseID {
			return nil, errors.New("Responses upstream identity changed")
		}
		s.upstreamID = identity.ResponseID
		s.owner.observe(Observation{ID: s.upstreamID})
	}
	status := strings.TrimPrefix(event.Type, "response.")
	if strings.HasPrefix(event.Type, "response.") && terminalStatus(status) {
		if "response."+response.Status != event.Type {
			return nil, errors.New("Responses terminal state mismatch")
		}
		s.terminal = true
		frame.Complete = true
		frame.Usage = usage
		s.owner.observe(Observation{ID: s.upstreamID, Status: response.Status, Usage: usage})
	} else if event.Type == "error" {
		s.terminal = true
		frame.Complete = true
		s.owner.observe(Observation{ID: s.upstreamID, Status: "failed"})
	}
	rewritten, err := rewrite(data, s.owner.publicID, false, event.Type == "error")
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(rewritten, data) {
		var out bytes.Buffer
		for _, line := range strings.Split(strings.TrimSuffix(string(s.wire.Bytes()), "\n"), "\n") {
			if strings.HasPrefix(line, "data:") || line == "data" {
				continue
			}
			out.WriteString(line)
			out.WriteByte('\n')
		}
		out.WriteString("data: ")
		out.Write(rewritten)
		out.WriteString("\n\n")
		frame.Data = out.Bytes()
	}
	return []core.Frame{frame}, nil
}

// Rewrite only documented envelope positions. Metadata and output items may
// legitimately contain fields named error, response or response_id themselves.
func rewrite(body []byte, publicID string, responseRoot, errorEvent bool) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	changed := false
	rewriteResponse := func(response map[string]json.RawMessage) {
		if _, ok := response["id"]; ok && publicID != "" {
			response["id"], _ = json.Marshal(publicID)
			changed = true
		}
		if present(response["error"]) {
			response["error"] = json.RawMessage(`{"code":"upstream_error","message":"Upstream generation failed."}`)
			changed = true
		}
	}
	if responseRoot {
		rewriteResponse(root)
	} else {
		if present(root["response_id"]) && publicID != "" {
			root["response_id"], _ = json.Marshal(publicID)
			changed = true
		}
		if present(root["response"]) {
			var response map[string]json.RawMessage
			if err := json.Unmarshal(root["response"], &response); err != nil {
				return nil, err
			}
			rewriteResponse(response)
			if changed {
				var err error
				root["response"], err = json.Marshal(response)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if errorEvent {
		for key := range root {
			if key != "type" && key != "sequence_number" && key != "response_id" {
				delete(root, key)
			}
		}
		root["message"] = json.RawMessage(`"Upstream generation failed."`)
		root["code"] = json.RawMessage(`"upstream_error"`)
		changed = true
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(root)
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
