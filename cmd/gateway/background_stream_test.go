package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	identity "minigateway/internal/foundation/identity"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func nativeStreamEvent(seq int, status string) string {
	typ := "response.created"
	if status == "completed" {
		typ = "response.completed"
	}
	return fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"sequence_number\":%d,\"response\":{\"id\":\"resp_provider\",\"object\":\"response\",\"status\":%q,\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n", typ, typ, seq, status)
}

func TestNativeBackgroundSSEObserverDisconnectRestartAndExplicitCursorReplay(t *testing.T) {
	var posts, resumes atomic.Int64
	finish := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-private-secret" {
			t.Error("wrong original credential")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Method == "POST" {
			posts.Add(1)
			var body struct{ Stream, Background bool }
			if json.NewDecoder(r.Body).Decode(&body) != nil || !body.Stream || !body.Background {
				t.Error("native stream flag changed")
			}
			_, _ = fmt.Fprint(w, nativeStreamEvent(0, "queued"))
			return
		}
		if r.Method != "GET" || r.URL.Path != "/v1/responses/resp_provider" || r.URL.Query().Get("stream") != "true" || r.URL.Query().Get("starting_after") != "0" {
			t.Error("wrong native resume", r.Method, r.URL)
			http.Error(w, "invalid", 400)
			return
		}
		resumes.Add(1)
		select {
		case <-finish:
			_, _ = fmt.Fprint(w, nativeStreamEvent(1, "completed"))
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	h := newBackgroundHarness(t, upstream.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, e := http.NewRequestWithContext(ctx, "POST", h.front.URL+"/v1/responses", strings.NewReader(`{"model":"m","background":true,"stream":true,"store":false,"input":"private prompt"}`))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+h.key.Key)
	r.Header.Set("Idempotency-Key", "event-observer-restart")
	response, e := h.front.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-TXAPI-Background-Retention") != "temporary" {
		t.Fatal("SSE privacy headers lost", response.Status, response.Header)
	}
	reader := bufio.NewReader(response.Body)
	var first strings.Builder
	for {
		line, e := reader.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}
		first.WriteString(line)
		if line == "\n" {
			break
		}
	}
	if !strings.Contains(first.String(), `"sequence_number":0`) || strings.Contains(first.String(), "resp_provider") || strings.Contains(first.String(), "[DONE]") {
		t.Fatal("first native frame was fabricated or leaked binding", first.String())
	}
	id := response.Header.Get("X-TXAPI-Response-ID")
	if id == "" {
		t.Fatal("public response ID missing")
	}
	_ = response.Body.Close()
	cancel()
	h.waitStage(id, "remote_active")
	h.reopen()
	close(finish)
	replay := h.call("GET", "/v1/responses/"+id+"?stream=true&starting_after=0", "", "", "")
	if replay.code != 200 || strings.Contains(string(replay.body), `"sequence_number":0`) || !strings.Contains(string(replay.body), `"sequence_number":1`) || !strings.Contains(string(replay.body), "response.completed") || replay.header.Get("Cache-Control") != "no-store" {
		t.Fatal("explicit zero did not skip only sequence zero", replay.code, string(replay.body), replay.header)
	}
	all := h.call("GET", "/v1/responses/"+id+"?stream=true", "", "", "")
	if all.code != 200 || !strings.Contains(string(all.body), `"sequence_number":0`) || !strings.Contains(string(all.body), `"sequence_number":1`) || posts.Load() != 1 || resumes.Load() < 1 {
		t.Fatal("restart resubmitted or lost events", all.code, string(all.body), posts.Load())
	}
	h.waitStage(id, "terminal")
	got := h.call("GET", "/v1/responses/"+id, "", "", "")
	if got.code != 200 || !strings.Contains(string(got.body), `"status":"completed"`) || strings.Contains(string(got.body), "resp_provider") {
		t.Fatal("terminal JSON not preserved", got.code, string(got.body))
	}
	if bad := h.call("GET", "/v1/responses/"+id+"?stream=true&starting_after=2", "", "", ""); bad.code != 409 {
		t.Fatal("future cursor did not fail explicitly", bad.code)
	}
	other, e := h.st.IssueToken(h.key.UserID, &store.TokenInput{Name: "other-stream-key", ModelMode: access.All})
	if e != nil {
		t.Fatal(e)
	}
	if hidden := h.callAs(other.Key, "GET", "/v1/responses/"+id+"?stream=true", "", "", ""); hidden.code != 404 {
		t.Fatal("cross-key stream enumerated content", hidden.code)
	}
}

