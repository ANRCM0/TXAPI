package generation

import (
	"bytes"
	"context"
	"errors"
	"io"
	state "minigateway/internal/foundation/contracts/state/v1"
	"minigateway/internal/foundation/persistence"
	bridge "minigateway/internal/foundation/runtime"
	"minigateway/internal/protocol"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func archiveOptions(st *store.Store) ArchiveOptions {
	return ArchiveOptions{Store: persistence.Archive(st), Resolve: st.ResolvePrincipal, ActiveKey: "key-1", Keys: map[string][]byte{"key-1": bytes.Repeat([]byte{21}, 32)},
		Retention: time.Hour, MaxRecordBytes: 65536,
		Limits: model.GenerationArchiveLimits{InstanceBytes: 1 << 20, OwnerBytes: 1 << 19, InstanceRecords: 16, OwnerRecords: 8}}
}

func testArchive(t *testing.T, opts ArchiveOptions) *Archive {
	t.Helper()
	a, err := NewArchive(opts)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func recordingRunner(t *testing.T, st *store.Store, a *Archive, execute func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome) *Runner {
	t.Helper()
	r, err := New(context.Background(), Options{Workers: 1, QueueCapacity: 2, MaxRequestBytes: 4096, MaxDuration: 5 * time.Second,
		Resolve: st.ResolvePrincipal, Execute: execute, Archive: a, Parser: protocol.BuiltinParser{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRunner(t, r) })
	return r
}

func recordGeneration(t *testing.T, r *Runner, binding access.Binding, in execv1.Invocation) (string, execv1.Outcome, error) {
	t.Helper()
	f, id, err := r.SubmitRecorded(context.Background(), binding, in)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wait(t, f)
	return id, result, err
}

func TestRecordedWorkerJSONAndSSESurviveDatabaseReopenWithoutPlaintext(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			st, binding, otherKey := fixture(t)
			opts := archiveOptions(st)
			a := testArchive(t, opts)
			secret := "sensitive-output-marker-617c9152"
			wire := `{"choices":[{"message":{"content":"` + secret + `"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
			contentType := "application/json"
			in := execution()
			if stream {
				contentType = "text/event-stream"
				wire = "data: {\"choices\":[{\"delta\":{\"content\":\"" + secret + "\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
				in.Body = []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"input-not-journaled"}]}`)
			}
			g, _ := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
			})}})
			r := recordingRunner(t, st, a, bridge.NewExecutor(g).Execute)
			id, result, err := recordGeneration(t, r, binding, in)
			if err != nil || result.StatusCode != 200 || !result.Committed || result.HTTPStatus != 0 {
				t.Fatalf("recorded execution: %+v %v", result, err)
			}
			got, err := a.Read(context.Background(), binding, id)
			if err != nil || !bytes.Contains(got.Body, []byte(secret)) || got.Metadata.ContentType != contentType || got.Result.StatusCode != 200 {
				t.Fatalf("recorded output: %+v %v", got, err)
			}
			if _, err := a.Read(context.Background(), otherKey, id); err == nil {
				t.Fatal("same-user other Key read content")
			}
			if err := a.Delete(context.Background(), otherKey, id); err == nil {
				t.Fatal("same-user other Key deleted content")
			}
			closeRunner(t, r)
			var dbs []struct{ File string }
			if err := st.DB().Raw("PRAGMA database_list").Scan(&dbs).Error; err != nil || len(dbs) == 0 {
				t.Fatal(err)
			}
			path := dbs[0].File
			for _, suffix := range []string{"", "-wal"} {
				data, err := os.ReadFile(path + suffix)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte("input-not-journaled")) {
					t.Fatal("journal persisted plaintext content")
				}
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(config.DatabaseConfig{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			opts.Store, opts.Resolve = persistence.Archive(reopened), reopened.ResolvePrincipal
			after, err := testArchive(t, opts).Read(context.Background(), binding, id)
			if err != nil || !bytes.Equal(got.Body, after.Body) {
				t.Fatalf("reopen lost output: %v", err)
			}
			if err := reopened.DeleteOwnedToken(binding.UserID, binding.TokenID); err != nil {
				t.Fatal(err)
			}
			if _, err := testArchive(t, opts).Read(context.Background(), binding, id); !errors.Is(err, access.ErrUnauthorized) {
				t.Fatalf("revoked Key read content: %v", err)
			}
		})
	}
}

