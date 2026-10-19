package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"io"
	cfg "minigateway/internal/foundation/contracts/configuration/v1"
	execution "minigateway/internal/foundation/contracts/execution/v2"
	identitycontract "minigateway/internal/foundation/contracts/identity/v1"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	identity "minigateway/internal/foundation/identity"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ws "github.com/coder/websocket"
	"minigateway/internal/access"
	"minigateway/internal/config"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func (f *wsAssemblyFixture) historyHTTP(t *testing.T, key, method, path string, body any, status int) map[string]json.RawMessage {
	t.Helper()
	var data []byte
	if body != nil {
		data = wsAssemblyJSON(body)
	}
	r, err := http.NewRequestWithContext(f.ctx, method, f.front.URL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := f.front.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 512<<10))
	if err != nil || response.StatusCode != status {
		t.Fatalf("history HTTP %s %s: status=%d body=%s error=%v", method, path, response.StatusCode, raw, err)
	}
	var out map[string]json.RawMessage
	if json.Unmarshal(raw, &out) != nil {
		t.Fatalf("invalid history response: %s", raw)
	}
	return out
}

func sendStoredCreate(t *testing.T, f *wsAssemblyFixture, c *ws.Conn, parent string, input any, tools bool) {
	t.Helper()
	request := map[string]any{"type": "response.create", "model": "alias", "store": true, "input": input}
	if parent != "" {
		request["previous_response_id"] = parent
	}
	if tools {
		request["tools"] = []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}}
	}
	if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(request)); err != nil {
		t.Fatal(err)
	}
}

func (f *wsAssemblyFixture) receiveStoredRound(t *testing.T, c *ws.Conn, parent string) string {
	t.Helper()
	return f.receiveStoredTerminal(t, c, parent, "completed")
}

