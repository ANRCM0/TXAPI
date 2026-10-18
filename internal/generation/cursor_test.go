package generation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	bridge "minigateway/internal/foundation/runtime"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func cursorStreamFixture() string {
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": strings.Repeat("x", 5000) + "\u4f60\u597d" + strings.Repeat("y", 32000)}}}})
	return "data: " + string(body) + "\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
}

func cursorRecordedOutput(t *testing.T, st *store.Store, binding access.Binding) (*Archive, string, *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	g, _ := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled,
		Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(cursorStreamFixture()))}, nil
		})}})
	a := testArchive(t, archiveOptions(st))
	in := execution()
	in.Body = []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	id, result, err := recordGeneration(t, recordingRunner(t, st, a, bridge.NewExecutor(g).Execute), binding, in)
	if err != nil || result.StatusCode != 200 || !result.ProtocolComplete {
		t.Fatalf("cursor consumer fixture failed: %+v %v", result, err)
	}
	return a, id, hits
}

func readCursorTail(t *testing.T, reader *CursorReader) []byte {
	t.Helper()
	defer reader.Close()
	var body []byte
	for i := 0; i < 2049; i++ {
		page, err := reader.Next(context.Background(), 4096)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (page.Metadata == nil || page.Metadata.ContentType != "text/event-stream") {
			t.Fatal("reconnected observer lost response metadata")
		}
		if i != 0 && page.Metadata != nil {
			t.Fatal("snapshot emitted metadata as repeated body events")
		}
		body = append(body, page.Body...)
		if page.Done {
			if page.Result == nil || !page.Result.ProtocolComplete || page.Cursor == "" {
				t.Fatal("cursor fabricated or lost terminal result")
			}
			if _, err := reader.Next(context.Background(), 4096); !errors.Is(err, io.EOF) {
				t.Fatal("completed cursor did not terminate", err)
			}
			return body
		}
		if page.Result != nil {
			t.Fatal("partial page claimed completion")
		}
	}
	t.Fatal("cursor traversal exceeded its bounded page count")
	return nil
}

