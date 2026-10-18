package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	ws "github.com/coder/websocket"
	cfg "minigateway/internal/foundation/contracts/configuration/v1"
)

func wsProblem(t *testing.T, f *wsAssemblyFixture, c *ws.Conn, expected string) {
	t.Helper()
	_, body, err := c.Read(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		t.Fatal("invalid problem JSON")
	}
	detail, ok := value["error"].(map[string]any)
	if !ok || value["type"] != "error" || detail["code"] != expected || detail["type"] != "txapi_error" {
		t.Fatalf("wrong problem: %s", body)
	}
}
func wsWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("websocket lifecycle did not join")
	}
}

func TestResponsesWSPublicationRetainsOldStreamAndRejectsOldSessionBoundary(t *testing.T) {
	f := newWSAssemblyFixture(t)
	c := f.dial(t)
	f.send(t, c, "", "block", false)
	wsWait(t, f.blocked)
	_, created, err := c.Read(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var initial map[string]any
	_ = json.Unmarshal(created, &initial)
	old := f.resources[0]
	f.failPrepare.Store(true)
	_, change, err := f.configuration.UpdateCredential(f.credentialID, &cfg.CredentialInput{Name: "rotated", APIKey: "upstream-rotated-fixture", Weight: 1})
	if err != nil || !change.Saved || change.Publication.DesiredRevision != 2 || change.Publication.AppliedRevision != 1 || change.Publication.LastError == "" {
		t.Fatalf("save/publication conflated: %+v %v", change, err)
	}
	f.failPrepare.Store(false)
	status := f.configuration.Apply()
	if status.Channels.DesiredRevision != 2 || status.Channels.AppliedRevision != 2 || len(f.resources) != 2 {
		t.Fatal("saved desired snapshot did not publish on retry")
	}
	select {
	case <-old.closed:
		t.Fatal("old stream transport closed while pinned")
	default:
	}
	close(f.release)
	previous, _ := f.receiveRound(t, c)
	select {
	case <-old.closed:
		t.Fatal("persistent session lost its resource lease")
	default:
	}
	f.send(t, c, previous, "next", false)
	wsProblem(t, f, c, "continuation_invalid")
	if _, _, err := c.Read(f.ctx); ws.CloseStatus(err) != ws.StatusPolicyViolation {
		t.Fatalf("wrong continuation close code: %v", err)
	}
	wsWait(t, old.closed)
	next := f.dial(t)
	f.send(t, next, "", "fresh", false)
	f.receiveRound(t, next)
	a, b := <-f.observations, <-f.observations
	if a.Connection == b.Connection || a.Key != "upstream-main-fixture" || b.Key != "upstream-rotated-fixture" || f.attempts.Load() != 2 || f.httpCalls.Load() != 0 {
		t.Fatal("target was replayed, rebound or routed through HTTP")
	}
}

func TestResponsesWSDisconnectCancelsActiveGenerationBeforeDependencyClose(t *testing.T) {
	f := newWSAssemblyFixture(t)
	c := f.dial(t)
	f.send(t, c, "", "block", false)
	wsWait(t, f.blocked)
	_ = c.CloseNow()
	f.waitReleased(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DB().Exec("SELECT 1").Error; err != nil {
		t.Fatal("shared store closed during WS drain")
	}
	if snapshot := f.capacity.Snapshot(); snapshot.Requests != 0 || snapshot.Attempts != 0 {
		t.Fatal(snapshot)
	}
	if f.attempts.Load() != 1 {
		t.Fatal("disconnect replayed the generation")
	}
}

func TestResponsesWSRevocationAndForeignContinuationDoNotDispatch(t *testing.T) {
	f := newWSAssemblyFixture(t)
	first := f.dial(t)
	f.send(t, first, "", "hello", false)
	id, _ := f.receiveRound(t, first)
	foreign := f.dial(t)
	f.send(t, foreign, id, "foreign", false)
	wsProblem(t, f, foreign, "continuation_invalid")
	if err := f.st.DeleteOwnedToken(f.user, f.keyID); err != nil {
		t.Fatal(err)
	}
	f.send(t, first, id, "revoked", false)
	wsProblem(t, f, first, "unauthorized")
	if f.attempts.Load() != 1 || f.connections.Load() != 1 || f.httpCalls.Load() != 0 {
		t.Fatal("revoked or foreign continuation reached upstream")
	}
}

func TestResponsesWSBinaryAndOversizedFramesFailClosed(t *testing.T) {
	for _, kind := range []string{"binary", "large"} {
		t.Run(kind, func(t *testing.T) {
			f := newWSAssemblyFixture(t)
			c := f.dial(t)
			if kind == "binary" {
				if err := c.Write(f.ctx, ws.MessageBinary, []byte("not a request")); err != nil {
					t.Fatal(err)
				}
				wsProblem(t, f, c, "unsupported_frame")
				if _, _, err := c.Read(f.ctx); ws.CloseStatus(err) != ws.StatusUnsupportedData {
					t.Fatalf("wrong binary close: %v", err)
				}
			} else {
				if err := c.Write(f.ctx, ws.MessageText, make([]byte, (256<<10)+1)); err != nil {
					t.Fatal(err)
				}
				if _, _, err := c.Read(f.ctx); ws.CloseStatus(err) != ws.StatusMessageTooBig {
					t.Fatalf("wrong large-message close: %v", err)
				}
			}
			if f.attempts.Load() != 0 {
				t.Fatal("invalid frame reached upstream")
			}
		})
	}
}
