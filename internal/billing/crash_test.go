package billing

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

	"minigateway/internal/config"
	"minigateway/internal/model"
	relaymodel "minigateway/internal/protocol/model"
	"minigateway/internal/store"
)

const crashReady = "BILLING_CRASH_READY "

// This helper is the actual killed OS process. No production crash hook or
// graceful Close runs between the selected durable boundary and Process.Kill.
func TestStrictCrashWorker(t *testing.T) {
	path := os.Getenv("TXAPI_TEST_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	stage := os.Getenv("TXAPI_TEST_CRASH_STAGE")
	st, err := store.Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user := model.User{Username: "crash-user", Quota: 1}
	if err := st.DB().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	key := model.Token{UserID: user.ID, Quota: 1}
	if err := st.DB().Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: .15, OutputPricePer1K: .6}); err != nil {
		t.Fatal(err)
	}
	e := New(st.DB(), NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := e.EnableStrict("0.1", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pre, err := e.Reserve(ctx, key.ID, user.ID, "m")
	if err != nil {
		t.Fatal(err)
	}
	ready := func() { fmt.Println(crashReady + pre.BillingID()) }
	if stage != "before_attempt" {
		if err := e.StartAttempt(ctx, pre, model.BillingAttempt{Number: 1, ActualModel: "m"}); err != nil {
			t.Fatal(err)
		}
		if stage == "during_attempt" {
			ready()
		}
		response, err := http.Get(os.Getenv("TXAPI_TEST_CRASH_UPSTREAM"))
		if err != nil {
			t.Fatal(err)
		}
		var receipt struct {
			Usage *relaymodel.Usage `json:"usage"`
		}
		err = json.NewDecoder(response.Body).Decode(&receipt)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := e.EndAttempt(ctx, pre, 1, 200, "success", receipt.Usage); err != nil {
			t.Fatal(err)
		}
		if stage == "after_settlement" {
			if _, _, err := e.Finish(ctx, pre, receipt.Usage, "m", "", true); err != nil {
				t.Fatal(err)
			}
		}
	}
	ready()
	for {
		time.Sleep(time.Hour)
	}
}

func TestStrictForcedProcessDeathRecoveryMatrix(t *testing.T) {
	for _, stage := range []string{"before_attempt", "during_attempt", "after_receipt", "after_settlement"} {
		t.Run(stage, func(t *testing.T) {
			var hits atomic.Int64
			started := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				started <- struct{}{}
				if stage == "during_attempt" {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`))
			}))
			defer upstream.Close()
			path := filepath.Join(t.TempDir(), "killed-ledger.db")
			command := exec.Command(os.Args[0], "-test.run=^TestStrictCrashWorker$", "-test.v")
			command.Env = append(os.Environ(), "TXAPI_TEST_CRASH_DB="+path,
				"TXAPI_TEST_CRASH_STAGE="+stage, "TXAPI_TEST_CRASH_UPSTREAM="+upstream.URL)
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
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			ids := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if id, ok := strings.CutPrefix(scanner.Text(), crashReady); ok {
						ids <- id
						return
					}
				}
				ids <- ""
			}()
			var id string
			select {
			case id = <-ids:
			case <-time.After(20 * time.Second):
				t.Fatal("crash worker did not reach the durable boundary")
			}
			if id == "" {
				_ = command.Wait()
				reaped = true
				t.Fatalf("worker exited before boundary: %s", stderr.String())
			}
			if stage == "during_attempt" {
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("upstream execution did not start")
				}
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err == nil {
				t.Fatal("worker exited gracefully instead of being killed")
			}
			reaped = true
			if (stage == "before_attempt" && hits.Load() != 0) || (stage != "before_attempt" && hits.Load() != 1) {
				t.Fatalf("wrong execution window: hits=%d", hits.Load())
			}
			st, err := store.Open(config.DatabaseConfig{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			e := New(st.DB(), NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
			if err := e.EnableStrict("0.1", time.Minute); err != nil {
				t.Fatal(err)
			}
			var row model.BillingReservation
			if err := st.DB().First(&row, "id=?", id).Error; err != nil {
				t.Fatal(err)
			}
			// Let the real persisted lease expire; do not fabricate a DB timestamp.
			if wait := time.Until(time.UnixMilli(row.LeaseUntil)); wait > 0 {
				time.Sleep(wait + 10*time.Millisecond)
			}
			for range 2 {
				if err := e.RecoverExpired(context.Background(), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			b := readBalance(t, st, "user", row.UserID)
			switch stage {
			case "before_attempt":
				if b.UsedUnits != 0 || b.ReservedUnits != 0 {
					t.Fatalf("unexecuted request not released: %+v", b)
				}
			case "during_attempt":
				if b.UsedUnits != 0 || b.ReservedUnits != 100_000_000 {
					t.Fatalf("unknown execution became free: %+v", b)
				}
				admin := billingAdmin(t, st)
				in := ResolutionInput{Action: ResolveWaive, Reason: "operator accepts uncertain cost", Evidence: "forced-death fixture"}
				for range 2 {
					if _, err := e.ResolveReservation(context.Background(), id, admin, in); err != nil {
						t.Fatal(err)
					}
				}
				if b := readBalance(t, st, "user", row.UserID); b.UsedUnits != 0 || b.ReservedUnits != 0 {
					t.Fatalf("audited waiver did not release hold: %+v", b)
				}
			default:
				if b.UsedUnits != 5_850_000 || b.ReservedUnits != 0 {
					t.Fatalf("receipt recovery lost or duplicated charge: %+v", b)
				}
			}
		})
	}
}