func (f *wsAssemblyFixture) receiveStoredTerminal(t *testing.T, c *ws.Conn, parent, status string) string {
	t.Helper()
	id := ""
	for {
		_, raw, err := c.Read(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type     string
			Response struct {
				ID, Model, Previous string
				Store               bool
			}
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || json.Unmarshal(raw, &event) != nil {
			t.Fatal("invalid WS history event")
		}
		if event.Type == "error" {
			t.Fatalf("unexpected stored round error: %s", raw)
		}
		if response := object["response"]; len(response) > 0 {
			var doc struct {
				ID, Model string
				Store     bool
				Previous  string `json:"previous_response_id"`
			}
			if json.Unmarshal(response, &doc) != nil || !strings.HasPrefix(doc.ID, "resp_") || len(doc.ID) != 37 || doc.Model != "alias" || !doc.Store || doc.Previous != parent {
				t.Fatalf("private native provenance or wrong local history: %s", raw)
			}
			if id != "" && id != doc.ID {
				t.Fatal("public ID changed during one stored round")
			}
			id = doc.ID
		}
		if event.Type == "response."+status {
			// Reading immediately after the terminal frame must work: publication
			// cannot outrun the durable history transaction.
			doc := f.historyHTTP(t, f.key, http.MethodGet, "/v1/responses/"+id, nil, 200)
			if string(doc["id"]) != `"`+id+`"` || string(doc["status"]) != `"`+status+`"` {
				t.Fatal("terminal history was not persisted before delivery", doc)
			}
			f.waitReleased(t)
			return id
		}
	}
}

func historyWire(t *testing.T, observation wsAssemblyObservation, expectedItems int) []map[string]json.RawMessage {
	t.Helper()
	var request struct {
		Model  string
		Store  bool
		Parent json.RawMessage `json:"previous_response_id"`
		Input  []map[string]json.RawMessage
	}
	if json.Unmarshal(observation.JSON, &request) != nil || request.Model != "m" || request.Store || len(request.Parent) != 0 || len(request.Input) != expectedItems {
		t.Fatalf("stored context wire mismatch: %s", observation.JSON)
	}
	return request.Input
}

func TestResponsesWSStoredHistoryHTTPAndReconnectFunctionChain(t *testing.T) {
	f := newWSAssemblyFixtureWithHistory(t, true, true)
	first := f.historyHTTP(t, f.key, http.MethodPost, "/v1/responses", map[string]any{"model": "alias", "input": "HTTP-first"}, 200)
	var parent string
	_ = json.Unmarshal(first["id"], &parent)
	if parent == "" {
		t.Fatal("missing local HTTP response identity")
	}
	historyWire(t, <-f.observations, 1)
	c := f.dial(t)
	sendStoredCreate(t, f, c, parent, "WS-function", true)
	second := f.receiveStoredRound(t, c, parent)
	a := <-f.observations
	input := historyWire(t, a, 3)
	if !bytes.Contains(input[0]["content"], []byte("HTTP-first")) || !bytes.Contains(input[1]["content"], []byte("HTTP-answer")) {
		t.Fatal("HTTP context was not reconstructed in WS")
	}
	if err := c.CloseNow(); err != nil {
		t.Fatal(err)
	}
	// HTTP receives a function call produced over WS and sends its result in
	// full context, without a native previous_response_id.
	third := f.historyHTTP(t, f.key, http.MethodPost, "/v1/responses", map[string]any{"model": "alias", "previous_response_id": second, "input": []any{map[string]any{"type": "function_call_output", "call_id": "call_fixture", "output": "tool-result-雪"}}}, 200)
	b := <-f.observations
	input = historyWire(t, b, 5)
	if string(input[3]["call_id"]) != `"call_fixture"` || string(input[3]["name"]) != `"lookup"` || string(input[4]["output"]) != `"tool-result-雪"` {
		t.Fatalf("HTTP lost WS function associations: %s", b.JSON)
	}
	_ = json.Unmarshal(third["id"], &parent)
	next := f.dial(t)
	sendStoredCreate(t, f, next, parent, "after-reconnect", false)
	fourth := f.receiveStoredRound(t, next, parent)
	d := <-f.observations
	historyWire(t, d, 7)
	if d.Connection == a.Connection || f.attempts.Load() != 4 || f.httpCalls.Load() != 2 {
		t.Fatal("reconnect reused a connection or replayed an old generation")
	}
	var rows []model.ResponseHistory
	if err := f.st.DB().Order("created_at,id").Find(&rows).Error; err != nil || len(rows) != 4 {
		t.Fatal("one history row per generation required", len(rows), err)
	}
	for _, row := range rows {
		if row.Busy != "" || bytes.Contains(row.Ciphertext, []byte("tool-result")) || bytes.Contains(row.Ciphertext, []byte("HTTP-first")) {
			t.Fatal("history state or encryption failed")
		}
	}
	var usage int64
	if err := f.st.DB().Model(&model.UsageInvocation{}).Count(&usage).Error; err != nil || usage != 4 {
		t.Fatal("usage was not one per new generation", usage, err)
	}
	other, err := f.st.IssueToken(f.user, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	f.historyHTTP(t, other.Key, http.MethodGet, "/v1/responses/"+fourth, nil, 404)
	f.historyHTTP(t, f.key, http.MethodGet, "/v1/responses/"+fourth+"/input_items?order=asc", nil, 200)
	f.historyHTTP(t, f.key, http.MethodDelete, "/v1/responses/"+fourth, nil, 200)
	f.historyHTTP(t, f.key, http.MethodGet, "/v1/responses/"+fourth, nil, 404)
	if f.attempts.Load() != 4 {
		t.Fatal("history queries or deletion dispatched inference")
	}
}

func (f *wsAssemblyFixture) storedCreated(t *testing.T, c *ws.Conn) string {
	t.Helper()
	_, data, err := c.Read(f.ctx)
	var event struct {
		Type     string
		Response struct{ ID string }
	}
	if err != nil || json.Unmarshal(data, &event) != nil || event.Type != "response.created" || len(event.Response.ID) != 37 {
		t.Fatalf("missing stored created event: %s %v", data, err)
	}
	return event.Response.ID
}
func (f *wsAssemblyFixture) storedRefusal(t *testing.T, c *ws.Conn) {
	t.Helper()
	for {
		_, data, err := c.Read(f.ctx)
		if err != nil {
			return
		}
		var event struct{ Type string }
		if json.Unmarshal(data, &event) != nil {
			t.Fatal("invalid refusal event")
		}
		if event.Type == "error" {
			return
		}
		t.Fatalf("refused round published an inference event: %s", data)
	}
}
func TestResponsesWSStoredHistoryCancelAndSaveFailureNeverPublishTerminal(t *testing.T) {
	for _, mode := range []string{"cancel", "save failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSAssemblyFixtureWithHistory(t, true, true)
			c := f.dial(t)
			sendStoredCreate(t, f, c, "", "block", false)
			id := f.storedCreated(t, c)
			select {
			case <-f.blocked:
			case <-f.ctx.Done():
				t.Fatal("upstream not blocked")
			}
			if mode == "cancel" {
				_ = c.CloseNow()
			} else {
				var failed atomic.Bool
				const callback = "test:stored-terminal-failure"
				if err := f.st.DB().Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "response_histories" && failed.CompareAndSwap(false, true) {
						tx.AddError(errors.New("injected terminal history failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { f.st.DB().Callback().Update().Remove(callback) })
				close(f.release)
				for {
					_, data, err := c.Read(f.ctx)
					if err != nil {
						break
					}
					var event struct{ Type string }
					_ = json.Unmarshal(data, &event)
					if event.Type == "response.completed" {
						t.Fatal("terminal escaped failed persistence")
					}
					if event.Type == "error" {
						break
					}
				}
				if !failed.Load() {
					t.Fatal("history failure was not exercised")
				}
			}
			f.waitReleased(t)
			deadline := time.Now().Add(3 * time.Second)
			for {
				var row model.ResponseHistory
				err := f.st.DB().Where("id = ?", id).First(&row).Error
				if err != nil {
					t.Fatal(err)
				}
				if row.Busy == "uncertain" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("possibly sent generation lost uncertainty", row.Busy)
				}
				time.Sleep(time.Millisecond)
			}
			f.historyHTTP(t, f.key, http.MethodGet, "/v1/responses/"+id, nil, 409)
			retry := f.dial(t)
			sendStoredCreate(t, f, retry, id, "do-not-replay", false)
			f.storedRefusal(t, retry)
			f.waitReleased(t)
			if f.attempts.Load() != 1 {
				t.Fatal("unknown generation was replayed")
			}
		})
	}
}
func TestResponsesWSStoredHistoryKnownTerminalSurvivesDownstreamFailure(t *testing.T) {
	f := newWSAssemblyFixtureWithHistory(t, true, true)
	trusted, _, ok := identity.NewTokenAuthenticator(f.st).Authenticate(f.ctx, f.key)
	if !ok {
		t.Fatal("authentication failed")
	}
	session, problem := f.runtime.service.Open(trusted, contract.Open{Binding: identitycontract.Binding{UserID: f.user, TokenID: f.keyID}, ConnectionID: "failed-observer"})
	if problem != nil {
		t.Fatal(problem)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	id := ""
	result := session.Execute(trusted, contract.Create{JSON: wsAssemblyJSON(map[string]any{"type": "response.create", "model": "alias", "store": true, "input": "known"})}, func(_ context.Context, frame execution.Frame) (int, error) {
		var event struct {
			Type     string
			Response struct{ ID string }
		}
		_ = json.Unmarshal(frame.Data, &event)
		if event.Response.ID != "" {
			id = event.Response.ID
		}
		if event.Type == "response.completed" {
			return 0, errors.New("observer write failed")
		}
		return len(frame.Data), nil
	})
	if result.Problem == nil || id == "" {
		t.Fatal("failed observer did not fail mechanically")
	}
	doc := f.historyHTTP(t, f.key, http.MethodGet, "/v1/responses/"+id, nil, 200)
	if string(doc["status"]) != `"completed"` || f.attempts.Load() != 1 {
		t.Fatal("known terminal was discarded or resent")
	}
}
func TestResponsesWSStoredHistoryKnownStatusesAndStatelessCompatibility(t *testing.T) {
	for _, status := range []string{"failed", "incomplete", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newWSAssemblyFixtureWithHistory(t, false, true)
			c := f.dial(t)
			sendStoredCreate(t, f, c, "", status, false)
			id := f.receiveStoredTerminal(t, c, "", status)
			next := f.dial(t)
			sendStoredCreate(t, f, next, id, "next", false)
			f.receiveStoredRound(t, next, id)
			if f.attempts.Load() != 2 {
				t.Fatal("known non-success parent was not used exactly once")
			}
		})
	}
	t.Run("stateless", func(t *testing.T) {
		f := newWSAssemblyFixtureWithHistory(t, false, true)
		c := f.dial(t)
		f.send(t, c, "", "first", false)
		id, _ := f.receiveRound(t, c)
		if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(map[string]any{"type": "response.create", "model": "alias", "input": "second", "previous_response_id": id, "store": false})); err != nil {
			t.Fatal(err)
		}
		f.receiveRound(t, c)
		first, second := <-f.observations, <-f.observations
		if first.Connection != second.Connection || second.Previous != id {
			t.Fatal("stateless connection-local continuation changed")
		}
		var rows int64
		f.st.DB().Model(&model.ResponseHistory{}).Count(&rows)
		if rows != 0 {
			t.Fatal("omitted store opted into persistence")
		}
	})
}
func TestResponsesWSStoredHistoryRuntimeRestartAndIndependentBranches(t *testing.T) {
	f := newWSAssemblyFixtureWithHistory(t, true, true)
	c := f.dial(t)
	sendStoredCreate(t, f, c, "", "root", false)
	parent := f.receiveStoredRound(t, c, "")
	<-f.observations
	sendStoredCreate(t, f, c, parent, "same-connection", false)
	parent = f.receiveStoredRound(t, c, parent)
	<-f.observations
	_ = c.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	f.front.Close()
	f.httpRuntime.Close()
	f.startPublicRuntimes(t, access.NewMemoryLimiter(), true)
	f.historyHTTP(t, f.key, http.MethodGet, "/v1/responses/"+parent, nil, 200)
	a, b := f.dial(t), f.dial(t)
	sendStoredCreate(t, f, a, parent, "branch-A", false)
	sendStoredCreate(t, f, b, parent, "branch-B", false)
	first := f.receiveStoredRound(t, a, parent)
	second := f.receiveStoredRound(t, b, parent)
	x, y := <-f.observations, <-f.observations
	if first == second || x.Connection == y.Connection || f.attempts.Load() != 4 {
		t.Fatal("branches shared a result, connection, or replayed generation")
	}
	historyWire(t, x, 5)
	historyWire(t, y, 5)
}

