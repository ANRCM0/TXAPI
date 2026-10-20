package generation

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	bs "minigateway/internal/generation/backgroundstate"
)

type occupancyReleaseRepository struct {
	bs.Repository
	entered, release chan struct{}
}

func (r *occupancyReleaseRepository) ReleaseBackground(ctx context.Context, claim bs.Claim, due int64) error {
	close(r.entered)
	select {
	case <-r.release:
		return r.Repository.ReleaseBackground(ctx, claim, due)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestBackgroundActiveControlsIncludeCompletionCancellationAndRelease(t *testing.T) {
	for _, cancelControl := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelControl), func(t *testing.T) {
			entered, finish := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				_, _ = io.Copy(io.Discard, request.Body)
				close(entered)
				select {
				case <-finish:
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, bgWire(0, "queued")+bgWire(1, "completed"))
				case <-request.Context().Done():
				}
			}))
			defer upstream.Close()
			defer func() {
				select {
				case <-finish:
				default:
					close(finish)
				}
			}()
			var wrapped *occupancyReleaseRepository
			f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "active.db"), upstream.URL, func(repo bs.Repository) bs.Repository {
				wrapped = &occupancyReleaseRepository{Repository: repo, entered: make(chan struct{}), release: make(chan struct{})}
				return wrapped
			})
			f.submit(t, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- f.runner.runOne(ctx) }()
			wait := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatal("background control did not reach lifecycle gate")
				}
			}
			wait(entered)
			if got := f.runner.ActiveControls(); got != 1 {
				t.Fatal("claimed control not visible", got)
			}
			if cancelControl {
				cancel()
			} else {
				close(finish)
			}
			wait(wrapped.entered)
			if got := f.runner.ActiveControls(); got != 1 {
				t.Fatal("control released before claim cleanup joined", got)
			}
			close(wrapped.release)
			select {
			case err := <-done:
				if err != nil && !cancelControl {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("background control did not join")
			}
			if got := f.runner.ActiveControls(); got != 0 {
				t.Fatal("finished control still occupied", got)
			}
		})
	}
}
