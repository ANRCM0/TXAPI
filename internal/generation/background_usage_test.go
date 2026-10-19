package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	bg "minigateway/internal/foundation/contracts/background/v1"
	task "minigateway/internal/foundation/contracts/taskcontrol/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

func TestBackgroundUsageLateTerminalDetailsAndRestart(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			var posts, cancels, resumes atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/cancel"):
					cancels.Add(1)
					_, _ = fmt.Fprint(w, `{"id":"resp_provider","object":"response","status":"incomplete","output":[],"usage":{"input_tokens":2,"input_tokens_details":{"cached_tokens":1}}}`)
				case r.Method == "POST":
					posts.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, bgWire(0, "queued"))
				default:
					w.Header().Set("Content-Type", "text/event-stream")
					if resumes.Add(1) == 1 {
						_, _ = fmt.Fprint(w, bgWire(0, "queued"))
						return
					}
					cached := 1
					if conflict {
						cached = 2
					}
					_, _ = fmt.Fprintf(w, "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"sequence_number\":1,\"response\":{\"id\":\"resp_provider\",\"object\":\"response\",\"status\":\"incomplete\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":%d},\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n", cached)
				}
			}))
			defer upstream.Close()
			path := filepath.Join(t.TempDir(), "usage.db")
			f := bgRuntimeOpen(t, path, upstream.URL, nil)
			receipt := f.submit(t, 1)
			if err := bgRunDue(t, f); err != nil {
				t.Fatal(err)
			}
			session, _, err := f.st.Login("native-event-owner", "fixture-password-123")
			if err != nil {
				t.Fatal(err)
			}
			scope := task.Access{Session: session}
			queued, err := f.st.TaskControl().Read(context.Background(), scope, receipt.ID)
			if err != nil || queued.ProtocolStatus != "queued" || queued.Usage != nil {
				t.Fatal("nonterminal usage became terminal", queued, err)
			}
			if _, err = f.s.Cancel(f.ctx, receipt.ID); err != nil {
				t.Fatal(err)
			}
			if err = bgRunDue(t, f); err != nil {
				t.Fatal(err)
			}
			partial, err := f.st.TaskControl().Read(context.Background(), scope, receipt.ID)
			if err != nil || partial.ProtocolStatus != "incomplete" || partial.Usage == nil || partial.Usage.InputTokens == nil || *partial.Usage.InputTokens != "2" || partial.Usage.OutputTokens != nil || partial.Usage.TotalTokens != nil || partial.Usage.Completeness != "partial" {
				t.Fatal("partial terminal facts lost", partial, err)
			}
			if err = bgRunDue(t, f); err != nil {
				t.Fatal("historical prefix failed", err)
			}
			prefix, err := f.st.TaskControl().Read(context.Background(), scope, receipt.ID)
			if err != nil || prefix.ProtocolStatus != "incomplete" || prefix.Usage.TotalTokens != nil || *prefix.Usage.CachedInputTokens != "1" {
				t.Fatal("historical prefix regressed metadata", prefix, err)
			}
			err = bgRunDue(t, f)
			if conflict != errors.Is(err, bg.ErrCursorUnavailable) || !conflict && err != nil {
				t.Fatal("conflicting quantity was not explicit", err)
			}
			if posts.Load() != 1 || cancels.Load() != 1 || resumes.Load() != 2 {
				t.Fatal("control RPC became another generation", posts.Load(), cancels.Load(), resumes.Load())
			}
			if err = f.st.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := bgRuntimeOpen(t, path, upstream.URL, nil)
			got, err := reopened.st.TaskControl().Read(context.Background(), scope, receipt.ID)
			if err != nil || got.ProtocolStatus != "incomplete" || got.Usage == nil || *got.Usage.InputTokens != "2" || *got.Usage.CachedInputTokens != "1" {
				t.Fatal("restart lost quantities", got, err)
			}
			if conflict {
				if got.Usage.TotalTokens != nil || got.EventCount != 1 {
					t.Fatal("rejected event was acknowledged", got)
				}
			} else if got.Usage.TotalTokens == nil || *got.Usage.TotalTokens != "3" || got.Usage.ReasoningOutputTokens == nil || *got.Usage.ReasoningOutputTokens != "0" || got.Usage.Completeness != "complete" || got.EventCount != 2 {
				t.Fatal("late counts were lost or summed", got)
			}
			if err = reopened.s.Delete(reopened.ctx, receipt.ID, nil); err != nil {
				t.Fatal(err)
			}
			page, err := reopened.st.TaskControl().List(context.Background(), scope, task.Query{})
			if err != nil || len(page.Data) != 1 || page.Data[0].Usage == nil || page.Data[0].ContentAvailable {
				t.Fatal("content deletion erased metadata", page, err)
			}
			data, _ := json.Marshal(page)
			for _, secret := range []string{"private background input", "resp_provider", "fixture-upstream-key", "snapshot", "ciphertext", "Fingerprint"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("metadata exposed private data", string(data))
				}
			}
			var count int64
			if err = reopened.st.DB().Model(&model.UsageInvocation{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("background added HTTP usage invocations", count, err)
			}
		})
	}
}

