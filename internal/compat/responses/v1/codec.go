// Package responses exposes copied native projections without codec implementation types.
package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/protocol"
	native "minigateway/internal/protocol/responses"
	"strings"
)

const Version = "1"

type Request struct {
	Model      string
	Stream     bool
	Store      bool
	validation error
}

func (r Request) ValidateHTTP() error { return r.validation }
func (r Request) StoreEnabled() bool  { return r.Store }
func ParseRequest(body []byte) (Request, error) {
	r, err := native.ParseRequest(body)
	if err != nil {
		return Request{}, err
	}
	return Request{Model: r.Model, Stream: r.Stream, Store: r.StoreEnabled(), validation: r.ValidateHTTP()}, nil
}
func ParseHTTP(body []byte) (Request, error) {
	r, err := protocol.ParseRequest(protocol.Responses, body)
	if err != nil {
		return Request{}, err
	}
	n, _ := r.Responses()
	return Request{Model: r.Model(), Stream: r.Stream(), Store: n.StoreEnabled()}, nil
}

type Response struct{ Output json.RawMessage }

func ParseResponse(body []byte) (Response, *execution.Usage, error) {
	r, u, err := native.ParseResponse(body)
	if u != nil {
		copy := *u
		u = &copy
	}
	return Response{Output: bytes.Clone(r.Output)}, u, err
}

func TerminalObject(body []byte, stream bool) (json.RawMessage, error) {
	if !stream {
		r, _, err := ParseResponse(body)
		if err != nil || r.Output == nil {
			return nil, errors.New("complete native response unavailable")
		}
		return bytes.Clone(body), nil
	}
	var terminal json.RawMessage
	for _, block := range bytes.Split(body, []byte("\n\n")) {
		var data []string
		for _, line := range bytes.Split(block, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				data = append(data, strings.TrimSpace(string(line[5:])))
			}
		}
		var event struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal([]byte(strings.Join(data, "\n")), &event) == nil && (event.Type == "response.completed" || event.Type == "response.incomplete" || event.Type == "response.failed" || event.Type == "response.cancelled") {
			terminal = event.Response
		}
	}
	r, _, err := ParseResponse(terminal)
	if err != nil || r.Output == nil {
		return nil, errors.New("complete native response unavailable")
	}
	return terminal, nil
}

func RewriteProvenance(p []byte, contentType, parent, conversation string) []byte {
	patch := func(raw []byte) []byte {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil || obj == nil {
			return raw
		}
		var kind string
		_ = json.Unmarshal(obj["object"], &kind)
		if kind == "response" {
			if parent != "" {
				obj["previous_response_id"], _ = json.Marshal(parent)
			}
			if conversation != "" {
				obj["conversation"], _ = json.Marshal(map[string]string{"id": conversation})
			}
		} else if response := obj["response"]; len(response) > 0 {
			var inner map[string]json.RawMessage
			if json.Unmarshal(response, &inner) == nil {
				if parent != "" {
					inner["previous_response_id"], _ = json.Marshal(parent)
				}
				if conversation != "" {
					inner["conversation"], _ = json.Marshal(map[string]string{"id": conversation})
				}
				obj["response"], _ = json.Marshal(inner)
			}
		} else {
			return raw
		}
		out, err := json.Marshal(obj)
		if err != nil {
			return raw
		}
		return out
	}
	if strings.Contains(contentType, "event-stream") {
		lines := bytes.Split(p, []byte("\n"))
		for i, line := range lines {
			if bytes.HasPrefix(line, []byte("data:")) {
				lines[i] = append([]byte("data: "), patch(bytes.TrimSpace(line[5:]))...)
			}
		}
		return bytes.Join(lines, []byte("\n"))
	}
	return patch(p)
}

func ReplayMetadata(in execution.ResponseMetadata) execution.ResponseMetadata {
	out := in
	out.Values = maps.Clone(in.Values)
	delete(out.Values, "X-Request-Id")
	delete(out.Values, "X-Request-ID")
	return out
}

type Association struct {
	PriceVersion string
	BillingID    string
}

func Associations(in execution.ResponseMetadata) Association {
	return Association{PriceVersion: in.Values["X-TXAPI-Price-Version"], BillingID: in.Values["X-TXAPI-Billing-ID"]}
}
