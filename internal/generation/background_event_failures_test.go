package generation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bg "minigateway/internal/foundation/contracts/background/v1"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identityv1 "minigateway/internal/foundation/contracts/identity/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

type bgRateRemote struct {
	bs.Remote
	finished atomic.Int64
}

func (r *bgRateRemote) FinishRate(ctx context.Context, id uint, reservation bs.RateReservation, u *execv1.Usage) {
	r.finished.Add(1)
	r.Remote.FinishRate(ctx, id, reservation, u)
}
func bgRunDue(t *testing.T, f bgRuntimeFixture) error {
	t.Helper()
	// Make only the polling timestamp due; fencing/epochs remain production facts.
	if e := f.st.DB().Model(&model.BackgroundJob{}).Where("claim_hash = ''").Update("due_at", 0).Error; e != nil {
		t.Fatal(e)
	}
	return f.runner.runOne(context.Background())
}

func TestBackgroundNativeJSONTerminalStillRecoversEventTailAndFinalizesRateOnce(t *testing.T) {
	var posts, cancels, resumes atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/responses":
			posts.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, bgWire(0, "queued"))
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			cancels.Add(1)
			_, _ = fmt.Fprint(w, `{"id":"resp_provider","object":"response","status":"cancelled","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			if resumes.Add(1) == 1 {
				_, _ = fmt.Fprint(w, bgWire(0, "queued"))
			} else {
				_, _ = fmt.Fprint(w, bgWire(1, "cancelled"))
			}
		}
	}))
	defer upstream.Close()
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "tail.db"), upstream.URL, nil)
	remote := &bgRateRemote{Remote: f.remote}
	f.runner.opts.Remote = remote
	receipt := f.submit(t, 1)
	if e := bgRunDue(t, f); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Cancel(f.ctx, receipt.ID); e != nil {
		t.Fatal(e)
	}
	if e := bgRunDue(t, f); e != nil {
		t.Fatal(e)
	}
	var row model.BackgroundJob
	if e := f.st.DB().First(&row).Error; e != nil {
		t.Fatal(e)
	}
	if row.Stage != string(bg.StageTerminal) || !row.ReplayPending || remote.finished.Load() != 1 {
		t.Fatal("JSON terminal lost replay obligation", row, remote.finished.Load())
	}
	page, e := f.s.Events(f.ctx, receipt.ID, bg.EventQuery{})
	if e != nil || page.Terminal {
		t.Fatal("JSON terminal falsely completed native event log", page, e)
	}
	if e = bgRunDue(t, f); e != nil {
		t.Fatal("historical prefix could not replay", e)
	}
	got, e := f.s.Read(f.ctx, receipt.ID)
	if e != nil || !strings.Contains(string(got.Document), `"status":"cancelled"`) {
		t.Fatal("historical queued event regressed latest document", string(got.Document), e)
	}
	if e = bgRunDue(t, f); e != nil {
		t.Fatal(e)
	}
	page, e = f.s.Events(f.ctx, receipt.ID, bg.EventQuery{})
	if e != nil || !page.Terminal || len(page.Events) != 2 || posts.Load() != 1 || cancels.Load() != 1 || resumes.Load() != 2 || remote.finished.Load() != 1 {
		t.Fatal("tail or exactly-once rate lifecycle failed", page, e, remote.finished.Load())
	}
}

func TestBackgroundNativeInvalidReplayIsExplicitButDoesNotInventRemoteTermination(t *testing.T) {
	for _, mode := range []string{"changed-duplicate", "gap", "unsupported-resume", "wrong-native-id", "usage-conflict", "event-capacity", "missing-event"} {
		t.Run(mode, func(t *testing.T) {
			var posts, cancels atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/cancel") {
					cancels.Add(1)
					_, _ = fmt.Fprint(w, `{"id":"resp_provider","object":"response","status":"cancelled","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if r.Method == "POST" {
					posts.Add(1)
					_, _ = fmt.Fprint(w, bgWire(0, "queued"))
					return
				}
				switch mode {
				case "changed-duplicate":
					_, _ = fmt.Fprint(w, strings.Replace(bgWire(0, "queued"), `"output":[]`, `"output":[{"changed":true}]`, 1))
				case "gap":
					_, _ = fmt.Fprint(w, bgWire(2, "completed"))
				case "unsupported-resume":
					http.Error(w, "resume not supported", 404)
				case "wrong-native-id":
					_, _ = fmt.Fprint(w, strings.Replace(bgWire(1, "completed"), "resp_provider", "resp_other", 1))
				case "usage-conflict":
					_, _ = fmt.Fprint(w, strings.ReplaceAll(strings.ReplaceAll(bgWire(1, "cancelled"), `"total_tokens":3`, `"total_tokens":4`), `"output_tokens":1`, `"output_tokens":2`))
				default:
					_, _ = fmt.Fprint(w, bgWire(0, "queued"))
				}
			}))
			defer upstream.Close()
			f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "invalid.db"), upstream.URL, nil)
			receipt := f.submit(t, 1)
			if e := bgRunDue(t, f); e != nil {
				t.Fatal(e)
			}
			if mode == "usage-conflict" {
				if _, e := f.s.Cancel(f.ctx, receipt.ID); e != nil {
					t.Fatal(e)
				}
				if e := bgRunDue(t, f); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "event-capacity" {
				if e := f.st.DB().Model(&model.BackgroundJob{}).Where("public_id = ?", receipt.ID).Update("event_count", bs.MaxEvents).Error; e != nil {
					t.Fatal(e)
				}
			}
			if mode == "missing-event" {
				if e := f.st.DB().Where("sequence = 0").Delete(&model.BackgroundEvent{}).Error; e != nil {
					t.Fatal(e)
				}
			}
			e := bgRunDue(t, f)
			if mode == "event-capacity" { // A new sequence, rather than an identical overlap, must hit the cap.
				var row model.BackgroundJob
				if e = f.st.DB().First(&row).Error; e != nil {
					t.Fatal(e)
				}
				claimRow, claim, err := f.st.ClaimBackground(context.Background(), time.Second)
				if errors.Is(err, bg.ErrNotFound) {
					_ = f.st.DB().Model(&row).Update("due_at", 0).Error
					claimRow, claim, err = f.st.ClaimBackground(context.Background(), time.Second)
				}
				if err != nil {
					t.Fatal(err)
				}
				p := &backgroundProgress{service: f.s, remote: f.remote, row: claimRow, claim: claim}
				p.snapshot, err = f.s.openSnapshot(claimRow)
				if err != nil {
					t.Fatal(err)
				}
				e = p.Observe(context.Background(), bg.Observation{RemoteID: "resp_provider", Events: []bg.Event{{Sequence: 1, Data: []byte("event: response.output_text.delta\ndata: {}\n\n")}}})
				if !errors.Is(e, bg.ErrCursorUnavailable) {
					t.Fatal("event cap bypassed", e)
				}
				if e = p.markEventsUnavailable(context.Background()); e != nil {
					t.Fatal(e)
				}
				_ = f.st.ReleaseBackground(context.Background(), claim, 0)
			} else if !errors.Is(e, bg.ErrCursorUnavailable) {
				t.Fatal("invalid event was treated as transient or complete", e)
			}
			if _, e = f.s.Events(f.ctx, receipt.ID, bg.EventQuery{}); !errors.Is(e, bg.ErrCursorUnavailable) {
				t.Fatal("unavailable log was silently served as complete", e)
			}
			var row model.BackgroundJob
			if e = f.st.DB().First(&row).Error; e != nil {
				t.Fatal(e)
			}
			if mode != "usage-conflict" && row.Stage != string(bg.StageRemoteActive) {
				t.Fatal("event failure fabricated task terminal", row.Stage)
			}
			if _, e = f.s.Cancel(f.ctx, receipt.ID); e != nil {
				t.Fatal(e)
			}
			if e = bgRunDue(t, f); e != nil && !errors.Is(e, bg.ErrNotFound) {
				t.Fatal("event failure blocked native cleanup", e)
			}
			if posts.Load() != 1 || cancels.Load() != 1 {
				t.Fatal("event recovery resubmitted or repeatedly cancelled", posts.Load(), cancels.Load())
			}
		})
	}
}

