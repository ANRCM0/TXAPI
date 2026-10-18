package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"minigateway/internal/billing"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/protocol"
)

const summarize execution.Operation = "test.summarize"

type summaryRequest struct{ body []byte }

func (r summaryRequest) Operation() execution.Operation { return summarize }
func (r summaryRequest) Body() []byte                   { return bytes.Clone(r.body) }
func (r summaryRequest) Model() string                  { return "m" }
func (r summaryRequest) Stream() bool                   { return false }
func (r summaryRequest) Requirements() execution.Requirements {
	return execution.Requirements{Operation: summarize, Transport: execution.HTTP}
}
func (r summaryRequest) LegacyMaxTokens() int              { return 0 }
func (r summaryRequest) AffinityText() string              { return "" }
func (r summaryRequest) PromptText() string                { return "native summary input" }
func (r summaryRequest) CompletionText(body []byte) string { return string(body) }

func summaryRegistration() execution.OperationRegistration {
	return execution.OperationRegistration{ID: summarize, Version: execution.Version,
		Parse: func(body []byte) (execution.Request, error) { return summaryRequest{bytes.Clone(body)}, nil }}
}

type summaryAdapter struct {
	prepared     atomic.Int64
	support      bool
	badTransport bool
}

func (a *summaryAdapter) Name() string { return "summary-fixture" }
func (a *summaryAdapter) Supports(r execution.Request) bool {
	return a.support && r.Operation() == summarize
}
func (a *summaryAdapter) CheckOutputLimit([]byte, int, string) error {
	return errors.New("no strict proof")
}
func (a *summaryAdapter) Prepare(r execution.Request, target execution.Target) (execution.Input, *execution.Error) {
	id := a.prepared.Add(1)
	return execution.Input{URL: target.BaseURL, Body: r.Body(), Header: http.Header{"X-Codec-Session": {fmt.Sprint(id)}},
		Stream: a.badTransport, Replayable: true, Decoder: &summaryDecoder{id: id}}, nil
}

type summaryDecoder struct {
	id   int64
	used bool
}

func (d *summaryDecoder) Decode(body []byte) ([]byte, *execution.Usage, error) {
	if d.used {
		return nil, nil, errors.New("decoder reused across attempts")
	}
	d.used = true
	return bytes.Clone(body), &execution.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}, nil
}
func (d *summaryDecoder) NewStream() execution.StreamDecoder { return nil }

