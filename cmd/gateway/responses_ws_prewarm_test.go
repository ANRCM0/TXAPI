package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	ws "github.com/coder/websocket"
	"minigateway/internal/access"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func sendPrewarm(t *testing.T, f *wsAssemblyFixture, c *ws.Conn, input string) {
	t.Helper()
	if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(map[string]any{"type": "response.create", "model": "alias", "input": input, "generate": false})); err != nil {
		t.Fatal(err)
	}
}

// Connection-local warmup ownership follows codex-proxy-rs's independent
// downstream lane scenario; every round still uses TXAPI's existing authority.
func TestResponsesWSPrewarmContinuationRechecksAuthorityAndUsage(t *testing.T) {
	for _, mode := range []string{"known zero", "unknown", "foreign connection", "revoked key", "model permission"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSAssemblyFixtureWithUsage(t, true)
			c := f.dial(t)
			input := "warmup"
			if mode == "unknown" {
				input = "unknown"
			}
			sendPrewarm(t, f, c, input)
			parent, kinds := f.receiveRound(t, c)
			if len(kinds) != 2 || kinds[0] != "response.created" || kinds[1] != "response.completed" {
				t.Fatal("warmup synthesized content", kinds)
			}
			problem := ""
			switch mode {
			case "unknown":
				problem = "invalid_request"
			case "foreign connection":
				c, problem = f.dial(t), "continuation_invalid"
			case "revoked key":
				if err := f.st.DeleteOwnedToken(f.user, f.keyID); err != nil {
					t.Fatal(err)
				}
				problem = "unauthorized"
			case "model permission":
				if _, err := f.st.UpdateToken(f.keyID, &store.TokenInput{ModelMode: access.Allowlist, Models: []string{"other-model"}}); err != nil {
					t.Fatal(err)
				}
				problem = "unauthorized"
			}
			f.send(t, c, parent, "next", false)
			want := int64(1)
			if problem == "" {
				f.receiveRound(t, c)
				want = 2
			} else {
				wsProblem(t, f, c, problem)
				f.waitReleased(t)
			}
			if f.attempts.Load() != want || f.httpCalls.Load() != 0 || f.connections.Load() != 1 {
				t.Fatal("prewarm replayed or bypassed authority")
			}
			// Admission release precedes the deferred usage recorder. Wait for
			// the durable seal rather than reading a still-prepared next round.
			deadline := time.Now().Add(3 * time.Second)
			for {
				var sealed int64
				if err := f.st.DB().Model(&model.UsageInvocation{}).Where("state=?", "sealed").Count(&sealed).Error; err != nil {
					t.Fatal(err)
				}
				if sealed == want {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("prewarm usage did not seal: %d/%d", sealed, want)
				}
				time.Sleep(time.Millisecond)
			}
			var rows []model.UsageAttemptReceipt
			if err := f.st.DB().Find(&rows).Error; err != nil || len(rows) != int(want) {
				t.Fatal("prewarm usage lost its known/unknown distinction", rows, err)
			}
			zero, unknown := 0, 0
			for _, row := range rows {
				if !row.MayDispatch {
					t.Fatal("actual WS create lost its dispatch fact")
				}
				if row.TotalTokens == nil {
					unknown++
				} else if *row.TotalTokens == 0 {
					zero++
				}
			}
			if mode == "unknown" && (zero != 0 || unknown != 1) || mode != "unknown" && (zero != 1 || unknown != 0) {
				t.Fatal("prewarm known zero or unknown changed", zero, unknown)
			}
		})
	}
}

func TestResponsesWSPrewarmDisconnectRetainsUnknownAndJoins(t *testing.T) {
	f := newWSAssemblyFixtureWithUsage(t, true)
	c := f.dial(t)
	sendPrewarm(t, f, c, "block")
	wsWait(t, f.blocked)
	_ = c.CloseNow()
	f.waitReleased(t)
	var rows []model.UsageAttemptReceipt
	if err := f.st.DB().Find(&rows).Error; err != nil || len(rows) != 1 || !rows[0].MayDispatch || rows[0].TotalTokens != nil {
		t.Fatal("canceled warmup became known zero", rows, err)
	}
	if f.attempts.Load() != 1 || f.httpCalls.Load() != 0 {
		t.Fatal("canceled warmup replayed")
	}
}

func TestResponsesWSReasoningHistorySurvivesHTTPAndNewConnection(t *testing.T) {
	f := newWSAssemblyFixtureWithHistory(t, true, true)
	c := f.dial(t)
	sendStoredCreate(t, f, c, "", "reasoning", false)
	parent := f.receiveStoredRound(t, c, "")
	historyWire(t, <-f.observations, 1)
	_ = c.CloseNow()
	response := f.historyHTTP(t, f.key, http.MethodPost, "/v1/responses", map[string]any{"model": "alias", "previous_response_id": parent, "input": "http-next"}, 200)
	input := historyWire(t, <-f.observations, 4)
	if !bytes.Contains(input[1]["encrypted_content"], []byte("fixture-history-opaque")) || string(input[1]["future"]) != "true" {
		t.Fatal("HTTP lost the opaque reasoning output")
	}
	_ = json.Unmarshal(response["id"], &parent)
	next := f.dial(t)
	sendStoredCreate(t, f, next, parent, "ws-next", false)
	f.receiveStoredRound(t, next, parent)
	input = historyWire(t, <-f.observations, 6)
	if string(input[1]["encrypted_content"]) != `"fixture-history-opaque"` || string(input[1]["future"]) != "true" || f.attempts.Load() != 3 || f.httpCalls.Load() != 1 || f.connections.Load() != 2 {
		t.Fatal("new connection duplicated or lost reasoning context")
	}
	var rows []model.ResponseHistory
	if err := f.st.DB().Find(&rows).Error; err != nil || len(rows) != 3 {
		t.Fatal("history generation count", err)
	}
	for _, row := range rows {
		if bytes.Contains(row.Ciphertext, []byte("fixture-history-opaque")) {
			t.Fatal("opaque content bypassed history encryption")
		}
	}
}
