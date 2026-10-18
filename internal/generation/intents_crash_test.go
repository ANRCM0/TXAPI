package generation

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"minigateway/internal/foundation/persistence"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

const intentCrashReady = "TXAPI_INTENT_CRASH_READY:"

// The hook lives only in the test process. Production has no crash callback or
// transaction observer; Process.Kill skips all deferred cleanup/finalization.
func TestIntentAdmissionCrashWorker(t *testing.T) {
	path := os.Getenv("TXAPI_TEST_INTENT_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	mode := os.Getenv("TXAPI_TEST_INTENT_CRASH_MODE")
	st, err := store.Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(&store.UserInput{Username: "intent-crash-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := strictRecordingFixture(t, st)
	if mode == "before-commit" {
		if err := st.DB().Callback().Create().After("gorm:create").Register("isolated_intent_crash_after_hold", func(tx *gorm.DB) {
			if tx.Statement.Table == "billing_reservations" && tx.Error == nil {
				fmt.Println(intentCrashReady + "inside-transaction")
				for {
					time.Sleep(time.Hour)
				}
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	opts := intentOptions(st, persistence.Admission(engine))
	opts.QueueTTL = 2 * time.Second
	a := intentAcceptor(t, opts)
	in := strictSocketExecution()
	in.Body = bytes.ReplaceAll(in.Body, []byte("hello"), []byte("encrypted-intent-crash-marker-3d8b"))
	got, err := a.Admit(context.Background(), access.Binding{UserID: user.ID, TokenID: key.ID}, "private-crash-idempotency-marker-d49a", in)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(intentCrashReady + got.ID)
	for {
		time.Sleep(time.Hour)
	}
}

func killIntentChild(t *testing.T, path, mode string) string {
	t.Helper()
	return killGenerationChild(t, path, mode, "TestIntentAdmissionCrashWorker", "TXAPI_TEST_INTENT_CRASH", intentCrashReady)
}

func killGenerationChild(t *testing.T, path, mode, testName, envPrefix, markerPrefix string) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^"+testName+"$", "-test.v")
	command.Env = append(os.Environ(), envPrefix+"_DB="+path, envPrefix+"_MODE="+mode)
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
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if id, ok := strings.CutPrefix(scanner.Text(), markerPrefix); ok {
				ready <- id
				return
			}
		}
		ready <- ""
	}()
	var marker string
	select {
	case marker = <-ready:
	case <-time.After(25 * time.Second):
		t.Fatal("intent crash child did not reach transaction boundary")
	}
	if marker == "" {
		_ = command.Wait()
		reaped = true
		t.Fatalf("child exited before transaction boundary: %s", stderr.String())
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("child exited gracefully")
	}
	reaped = true
	return marker
}

func TestIntentForcedProcessDeathPreservesAtomicAdmission(t *testing.T) {
	for _, mode := range []string{"before-commit", "after-commit"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "intent-crash.db")
			marker := killIntentChild(t, path, mode)
			st, err := store.Open(config.DatabaseConfig{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			assertIntentDatabaseNoPlaintext(t, path, "encrypted-intent-crash-marker-3d8b", "private-crash-idempotency-marker-d49a")
			if mode == "before-commit" {
				if marker != "inside-transaction" {
					t.Fatal("wrong crash boundary")
				}
				assertIntentBudget(t, st, 0, 0, 0)
				var user model.User
				if err := st.DB().First(&user).Error; err != nil || user.UsedQuota != 0 {
					t.Fatalf("crash left compatibility projection: %+v %v", user, err)
				}
				return
			}
			assertIntentBudget(t, st, 1, 1, billing.UnitScale)
			var stored model.GenerationIntent
			if err := st.DB().First(&stored, "id = ?", marker).Error; err != nil {
				t.Fatal(err)
			}
			engine, _ := strictRecordingFixture(t, st)
			opts := intentOptions(st, persistence.Admission(engine))
			opts.QueueTTL = 2 * time.Second
			a := intentAcceptor(t, opts)
			binding := access.Binding{UserID: stored.UserID, TokenID: stored.TokenID}
			if _, in, err := a.Read(context.Background(), binding, marker); err != nil || !bytes.Contains(in.Body, []byte("encrypted-intent-crash-marker")) {
				t.Fatalf("committed task could not be authenticated after restart: %v", err)
			}
			if remaining := time.Until(time.UnixMilli(stored.Deadline)); remaining > 0 {
				time.Sleep(remaining + 20*time.Millisecond)
			}
			for range 2 {
				if err := engine.RecoverExpired(context.Background(), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			assertIntentBudget(t, st, 1, 1, 0)
			if receipt, _, err := a.Read(context.Background(), binding, marker); err != nil || receipt.State != model.IntentExpired {
				t.Fatalf("restart lost terminal receipt: %+v %v", receipt, err)
			}
		})
	}
}
