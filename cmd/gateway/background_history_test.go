package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func TestNativeBackgroundSavedHTTPAndWSParentsUseCompleteBoundContext(t *testing.T) {
	for _, transport := range []string{"HTTP", "persistent WS"} {
		t.Run(transport, func(t *testing.T) {
			f := newWSAssemblyFixtureWithHistory(t, true, true)
			var parent string
			if transport == "HTTP" {
				doc := f.historyHTTP(t, f.key, http.MethodPost, "/v1/responses", map[string]any{"model": "alias", "input": "saved-parent-input"}, http.StatusOK)
				_ = json.Unmarshal(doc["id"], &parent)
			} else {
				c := f.dial(t)
				sendStoredCreate(t, f, c, "", "saved-parent-input", true)
				parent = f.receiveStoredRound(t, c, "")
				_ = c.CloseNow()
			}
			select {
			case <-f.observations:
			case <-f.ctx.Done():
				t.Fatal("parent inference was not observed")
			}
			if parent == "" {
				t.Fatal("parent was not saved")
			}
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			if err := f.runtime.Close(ctx); err != nil {
				t.Fatal(err)
			}
			f.front.Close()
			f.httpRuntime.Close()

			// A new higher-priority channel makes loss of the parent's target
			// binding observable even though both routes share this local server.
			alternate, err := f.st.CreateChannel(&store.ChannelInput{Name: "higher-priority", Type: "openai", BaseURL: "https://api.openai.com", Models: []string{"alias"}, ModelMapping: map[string]string{"alias": "m"}, APIKey: "wrong-target-fixture", Priority: 100, Weight: 1, Status: "enabled"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.gw.Publish(f.st.Channels()); err != nil {
				t.Fatal(err)
			}
			f.historyConfig.Background = config.BackgroundConfig{Enabled: true, ChannelIDs: []uint{f.channelID, alternate.ID}, RetentionSeconds: 600, MetadataSeconds: 86400, InstanceRecords: 32, OwnerRecords: 16, InstanceBytes: 32 << 20, OwnerBytes: 16 << 20, InstanceActive: 8, OwnerActive: 4}
			type observed struct {
				body []byte
				key  string
			}
			requests := make(chan observed, 4)
			var posts atomic.Int64
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
					http.Error(w, "unexpected background RPC", http.StatusBadRequest)
					return
				}
				posts.Add(1)
				raw, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
				if err != nil {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				requests <- observed{body: raw, key: r.Header.Get("Authorization")}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_background_native","object":"response","model":"m","status":"completed","output":[{"type":"message","id":"msg_background","role":"assistant","content":[{"type":"output_text","text":"background-answer","annotations":[]}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}`))
			})
			f.nativeOverride.Store(&handler)
			f.startPublicRuntimes(t, access.NewMemoryLimiter(), true)

			var input any = "background-next"
			if transport == "persistent WS" {
				input = []any{map[string]any{"type": "function_call_output", "call_id": "call_fixture", "output": "background-tool-result"}}
			}
			body := map[string]any{"model": "alias", "background": true, "store": false, "previous_response_id": parent, "input": input}
			other, err := f.st.IssueToken(f.user, &store.TokenInput{ModelMode: access.All})
			if err != nil {
				t.Fatal(err)
			}
			f.historyHTTP(t, other.Key, http.MethodPost, "/v1/responses", body, http.StatusNotFound)
			var count int64
			if err := f.st.DB().Model(&model.BackgroundJob{}).Count(&count).Error; err != nil || count != 0 || posts.Load() != 0 {
				t.Fatal("cross-Key parent read admitted or dispatched work", count, posts.Load(), err)
			}

			receipt := f.historyHTTP(t, f.key, http.MethodPost, "/v1/responses", body, http.StatusOK)
			var id string
			_ = json.Unmarshal(receipt["id"], &id)
			if id == "" || id == parent {
				t.Fatal("background did not get its own durable identity")
			}
			var wire observed
			select {
			case wire = <-requests:
			case <-f.ctx.Done():
				t.Fatal("background worker did not dispatch")
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(wire.body, &fields)
			var items []map[string]json.RawMessage
			_ = json.Unmarshal(fields["input"], &items)
			if wire.key != "Bearer upstream-main-fixture" || string(fields["model"]) != `"m"` || string(fields["background"]) != "true" || string(fields["store"]) != "false" || fields["previous_response_id"] != nil || len(items) != 3 || !bytes.Contains(items[0]["content"], []byte("saved-parent-input")) || strings.Contains(string(wire.body), parent) {
				t.Fatalf("background lost full context or target binding: key=%s body=%s", wire.key, wire.body)
			}
			if transport == "persistent WS" {
				if string(items[1]["type"]) != `"function_call"` || string(items[1]["call_id"]) != `"call_fixture"` || string(items[2]["output"]) != `"background-tool-result"` {
					t.Fatalf("WS tool association did not survive background transfer: %s", wire.body)
				}
			} else if !bytes.Contains(items[1]["content"], []byte("HTTP-answer")) || string(items[2]["content"]) != `"background-next"` {
				t.Fatalf("HTTP saved output did not survive background transfer: %s", wire.body)
			}
			for {
				var row model.BackgroundJob
				if err := f.st.DB().Where("public_id = ?", id).First(&row).Error; err != nil {
					t.Fatal(err)
				}
				if row.Stage == "terminal" {
					break
				}
				select {
				case <-f.ctx.Done():
					t.Fatal("background did not reach terminal")
				case <-time.After(20 * time.Millisecond):
				}
			}
			if posts.Load() != 1 {
				t.Fatal("background submission was repeated", posts.Load())
			}
		})
	}
}