func TestBackgroundNativeTerminalReplayDeleteAndTTLKeepDistinctTaskFacts(t *testing.T) {
	for _, mode := range []string{"delete-terminal-tail", "expire-terminal-tail", "expire-active"} {
		t.Run(mode, func(t *testing.T) {
			var posts atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/cancel") {
					_, _ = fmt.Fprint(w, `{"id":"resp_provider","object":"response","status":"cancelled","output":[]}`)
					return
				}
				if r.Method == "POST" {
					posts.Add(1)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, bgWire(0, "queued"))
			}))
			defer upstream.Close()
			f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "retention.db"), upstream.URL, nil)
			receipt := f.submit(t, 1)
			if e := bgRunDue(t, f); e != nil {
				t.Fatal(e)
			}
			if mode != "expire-active" {
				if _, e := f.s.Cancel(f.ctx, receipt.ID); e != nil {
					t.Fatal(e)
				}
				if e := bgRunDue(t, f); e != nil {
					t.Fatal(e)
				}
			}
			var row model.BackgroundJob
			if e := f.st.DB().First(&row).Error; e != nil {
				t.Fatal(e)
			}
			if mode != "expire-active" && (row.Stage != string(bg.StageTerminal) || !row.ReplayPending) {
				t.Fatal("fixture did not retain terminal tail", row)
			}
			_ = f.st.DB().Model(&row).Update("due_at", 0).Error
			claimed, c, e := f.st.ClaimBackground(context.Background(), time.Second)
			if e != nil {
				t.Fatal("replay obligation not claimable", e)
			}
			if mode == "delete-terminal-tail" {
				version := claimed.Revision
				if e = f.s.Delete(f.ctx, receipt.ID, &version); e != nil {
					t.Fatal(e)
				}
				if _, e = f.s.Submit(f.ctx, bg.Submit{ID: fmt.Sprintf("resp_%064x", 2), Operation: execv1.Responses, Body: []byte(`{"model":"m","background":true,"stream":true,"store":false,"input":"private background input"}`), Stream: true, Store: false, IdempotencyKey: "private-event-key-1"}); !errors.Is(e, bg.ErrCursorUnavailable) {
					t.Fatal("deleted tail resubmitted", e)
				}
			} else {
				if e = f.st.DB().Model(&row).Update("content_expires_at", time.Now().Add(-time.Second).UnixMilli()).Error; e != nil {
					t.Fatal(e)
				}
				if e = f.st.CleanupBackground(context.Background(), 200); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = f.st.UpdateBackground(context.Background(), c, claimed.Revision, bs.Update{Stage: bg.StageTerminal}); !errors.Is(e, bs.ErrClaimLost) {
				t.Fatal("deleted/expired tail did not fence writer", e)
			}
			if e = f.st.DB().First(&row).Error; e != nil {
				t.Fatal(e)
			}
			if row.ReplayPending || len(row.Ciphertext) != 0 || len(row.Snapshot) != 0 || row.ReservedBytes != 0 {
				t.Fatal("retention kept recoverable content", row)
			}
			if mode == "expire-active" && row.Stage != string(bg.StageUncertain) || mode != "expire-active" && row.Stage != string(bg.StageTerminal) {
				t.Fatal("content expiry fabricated or regressed native task terminal", row.Stage)
			}
			var count int64
			if e = f.st.DB().Model(&model.BackgroundEvent{}).Count(&count).Error; e != nil || count != 0 || posts.Load() != 1 {
				t.Fatal("retention leaked event or repeated POST", count, e)
			}
			if mode != "expire-active" {
				if _, e = f.st.InitializeBackground(context.Background(), "", "legacy"); e != nil {
					t.Fatal("known terminal event tail falsely blocks billing guard", e)
				}
			}
		})
	}
}