func TestResponsesWSStoredHistoryParentScopeAndCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"cross key", "cross owner", "deleted", "expired", "rotated credential", "revoked key", "model permission"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSAssemblyFixtureWithHistory(t, false, true)
			c := f.dial(t)
			sendStoredCreate(t, f, c, "", "root", false)
			parent := f.receiveStoredRound(t, c, "")
			_ = c.CloseNow()
			key := f.key
			if mode == "cross key" || mode == "cross owner" {
				owner := f.user
				if mode == "cross owner" {
					u, err := f.st.CreateUser(&store.UserInput{Username: "other-history-owner", Password: "fixture-password-123", ModelMode: access.All})
					if err != nil {
						t.Fatal(err)
					}
					owner = u.ID
				}
				token, err := f.st.IssueToken(owner, &store.TokenInput{ModelMode: access.All})
				if err != nil {
					t.Fatal(err)
				}
				key = token.Key
			}
			next, _, err := ws.Dial(f.ctx, "ws"+strings.TrimPrefix(f.front.URL, "http")+"/v1/responses", &ws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = next.CloseNow() })
			switch mode {
			case "deleted":
				f.historyHTTP(t, f.key, http.MethodDelete, "/v1/responses/"+parent, nil, 200)
			case "expired":
				if err := f.st.DB().Model(&model.ResponseHistory{}).Where("id = ?", parent).Update("expires_at", time.Now().Add(-time.Hour).UnixMilli()).Error; err != nil {
					t.Fatal(err)
				}
			case "rotated credential":
				_, change, err := f.configuration.UpdateCredential(f.credentialID, &cfg.CredentialInput{Name: "rotated", APIKey: "changed-history-fixture", Weight: 1})
				if err != nil || !change.Saved {
					t.Fatal("credential rotation failed", err)
				}
			case "revoked key":
				if err := f.st.DeleteToken(f.keyID); err != nil {
					t.Fatal(err)
				}
			case "model permission":
				if _, err := f.st.UpdateToken(f.keyID, &store.TokenInput{ModelMode: access.Allowlist, Models: []string{"other-model"}}); err != nil {
					t.Fatal(err)
				}
			}
			sendStoredCreate(t, f, next, parent, "blocked-child", false)
			f.storedRefusal(t, next)
			f.waitReleased(t)
			if f.attempts.Load() != 1 {
				t.Fatal("invalid parent bypassed current authority or binding")
			}
		})
	}
}

func TestResponsesWSStoredHistoryStartupRejectsMissingSharedOwner(t *testing.T) {
	f := newWSAssemblyFixture(t)
	cfg := config.ResponsesWSConfig{Enabled: true, PersistentHistory: true, Models: []string{"m"}}
	if runtime, err := startResponsesWSRuntime(cfg, f.st, f.gw, access.NewMemoryLimiter()); err == nil || runtime != nil {
		t.Fatal("missing shared history owner accepted")
	}
	if runtime, err := startResponsesWSRuntime(cfg, f.st, f.gw, access.NewMemoryLimiter(), history.Repository(nil)); err == nil || runtime != nil {
		t.Fatal("nil shared history owner accepted")
	}
}
