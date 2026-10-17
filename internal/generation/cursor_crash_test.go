package generation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

const cursorCrashReady = "TXAPI_CURSOR_CRASH_READY:"

func TestArchiveCursorCrashWorker(t *testing.T) {
	path := os.Getenv("TXAPI_TEST_CURSOR_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	st, err := store.Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(&store.UserInput{Username: "cursor-crash-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	binding := access.Binding{UserID: user.ID, TokenID: key.ID}
	a, id, _ := cursorRecordedOutput(t, st, binding)
	r, err := a.OpenCursor(context.Background(), binding, id, "")
	if err != nil {
		t.Fatal(err)
	}
	page, err := r.Next(context.Background(), 4096)
	if err != nil || page.Done {
		t.Fatal("cursor crash barrier did not consume a partial page", err)
	}
	ack := sha256.Sum256(page.Body)
	fmt.Println(cursorCrashReady + id + "." + page.Cursor + "." + hex.EncodeToString(ack[:]))
	for {
		time.Sleep(time.Hour)
	}
}

func TestArchiveCursorSurvivesObserverProcessDeathAndKeyRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor-observer-crash.db")
	marker := killGenerationChild(t, path, "after-page", "TestArchiveCursorCrashWorker", "TXAPI_TEST_CURSOR_CRASH", cursorCrashReady)
	parts := strings.Split(marker, ".")
	if len(parts) != 3 {
		t.Fatal("cursor child did not expose an acknowledged position")
	}
	st, err := store.Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var record model.GenerationRecord
	if err := st.DB().First(&record, "id = ?", parts[0]).Error; err != nil {
		t.Fatal(err)
	}
	binding := access.Binding{UserID: record.UserID, TokenID: record.TokenID}
	opts := archiveOptions(st)
	opts.ActiveKey = "key-2"
	opts.Keys["key-2"] = bytes.Repeat([]byte{43}, 32)
	a := testArchive(t, opts)
	original, err := a.Read(context.Background(), binding, record.ID)
	if err != nil || len(original.Body) <= 4096 {
		t.Fatal("sealed output did not survive process death", err)
	}
	ack := sha256.Sum256(original.Body[:4096])
	if hex.EncodeToString(ack[:]) != parts[2] {
		t.Fatal("saved cursor does not follow the acknowledged bytes")
	}
	r, err := a.OpenCursor(context.Background(), binding, record.ID, parts[1])
	if err != nil {
		t.Fatal("cursor required old process affinity or the old active key", err)
	}
	tail := readCursorTail(t, r)
	if !bytes.Equal(tail, original.Body[4096:]) {
		t.Fatal("restarted observer duplicated or lost encoded events")
	}
	var intents, attempts int64
	if err := st.DB().Model(&model.GenerationIntent{}).Count(&intents).Error; err != nil || intents != 0 {
		t.Fatal("observation created a durable job", err)
	}
	if err := st.DB().Model(&model.BillingAttempt{}).Count(&attempts).Error; err != nil || attempts != 0 {
		t.Fatal("reconnecting observer started another attempt", err)
	}
}
