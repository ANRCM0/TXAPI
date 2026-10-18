package generation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	bg "minigateway/internal/foundation/contracts/background/v1"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/identity"
	"minigateway/internal/gateway"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	native "minigateway/internal/protocol/responses"
	"minigateway/internal/store"
)

type bgRuntimeFixture struct {
	st     *store.Store
	s      *BackgroundService
	runner *BackgroundRunner
	remote *gateway.BackgroundRemote
	ctx    context.Context
	key    model.Token
}

func bgRuntimeOpen(t *testing.T, path, upstream string, wrap func(bs.Repository) bs.Repository) bgRuntimeFixture {
	t.Helper()
	credentialFile := path + ".keys"
	if _, e := os.Stat(credentialFile); os.IsNotExist(e) {
		data, _ := json.Marshal(map[string]interface{}{"active_key": "fixture", "keys": map[string]string{"fixture": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{53}, 32))}, "digest_key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{59}, 32))})
		if e = os.WriteFile(credentialFile, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	st, e := store.Open(config.DatabaseConfig{Path: path, CredentialKeysFile: credentialFile})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = st.Close() })
	var key model.Token
	q := st.DB().Limit(1).Find(&key)
	if q.Error != nil {
		t.Fatal(q.Error)
	}
	if q.RowsAffected == 0 {
		u, e := st.CreateUser(&store.UserInput{Username: "native-event-owner", Password: "fixture-password-123", ModelMode: access.All})
		if e != nil {
			t.Fatal(e)
		}
		k, e := st.CreateToken(&store.TokenInput{UserID: u.ID, Key: "sk-native-event-fixture-only", ModelMode: access.All})
		if e != nil {
			t.Fatal(e)
		}
		key = *k
		if _, e = st.CreateChannel(&store.ChannelInput{Name: "native-event-provider", Type: "openai", BaseURL: upstream, APIKey: "fixture-upstream-key", Models: []string{"m"}}); e != nil {
			t.Fatal(e)
		}
	}
	key.Key = "sk-native-event-fixture-only"
	ctx, _, ok := identity.NewTokenAuthenticator(st).Authenticate(context.Background(), key.Key)
	if !ok {
		t.Fatal("fixture authentication failed")
	}
	repo := bs.Repository(st)
	if wrap != nil {
		repo = wrap(repo)
	}
	s, e := NewBackgroundService(ctx, BackgroundOptions{Repository: repo, Parser: native.BackgroundParser{}, BillingMode: "disabled", ActiveKey: "event", Keys: map[string][]byte{"event": bytes.Repeat([]byte{61}, 32)}, DigestKey: bytes.Repeat([]byte{67}, 32), Retention: 10 * time.Minute, MetadataRetention: 24 * time.Hour, Limits: bs.Limits{InstanceRecords: 32, OwnerRecords: 16, InstanceActive: 8, OwnerActive: 4, InstanceBytes: 64 << 20, OwnerBytes: 32 << 20}})
	if e != nil {
		t.Fatal(e)
	}
	g, e := gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: st.Channels(), BillingMode: billing.ModeDisabled})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(g.Close)
	var ch model.Channel
	if e = st.DB().First(&ch).Error; e != nil {
		t.Fatal(e)
	}
	remote, e := gateway.NewBackgroundRemote(g, gateway.BackgroundRemoteOptions{Registrations: []bg.Registration{native.BackgroundRegistration()}, Channels: map[uint]string{ch.ID: native.BackgroundRegistrationID}})
	if e != nil {
		t.Fatal(e)
	}
	r := &BackgroundRunner{opts: BackgroundRunnerOptions{Service: s, Remote: remote, Resolve: st.ResolvePrincipal}}
	return bgRuntimeFixture{st: st, s: s, runner: r, remote: remote, ctx: ctx, key: key}
}

func (f bgRuntimeFixture) submit(t *testing.T, n int) bg.Receipt {
	t.Helper()
	r, e := f.s.Submit(f.ctx, bg.Submit{ID: fmt.Sprintf("resp_%064x", n), Operation: execv1.Responses, Body: []byte(`{"model":"m","background":true,"stream":true,"store":false,"input":"private background input"}`), Stream: true, Store: false, IdempotencyKey: fmt.Sprintf("private-event-key-%d", n)})
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func bgWire(seq int, status string) string {
	typ := "response.created"
	if status == "completed" || status == "cancelled" {
		typ = "response." + status
	}
	return fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"sequence_number\":%d,\"response\":{\"id\":\"resp_provider\",\"object\":\"response\",\"status\":%q,\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n", typ, typ, seq, status)
}

type bgCrashRepository struct {
	bs.Repository
	mode, ready string
	claim       bs.Claim
}

