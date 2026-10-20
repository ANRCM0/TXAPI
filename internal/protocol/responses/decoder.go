package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	execv2 "minigateway/internal/foundation/contracts/execution/v2"
	execv3 "minigateway/internal/foundation/contracts/execution/v3"
	execv4 "minigateway/internal/foundation/contracts/execution/v4"
	authv3 "minigateway/internal/foundation/contracts/upstreamauth/v3"
	"minigateway/internal/protocol/jsonutil"
	"minigateway/internal/protocol/openaiauth"
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

type usageWire struct {
	Usage
	InputDetails  json.RawMessage `json:"input_tokens_details"`
	OutputDetails json.RawMessage `json:"output_tokens_details"`
}

func ParseResponse(body []byte) (Response, *execv1.Usage, error) {
	r, usage, _, err := parseResponse(body)
	return r, usage, err
}

func parseResponse(body []byte) (Response, *execv1.Usage, *usageWire, error) {
	var r Response
	if err := jsonutil.Validate(body); err != nil {
		return r, nil, nil, err
	}
	var wire struct {
		Response
		Usage *usageWire `json:"usage"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return r, nil, nil, err
	}
	r = wire.Response
	if wire.Usage != nil {
		r.Usage = &wire.Usage.Usage
	}

	if r.ID == "" || r.Object != "response" {
		return r, nil, nil, errors.New("invalid Responses object")
	}
	switch r.Status {
	case "completed", "incomplete", "failed", "cancelled", "queued", "in_progress":
	default:
		return r, nil, nil, errors.New("unknown Responses state")
	}
	if present(r.Output) {
		var output []json.RawMessage
		if json.Unmarshal(r.Output, &output) != nil {
			return r, nil, nil, errors.New("invalid Responses output")
		}
		for _, item := range output {
			if err := validateOutputNamespace(item); err != nil {
				return r, nil, nil, err
			}
		}
	}
	if r.Usage == nil {
		return r, nil, wire.Usage, nil
	}
	u := r.Usage
	if u.Input == nil || u.Output == nil || u.Total == nil {
		return r, nil, wire.Usage, nil
	}
	if *u.Input < 0 || *u.Output < 0 || *u.Total < 0 || *u.Input > math.MaxInt-*u.Output || *u.Input+*u.Output != *u.Total {
		return r, nil, nil, errors.New("invalid Responses usage")
	}
	return r, &execv1.Usage{PromptTokens: *u.Input, CompletionTokens: *u.Output, TotalTokens: *u.Total}, wire.Usage, nil
}

type Observation struct {
	ID     string
	Status string
	Usage  *execv1.Usage
}

type Decoder struct {
	publicID    string
	mu          sync.Mutex
	observation Observation
	failure     authv3.FailureSignal
	hasFailure  bool
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
	if o.ID == "" && o.Status == "" {
		d.failure, d.hasFailure = authv3.FailureSignal{}, false
	}
}

func (d *Decoder) InferenceFailure() (authv3.FailureSignal, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.failure, d.hasFailure
}

func (d *Decoder) observeFailure(data []byte) {
	signal, ok := openaiauth.ClassifyInferenceFailure(0, data, true, authv3.ResponsesEvent)
	if !ok {
		return
	}
	d.mu.Lock()
	d.failure, d.hasFailure = signal, true
	d.mu.Unlock()
}
func terminalStatus(status string) bool {
	return status == "completed" || status == "incomplete" || status == "failed" || status == "cancelled"
}
func (d *Decoder) Decode(body []byte) ([]byte, *execv1.Usage, error) {
	envelope, usage, err := d.decode(body, false)
	return envelope.Data, usage, err
}

func (d *Decoder) decode(body []byte, facts bool) (execv2.Envelope, *execv1.Usage, error) {
	envelope, usage, err := d.decodeUsage(body, facts, false)
	return envelope.Execution, usage, err
}

func (d *Decoder) decodeUsage(body []byte, facts, enriched bool) (execv3.Envelope, *execv1.Usage, error) {
	d.observe(Observation{})
	r, usage, details, err := parseResponse(body)
	if err != nil {
		return execv3.Envelope{}, nil, err
	}
	if !terminalStatus(r.Status) {
		return execv3.Envelope{}, nil, errors.New("Responses synchronous response is not terminal")
	}
	envelope := execv2.Envelope{ProtocolTerminal: true, Native: &execv1.NativeCompletion{ID: r.ID, Status: r.Status}, Failed: r.Status == "failed" || r.Status == "cancelled"}
	if facts {
		envelope.Usage, err = usageObservation(r.Usage)
		if err != nil {
			return execv3.Envelope{}, nil, err
		}
	}
	var enhanced *execv3.UsageObservation
	if enriched {
		enhanced, err = enrichedUsageObservation(details, envelope.Usage)
		if err != nil {
			return execv3.Envelope{}, nil, err
		}
	}
	out, err := rewrite(body, d.publicID, true, false)
	if err != nil {
		return execv3.Envelope{}, nil, err
	}
	d.observe(Observation{ID: r.ID, Status: r.Status, Usage: usage})
	envelope.Data = out
	return execv3.Envelope{Execution: envelope, Usage: enhanced}, usage, nil
}
func (d *Decoder) NewStream() execv1.StreamDecoder {
	d.observe(Observation{})
	return &streamDecoder{lastSequence: -1, owner: d}
}

// SSE framing and mutable protocol state belong to exactly one attempt.
type streamDecoder struct {
	owner         *Decoder
	upstreamID    string
	wire          bytes.Buffer
	data          []string
	event         string
	lastSequence  int64
	terminal      bool
	captureFacts  bool
	captureUsage  bool
	enrichedUsage *execv3.UsageObservation
	factUsage     *execv2.UsageObservation
	factNative    *execv1.NativeCompletion
	progress      execv4.Progress
}

func (s *streamDecoder) Progress() execv4.Progress { return s.progress }

const maxEventBytes = 1 << 20

func (s *streamDecoder) DecodeLine(line []byte) ([]execv1.Frame, error) {
	// The HTTP scanner removes CRLF; tolerate a retained CR from other line
	// consumers as well, without making it part of the SSE field value.
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if s.terminal {
		return nil, errors.New("Responses event after terminal state")
	}
	if len(line) == 0 {
		return s.flush()
	}
	// Reserve the final blank line as part of the complete event budget.
	if s.wire.Len()+len(line)+2 > maxEventBytes {
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

func (s *streamDecoder) flush() ([]execv1.Frame, error) {
	defer func() { s.wire.Reset(); s.data = nil; s.event = "" }()
	if s.wire.Len() == 0 {
		return nil, nil
	}
	wire := append(append([]byte(nil), s.wire.Bytes()...), '\n')
	if len(s.data) == 0 {
		s.progress.CompleteEvents++
		return []execv1.Frame{{Data: wire}}, nil
	}
	var event struct {
		Type     string          `json:"type"`
		Sequence *int64          `json:"sequence_number"`
		Response json.RawMessage `json:"response"`
		Delta    any             `json:"delta"`
		Item     any             `json:"item"`
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
	if event.Type == "response.output_item.added" || event.Type == "response.output_item.done" {
		if item, ok := event.Item.(map[string]any); ok {
			raw, _ := json.Marshal(item)
			if err := validateOutputNamespace(raw); err != nil {
				return nil, err
			}
		}
	}
	if event.Type == "response.output_text.delta" || event.Type == "response.function_call_arguments.delta" {
		if _, ok := event.Delta.(string); !ok {
			return nil, errors.New("invalid Responses output delta")
		}
	}
	if event.Sequence != nil {
		if *event.Sequence < 0 || *event.Sequence <= s.lastSequence {
			return nil, errors.New("Responses sequence is not increasing")
		}
		s.lastSequence = *event.Sequence
	}
	frame := execv1.Frame{Data: wire}
	var response Response
	var usage *execv1.Usage
	if present(event.Response) {
		var err error
		var details *usageWire
		response, usage, details, err = parseResponse(event.Response)
		if err != nil {
			return nil, err
		}
		if s.upstreamID != "" && s.upstreamID != response.ID {
			return nil, errors.New("Responses upstream identity changed")
		}
		s.upstreamID = response.ID
		s.owner.observe(Observation{ID: s.upstreamID})
		if s.captureFacts {
			s.factUsage, err = usageObservation(response.Usage)
			if err != nil {
				return nil, err
			}
			if s.captureUsage {
				s.enrichedUsage, err = enrichedUsageObservation(details, s.factUsage)
				if err != nil {
					return nil, err
				}
			}
			s.factNative = &execv1.NativeCompletion{ID: s.upstreamID, Status: response.Status}
		}
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
		if s.captureFacts && s.factNative == nil {
			s.factNative = &execv1.NativeCompletion{ID: s.upstreamID}
		}
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
		if s.captureFacts {
			s.factNative = &execv1.NativeCompletion{ID: s.upstreamID, Status: "failed"}
		}
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
	if frame.Complete {
		s.owner.observeFailure(data)
	}
	// These fields were decoded with the owning event. Unknown event types and
	// snapshots do not imply text or tool progress.
	effective := false
	switch event.Type {
	case "response.output_text.delta", "response.function_call_arguments.delta":
		if delta, ok := event.Delta.(string); ok {
			effective = delta != ""
		}
	case "response.output_item.added":
		if item, ok := event.Item.(map[string]any); ok && item["type"] == "function_call" {
			name, _ := item["name"].(string)
			arguments, _ := item["arguments"].(string)
			effective = name != "" || arguments != ""
		}
	}
	s.progress.EffectiveOutput = s.progress.EffectiveOutput || effective
	s.progress.CompleteEvents++
	return []execv1.Frame{frame}, nil
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

func (s *streamDecoder) End() ([]execv1.Frame, error) {
	// Only a blank line dispatches an SSE event. EOF must not promote a
	// truncated terminal event into a successful protocol completion.
	s.wire.Reset()
	s.data = nil
	s.event = ""
	if !s.terminal {
		return nil, fmt.Errorf("Responses stream ended without a terminal response")
	}
	return nil, nil
}

var _ execv1.Decoder = (*Decoder)(nil)