func TestArchiveKeyRotationCopiesKeysAndRejectsMissingOrCorruptContent(t *testing.T) {
	st, binding, _ := fixture(t)
	opts := archiveOptions(st)
	a := testArchive(t, opts)
	// Caller-owned key slices/maps cannot change the running archive.
	opts.Keys["key-1"][0]++
	delete(opts.Keys, "key-1")
	g, _ := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(), nil })}})
	id, _, err := recordGeneration(t, recordingRunner(t, st, a, bridge.NewExecutor(g).Execute), binding, execution())
	if err != nil {
		t.Fatal(err)
	}
	rotated := archiveOptions(st)
	rotated.ActiveKey = "key-2"
	rotated.Keys["key-2"] = bytes.Repeat([]byte{49}, 32)
	a2 := testArchive(t, rotated)
	if _, err := a2.Read(context.Background(), binding, id); err != nil {
		t.Fatalf("retained old key cannot decrypt: %v", err)
	}
	wrong := archiveOptions(st)
	wrong.Keys["key-1"] = bytes.Repeat([]byte{77}, 32)
	if _, err := testArchive(t, wrong).Read(context.Background(), binding, id); !errors.Is(err, ErrArchiveIntegrity) {
		t.Fatalf("wrong key: %v", err)
	}
	delete(rotated.Keys, "key-1")
	if _, err := testArchive(t, rotated).Read(context.Background(), binding, id); !errors.Is(err, ErrArchiveIntegrity) {
		t.Fatalf("missing old key: %v", err)
	}
	var segment model.GenerationSegment
	if err := st.DB().Where("record_id = ? AND sequence = 1", id).First(&segment).Error; err != nil {
		t.Fatal(err)
	}
	segment.Ciphertext[len(segment.Ciphertext)-1] ^= 1
	if err := st.DB().Model(&model.GenerationSegment{}).Where("record_id = ? AND sequence = 1", id).Update("ciphertext", segment.Ciphertext).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := a2.Read(context.Background(), binding, id); !errors.Is(err, ErrArchiveIntegrity) || len(got.Body) != 0 {
		t.Fatalf("corrupt content exposed partial plaintext: %v", err)
	}
}

func TestArchiveReadAppliesCurrentModelPolicyAndOwnerStatus(t *testing.T) {
	st, binding, _ := fixture(t)
	a := testArchive(t, archiveOptions(st))
	g, _ := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(), nil })}})
	id, _, err := recordGeneration(t, recordingRunner(t, st, a, bridge.NewExecutor(g).Execute), binding, execution())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&model.Token{}).Where("id = ?", binding.TokenID).Updates(map[string]any{"model_mode": access.Allowlist, "models": `[]`}).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := a.Read(context.Background(), binding, id); !errors.Is(err, access.ErrUnauthorized) || len(got.Body) != 0 {
		t.Fatalf("narrowed model policy exposed content: %v", err)
	}
	if err := st.DB().Model(&model.User{}).Where("id = ?", binding.UserID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(context.Background(), binding, id); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("disabled owner read content: %v", err)
	}
}

func TestArchiveModelAndDispatchUseTheSameInputSnapshotAcrossAuthorityCallbacks(t *testing.T) {
	st, binding, _ := fixture(t)
	in := execution()
	opts := archiveOptions(st)
	opts.Resolve = func(ctx context.Context, b access.Binding) (access.Principal, error) {
		// A callback may retain caller-owned data. It must not make the journal
		// authorize model m while the worker executes a different body.
		copy(in.Body, bytes.ReplaceAll(in.Body, []byte(`"model":"m"`), []byte(`"model":"z"`)))
		return st.ResolvePrincipal(ctx, b)
	}
	a := testArchive(t, opts)
	g, _ := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if !bytes.Contains(body, []byte(`"model":"m"`)) {
			t.Errorf("dispatch did not use its original snapshot: %s", body)
		}
		return response(), nil
	})}})
	id, result, err := recordGeneration(t, recordingRunner(t, st, a, bridge.NewExecutor(g).Execute), binding, in)
	if err != nil || result.StatusCode != 200 {
		t.Fatalf("snapshot mismatch: %+v %v", result, err)
	}
	got, err := a.Read(context.Background(), binding, id)
	if err != nil || got.Result.Model != "m" {
		t.Fatalf("journal model disagreed with dispatched model: %+v %v", got.Result, err)
	}
}