func TestRegisteredOperationRunsWithoutBuiltinDispatchChanges(t *testing.T) {
	var requests atomic.Int64
	body := []byte(`{"model":"m","native_input":{"summarize":"keep this payload"}}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, body) || r.Header.Get("X-Codec-Session") == "" {
			t.Error("registered codec lost native data or per-attempt session")
		}
		requests.Add(1)
		_, _ = w.Write([]byte(`{"native_output":"summary"}`))
	}))
	defer upstream.Close()
	adapter := &summaryAdapter{support: true}
	operations := []execution.OperationRegistration{summaryRegistration()}
	protocols := []execution.ProtocolRegistration{{ID: "openai", Version: execution.Version, Adapter: adapter}}
	g, err := New(Options{Operations: operations, Protocols: protocols, Channels: lifecycleChannels(upstream.URL), BillingMode: billing.ModeDisabled})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	// Construction owns the registration values even if the caller reuses slices.
	operations[0].ID, operations[0].Parse = "changed", nil
	protocols[0].ID, protocols[0].Adapter = "changed", nil
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			out := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: summarize, Body: body})
			if out.StatusCode != 200 || !out.ProtocolComplete || out.Operation != summarize || out.Usage == nil || out.Usage.TotalTokens != 3 || len(out.Attempts) != 1 {
				t.Errorf("registered execution lost facts: %+v", out)
			}
		}()
	}
	group.Wait()
	if requests.Load() != 12 || adapter.prepared.Load() != 12 {
		t.Fatalf("requests=%d sessions=%d", requests.Load(), adapter.prepared.Load())
	}
	for _, id := range []execution.Operation{execution.Chat, "unregistered"} {
		out := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: id, Body: body})
		if out.StatusCode != 400 || len(out.Attempts) != 0 || out.Error.Error.Type != "unsupported_operation" {
			t.Fatalf("unknown operation fell back: %+v", out)
		}
	}
	if requests.Load() != 12 {
		t.Fatal("unknown operation dispatched")
	}
}

func TestRegisteredOperationRejectsMissingCapabilityAndUnsafePreparation(t *testing.T) {
	for _, mode := range []string{"capability", "transport", "strict"} {
		t.Run(mode, func(t *testing.T) {
			adapter := &summaryAdapter{support: mode != "capability", badTransport: mode == "transport"}
			var requests atomic.Int64
			opts := Options{Operations: []execution.OperationRegistration{summaryRegistration()},
				Protocols: []execution.ProtocolRegistration{{ID: "openai", Version: execution.Version, Adapter: adapter}},
				Channels:  lifecycleChannels("http://fixture.test"), BillingMode: billing.ModeDisabled,
				Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					requests.Add(1)
					panic("unsupported operation reached network")
				})}}
			if mode == "strict" {
				opts.BillingMode, opts.StrictBilling = billing.ModeStrict, forbiddenStrictBilling{}
			}
			g, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			out := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: summarize, Body: []byte(`{"model":"m"}`)})
			if out.StatusCode != 400 || requests.Load() != 0 || out.Phase != "not_dispatched" {
				t.Fatalf("mode=%s: %+v", mode, out)
			}
		})
	}
}

func TestRegistryRejectsIncompleteDuplicateAndTypedNilRegistrations(t *testing.T) {
	var typedNil *summaryAdapter
	for _, change := range []func(*Options){
		func(o *Options) { o.Operations = nil },
		func(o *Options) { o.Protocols = nil },
		func(o *Options) { o.Operations[0].Version = "2" },
		func(o *Options) { o.Operations[0].Parse = nil },
		func(o *Options) { o.Operations = append(o.Operations, o.Operations[0]) },
		func(o *Options) { o.Operations[0].OpaqueErrors = true },
		func(o *Options) { o.Protocols[0].ID = "" },
		func(o *Options) { o.Protocols[0].Version = "2" },
		func(o *Options) { o.Protocols[0].Adapter = typedNil },
		func(o *Options) { o.Protocols = append(o.Protocols, o.Protocols[0]) },
	} {
		opts := Options{Operations: protocol.Operations(), Protocols: protocol.Protocols(nil)}
		change(&opts)
		if g, err := New(opts); err == nil || g != nil {
			if g != nil {
				g.Close()
			}
			t.Fatal("invalid startup registration accepted")
		}
	}
}

func TestRegisteredParserRejectsRoutingMismatchAndCopiesPayload(t *testing.T) {
	registration := summaryRegistration()
	var callbackBody []byte
	registration.Parse = func(body []byte) (execution.Request, error) { callbackBody = body; return summaryRequest{body}, nil }
	g, err := New(Options{Operations: []execution.OperationRegistration{registration}, Protocols: protocol.Protocols(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	body := []byte(`{"model":"m","opaque":"original"}`)
	r, err := g.ParseRequest(summarize, body)
	if err != nil {
		t.Fatal(err)
	}
	callbackBody[0], body[0] = '!', '!'
	returned := r.Body()
	returned[0] = '!'
	if !json.Valid(r.Body()) {
		t.Fatal("caller or parser scratch buffer changed owned request")
	}
	registration.Parse = func(body []byte) (execution.Request, error) { return protocol.ParseRequest(execution.Chat, body) }
	bad, err := New(Options{Operations: []execution.OperationRegistration{registration}, Protocols: protocol.Protocols(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	if _, err := bad.ParseRequest(summarize, []byte(`{"model":"m"}`)); err == nil {
		t.Fatal("mismatched registered operation accepted")
	}
}

var _ execution.Parser = (*Gateway)(nil)
var _ execution.ModelCatalog = (*PublicExecutor)(nil)