func TestBackgroundNativeTransportBreakResumesFromCommittedCursorWithoutInvalidatingLog(t *testing.T) {
	var posts, resumes atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
			conn, rw, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			defer conn.Close()
			wire := bgWire(0, "queued")
			_, _ = fmt.Fprintf(rw, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(wire)+200, wire)
			_ = rw.Flush()
			return
		}
		resumes.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, bgWire(1, "completed"))
	}))
	defer upstream.Close()
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "network.db"), upstream.URL, nil)
	receipt := f.submit(t, 1)
	if e := bgRunDue(t, f); !errors.Is(e, bg.ErrUnavailable) {
		t.Fatal("transport break not reported", e)
	}
	var row model.BackgroundJob
	if e := f.st.DB().First(&row).Error; e != nil {
		t.Fatal(e)
	}
	snapshot, e := f.s.openSnapshot(backgroundRowForTest(f, t))
	if e != nil || snapshot.EventUnavailable || row.LastSequence != 0 || row.Stage != string(bg.StageRemoteActive) {
		t.Fatal("transient network failure invalidated durable event prefix", e, row)
	}
	if e = bgRunDue(t, f); e != nil {
		t.Fatal(e)
	}
	page, e := f.s.Events(f.ctx, receipt.ID, bg.EventQuery{})
	if e != nil || !page.Terminal || len(page.Events) != 2 || posts.Load() != 1 || resumes.Load() != 1 {
		t.Fatal("network resume duplicated POST or lost prefix", page, e)
	}
}