func TestArchiveCursorResumesEncodedStreamWithoutRepeatingBytesOrExecution(t *testing.T) {
	st, binding, other := fixture(t)
	a, id, hits := cursorRecordedOutput(t, st, binding)
	original, err := a.Read(context.Background(), binding, id)
	if err != nil {
		t.Fatal(err)
	}
	marker := bytes.Index(original.Body, []byte("\u4f60"))
	if marker < 4096 || marker >= 64<<10 {
		t.Fatal("fixture does not test UTF-8 crossing a page boundary")
	}
	reader, err := a.OpenCursor(context.Background(), binding, id, "")
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.Next(context.Background(), marker+1)
	if err != nil || page.Done || page.Result != nil || page.Metadata == nil || utf8.Valid(page.Body) {
		t.Fatal("cursor is not a byte-accurate encoded stream position", err)
	}
	consumed := bytes.Clone(page.Body)
	page.Body[0] ^= 1
	page.Metadata.ContentType = "caller mutation"
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.OpenCursor(context.Background(), other, id, page.Cursor); err == nil {
		t.Fatal("another Key resumed the output")
	}
	resumed, err := a.OpenCursor(context.Background(), binding, id, page.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	got := append(consumed, readCursorTail(t, resumed)...)
	if !bytes.Equal(got, original.Body) || hits.Load() != 1 {
		t.Fatal("cursor resume duplicated/dropped bytes or executed the operation again")
	}
	if !bytes.Contains(got, []byte("data: [DONE]")) || !utf8.Valid(got) {
		t.Fatal("reconstructed protocol stream is incomplete")
	}
	var attempts int64
	if err := st.DB().Model(&model.BillingAttempt{}).Count(&attempts).Error; err != nil || attempts != 0 {
		t.Fatal("observation changed financial execution", err)
	}
}

func TestArchiveCursorRejectsTamperCrossRecordAndCurrentModelRevocation(t *testing.T) {
	st, binding, _ := fixture(t)
	a, id, _ := cursorRecordedOutput(t, st, binding)
	reader, err := a.OpenCursor(context.Background(), binding, id, "")
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.Next(context.Background(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_, otherID, _ := cursorRecordedOutput(t, st, binding)
	encoded, err := base64.RawURLEncoding.DecodeString(page.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)-1] ^= 1
	original, err := a.Read(context.Background(), binding, id)
	if err != nil {
		t.Fatal(err)
	}
	outside := make([]byte, 9)
	outside[0] = 1
	binary.BigEndian.PutUint64(outside[1:], ^uint64(0))
	sealed, err := a.keys.Seal(outside, cursorAAD(original.record))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ id, token string }{{id, base64.RawURLEncoding.EncodeToString(encoded)}, {otherID, page.Cursor}, {id, strings.Repeat("a", 257)}, {id, base64.RawURLEncoding.EncodeToString(sealed)}} {
		if got, err := a.OpenCursor(context.Background(), binding, test.id, test.token); err == nil || got != nil {
			t.Fatal("cursor tamper/substitution returned plaintext access")
		}
	}
	live, err := a.OpenCursor(context.Background(), binding, id, page.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if err := st.DB().Model(&model.Token{}).Where("id = ?", binding.TokenID).Updates(map[string]any{"model_mode": access.Allowlist, "models": model.StringList{}}).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := live.Next(context.Background(), 4096); !errors.Is(err, access.ErrUnauthorized) || len(got.Body) != 0 || got.Result != nil {
		t.Fatal("later cursor page retained revoked model authority", err)
	}
}

func TestArchiveCursorAuthenticatesWholeSnapshotBeforeFirstPage(t *testing.T) {
	st, binding, _ := fixture(t)
	a, id, _ := cursorRecordedOutput(t, st, binding)
	unfinished, err := a.start(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := a.OpenCursor(context.Background(), binding, unfinished.record.ID, ""); !errors.Is(err, ErrArchiveIncomplete) || r != nil {
		t.Fatal("an unsealed record exposed a snapshot", err)
	}
	var segment model.GenerationSegment
	if err := st.DB().Where("record_id = ? AND kind = 'body'", id).Order("sequence DESC").First(&segment).Error; err != nil {
		t.Fatal(err)
	}
	segment.Ciphertext[len(segment.Ciphertext)-1] ^= 1
	if err := st.DB().Model(&model.GenerationSegment{}).Where("record_id = ? AND sequence = ?", id, segment.Sequence).Update("ciphertext", segment.Ciphertext).Error; err != nil {
		t.Fatal(err)
	}
	if reader, err := a.OpenCursor(context.Background(), binding, id, ""); !errors.Is(err, ErrArchiveIntegrity) || reader != nil {
		t.Fatal("late tamper exposed an earlier plaintext page", err)
	}
	a.cursorMu.Lock()
	defer a.cursorMu.Unlock()
	if a.cursors != 0 || len(a.owners) != 0 {
		t.Fatal("failed authentication leaked a snapshot slot")
	}
}

type cursorStorageFault struct {
	ArchiveStore
	panic bool
}

func (f cursorStorageFault) ReadGenerationRecord(context.Context, access.Binding, string) (model.GenerationRecord, error) {
	if f.panic {
		panic("private cursor storage fixture")
	}
	return model.GenerationRecord{}, errors.New("private cursor storage fixture")
}

func TestArchiveCursorStorageFailureAndPanicReleaseCapacityWithoutRawErrors(t *testing.T) {
	st, binding, _ := fixture(t)
	for _, panicFault := range []bool{false, true} {
		opts := archiveOptions(st)
		opts.Store = cursorStorageFault{ArchiveStore: st, panic: panicFault}
		a := testArchive(t, opts)
		if reader, err := a.OpenCursor(context.Background(), binding, strings.Repeat("a", 64), ""); !errors.Is(err, ErrArchiveUnavailable) || reader != nil || strings.Contains(err.Error(), "private") {
			t.Fatal("storage failure exposed data or a live reader", err)
		}
		a.cursorMu.Lock()
		count := a.cursors
		a.cursorMu.Unlock()
		if count != 0 {
			t.Fatal("storage failure leaked observation capacity")
		}
	}
}

func TestArchiveCursorBoundsSnapshotCapacityAndObserverCancellation(t *testing.T) {
	st, binding, otherKey := fixture(t)
	a, id, hits := cursorRecordedOutput(t, st, binding)
	user, err := st.CreateUser(&store.UserInput{Username: "cursor-other-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	otherOwner := access.Binding{UserID: user.ID, TokenID: key.ID}
	_, otherID, _ := cursorRecordedOutput(t, st, otherOwner)
	thirdUser, err := st.CreateUser(&store.UserInput{Username: "cursor-third-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	thirdKey, err := st.IssueToken(thirdUser.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	thirdOwner := access.Binding{UserID: thirdUser.ID, TokenID: thirdKey.ID}
	_, thirdID, _ := cursorRecordedOutput(t, st, thirdOwner)
	var readers []*CursorReader
	defer func() {
		for _, r := range readers {
			_ = r.Close()
		}
	}()
	for _, b := range []access.Binding{binding, otherOwner} {
		outputID := id
		if b == otherOwner {
			outputID = otherID
		}
		for range 2 {
			r, err := a.OpenCursor(context.Background(), b, outputID, "")
			if err != nil {
				t.Fatal(err)
			}
			readers = append(readers, r)
		}
		if b == binding {
			if r, err := a.OpenCursor(context.Background(), otherKey, id, ""); !errors.Is(err, ErrArchiveLimit) || r != nil {
				t.Fatal("same owner bypassed snapshot capacity with another Key", err)
			}
		}
	}
	if r, err := a.OpenCursor(context.Background(), thirdOwner, thirdID, ""); !errors.Is(err, ErrArchiveLimit) || r != nil {
		t.Fatal("instance snapshot capacity was exceeded", err)
	}
	if err := readers[0].Close(); err != nil {
		t.Fatal(err)
	}
	observer, cancel := context.WithCancel(context.Background())
	r, err := a.OpenCursor(observer, binding, id, "")
	if err != nil {
		t.Fatal("Close did not release snapshot capacity", err)
	}
	readers = append(readers, r)
	if deadline, ok := r.ctx.Deadline(); !ok || time.Until(deadline) > cursorLifetime {
		t.Fatal("snapshot has no finite observation lifetime")
	}
	cancel()
	until := time.Now().Add(2 * time.Second)
	for {
		a.cursorMu.Lock()
		count := a.cursors
		a.cursorMu.Unlock()
		if count == 3 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("observer cancellation did not reclaim snapshot capacity")
		}
		time.Sleep(time.Millisecond)
	}
	if hits.Load() != 1 {
		t.Fatal("observer cancellation touched execution")
	}
}

func TestArchiveCursorCloseCancelsAndJoinsBlockedRead(t *testing.T) {
	st, binding, _ := fixture(t)
	a, id, _ := cursorRecordedOutput(t, st, binding)
	r, err := a.OpenCursor(context.Background(), binding, id, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	started := make(chan struct{})
	returned := make(chan struct{})
	a.resolve = func(ctx context.Context, b access.Binding) (access.Principal, error) {
		defer close(returned)
		close(started)
		<-ctx.Done()
		return access.Principal{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := r.Next(context.Background(), 4096); done <- err }()
	signal(t, started)
	if _, err := r.Next(context.Background(), 4096); !errors.Is(err, ErrArchiveUnavailable) {
		t.Fatal("parallel cursor reader was accepted", err)
	}
	closed := make(chan struct{})
	go func() { _ = r.Close(); close(closed) }()
	signal(t, closed)
	select {
	case <-returned:
	default:
		t.Fatal("Close returned before the authority read joined")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed observer read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked cursor call did not return after Close")
	}
	a.cursorMu.Lock()
	defer a.cursorMu.Unlock()
	if a.cursors != 0 {
		t.Fatal("blocked reader kept snapshot capacity")
	}
}
