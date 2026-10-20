package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	bg "minigateway/internal/foundation/contracts/background/v1"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
	native "minigateway/internal/protocol/responses"
)

type publicationCrashRepository struct {
	bs.Repository
	path, mode string
}

func (r publicationCrashRepository) ClaimBackground(ctx context.Context, _ time.Duration) (bs.Record, bs.Claim, error) {
	return r.Repository.ClaimBackground(ctx, time.Second)
}

func (r publicationCrashRepository) pause() {
	if err := os.WriteFile(r.path+".ready", []byte("publication"), 0600); err != nil {
		panic(err)
	}
	select {}
}

func (r publicationCrashRepository) UpdateBackground(ctx context.Context, c bs.Claim, revision uint64, u bs.Update) (bs.Record, error) {
	if u.History != nil && r.mode == "before" {
		r.pause()
	}
	row, err := r.Repository.UpdateBackground(ctx, c, revision, u)
	if err == nil && u.History != nil && r.mode == "after" {
		r.pause()
	}
	return row, err
}

func TestBackgroundPublicationCrashChild(t *testing.T) {
	path := os.Getenv("TXAPI_PUBLICATION_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	f := bgRuntimeOpen(t, path, "", func(repo bs.Repository) bs.Repository {
		return publicationCrashRepository{Repository: repo, path: path, mode: os.Getenv("TXAPI_PUBLICATION_CRASH_MODE")}
	})
	backgroundHistoryRuntimeSetup(t, f)
	f.s.projector = native.BackgroundHistoryCodec{}
	if err := f.st.RecoverResponseHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.runOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("publication crash barrier was not reached")
}

func TestBackgroundPublicationProcessDeathCommitsHistoryAndTerminalTogether(t *testing.T) {
	for _, mode := range []string{"before", "after"} {
		t.Run(mode, func(t *testing.T) {
			var posts, resumes atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				switch {
				case r.Method == "POST" && r.URL.Path == "/v1/responses":
					posts.Add(1)
					_, _ = fmt.Fprint(w, bgWire(0, "queued")+bgWire(1, "completed"))
				case r.Method == "GET" && r.URL.Path == "/v1/responses/resp_provider" && r.URL.Query().Get("starting_after") == "0":
					resumes.Add(1)
					_, _ = fmt.Fprint(w, bgWire(1, "completed"))
				default:
					t.Error("unexpected provider call", r.Method, r.URL)
					http.Error(w, "unexpected", 400)
				}
			}))
			defer upstream.Close()
			path := filepath.Join(t.TempDir(), "publication.db")
			f := bgRuntimeOpen(t, path, upstream.URL, nil)
			repo, _ := backgroundHistoryRuntimeSetup(t, f)
			f.s.projector = native.BackgroundHistoryCodec{}
			sub := bg.Submit{ID: fmt.Sprintf("resp_%064x", 8101), Operation: execv1.Responses, Body: []byte(`{"model":"m","background":true,"store":true,"stream":true,"input":"private publication input"}`), Store: true, Stream: true, IdempotencyKey: "publication-crash"}
			receipt, err := f.s.Submit(f.ctx, sub)
			if err != nil {
				t.Fatal(err)
			}
			binding := identity.Binding{UserID: f.key.UserID, TokenID: f.key.ID}
			pending, content, err := repo.Read(f.ctx, binding, receipt.ID)
			if err != nil || content.Ready || pending.Busy == "" || pending.ReservedBytes != state.ResponseHistoryMaxBytes {
				t.Fatal("acceptance did not reserve bounded unpublished history", pending, content.Ready, err)
			}
			if err = f.st.Close(); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestBackgroundPublicationCrashChild$", "-test.count=1", "-test.timeout=25s")
			command.Env = append(os.Environ(), "TXAPI_PUBLICATION_CRASH_DB="+path, "TXAPI_PUBLICATION_CRASH_MODE="+mode)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			t.Cleanup(func() {
				if command.ProcessState == nil {
					_ = command.Process.Kill()
					<-done
				}
			})
			until := time.Now().Add(15 * time.Second)
			for {
				if _, err = os.Stat(path + ".ready"); err == nil {
					break
				}
				select {
				case err = <-done:
					t.Fatalf("child exited before publication: %v %s", err, output.String())
				default:
				}
				if time.Now().After(until) {
					t.Fatalf("publication barrier timeout: %s", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err = command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			<-done
			f = bgRuntimeOpen(t, path, upstream.URL, nil)
			repo, _ = backgroundHistoryRuntimeSetup(t, f)
			f.s.projector = native.BackgroundHistoryCodec{}
			if err = f.st.RecoverResponseHistory(context.Background()); err != nil {
				t.Fatal(err)
			}
			var job model.BackgroundJob
			if err = f.st.DB().First(&job, "public_id = ?", receipt.ID).Error; err != nil {
				t.Fatal(err)
			}
			row, saved, err := repo.Read(f.ctx, binding, receipt.ID)
			if err != nil || saved.Ready != (mode == "after") || (job.Stage == string(bg.StageTerminal)) != saved.Ready {
				t.Fatal("process death split task terminal from history", job.Stage, saved.Ready, err)
			}
			if mode == "before" {
				if row.Busy != bs.HistoryBusy(job.ID) {
					t.Fatal("restart reclassified live background reservation as uncertain", row.Busy)
				}
				if remaining := time.Until(time.UnixMilli(job.ClaimUntil)); remaining > 0 {
					time.Sleep(remaining + 30*time.Millisecond)
				}
				if err = f.runner.runOne(context.Background()); err != nil {
					t.Fatal(err)
				}
				row, saved, err = repo.Read(f.ctx, binding, receipt.ID)
			}
			if err != nil || !saved.Ready || row.Version != 2 || row.Busy != "" || saved.Active != "" || len(saved.Items) != 1 || !completeBackgroundTarget(saved.Target) {
				t.Fatal("history was not published exactly once", row.Version, saved.Ready, err)
			}
			var document map[string]json.RawMessage
			_ = json.Unmarshal(saved.Response, &document)
			var publicID string
			_ = json.Unmarshal(document["id"], &publicID)
			if publicID != receipt.ID || posts.Load() != 1 || resumes.Load() != map[string]int64{"before": 1, "after": 0}[mode] {
				t.Fatal("recovery changed identity or repeated provider submit", publicID, posts.Load(), resumes.Load())
			}
			if err = f.runner.runOne(context.Background()); !errors.Is(err, bg.ErrNotFound) {
				t.Fatal("settled generation became executable again", err)
			}
			again, _, err := repo.Read(f.ctx, binding, receipt.ID)
			if err != nil || again.Version != row.Version || again.CreatedAt != row.CreatedAt || again.ExpiresAt != row.ExpiresAt || !bytes.Equal(again.Ciphertext, row.Ciphertext) {
				t.Fatal("recovery republished or extended history", err)
			}
		})
	}
}