func (r *bgCrashRepository) pause(row bs.Record) {
	data, _ := json.Marshal(struct {
		Row   bs.Record
		Claim bs.Claim
	}{row, r.claim})
	if e := os.WriteFile(r.ready, data, 0600); e != nil {
		panic(e)
	}
	for {
		time.Sleep(time.Hour)
	}
}
func (r *bgCrashRepository) ClaimBackground(ctx context.Context, _ time.Duration) (bs.Record, bs.Claim, error) {
	row, c, e := r.Repository.ClaimBackground(ctx, time.Second)
	r.claim = c
	if e == nil && r.mode == "claimed" {
		r.pause(row)
	}
	return row, c, e
}
func (r *bgCrashRepository) UpdateBackground(ctx context.Context, c bs.Claim, revision uint64, u bs.Update) (bs.Record, error) {
	if r.mode == "accepted-before-bind" && len(u.Events) > 0 || r.mode == "terminal-before-db" && u.Stage == bg.StageTerminal {
		row, e := r.Repository.ReadClaimedBackground(ctx, c)
		if e != nil {
			return row, e
		}
		r.pause(row)
	}
	row, e := r.Repository.UpdateBackground(ctx, c, revision, u)
	if e == nil && (r.mode == "dispatching-before-send" && u.Stage == bg.StageDispatching || r.mode == "event-before-ack" && len(u.Events) > 0) {
		r.pause(row)
	}
	return row, e
}

func TestBackgroundNativeCrashChild(t *testing.T) {
	path := os.Getenv("TXAPI_BG_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	f := bgRuntimeOpen(t, path, "", func(repo bs.Repository) bs.Repository {
		return &bgCrashRepository{Repository: repo, mode: os.Getenv("TXAPI_BG_CRASH_MODE"), ready: path + ".ready"}
	})
	if e := f.runner.runOne(context.Background()); e != nil {
		t.Fatal(e)
	}
	t.Fatal("crash barrier was not reached")
}