func TestArchiveAuthenticatesOwnershipSequenceAndRetentionMetadata(t *testing.T) {
	st, binding, _ := fixture(t)
	a := testArchive(t, archiveOptions(st))
	g, _ := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(), nil })}})
	id, _, err := recordGeneration(t, recordingRunner(t, st, a, bridge.NewExecutor(g).Execute), binding, execution())
	if err != nil {
		t.Fatal(err)
	}
	var original model.GenerationRecord
	if err := st.DB().First(&original, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"expires_at", "created_at", "max_bytes"} {
		value := map[string]int64{"expires_at": original.ExpiresAt, "created_at": original.CreatedAt, "max_bytes": original.MaxBytes}[field]
		if err := st.DB().Model(&model.GenerationRecord{}).Where("id = ?", id).Update(field, value-1).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := a.Read(context.Background(), binding, id); !errors.Is(err, ErrArchiveIntegrity) {
			t.Fatalf("unauthenticated %s: %v", field, err)
		}
		if err := st.DB().Model(&model.GenerationRecord{}).Where("id = ?", id).Update(field, value).Error; err != nil {
			t.Fatal(err)
		}
	}
	var body model.GenerationSegment
	st.DB().First(&body, "record_id = ? AND sequence = 1", id)
	if err := st.DB().Model(&model.GenerationSegment{}).Where("record_id = ? AND sequence = 0", id).Update("ciphertext", body.Ciphertext).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(context.Background(), binding, id); err == nil {
		t.Fatal("ciphertext transplantation accepted")
	}
}

type failingArchiveStore struct {
	ArchiveStore
	kind       string
	panicFinal bool
}

func (s failingArchiveStore) AppendGenerationSegment(ctx context.Context, r state.GenerationRecord, segment state.GenerationSegment) error {
	if segment.Kind == "result" && s.panicFinal {
		panic("private storage failure")
	}
	if segment.Kind == s.kind {
		return errors.New("private database path and secret must not escape")
	}
	if kind, ok := strings.CutPrefix(s.kind, "ack-"); ok && kind == segment.Kind {
		if err := s.ArchiveStore.AppendGenerationSegment(ctx, r, segment); err != nil {
			return err
		}
		return errors.New("private storage acknowledgement was lost")
	}
	return s.ArchiveStore.AppendGenerationSegment(ctx, r, segment)
}

func TestArchiveStorageFailureAndFinalizerPanicNeverReplayOrKillWorker(t *testing.T) {
	for _, kind := range []string{"metadata", "body", "result", "panic", "ack-metadata", "ack-body", "ack-result"} {
		t.Run(kind, func(t *testing.T) {
			st, binding, _ := fixture(t)
			opts := archiveOptions(st)
			opts.Store = failingArchiveStore{ArchiveStore: persistence.Archive(st), kind: kind, panicFinal: kind == "panic"}
			a := testArchive(t, opts)
			var calls atomic.Int64
			g, capacity := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, RetryTimes: 2, Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return response(), nil })}})
			r := recordingRunner(t, st, a, bridge.NewExecutor(g).Execute)
			for range 2 {
				id, result, err := recordGeneration(t, r, binding, execution())
				if !errors.Is(err, ErrArchiveUnavailable) || strings.Contains(err.Error(), "private") || !result.Committed || result.HTTPStatus != 0 {
					t.Fatalf("storage failure lost facts/leaked error: %+v %v", result, err)
				}
				if kind == "metadata" || kind == "body" || kind == "ack-metadata" || kind == "ack-body" {
					if result.Termination != execv1.ClientWriteError {
						t.Fatalf("output failure pretended success: %+v", result)
					}
				}
				recorded, readErr := a.Read(context.Background(), binding, id)
				if kind == "ack-result" {
					if readErr != nil || recorded.Result.StatusCode != 200 || len(recorded.Body) == 0 {
						t.Fatalf("lost acknowledgement hid a durable observation: %+v %v", recorded.Result, readErr)
					}
				} else if !errors.Is(readErr, ErrArchiveIncomplete) {
					t.Fatalf("incomplete storage returned success: %v", readErr)
				}
			}
			if calls.Load() != 2 || capacity.Snapshot().Requests != 0 || capacity.Snapshot().Attempts != 0 {
				t.Fatalf("storage failure replayed/leaked slots: %d %+v", calls.Load(), capacity.Snapshot())
			}
		})
	}
}

type blockedArchiveCreate struct {
	ArchiveStore
	started, release chan struct{}
}

func (s blockedArchiveCreate) CreateGenerationRecord(ctx context.Context, r state.GenerationRecord, limits model.GenerationArchiveLimits) error {
	close(s.started)
	<-s.release
	return s.ArchiveStore.CreateGenerationRecord(ctx, r, limits)
}

