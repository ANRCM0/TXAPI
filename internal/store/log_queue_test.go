package store

import (
	"sync"
	"testing"

	"minigateway/internal/model"
)

func TestFullOrClosedLogQueueDropsWithoutDatabaseIO(t *testing.T) {
	// No database and no consumer: any fallback DB write would panic.
	w := &asyncLogWriter{ch: make(chan *model.RequestLog, 1)}
	entry := &model.RequestLog{RequestID: "original"}
	w.Write(entry)
	entry.RequestID = "mutated"
	w.Write(entry)
	if w.dropped.Load() != 1 || (<-w.ch).RequestID != "original" {
		t.Fatal("full queue blocked, did not count a drop, or retained caller-owned data")
	}
	w.closed = true
	w.Write(entry)
	if w.dropped.Load() != 2 || len(w.ch) != 0 {
		t.Fatal("closed writer accepted a log or failed to count the drop")
	}
}

func TestLogQueueConcurrentCloseAccountsForEveryAcceptedOrDroppedRecord(t *testing.T) {
	st := newTestStore(t)
	var wg sync.WaitGroup
	const count = 100
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); st.WriteLog(&model.RequestLog{RequestID: "close-race"}) }()
	}
	st.logs.Close()
	wg.Wait()
	n, err := st.CountLogs()
	if err != nil {
		t.Fatal(err)
	}
	stats := st.LogQueueStats()
	if uint64(n)+stats.Dropped+stats.Failed != count || stats.Depth != 0 {
		t.Fatalf("accepted entries disappeared at close: rows=%d stats=%+v", n, stats)
	}
}