func TestBackgroundNativeProcessDeathFencesEveryDispatchAndEventCommitWindow(t *testing.T) {
	for _, mode := range []string{"claimed", "dispatching-before-send", "accepted-before-bind", "event-before-ack", "terminal-before-db"} {
		t.Run(mode, func(t *testing.T) {
			var posts, resumes atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-upstream-key" {
					t.Error("credential changed")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if r.Method == "POST" && r.URL.Path == "/v1/responses" {
					posts.Add(1)
					_, _ = fmt.Fprint(w, bgWire(0, "queued")+bgWire(1, "completed"))
					return
				}
				if r.Method == "GET" && r.URL.Path == "/v1/responses/resp_provider" && r.URL.Query().Get("stream") == "true" && r.URL.Query().Get("starting_after") == "0" {
					resumes.Add(1)
					_, _ = fmt.Fprint(w, bgWire(1, "completed"))
					return
				}
				t.Error("unexpected resumed call", r.Method, r.URL)
				http.Error(w, "unexpected", 400)
			}))
			defer upstream.Close()
			path := filepath.Join(t.TempDir(), "native-crash.db")
			f := bgRuntimeOpen(t, path, upstream.URL, nil)
			receipt := f.submit(t, 1)
			if e := f.st.Close(); e != nil {
				t.Fatal(e)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestBackgroundNativeCrashChild$", "-test.count=1", "-test.timeout=25s")
			command.Env = append(os.Environ(), "TXAPI_BG_CRASH_DB="+path, "TXAPI_BG_CRASH_MODE="+mode)
			var output bytes.Buffer
			command.Stdout = &output
			command.Stderr = &output
			if e := command.Start(); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			t.Cleanup(func() {
				if command.ProcessState == nil {
					_ = command.Process.Kill()
					<-done
				}
			})
			var barrier struct {
				Row   bs.Record
				Claim bs.Claim
			}
			until := time.Now().Add(15 * time.Second)
			for {
				data, e := os.ReadFile(path + ".ready")
				if e == nil && json.Unmarshal(data, &barrier) == nil {
					break
				}
				select {
				case e := <-done:
					t.Fatalf("child exited before barrier: %v %s", e, output.String())
				default:
				}
				if time.Now().After(until) {
					_ = command.Process.Kill()
					<-done
					t.Fatalf("crash barrier timeout: %s", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if e := command.Process.Kill(); e != nil {
				t.Fatal(e)
			}
			<-done
			if remaining := time.Until(time.UnixMilli(barrier.Row.ClaimUntil)); remaining > 0 {
				time.Sleep(remaining + 30*time.Millisecond)
			}
			f = bgRuntimeOpen(t, path, upstream.URL, nil)
			if _, e := f.st.RenewBackground(context.Background(), barrier.Claim, time.Second); !errors.Is(e, bs.ErrClaimLost) {
				t.Fatal("dead worker renewed old ownership", e)
			}
			if _, e := f.st.UpdateBackground(context.Background(), barrier.Claim, barrier.Row.Revision, bs.Update{Stage: bg.StageTerminal}); !errors.Is(e, bs.ErrClaimLost) {
				t.Fatal("dead worker published terminal", e)
			}
			e := f.runner.runOne(context.Background())
			unknown := mode == "dispatching-before-send" || mode == "accepted-before-bind"
			if unknown && !errors.Is(e, bg.ErrNotFound) || !unknown && e != nil {
				t.Fatal("restart result", e)
			}
			var row model.BackgroundJob
			if e = f.st.DB().First(&row, "public_id = ?", receipt.ID).Error; e != nil {
				t.Fatal(e)
			}
			wantPosts := int64(1)
			if mode == "dispatching-before-send" {
				wantPosts = 0
			}
			if posts.Load() != wantPosts {
				t.Fatal("unsafe repeated POST", posts.Load(), wantPosts)
			}
			if unknown {
				if row.Stage != string(bg.StageUncertain) || resumes.Load() != 0 {
					t.Fatal("unknown POST was invented or recovered without ID", row.Stage)
				}
				if _, e = f.s.Events(f.ctx, receipt.ID, bg.EventQuery{}); !errors.Is(e, bg.ErrUncertain) {
					t.Fatal("unknown event stream looked complete", e)
				}
			} else {
				if row.Stage != string(bg.StageTerminal) || row.ReplayPending || row.LastSequence != 1 {
					t.Fatal("terminal event not recovered", row)
				}
				page, e := f.s.Events(f.ctx, receipt.ID, bg.EventQuery{})
				if e != nil || !page.Terminal || len(page.Events) != 2 {
					t.Fatal("durable prefix or terminal tail lost", page, e)
				}
				if mode != "claimed" && resumes.Load() != 1 {
					t.Fatal("bound task did not resume without POST")
				}
			}
			data, _ := os.ReadFile(path)
			if bytes.Contains(data, []byte("private background input")) || bytes.Contains(data, []byte("fixture-upstream-key")) {
				t.Fatal("plaintext reached database")
			}
			if _, e = f.runner.opts.Service.repository.ReadClaimedBackground(context.Background(), barrier.Claim); !errors.Is(e, bs.ErrClaimLost) {
				t.Fatal("stale claim read permitted", e)
			}
		})
	}
}

func TestBackgroundNativeOverlapReplayUsesOriginalCiphertextAndRejectsChangedBytes(t *testing.T) {
	var posts, resumes atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Method == "POST" {
			posts.Add(1)
			_, _ = fmt.Fprint(w, bgWire(0, "queued"))
			return
		}
		resumes.Add(1)
		_, _ = fmt.Fprint(w, bgWire(0, "queued")+bgWire(1, "completed"))
	}))
	defer upstream.Close()
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "overlap.db"), upstream.URL, nil)
	receipt := f.submit(t, 1)
	if e := f.runner.runOne(context.Background()); e != nil {
		t.Fatal(e)
	}
	var before model.BackgroundEvent
	if e := f.st.DB().First(&before).Error; e != nil {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	if e := f.runner.runOne(context.Background()); e != nil {
		t.Fatal("same canonical event was rejected because of fresh AEAD nonce", e)
	}
	var after model.BackgroundEvent
	if e := f.st.DB().First(&after).Error; e != nil || !bytes.Equal(before.Ciphertext, after.Ciphertext) {
		t.Fatal("duplicate rewrote original ciphertext", e)
	}
	zero := uint64(0)
	page, e := f.s.Events(f.ctx, receipt.ID, bg.EventQuery{After: &zero, Limit: 1})
	if e != nil || !page.Terminal || len(page.Events) != 1 || page.Events[0].Sequence != 1 || posts.Load() != 1 || resumes.Load() != 1 {
		t.Fatal("explicit zero resume lost terminal", page, e)
	}
	page, e = f.s.Events(f.ctx, receipt.ID, bg.EventQuery{Limit: 1})
	if e != nil || page.Terminal || len(page.Events) != 1 || page.Events[0].Sequence != 0 {
		t.Fatal("first page claimed whole log complete", page, e)
	}
	bad := uint64(2)
	if _, e = f.s.Events(f.ctx, receipt.ID, bg.EventQuery{After: &bad}); !errors.Is(e, bg.ErrCursorUnavailable) {
		t.Fatal("future cursor silently accepted", e)
	}
	var row model.BackgroundJob
	if e = f.st.DB().First(&row).Error; e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(row.Snapshot), "resp_provider") {
		t.Fatal("private binding not encrypted")
	}
}
