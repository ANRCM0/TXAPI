package responses

import (
	"context"
	"errors"
	"testing"
	"time"

	bg "minigateway/internal/foundation/contracts/background/v1"
)

type observationBackground struct {
	bg.Service
	page bg.EventPage
}

func (b observationBackground) Events(context.Context, string, bg.EventQuery) (bg.EventPage, error) {
	return b.page, nil
}

type observationDelivery struct {
	callDelivery
	rejectDeadline bool
	bytes          int
	afterWrite     func()
}

func (d *observationDelivery) SetWriteDeadline(time.Time) error {
	if d.rejectDeadline {
		return errors.New("isolated deadline failure")
	}
	return nil
}
func (d *observationDelivery) Write(_ context.Context, p []byte) (int, error) {
	d.bytes += len(p)
	if d.afterWrite != nil {
		d.afterWrite()
	}
	return len(p), nil
}

func TestBackgroundObservationBeforeCommitExpiryAndDeadlineFailuresAreExplicit(t *testing.T) {
	for _, mode := range []string{"already-expired", "expiry-before-first-event", "cancelled-before-first-event", "write-deadline-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			expiry := time.Now().Add(time.Minute)
			page := bg.EventPage{}
			delivery := &observationDelivery{}
			want := "background_result_unavailable"
			switch mode {
			case "already-expired":
				expiry = time.Now().Add(-time.Second)
			case "expiry-before-first-event":
				expiry = time.Now().Add(20 * time.Millisecond)
			case "cancelled-before-first-event":
				cancel()
				want = "background_unavailable"
			case "write-deadline-failure":
				page.Events = []bg.Event{{Sequence: 0, Data: []byte("native event")}}
				delivery.rejectDeadline = true
				want = "background_unavailable"
			}
			s := &Service{background: observationBackground{page: page}}
			s.observeBackground(ctx, delivery, 1, bg.Receipt{ID: "resp_fixture", ContentExpiresAt: expiry}, nil)
			if delivery.problem.Code != want || delivery.state.Committed || delivery.bytes != 0 {
				t.Fatal("uncommitted failure became empty HTTP success", delivery.problem, delivery.state)
			}
		})
	}
}

func TestBackgroundObservationAfterCommitCancellationOnlyClosesWithoutAppendingProblem(t *testing.T) {
	s := &Service{background: observationBackground{page: bg.EventPage{Events: []bg.Event{{Sequence: 0, Data: []byte("native event")}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &observationDelivery{afterWrite: cancel}
	s.observeBackground(ctx, d, 1, bg.Receipt{ID: "resp_fixture", ContentExpiresAt: time.Now().Add(time.Minute)}, nil)
	if !d.state.Committed || d.bytes != len("native event") || d.problem.Code != "" {
		t.Fatal("committed SSE expiry appended error or fake completion", d.problem, d.bytes)
	}
}