type backgroundShortWriter struct {
	header            http.Header
	writes, deadlines int
}

func (w *backgroundShortWriter) Header() http.Header              { return w.header }
func (w *backgroundShortWriter) WriteHeader(int)                  {}
func (w *backgroundShortWriter) Write(p []byte) (int, error)      { w.writes++; return len(p) / 2, nil }
func (w *backgroundShortWriter) Flush()                           {}
func (w *backgroundShortWriter) SetWriteDeadline(time.Time) error { w.deadlines++; return nil }

func TestNativeBackgroundSSEFailedObserverDoesNotBackpressureOrCancelDurableOwner(t *testing.T) {
	var posts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, nativeStreamEvent(0, "queued")+nativeStreamEvent(1, "completed"))
	}))
	defer upstream.Close()
	h := newBackgroundHarness(t, upstream.URL)
	ctx, _, ok := identity.NewTokenAuthenticator(h.st).Authenticate(context.Background(), h.key.Key)
	if !ok {
		t.Fatal("auth failed")
	}
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","background":true,"stream":true,"store":false,"input":"private prompt"}`)).WithContext(ctx)
	w := &backgroundShortWriter{header: make(http.Header)}
	h.runtime.Handler().ServeHTTP(w, r)
	id := w.header.Get("X-TXAPI-Response-ID")
	row := h.waitStage(id, "terminal")
	if w.writes != 1 || w.deadlines == 0 || row.EventCount != 2 || row.ReplayPending || posts.Load() != 1 || w.header.Get("Cache-Control") != "no-store" {
		t.Fatal("observer write failure changed durable execution", w, row, posts.Load())
	}
	var events []model.BackgroundEvent
	if e := h.st.DB().Where("task_id = ?", row.ID).Find(&events).Error; e != nil || len(events) != 2 {
		t.Fatal("events not persisted before observer failure", e)
	}
	all := h.call("GET", "/v1/responses/"+id+"?stream=true", "", "", "")
	if all.code != 200 || !strings.Contains(string(all.body), "response.completed") {
		t.Fatal("other observer cannot recover", all.code, string(all.body))
	}
}

func TestNativeBackgroundSSEShutdownInterruptsObserverAndJoinsWithoutFakeNativeCancel(t *testing.T) {
	var posts, cancels atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			cancels.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Method == "POST" {
			posts.Add(1)
			_, _ = fmt.Fprint(w, nativeStreamEvent(0, "queued"))
			return
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()
	h := newBackgroundHarness(t, upstream.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", h.front.URL+"/v1/responses", strings.NewReader(`{"model":"m","background":true,"stream":true,"store":false,"input":"x"}`))
	r.Header.Set("Authorization", "Bearer "+h.key.Key)
	response, e := h.front.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for {
		line, e := reader.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}
		if line == "\n" {
			break
		}
	}
	id := response.Header.Get("X-TXAPI-Response-ID")
	start := time.Now()
	h.runtime.Stop()
	h.runtime.Close()
	h.runtime = nil
	if _, e = io.ReadAll(reader); e != nil || time.Since(start) > 3*time.Second {
		t.Fatal("observer or owner failed to join", e)
	}
	var row model.BackgroundJob
	if e = h.st.DB().First(&row, "public_id = ?", id).Error; e != nil {
		t.Fatal(e)
	}
	if row.Stage != "remote_active" || posts.Load() != 1 || cancels.Load() != 0 {
		t.Fatal("shutdown invented native terminal/cancel", row.Stage, posts.Load(), cancels.Load())
	}
}
