package generation

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	bridge "minigateway/internal/foundation/runtime"
	"net/http"
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
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/store"
)

func TestRecordedOutputFailureRetainsStrictUnknownCost(t *testing.T) {
	st, binding, _ := fixture(t)
	engine, ch := strictRecordingFixture(t, st)
	opts := archiveOptions(st)
	opts.Store = failingArchiveStore{ArchiveStore: st, kind: "body"}
	a := testArchive(t, opts)
	var hits atomic.Int64
	g, capacity := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict, RetryTimes: 2,
		Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"output without reliable usage"}}]}`))}, nil
		})}})
	id, result, err := recordGeneration(t, recordingRunner(t, st, a, bridge.NewExecutor(g).Execute), binding, execv1.Invocation{Operation: protocol.Chat,
		Body: []byte(`{"model":"m","max_completion_tokens":32,"service_tier":"default","messages":[{"role":"user","content":"hello"}]}`)})
	if !errors.Is(err, ErrArchiveUnavailable) || result.Termination != execv1.ClientWriteError || result.BillingState != billing.Pending || hits.Load() != 1 {
		t.Fatalf("storage fault refunded/replayed unknown execution: %+v %v hits=%d", result, err, hits.Load())
	}
	assertArchiveHolds(t, st)
	if _, err := a.Read(context.Background(), binding, id); !errors.Is(err, ErrArchiveIncomplete) {
		t.Fatalf("incomplete output was exposed: %v", err)
	}
	if got := capacity.Snapshot(); got.Requests != 0 || got.Attempts != 0 {
		t.Fatalf("slots leaked: %+v", got)
	}
}

func assertArchiveHolds(t *testing.T, st *store.Store) {
	t.Helper()
	var reservations []model.BillingReservation
	if err := st.DB().Find(&reservations).Error; err != nil || len(reservations) != 1 || reservations[0].State != billing.Pending ||
		reservations[0].ProviderHeldUnits <= 0 || reservations[0].ChargeUnits != 0 {
		t.Fatalf("unknown provider occupation lost: %+v %v", reservations, err)
	}
	var balances []model.BillingBalance
	if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
		t.Fatalf("dual balances lost: %+v %v", balances, err)
	}
	for _, b := range balances {
		if b.ReservedUnits <= 0 || b.UsedUnits != 0 {
			t.Fatalf("unknown execution became free: %+v", b)
		}
	}
}

func TestArchiveFinalObservationFailureDoesNotUndoSettledBill(t *testing.T) {
	st, binding, _ := fixture(t)
	engine, ch := strictRecordingFixture(t, st)
	opts := archiveOptions(st)
	opts.Store = failingArchiveStore{ArchiveStore: st, kind: "result"}
	a := testArchive(t, opts)
	var hits atomic.Int64
	g, _ := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict, RetryTimes: 2,
		Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { hits.Add(1); return response(), nil })}})
	_, result, err := recordGeneration(t, recordingRunner(t, st, a, bridge.NewExecutor(g).Execute), binding, execv1.Invocation{Operation: protocol.Chat,
		Body: []byte(`{"model":"m","max_completion_tokens":32,"service_tier":"default","messages":[{"role":"user","content":"hello"}]}`)})
	if !errors.Is(err, ErrArchiveUnavailable) || !result.Committed || result.Replayable || result.BillingState != billing.Settled || hits.Load() != 1 {
		t.Fatalf("observation failure changed generation: %+v %v", result, err)
	}
	if _, err := st.InterruptGenerationRecords(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if err := engine.RecoverExpired(context.Background(), time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var balances []model.BillingBalance
	if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
		t.Fatalf("balances: %+v %v", balances, err)
	}
	for _, b := range balances {
		if b.ReservedUnits != 0 || b.UsedUnits <= 0 {
			t.Fatalf("settled bill undone: %+v", b)
		}
	}
}

const archiveCrashReady = "GENERATION_ARCHIVE_CRASH_READY "

// Only this test process is killed. There is no production crash hook, external
// provider access or graceful finalizer between the marker and Process.Kill.
func TestArchiveCrashWorker(t *testing.T) {
	path := os.Getenv("TXAPI_TEST_ARCHIVE_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	st, err := store.Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(&store.UserInput{Username: "archive-crash-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	binding := access.Binding{UserID: user.ID, TokenID: key.ID}
	engine, ch := strictRecordingFixture(t, st)
	if err := engine.EnableStrict("1", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	a := testArchive(t, archiveOptions(st))
	g, _ := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict,
		Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			reader, writer := io.Pipe()
			go func() {
				_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"partial-encrypted-crash-output\"}}]}\n\n")
				<-req.Context().Done()
				writer.CloseWithError(req.Context().Err())
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
		})}})
	r := recordingRunner(t, st, a, bridge.NewExecutor(g).Execute)
	_, id, err := r.SubmitRecorded(context.Background(), binding, execv1.Invocation{Operation: protocol.Chat,
		Body: []byte(`{"model":"m","stream":true,"max_completion_tokens":32,"service_tier":"default","messages":[{"role":"user","content":"hello"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var row model.GenerationRecord
		if err := st.DB().First(&row, "id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		if row.Committed && row.NextSequence >= 2 && row.State == "open" {
			fmt.Println(archiveCrashReady + id)
			for {
				time.Sleep(time.Hour)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("did not reach durable output boundary")
}

func TestArchiveForcedProcessDeathDoesNotReplayOrRefund(t *testing.T) {
	path := filepath.Join(t.TempDir(), "killed-output.db")
	command := exec.Command(os.Args[0], "-test.run=^TestArchiveCrashWorker$", "-test.v")
	command.Env = append(os.Environ(), "TXAPI_TEST_ARCHIVE_CRASH_DB="+path)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	defer func() {
		if !reaped {
			command.Process.Kill()
			command.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if id, ok := strings.CutPrefix(scanner.Text(), archiveCrashReady); ok {
				ready <- id
				return
			}
		}
		ready <- ""
	}()
	var id string
	select {
	case id = <-ready:
	case <-time.After(25 * time.Second):
		t.Fatal("crash child did not reach durable boundary")
	}
	if id == "" {
		command.Wait()
		reaped = true
		t.Fatalf("child exited before durable boundary: %s", stderr.String())
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("child exited gracefully")
	}
	reaped = true
	st, err := store.Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var original model.GenerationRecord
	if err := st.DB().First(&original, "id = ?", id).Error; err != nil || !original.Committed || original.State != "open" {
		t.Fatalf("crash boundary lost: %+v %v", original, err)
	}
	binding := access.Binding{UserID: original.UserID, TokenID: original.TokenID}
	a := testArchive(t, archiveOptions(st))
	for range 2 {
		if _, err := st.InterruptGenerationRecords(context.Background(), 100); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Read(context.Background(), binding, id); !errors.Is(err, ErrArchiveIncomplete) {
			t.Fatalf("partial crash output became complete: %v", err)
		}
	}
	if err := st.AppendGenerationSegment(context.Background(), original, model.GenerationSegment{RecordID: id, Sequence: original.NextSequence, Kind: "body", Ciphertext: make([]byte, 40)}); !errors.Is(err, store.ErrArchiveConflict) {
		t.Fatalf("stale process handle survived recovery: %v", err)
	}
	engine, _ := strictRecordingFixture(t, st)
	var reservation model.BillingReservation
	if err := st.DB().First(&reservation).Error; err != nil {
		t.Fatal(err)
	}
	if delay := time.Until(time.UnixMilli(reservation.LeaseUntil)); delay > 0 {
		time.Sleep(delay + 10*time.Millisecond)
	}
	for range 2 {
		if err := engine.RecoverExpired(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
		assertArchiveHolds(t, st)
	}
	var attempts int64
	st.DB().Model(&model.BillingAttempt{}).Count(&attempts)
	if attempts != 1 {
		t.Fatalf("recovery recreated generation: %d attempts", attempts)
	}
}
