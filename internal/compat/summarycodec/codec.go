// Package summarycodec demonstrates a pure L2 operation/codec registration.
// It has no reference to the L3 feature, network client or financial engine.
package summarycodec

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	jsonvalue "minigateway/internal/foundation/contracts/json/v1"
)

type payload struct {
	Model string `json:"model"`
	Text  string `json:"text"`
}

type response struct {
	Summary string           `json:"summary"`
	Usage   *execution.Usage `json:"usage"`
}

func parse[T payload | response](body []byte, into *T) error {
	if err := jsonvalue.Validate(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into)
}

type request struct {
	id   execution.Operation
	body []byte
	data payload
}

func (r request) Operation() execution.Operation { return r.id }
func (r request) Body() []byte                   { return bytes.Clone(r.body) }
func (r request) Model() string                  { return r.data.Model }
func (r request) Stream() bool                   { return false }
func (r request) Requirements() execution.Requirements {
	return execution.Requirements{Operation: r.id, Transport: execution.HTTP}
}
func (r request) LegacyMaxTokens() int { return 0 }
func (r request) PromptText() string   { return r.data.Text }
func (r request) AffinityText() string { return r.data.Text }
func (r request) CompletionText(body []byte) string {
	var out response
	_ = json.Unmarshal(body, &out)
	return out.Summary
}

// Registration takes the operation from assembly, rather than importing L3.
func Registration(id execution.Operation) execution.OperationRegistration {
	return execution.OperationRegistration{ID: id, Version: execution.Version, Parse: func(body []byte) (execution.Request, error) {
		if len(body) > 4096 {
			return nil, errors.New("summary input exceeds 4096 bytes")
		}
		var data payload
		if err := parse(body, &data); err != nil {
			return nil, err
		}
		if strings.TrimSpace(data.Model) == "" || strings.TrimSpace(data.Text) == "" || len(data.Text) > 1024 {
			return nil, errors.New("summary requires a model and 1 to 1024 bytes of text")
		}
		return request{id: id, body: bytes.Clone(body), data: data}, nil
	}}
}

type adapter struct{ operation execution.Operation }

func Protocol(id string, operation execution.Operation) execution.ProtocolRegistration {
	return execution.ProtocolRegistration{ID: id, Version: execution.Version, Adapter: adapter{operation: operation}}
}

func (a adapter) Name() string { return "summary-example" }
func (a adapter) Supports(r execution.Request) bool {
	return r.Operation() == a.operation && !r.Stream() && r.Requirements().Transport == execution.HTTP
}
func (a adapter) CheckOutputLimit([]byte, int, string) error {
	return errors.New("summary has no verified strict billing profile")
}
func (a adapter) Prepare(r execution.Request, target execution.Target) (execution.Input, *execution.Error) {
	var data payload
	if err := parse(r.Body(), &data); err != nil {
		return execution.Input{}, &execution.Error{StatusCode: 400, Error: execution.ErrorDetail{Message: err.Error()}}
	}
	data.Model = target.ActualModel
	body, err := json.Marshal(data)
	if err != nil {
		return execution.Input{}, &execution.Error{StatusCode: 400, Error: execution.ErrorDetail{Message: err.Error()}}
	}
	return execution.Input{Method: http.MethodPost, URL: strings.TrimRight(target.BaseURL, "/") + "/summary",
		Header: http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + target.APIKey}},
		Body:   body, Replayable: true, Decoder: &decoder{}}, nil
}

type decoder struct{ used bool }

func (d *decoder) Decode(body []byte) ([]byte, *execution.Usage, error) {
	if d.used {
		return nil, nil, errors.New("summary decoder cannot be reused")
	}
	d.used = true
	var out response
	if err := parse(body, &out); err != nil {
		return nil, nil, err
	}
	if out.Summary == "" || out.Usage == nil || out.Usage.PromptTokens < 0 || out.Usage.CompletionTokens < 0 || out.Usage.TotalTokens < 0 ||
		out.Usage.TotalTokens != out.Usage.PromptTokens+out.Usage.CompletionTokens {
		return nil, nil, errors.New("invalid summary completion or usage")
	}
	return bytes.Clone(body), out.Usage, nil
}
func (d *decoder) NewStream() execution.StreamDecoder { return nil }
