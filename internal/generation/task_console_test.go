package generation

import (
	"context"
	"errors"
	"fmt"
	bg "minigateway/internal/foundation/contracts/background/v1"
	task "minigateway/internal/foundation/contracts/taskcontrol/v1"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTaskConsoleCancellationReusesNativeWorkerAfterKeyRevocation(t *testing.T) {
	for _, sent := range []bool{false, true} {
		t.Run(fmt.Sprint(sent), func(t *testing.T) {
			var posts, cancels atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/cancel") {
					cancels.Add(1)
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"resp_provider","object":"response","status":"cancelled","output":[]}`)
					return
				}
				if r.Method == "POST" {
					posts.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, bgWire(0, "queued"))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, bgWire(1, "cancelled"))
			}))
			defer upstream.Close()
			f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "console-worker.db"), upstream.URL, nil)
			receipt := f.submit(t, 1)
			if sent {
				if e := f.runner.runOne(context.Background()); e != nil {
					t.Fatal(e)
				}
			}
			session, _, e := f.st.Login("native-event-owner", "fixture-password-123")
			if e != nil {
				t.Fatal(e)
			}
			if e = f.st.DeleteOwnedToken(f.key.UserID, f.key.ID); e != nil {
				t.Fatal(e)
			}
			repo := f.st.TaskControl()
			a := task.Access{Session: session}
			meta, e := repo.Read(context.Background(), a, receipt.ID)
			if e != nil {
				t.Fatal(e)
			}
			canceled, e := repo.Cancel(context.Background(), a, receipt.ID, meta.Revision)
			if e != nil || !canceled.CancelRequested {
				t.Fatal(canceled, e)
			}
			if sent {
				if e = f.runner.runOne(context.Background()); e != nil {
					t.Fatal(e)
				}
				meta, e = repo.Read(context.Background(), a, receipt.ID)
				if e != nil || meta.Stage != "terminal" {
					t.Fatal(meta, e)
				}
				if posts.Load() != 1 || cancels.Load() != 1 {
					t.Fatal("cancel replayed generation", posts.Load(), cancels.Load())
				}
			} else {
				if e = f.runner.runOne(context.Background()); !errors.Is(e, bg.ErrNotFound) {
					t.Fatal(e)
				}
				if canceled.Stage != "terminal" || posts.Load() != 0 || cancels.Load() != 0 {
					t.Fatal("queued cancellation dispatched")
				}
			}
		})
	}
}
