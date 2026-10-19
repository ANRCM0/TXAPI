package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/billing"
	"minigateway/internal/core"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
)

func TestHTTPStageTimeoutSemanticProgressOverSocket(t *testing.T) {
	for _, mode := range []string{"role", "usage", "finish", "partial", "text-idle", "chat-tool", "responses-tool", "responses-empty", "responses-incomplete"} {
		t.Run(mode, func(t *testing.T) {
			var hits, penalties atomic.Int64
			terminal := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			response := strings.HasPrefix(mode, "responses-")
			if response {
				status := "completed"
				if mode == "responses-incomplete" {
					status = "incomplete"
				}
				terminal = "data: {\"type\":\"response." + status + "\",\"response\":{\"id\":\"upstream\",\"object\":\"response\",\"status\":\"" + status + "\",\"output\":[]}}\n\n"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				send := func(s string) { io.WriteString(w, s); w.(http.Flusher).Flush() }
				switch mode {
				case "responses-empty", "responses-incomplete":
					send(terminal)
					<-r.Context().Done()
					return
				case "text-idle":
					send("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
					<-r.Context().Done()
					return
				case "chat-tool":
					send("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"lookup\"}}]}}]}\n\n")
				case "responses-tool":
					send("data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\"}\n\n")
				}
				tick := time.NewTicker(15 * time.Millisecond)
				defer tick.Stop()
				done := time.NewTimer(350 * time.Millisecond)
				defer done.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-done.C:
						if mode == "chat-tool" || mode == "responses-tool" {
							send(terminal)
						}
						<-r.Context().Done()
						return
					case <-tick.C:
						switch mode {
						case "role":
							send("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
						case "usage":
							send("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":0,\"total_tokens\":1}}\n\n")
						case "finish":
							send("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
						case "partial":
							send(" ")
						default:
							send(": heartbeat\n\n")
						}
					}
				}
			}))
			defer upstream.Close()
			stages := HTTPStageTimeouts{FirstEffectiveOutput: 200 * time.Millisecond, StreamIdle: 150 * time.Millisecond}
			if mode == "partial" {
				stages.FirstEffectiveOutput = time.Second
			}
			g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, Channels: lifecycleChannels(upstream.URL), RetryTimes: 2,
				TotalTimeout: 2 * time.Second, HTTPStageTimeouts: stages, HealthReporter: func(_ *dbmodel.Credential, _ int, _ string) { penalties.Add(1) }})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			in := Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m","messages":[],"stream":true}`)}
			if response {
				in = Execution{Operation: protocol.Responses, ResponseID: "public", Body: []byte(`{"model":"m","input":"hello","stream":true,"store":false}`)}
			}
			out := httptest.NewRecorder()
			result := g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(out), in)
			if hits.Load() != 1 || len(result.Attempts) != 1 {
				t.Fatalf("unexpected retry: hits=%d result=%+v", hits.Load(), result)
			}
			want := core.TimeoutFirstEffectiveOutput
			switch mode {
			case "partial", "text-idle":
				want = core.TimeoutStreamIdle
			case "chat-tool", "responses-tool", "responses-empty", "responses-incomplete":
				want = ""
			}
			if result.TimeoutStage != want {
				t.Fatalf("stage=%q want=%q result=%+v", result.TimeoutStage, want, result)
			}
			if want != "" {
				if result.ProtocolComplete || result.Termination != core.Deadline || penalties.Load() != 0 {
					t.Fatalf("timeout facts/health: %+v penalties=%d", result, penalties.Load())
				}
				if mode != "partial" && result.HTTPStatus != 200 {
					t.Fatalf("committed status changed: %+v", result)
				}
			} else if !result.ProtocolComplete || result.Error != nil {
				t.Fatalf("legal terminal failed: %+v", result)
			}
		})
	}
}
