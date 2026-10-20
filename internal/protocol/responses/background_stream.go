package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	bg "minigateway/internal/foundation/contracts/background/v1"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	usagev3 "minigateway/internal/foundation/contracts/execution/v3"
	"minigateway/internal/protocol/jsonutil"
)

type backgroundStreamDecoder struct {
	owner           *backgroundDecoder
	remoteID, event string
	data            []string
	size            int
	lastSequence    int64
	terminal        bool
}

func backgroundState(status string) bg.RemoteState {
	return map[string]bg.RemoteState{"queued": bg.RemoteAccepted, "in_progress": bg.RemoteRunning, "completed": bg.RemoteSucceeded, "incomplete": bg.RemoteIncomplete, "failed": bg.RemoteFailed, "cancelled": bg.RemoteCancelled}[status]
}

func (s *backgroundStreamDecoder) DecodeLine(line []byte) ([]execution.Frame, error) {
	if s.terminal {
		return nil, errors.New("background event after terminal")
	}
	if len(line) == 0 {
		return s.flush()
	}
	if s.size+len(line)+1 > 64<<10 {
		return nil, errors.New("background event exceeds limit")
	}
	s.size += len(line) + 1
	field, value, _ := strings.Cut(string(line), ":")
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "event":
		s.event = value
	case "data":
		s.data = append(s.data, value)
	case "", "id", "retry":
	default:
		return nil, errors.New("background stream is not SSE")
	}
	return nil, nil
}

func (s *backgroundStreamDecoder) flush() ([]execution.Frame, error) {
	defer func() { s.event = ""; s.data = nil; s.size = 0 }()
	if len(s.data) == 0 {
		return nil, nil
	}
	data := []byte(strings.Join(s.data, "\n"))
	if jsonutil.Validate(data) != nil {
		return nil, errors.New("invalid background event")
	}
	var event struct {
		Type       string          `json:"type"`
		Sequence   *int64          `json:"sequence_number"`
		Response   json.RawMessage `json:"response"`
		ResponseID string          `json:"response_id"`
	}
	if json.Unmarshal(data, &event) != nil || event.Sequence == nil || *event.Sequence < 0 || *event.Sequence < s.lastSequence || !validBackgroundEventType(event.Type) || s.event != "" && s.event != event.Type {
		return nil, errors.New("invalid background event sequence or type")
	}
	// Custom tool input is text, never an executable grammar or function JSON.
	// Apply the same known-field checks before persisting replayable events.
	if event.Type == "response.output_item.added" || event.Type == "response.output_item.done" {
		var itemEvent struct {
			Item json.RawMessage `json:"item"`
		}
		_ = json.Unmarshal(data, &itemEvent)
		if present(itemEvent.Item) {
			if err := validateOutputToolItem(itemEvent.Item); err != nil {
				return nil, err
			}
		}
	}
	if event.Type == "response.custom_tool_call_input.delta" || event.Type == "response.custom_tool_call_input.done" {
		var custom struct {
			Delta *string `json:"delta"`
			Input *string `json:"input"`
		}
		if json.Unmarshal(data, &custom) != nil || event.Type == "response.custom_tool_call_input.delta" && custom.Delta == nil || event.Type == "response.custom_tool_call_input.done" && custom.Input == nil {
			return nil, errors.New("invalid background custom input event")
		}
	}
	remoteID := s.remoteID
	observation := bg.Observation{}
	var enriched *usagev3.UsageObservation
	if present(event.Response) {
		r, usage, details, e := parseResponse(event.Response)
		if e != nil || !validRemoteID(r.ID) || remoteID != "" && r.ID != remoteID {
			return nil, errors.New("invalid background event identity")
		}
		base, e := usageObservation(r.Usage)
		if e != nil {
			return nil, e
		}
		enriched, e = enrichedUsageObservation(details, base)
		if e != nil {
			return nil, e
		}
		remoteID = r.ID
		observation.State = backgroundState(r.Status)
		observation.Usage = usage
		observation.Document, e = rewrite(event.Response, s.owner.publicID, true, false)
		if e != nil {
			return nil, e
		}
	}
	if event.ResponseID != "" {
		if !validRemoteID(event.ResponseID) || remoteID != "" && event.ResponseID != remoteID {
			return nil, errors.New("background event identity changed")
		}
		remoteID = event.ResponseID
	}
	if remoteID == "" {
		return nil, errors.New("background event lacks binding")
	}
	terminal := terminalStatus(strings.TrimPrefix(event.Type, "response."))
	if terminal {
		var response Response
		if json.Unmarshal(event.Response, &response) != nil || "response."+response.Status != event.Type {
			return nil, errors.New("background terminal state mismatch")
		}
	} else if observation.State == bg.RemoteSucceeded || observation.State == bg.RemoteIncomplete || observation.State == bg.RemoteFailed || observation.State == bg.RemoteCancelled {
		return nil, errors.New("background terminal document in nonterminal event")
	}
	rewritten, e := rewrite(data, s.owner.publicID, false, false)
	if e != nil {
		return nil, e
	}
	// Canonical protocol bytes make identical overlap replay independent of SSE
	// whitespace and fresh AEAD nonces, while retaining all native JSON fields.
	var value interface{}
	d := json.NewDecoder(bytes.NewReader(rewritten))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return nil, errors.New("invalid background event JSON")
	}
	canonical, e := json.Marshal(value)
	if e != nil {
		return nil, e
	}
	wire := append([]byte("event: "+event.Type+"\ndata: "), canonical...)
	wire = append(wire, '\n', '\n')
	if len(wire) > 64<<10 {
		return nil, errors.New("background encoded event exceeds limit")
	}
	observation.RemoteID = remoteID
	observation.Events = []bg.Event{{Sequence: uint64(*event.Sequence), Data: bytes.Clone(wire)}}
	s.owner.mu.Lock()
	s.owner.observation = observation
	s.owner.detailedUsage = enriched
	s.owner.mu.Unlock()
	s.remoteID, s.lastSequence, s.terminal = remoteID, *event.Sequence, terminal
	return []execution.Frame{{Data: wire, Usage: observation.Usage, Complete: terminal}}, nil
}

func validBackgroundEventType(t string) bool {
	if !strings.HasPrefix(t, "response.") || len(t) > 160 {
		return false
	}
	for _, c := range strings.TrimPrefix(t, "response.") {
		if c != '_' && c != '.' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return len(t) > len("response.")
}

// EOF ends this RPC, not the task or its event log. An incomplete frame is not
// acknowledged; the next resume requests it again after the durable cursor.
func (s *backgroundStreamDecoder) End() ([]execution.Frame, error) { return nil, nil }
