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
	"sync"
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
	observed         chan context.Context
}

func (s blockedArchiveCreate) CreateGenerationRecord(ctx context.Context, r state.GenerationRecord, limits model.GenerationArchiveLimits) error {
	if s.observed != nil {
		s.observed <- ctx
	}
	close(s.started)
	<-s.release
	return s.ArchiveStore.CreateGenerationRecord(ctx, r, limits)
}

func TestArchiveHandoffDoesNotHoldShutdownLockAndCloseJoinsStorage(t *testing.T) {
	st, binding, _ := fixture(t)
	opts := archiveOptions(st)
	started, release := make(chan struct{}), make(chan struct{})
	observed := make(chan context.Context, 1)
	opts.Store = blockedArchiveCreate{ArchiveStore: persistence.Archive(st), started: started, release: release, observed: observed}
	r := recordingRunner(t, st, testArchive(t, opts), func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome {
		panic("closed handoff executed")
	})
	submitted := make(chan error, 1)
	go func() { _, _, err := r.SubmitRecorded(context.Background(), binding, execution()); submitted <- err }()
	signal(t, started)
	storageContext := <-observed
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- r.Close(ctx) }()
	select {
	case err := <-closed:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("close pretended storage was joined: %v", err)
		}
		if storageContext.Err() != context.Canceled {
			t.Errorf("Close returned before canceling its admitted storage preparation: %v", storageContext.Err())
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

type preparationStore struct {
	ArchiveStore
	entered chan context.Context
	release <-chan struct{}
	failure string
}

func (s preparationStore) CreateGenerationRecord(ctx context.Context, record state.GenerationRecord, limits model.GenerationArchiveLimits) error {
	if s.entered != nil {
		s.entered <- ctx
	}
	if s.release != nil {
		<-s.release
	}
	switch s.failure {
	case "error":
		return errors.New("preparation fixture failure")
	case "panic":
		panic("preparation fixture panic")
	}
	return s.ArchiveStore.CreateGenerationRecord(ctx, record, limits)
}

func TestArchiveShutdownCancelsAllPreparationsAndConcurrentCloseJoins(t *testing.T) {
	for _, parentShutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "concurrent-close", true: "parent-cancel"}[parentShutdown], func(t *testing.T) {
			st, binding, _ := fixture(t)
			opts := archiveOptions(st)
			entered, release := make(chan context.Context, 2), make(chan struct{})
			opts.Store = preparationStore{ArchiveStore: persistence.Archive(st), entered: entered, release: release}
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()
			r, err := New(parent, Options{Parser: protocol.BuiltinParser{}, Workers: 1, QueueCapacity: 2, MaxRequestBytes: 4096, MaxDuration: 5 * time.Second, Resolve: st.ResolvePrincipal, Archive: testArchive(t, opts), Execute: func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome {
				panic("closed preparation executed")
			}})
			if err != nil {
				t.Fatal(err)
			}
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(func() { unblock(); closeRunner(t, r) })
			submitted := make(chan error, 2)
			for range 2 {
				go func() { _, _, err := r.SubmitRecorded(context.Background(), binding, execution()); submitted <- err }()
			}
			contexts := make([]context.Context, 2)
			for i := range contexts {
				select {
				case contexts[i] = <-entered:
				case <-time.After(time.Second):
					t.Fatal("preparation did not enter storage")
				}
			}
			if parentShutdown {
				cancelParent()
				// Parent cancellation schedules the owner watcher. Observe its stop,
				// then acquire its mutex before claiming cancellation is synchronous.
				select {
				case _, ok := <-r.jobs:
					if ok {
						t.Fatal("unexpected queued job")
					}
				case <-time.After(time.Second):
					t.Fatal("parent shutdown did not stop owner")
				}
				r.mu.Lock()
				r.mu.Unlock()
			}
			wait, cancelWait := context.WithCancel(context.Background())
			cancelWait()
			closed := make(chan error, 8)
			for range 8 {
				go func() { closed <- r.Close(wait) }()
			}
			for range 8 {
				select {
				case err := <-closed:
					if !errors.Is(err, context.Canceled) {
						t.Error("Close claimed to join blocked storage", err)
					}
				case <-time.After(time.Second):
					t.Fatal("Close held shutdown mutex on storage")
				}
				for _, ctx := range contexts {
					if ctx.Err() != context.Canceled {
						t.Error("Close returned before canceling all registered preparations", ctx.Err())
					}
				}
			}
			if _, _, err := r.SubmitRecorded(context.Background(), binding, execution()); !errors.Is(err, ErrClosed) {
				t.Fatal("closed owner admitted storage", err)
			}
			select {
			case <-entered:
				t.Fatal("closed submission reached storage")
			default:
			}
			unblock()
			for range 2 {
				if err := <-submitted; err == nil {
					t.Error("closed handoff accepted")
				}
			}
			closeRunner(t, r)
			r.mu.Lock()
			pending, registered := r.pending, len(r.preparations)
			r.mu.Unlock()
			if pending != 0 || registered != 0 {
				t.Fatal("joined preparations retained ownership", pending, registered)
			}
			var rows int64
			if err := st.DB().Model(&model.GenerationRecord{}).Count(&rows).Error; err != nil || rows != 0 {
				t.Fatal("canceled preparation committed", rows, err)
			}
		})
	}
}

