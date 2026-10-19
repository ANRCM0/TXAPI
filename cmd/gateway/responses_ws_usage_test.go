package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	"minigateway/internal/store"
)

func TestResponsesWSUsageRealSocketAndSessionQuery(t *testing.T) {
	f := newWSAssemblyFixtureWithUsage(t, true)
	connection := f.dial(t)
	previous := ""
	for round := 0; round < 2; round++ {
		f.send(t, connection, previous, "hello", round == 1)
		previous, _ = f.receiveRound(t, connection)
	}
	session, _, err := f.st.Login("native-runtime", "fixture-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateUser(&store.UserInput{Username: "usage-other", Password: "fixture-password-123", ModelMode: "all"}); err != nil {
		t.Fatal(err)
	}
	other, _, err := f.st.Login("usage-other", "fixture-password-123")
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, secret string, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(f.ctx, "GET", f.front.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		response, err := f.front.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 65536))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want || want == 200 && response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("query got %d: %s", response.StatusCode, body)
		}
		return body
	}
	var page history.Page
	body := request("/api/me/usage-records", session, 200)
	if err := json.Unmarshal(body, &page); err != nil || len(page.Data) != 2 || !page.Enabled {
		t.Fatalf("page: %s %v", body, err)
	}
	for _, row := range page.Data {
		if row.Transport != "websocket" || row.UserID != f.user || row.TokenID != f.keyID || row.Host.ProtocolStatus != "completed" || row.Host.HTTPStatus != 0 || row.AttemptCount != 1 || row.State != "sealed" {
			t.Fatalf("wrong native row: %+v", row)
		}
		body = request("/api/me/usage-records/"+row.ID, session, 200)
		var wrapped struct{ Data history.Detail }
		if err := json.Unmarshal(body, &wrapped); err != nil {
			t.Fatal(err)
		}
		d := wrapped.Data
		if len(d.Attempts) != 1 || d.Attempts[0].Usage.TotalTokens == nil || *d.Attempts[0].Usage.TotalTokens != "3" || d.Attempts[0].ChannelID != 0 || d.Attempts[0].Host.ProtocolStatus != "completed" {
			t.Fatalf("wrong native detail: %s", body)
		}
		for _, private := range []string{f.key, "upstream-main-fixture", `"actual_model"`, `"channel_id"`, `"input":"hello"`, `"usage_record_id"`} {
			if strings.Contains(string(body), private) {
				t.Fatal("private execution data leaked", private)
			}
		}
		request("/api/me/usage-records/"+row.ID, other, 404)
		request("/api/me/usage-records/"+row.ID, f.key, 401)
	}
	if f.attempts.Load() != 2 || f.httpCalls.Load() != 0 || f.connections.Load() != 1 {
		t.Fatal("wrong WS route/attempt counts")
	}
}
