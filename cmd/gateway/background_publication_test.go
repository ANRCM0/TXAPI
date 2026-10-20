package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func TestBackgroundPublishedFunctionResultFeedsHTTPAndNewWS(t *testing.T) {
	f := newWSAssemblyFixtureWithHistory(t, true, true)
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if err := f.runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	f.front.Close()
	f.httpRuntime.Close()
	f.historyConfig.Background = config.BackgroundConfig{Enabled: true, ChannelIDs: []uint{f.channelID}, RetentionSeconds: 600, MetadataSeconds: 86400, InstanceRecords: 32, OwnerRecords: 16, InstanceBytes: 32 << 20, OwnerBytes: 16 << 20, InstanceActive: 8, OwnerActive: 4}
	requests := make(chan []byte, 8)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
		if err != nil {
			t.Error(err)
			return
		}
		requests <- raw
		if r.Header.Get("Authorization") != "Bearer upstream-main-fixture" {
			t.Error("changed target credential")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_bg_native","object":"response","model":"m","status":"completed","output":[{"type":"function_call","id":"fc_background","call_id":"call_background","name":"lookup","arguments":"{\"q\":\"background\"}"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`))
	})
	f.nativeOverride.Store(&handler)
	f.startPublicRuntimes(t, access.NewMemoryLimiter(), true)
	tools := []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}}
	doc := f.historyHTTP(t, f.key, http.MethodPost, "/v1/responses", map[string]any{"model": "alias", "background": true, "store": true, "input": "publication-question", "tools": tools}, http.StatusOK)
	var id string
	_ = json.Unmarshal(doc["id"], &id)
	if id == "" {
		t.Fatal("missing background identity")
	}
	select {
	case <-requests:
	case <-f.ctx.Done():
		t.Fatal("background did not dispatch")
	}
	for {
		var row model.ResponseHistory
		q := f.st.DB().Where("id = ?", id).Limit(1).Find(&row)
		if q.Error != nil {
			t.Fatal(q.Error)
		}
		if q.RowsAffected == 1 && row.Busy == "" {
			break
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("background history was not published")
		case <-time.After(20 * time.Millisecond):
		}
	}
	other, err := f.st.IssueToken(f.user, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	f.historyHTTP(t, other.Key, http.MethodGet, "/v1/responses/"+id+"/input_items", nil, http.StatusNotFound)
	page := f.historyHTTP(t, f.key, http.MethodGet, "/v1/responses/"+id+"/input_items?limit=1&order=asc", nil, http.StatusOK)
	if !bytes.Contains(page["data"], []byte("publication-question")) {
		t.Fatal("published input page lost original input", string(page["data"]))
	}
	f.historyHTTP(t, f.key, http.MethodGet, "/v1/responses/"+id+"/input_items?limit=0", nil, http.StatusBadRequest)
	input := []any{map[string]any{"type": "function_call_output", "call_id": "call_background", "output": "publication-result"}}
	child := map[string]any{"model": "alias", "previous_response_id": id, "input": input, "tools": tools}
	f.historyHTTP(t, other.Key, http.MethodPost, "/v1/responses", child, http.StatusNotFound)
	f.historyHTTP(t, f.key, http.MethodPost, "/v1/responses", child, http.StatusOK)
	select {
	case raw := <-requests:
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if fields["previous_response_id"] != nil || !bytes.Contains(fields["input"], []byte("publication-question")) || !bytes.Contains(fields["input"], []byte("call_background")) || !bytes.Contains(fields["input"], []byte("publication-result")) {
			t.Fatal("HTTP child lost complete context", string(raw))
		}
	case <-f.ctx.Done():
		t.Fatal("HTTP child did not dispatch")
	}
	// Restore the existing WS-capable fixture after observing the HTTP wire.
	f.nativeOverride.Store(nil)
	c := f.dial(t)
	sendStoredCreate(t, f, c, id, input, true)
	f.receiveStoredRound(t, c, id)
	_ = c.CloseNow()
	select {
	case observed := <-f.observations:
		items := historyWire(t, observed, 3)
		if !bytes.Contains(items[0]["content"], []byte("publication-question")) || string(items[1]["call_id"]) != `"call_background"` || string(items[2]["output"]) != `"publication-result"` {
			t.Fatal("WS child lost function context", items)
		}
	case <-f.ctx.Done():
		t.Fatal("WS child did not dispatch")
	}
	f.historyHTTP(t, f.key, http.MethodDelete, "/v1/responses/"+id, nil, http.StatusOK)
	f.historyHTTP(t, f.key, http.MethodGet, "/v1/responses/"+id+"/input_items", nil, http.StatusNotFound)
	f.historyHTTP(t, f.key, http.MethodPost, "/v1/responses", child, http.StatusNotFound)
}