func TestArchiveHandoffDoesNotHoldShutdownLockAndCloseJoinsStorage(t *testing.T) {
	st, binding, _ := fixture(t)
	opts := archiveOptions(st)
	started, release := make(chan struct{}), make(chan struct{})
	opts.Store = blockedArchiveCreate{ArchiveStore: persistence.Archive(st), started: started, release: release}
	r := recordingRunner(t, st, testArchive(t, opts), func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome {
		panic("closed handoff executed")
	})
	submitted := make(chan error, 1)
	go func() { _, _, err := r.SubmitRecorded(context.Background(), binding, execution()); submitted <- err }()
	signal(t, started)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- r.Close(ctx) }()
	select {
	case err := <-closed:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("close pretended storage was joined: %v", err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("storage I/O held the shutdown lock")
	}
	close(release)
	if err := <-submitted; err == nil {
		t.Fatal("closed runner accepted a storage handoff")
	}
	closeRunner(t, r)
	var count int64
	st.DB().Model(&model.GenerationRecord{}).Count(&count)
	if count != 0 {
		t.Fatalf("canceled storage operation committed: %d", count)
	}
}

func TestArchiveQuotaOutputBoundsDeletionAndInterruptedRecovery(t *testing.T) {
	st, binding, _ := fixture(t)
	opts := archiveOptions(st)
	opts.MaxRecordBytes = 16384
	opts.Limits = model.GenerationArchiveLimits{InstanceBytes: 16384, OwnerBytes: 16384, InstanceRecords: 1, OwnerRecords: 1}
	a := testArchive(t, opts)
	g, _ := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(), nil })}})
	r := recordingRunner(t, st, a, bridge.NewExecutor(g).Execute)
	id, _, err := recordGeneration(t, r, binding, execution())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.SubmitRecorded(context.Background(), binding, execution()); !errors.Is(err, ErrArchiveUnavailable) {
		t.Fatalf("storage capacity bypassed: %v", err)
	}
	if err := a.Delete(context.Background(), binding, id); err != nil {
		t.Fatal(err)
	}
	o, err := a.start(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Commit(context.Background(), execv1.ResponseMetadata{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	if n, err := o.Write(context.Background(), bytes.Repeat([]byte("x"), 32768)); !errors.Is(err, ErrArchiveLimit) || n != 0 {
		t.Fatalf("output capacity bypassed: %d %v", n, err)
	}
	if n, err := st.InterruptGenerationRecords(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("interruption: %d %v", n, err)
	}
	if _, err := o.Write(context.Background(), []byte("stale writer")); !errors.Is(err, ErrArchiveUnavailable) {
		t.Fatalf("interrupted handle revived: %v", err)
	}
	if _, err := a.Read(context.Background(), binding, o.record.ID); !errors.Is(err, ErrArchiveIncomplete) {
		t.Fatalf("interrupted record looked complete: %v", err)
	}
	if err := st.DB().Model(&model.GenerationRecord{}).Where("id = ?", o.record.ID).Update("expires_at", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(context.Background(), binding, o.record.ID); err == nil {
		t.Fatal("expired content exposed")
	}
	if _, err := st.CleanupGenerationRecords(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.start(context.Background(), binding); err != nil {
		t.Fatalf("cleanup did not reclaim capacity: %v", err)
	}
}

func TestArchiveRejectsMissingKeysAndCanceledObserversWithoutRecords(t *testing.T) {
	st, binding, _ := fixture(t)
	for _, modify := range []func(*ArchiveOptions){
		func(o *ArchiveOptions) { o.Keys = nil },
		func(o *ArchiveOptions) { o.ActiveKey = "missing" },
		func(o *ArchiveOptions) { o.Keys["key-1"] = []byte("not-32-bytes") },
		func(o *ArchiveOptions) { o.Retention = 0 },
		func(o *ArchiveOptions) { o.MaxRecordBytes = 16 },
		func(o *ArchiveOptions) { o.Store = (*failingArchiveStore)(nil) },
		func(o *ArchiveOptions) { o.Store = persistence.Archive((*store.Store)(nil)) },
	} {
		opts := archiveOptions(st)
		modify(&opts)
		if _, err := NewArchive(opts); err == nil {
			t.Fatal("unsafe archive configuration accepted")
		}
	}
	a := testArchive(t, archiveOptions(st))
	r := recordingRunner(t, st, a, func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome {
		panic("canceled handoff dispatched")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.SubmitRecorded(ctx, binding, execution()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var count int64
	st.DB().Model(&model.GenerationRecord{}).Count(&count)
	if count != 0 {
		t.Fatalf("canceled handoff reserved storage: %d", count)
	}
}
