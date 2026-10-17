package responses_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/model"
)

type replayDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	fail      bool
}

func (w *replayDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func (w *replayDeadlineWriter) Write(data []byte) (int, error) {
	if w.fail {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(data)
}

func TestResponsesReplayDeadlineIsScopedToDelivery(t *testing.T) {
	for _, failure := range []bool{false, true} {
		f := newResponseFixture(t)
		body := `{"model":"m","input":"hello"}`
		if first := f.request("delivery", body); first.Code != 200 {
			t.Fatal("first generation failed", first.Code)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", "delivery")
		r = r.WithContext(access.WithPrincipal(r.Context(), f.principal))
		w := &replayDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), fail: failure}
		f.service.ServeHTTP(w, r)
		if len(w.deadlines) != 2 || w.deadlines[0].IsZero() || !w.deadlines[1].IsZero() || f.executor.calls.Load() != 1 {
			t.Fatal("replay leaked deadline or re-executed", w.deadlines)
		}
	}
}

func TestResponsesArchiveCommitFailureDoesNotReturnEmptySuccess(t *testing.T) {
	f := newResponseFixture(t)
	if err := f.st.DB().Exec("CREATE TRIGGER reject_response_segment BEFORE INSERT ON generation_segments BEGIN SELECT RAISE(FAIL, 'isolated archive failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	body := `{"model":"m","input":"hello"}`
	first := f.request("archive-failure", body)
	if first.Code != 503 || !strings.Contains(first.Body.String(), "responses_unavailable") {
		t.Fatal("archive failure became empty HTTP success", first.Code, first.Body)
	}
	if receipt := f.receipt(t, first); receipt.State != model.ResponseUncertain || receipt.OutputRecordID != "" {
		t.Fatal("failed archive sealed", receipt.State)
	}
	repeat := f.request("archive-failure", body)
	if repeat.Code != 409 || f.executor.calls.Load() != 1 {
		t.Fatal("archive failure was re-executed", repeat.Code)
	}
}

func TestResponsesPanicPreservesUnknownScope(t *testing.T) {
	f := newResponseFixture(t)
	f.executor.before = func() { panic("isolated executor panic") }
	body := `{"model":"m","input":"hello","store":false}`
	first := f.request("panic", body)
	if first.Code != 500 || f.receipt(t, first).State != model.ResponseUncertain {
		t.Fatal("panic lost conservative receipt", first.Code)
	}
	if second := f.request("panic", body); second.Code != 409 || f.executor.calls.Load() != 1 {
		t.Fatal("panic triggered retry", second.Code)
	}
}
