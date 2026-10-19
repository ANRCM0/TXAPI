package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/core"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v4 "minigateway/internal/foundation/contracts/execution/v4"
	"minigateway/internal/protocol"
)

func TestHTTPStageTimeoutHeadersKeepAttemptAndPublicFacts(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	g, err := newRegisteredGateway(Options{Channels: lifecycleChannels(upstream.URL), RetryTimes: 2,
		TotalTimeout: 2 * time.Second, HTTPStageTimeouts: HTTPStageTimeouts{Headers: 40 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	out := httptest.NewRecorder()
	result := g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(out), Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m","messages":[]}`)})
	if hits.Load() != 1 || len(result.Attempts) != 1 || result.Phase != core.Dispatching || result.TimeoutStage != "headers" || result.ProtocolComplete {
		t.Fatalf("stage changed execution facts: hits=%d result=%+v", hits.Load(), result)
	}
	public := fromCoreResult(result.Attempts[0].Result)
	if public.Error == nil || string(public.Error.Error.Code) != `{"timeout_stage":"headers"}` {
		t.Fatalf("public stage missing: %+v", public)
	}
	var wire struct {
		Error struct {
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &wire); err != nil || string(wire.Error.Code) != `{"timeout_stage":"headers"}` {
		t.Fatalf("HTTP stage missing: %s %v", out.Body.String(), err)
	}
	if routeReason(&result, true) != "timeout_headers" {
		t.Fatal("diagnostic stage missing")
	}
}

func TestHTTPStageTimeoutJSONIgnoresSemanticStreamBudgets(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond)
		io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`)
	}))
	defer upstream.Close()
	g, err := newRegisteredGateway(Options{Channels: lifecycleChannels(upstream.URL), TotalTimeout: time.Second,
		HTTPStageTimeouts: HTTPStageTimeouts{FirstEffectiveOutput: time.Millisecond, StreamIdle: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m","messages":[]}`)})
	if result.Error != nil || !result.ProtocolComplete || result.TimeoutStage != "" {
		t.Fatalf("JSON acquired stream timers: %+v", result)
	}
}

type stageProgressDecoder struct {
	created int
	stream  *stageProgressStream
}

func (d *stageProgressDecoder) Decode(b []byte) ([]byte, *v1.Usage, error) { return b, nil, nil }
func (d *stageProgressDecoder) NewStream() v1.StreamDecoder                { d.created++; return d.stream }

type stageProgressStream struct{ p v4.Progress }

func (s *stageProgressStream) Progress() v4.Progress { return s.p }
func (s *stageProgressStream) DecodeLine([]byte) ([]v1.Frame, error) {
	s.p.CompleteEvents++
	s.p.EffectiveOutput = true
	return nil, nil
}
func (*stageProgressStream) End() ([]v1.Frame, error) { return []v1.Frame{{Complete: true}}, nil }

func TestHTTPStageTimeoutCapabilityCachesSameStreamAndMetadata(t *testing.T) {
	d := &stageProgressDecoder{stream: &stageProgressStream{}}
	in := ToCoreInput(v1.Input{Stream: true, Decoder: d})
	g := &Gateway{httpStageTimeouts: HTTPStageTimeouts{FirstEffectiveOutput: time.Second}}
	if failure := g.prepareHTTPStageTimeouts(&in, &observerOutput{}); failure != nil {
		t.Fatal(failure)
	}
	stream := in.Decoder.NewStream()
	frames, err := stream.DecodeLine(nil)
	if err != nil || d.created != 1 || len(frames) != 1 || len(frames[0].Data) != 0 || !frames[0].EventComplete || !frames[0].EffectiveOutput {
		t.Fatalf("progress lost or stream recreated: %d %+v %v", d.created, frames, err)
	}
	d.stream.p = v4.Progress{}
	if _, err := stream.End(); err == nil {
		t.Fatal("decreasing progress accepted")
	}
	unsupported := core.Input{Stream: true, Decoder: decoderBridge{&summaryDecoder{}}}
	if failure := g.prepareHTTPStageTimeouts(&unsupported, &observerOutput{}); failure == nil {
		t.Fatal("unsupported stream was accepted")
	}
	zero := &Gateway{}
	if failure := zero.prepareHTTPStageTimeouts(&unsupported, &observerOutput{}); failure != nil {
		t.Fatal("zero configuration changed legacy capability behavior")
	}
}

func TestHTTPStageTimeoutRejectsNegativeAndUnsupportedWrite(t *testing.T) {
	for _, stage := range []HTTPStageTimeouts{{Headers: -1}, {FirstEffectiveOutput: -1}, {StreamIdle: -1}, {DownstreamWrite: -1}} {
		if _, err := newRegisteredGateway(Options{Channels: lifecycleChannels("http://unused.invalid"), HTTPStageTimeouts: stage}); err == nil {
			t.Fatalf("negative stage accepted: %+v", stage)
		}
	}
	g := &Gateway{httpStageTimeouts: HTTPStageTimeouts{DownstreamWrite: time.Second}}
	if failure := g.prepareHTTPStageTimeouts(&core.Input{}, &observerOutput{}); failure == nil {
		t.Fatal("unsupported output accepted")
	}
}