type preparationParser struct{ failure string }

func (p *preparationParser) ParseRequest(operation execv1.Operation, body []byte) (execv1.Request, error) {
	if p.failure == "parse-panic" {
		panic("parser fixture panic")
	}
	if p.failure == "parse-error" {
		return nil, errors.New("parser fixture failure")
	}
	r, err := (protocol.BuiltinParser{}).ParseRequest(operation, body)
	if p.failure == "model-panic" && err == nil {
		return preparationRequest{Request: r}, nil
	}
	return r, err
}

type preparationRequest struct{ execv1.Request }

func (preparationRequest) Model() string { panic("model fixture panic") }

func TestArchivePreparationFailuresAndPanicsReleaseAdmission(t *testing.T) {
	for _, failure := range []string{"parse-error", "parse-panic", "model-panic", "store-error", "store-panic"} {
		t.Run(failure, func(t *testing.T) {
			st, binding, _ := fixture(t)
			opts := archiveOptions(st)
			if kind, ok := strings.CutPrefix(failure, "store-"); ok {
				opts.Store = preparationStore{ArchiveStore: persistence.Archive(st), failure: kind}
			}
			parser := &preparationParser{failure: failure}
			r, err := New(context.Background(), Options{Parser: parser, Workers: 1, QueueCapacity: 1, MaxRequestBytes: 4096, MaxDuration: 5 * time.Second, Resolve: st.ResolvePrincipal, Archive: testArchive(t, opts), Execute: func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome { return execv1.Outcome{} }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeRunner(t, r) })
			var panicValue any
			func() {
				defer func() { panicValue = recover() }()
				_, _, err = r.SubmitRecorded(context.Background(), binding, execution())
			}()
			wantPanic := failure == "parse-panic" || failure == "model-panic"
			if (panicValue != nil) != wantPanic || !wantPanic && err == nil {
				t.Fatal("unexpected preparation failure", panicValue, err)
			}
			r.mu.Lock()
			pending, registered := r.pending, len(r.preparations)
			r.mu.Unlock()
			if pending != 0 || registered != 0 {
				t.Fatal("failed preparation retained capacity/cancellation", pending, registered)
			}
			// Reuse the only admission slot, proving a failed preparation cannot
			// permanently fill the runner even when the parser panicked.
			future, err := r.Submit(context.Background(), binding, &memoryOutput{}, execution())
			if err != nil {
				t.Fatal("failure leaked admission", err)
			}
			if _, err := wait(t, future); err != nil {
				t.Fatal(err)
			}
			closeRunner(t, r)
		})
	}
}

func TestArchiveCanceledBeforeAdmissionNeverStartsPreparation(t *testing.T) {
	st, binding, _ := fixture(t)
	for _, canceledParent := range []bool{false, true} {
		opts := archiveOptions(st)
		entered := make(chan context.Context, 1)
		opts.Store = preparationStore{ArchiveStore: persistence.Archive(st), entered: entered}
		parent, cancelParent := context.WithCancel(context.Background())
		observer, cancelObserver := context.WithCancel(context.Background())
		if canceledParent {
			cancelParent()
		} else {
			cancelObserver()
		}
		r, err := New(parent, Options{Parser: &preparationParser{failure: "parse-panic"}, Workers: 1, QueueCapacity: 1, MaxRequestBytes: 4096, MaxDuration: 5 * time.Second, Resolve: st.ResolvePrincipal, Archive: testArchive(t, opts), Execute: func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome {
			panic("canceled admission executed")
		}})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = r.SubmitRecorded(observer, binding, execution())
		want := error(context.Canceled)
		if canceledParent {
			want = ErrClosed
		}
		if !errors.Is(err, want) {
			t.Errorf("canceled admission: %v, want %v", err, want)
		}
		cancelObserver()
		cancelParent()
		closeRunner(t, r)
		select {
		case <-entered:
			t.Fatal("canceled admission reached storage")
		default:
		}
		r.mu.Lock()
		pending, registered := r.pending, len(r.preparations)
		r.mu.Unlock()
		if pending != 0 || registered != 0 {
			t.Fatal("canceled admission retained owner state", pending, registered)
		}
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