func backgroundRowForTest(f bgRuntimeFixture, t *testing.T) bs.Record {
	t.Helper()
	var row model.BackgroundJob
	if e := f.st.DB().First(&row).Error; e != nil {
		t.Fatal(e)
	}
	r, e := f.st.ReadBackground(f.ctx, identityv1.Binding{UserID: f.key.UserID, TokenID: f.key.ID}, row.PublicID)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

type bgEventWriteFailure struct {
	bs.Repository
	failAt int64
	calls  atomic.Int64
}

func (r *bgEventWriteFailure) UpdateBackground(ctx context.Context, c bs.Claim, rev uint64, u bs.Update) (bs.Record, error) {
	if len(u.Events) > 0 && r.calls.Add(1) == r.failAt {
		return bs.Record{}, errors.New("isolated disk write failure")
	}
	return r.Repository.UpdateBackground(ctx, c, rev, u)
}

func TestBackgroundNativePersistenceFailureNeverAcknowledgesUncommittedEventOrReposts(t *testing.T) {
	for _, at := range []int64{1, 2} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			var posts, resumes atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if r.Method == "POST" {
					posts.Add(1)
					_, _ = fmt.Fprint(w, bgWire(0, "queued")+bgWire(1, "completed"))
					return
				}
				resumes.Add(1)
				_, _ = fmt.Fprint(w, bgWire(1, "completed"))
			}))
			defer upstream.Close()
			f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "write.db"), upstream.URL, func(repo bs.Repository) bs.Repository { return &bgEventWriteFailure{Repository: repo, failAt: at} })
			receipt := f.submit(t, 1)
			if e := bgRunDue(t, f); e == nil {
				t.Fatal("injected persistence failure ignored")
			}
			var row model.BackgroundJob
			if e := f.st.DB().First(&row).Error; e != nil {
				t.Fatal(e)
			}
			if at == 1 {
				if row.Stage != string(bg.StageUncertain) || row.LastSequence != -1 {
					t.Fatal("uncommitted ID/event acknowledged", row)
				}
				if e := bgRunDue(t, f); !errors.Is(e, bg.ErrNotFound) {
					t.Fatal("unknown acceptance was dispatched again", e)
				}
			} else {
				if row.Stage != string(bg.StageRemoteActive) || row.LastSequence != 0 {
					t.Fatal("uncommitted terminal acknowledged", row)
				}
				if e := bgRunDue(t, f); e != nil {
					t.Fatal("bound write failure could not resume", e)
				}
				page, e := f.s.Events(f.ctx, receipt.ID, bg.EventQuery{})
				if e != nil || !page.Terminal || len(page.Events) != 2 || resumes.Load() != 1 {
					t.Fatal("persisted prefix/tail lost", page, e)
				}
			}
			if posts.Load() != 1 {
				t.Fatal("persistence error caused another POST")
			}
		})
	}
}