func TestBackgroundUsageEventAndMetadataRollbackTogether(t *testing.T) {
	var posts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Method == "POST" {
			posts.Add(1)
			_, _ = fmt.Fprint(w, bgWire(0, "queued"))
		} else {
			_, _ = fmt.Fprint(w, bgWire(1, "completed"))
		}
	}))
	defer upstream.Close()
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "atomic.db"), upstream.URL, nil)
	receipt := f.submit(t, 1)
	if err := bgRunDue(t, f); err != nil {
		t.Fatal(err)
	}
	var rejected atomic.Bool
	const name = "reject_background_usage_commit"
	if err := f.st.DB().Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.BackgroundJob); ok && row.NativeState == string(bg.RemoteSucceeded) && rejected.CompareAndSwap(false, true) {
			tx.AddError(errors.New("injected metadata transaction failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.st.DB().Callback().Update().Remove(name) })
	if err := bgRunDue(t, f); !errors.Is(err, bg.ErrUnavailable) || !rejected.Load() {
		t.Fatal("fixture did not fail the persistence transaction", err)
	}
	var row model.BackgroundJob
	if err := f.st.DB().Where("public_id = ?", receipt.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	var events int64
	if err := f.st.DB().Model(&model.BackgroundEvent{}).Where("task_id = ?", row.ID).Count(&events).Error; err != nil || events != 1 || row.EventCount != 1 || row.LastSequence != 0 || row.Stage != string(bg.StageRemoteActive) || row.NativeState != string(bg.RemoteAccepted) || row.Usage.Completeness != "" {
		t.Fatal("event or metadata escaped failed transaction", row, events, err)
	}
	if err := bgRunDue(t, f); err != nil {
		t.Fatal("durable replay failed after rollback", err)
	}
	if err := f.st.DB().Where("public_id = ?", receipt.ID).First(&row).Error; err != nil || row.EventCount != 2 || row.Usage.TotalTokens == nil || *row.Usage.TotalTokens != 3 || posts.Load() != 1 {
		t.Fatal("recovery lost/doubled the generation", row, err, posts.Load())
	}
	// A stale worker has no metadata write authority after the final CAS.
	if _, err := f.st.UpdateBackground(context.Background(), bs.Claim{ID: row.ID, Epoch: row.ClaimEpoch, Token: "stale-fence"}, row.Revision, bs.Update{Stage: bg.StageTerminal, UsageMetadata: &bs.UsageMetadata{NativeState: bg.RemoteFailed}}); !errors.Is(err, bs.ErrClaimLost) {
		t.Fatal("lost claim updated terminal metadata", err)
	}
}
