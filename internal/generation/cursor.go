package generation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"sync"
	"time"

	"minigateway/internal/access"
	state "minigateway/internal/foundation/contracts/state/v1"
)

const cursorLifetime = 30 * time.Second

// CursorPage contains encoded bytes, not independently framed protocol events.
// Cursor points AFTER Body; consumers persist it only after accepting that page.
type CursorPage = state.CursorPage

// CursorReader owns one authenticated sealed snapshot (at most 8 MiB). Its
// observation context/Close cannot cancel, replay or settle the generation.
type CursorReader struct {
	mu      sync.Mutex
	archive *Archive
	binding access.Binding
	data    Recording
	offset  int
	first   bool
	done    bool
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	stop    func() bool
	release func()
}

// There are at most four snapshots (two per user), independent of stored
// records. The owner map cannot grow beyond the bounded live snapshot count.
func (a *Archive) acquireCursor(userID uint) (func(), error) {
	a.cursorMu.Lock()
	defer a.cursorMu.Unlock()
	if a.cursors >= 4 || a.owners[userID] >= 2 {
		return nil, ErrArchiveLimit
	}
	if a.owners == nil {
		a.owners = make(map[uint]int)
	}
	a.cursors++
	a.owners[userID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			a.cursorMu.Lock()
			defer a.cursorMu.Unlock()
			a.cursors--
			a.owners[userID]--
			if a.owners[userID] == 0 {
				delete(a.owners, userID)
			}
		})
	}, nil
}

func cursorAAD(record state.GenerationRecord) []byte {
	return []byte(fmt.Sprintf("TXAPI/generation-cursor/v1/%s/%d/%d/%d/%d/%d/%d/%d/",
		record.ID, record.UserID, record.TokenID, record.CreatedAt, record.ExpiresAt, record.MaxBytes, record.StoredBytes, record.NextSequence))
}

// OpenCursor authenticates ALL segments before exposing a page. Reconnection
// reloads one bounded snapshot, not one full body for every page. No DB cursor,
// in-memory affinity, extra persistent state or transaction survives this call.
func (a *Archive) OpenCursor(ctx context.Context, binding access.Binding, id, token string) (reader *CursorReader, err error) {
	if len(token) > 256 {
		return nil, ErrArchiveIntegrity
	}
	if err := a.authorize(ctx, binding); err != nil {
		return nil, err
	}
	release, err := a.acquireCursor(binding.UserID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if recover() != nil {
			reader, err = nil, ErrArchiveUnavailable
		}
		if reader == nil {
			release()
		}
	}()
	data, err := a.Read(ctx, binding, id)
	if err != nil {
		return nil, err
	}
	offset := uint64(0)
	if token != "" {
		encoded, decodeErr := base64.RawURLEncoding.Strict().DecodeString(token)
		if decodeErr != nil {
			return nil, ErrArchiveIntegrity
		}
		plain, openErr := a.keys.Open(encoded, cursorAAD(data.record))
		if openErr != nil || len(plain) != 9 || plain[0] != 1 {
			return nil, ErrArchiveIntegrity
		}
		offset = binary.BigEndian.Uint64(plain[1:])
		clear(plain)
		if offset > uint64(len(data.Body)) {
			return nil, ErrArchiveIntegrity
		}
	}
	deadline := time.Now().Add(cursorLifetime)
	if expiry := time.UnixMilli(data.record.ExpiresAt); expiry.Before(deadline) {
		deadline = expiry
	}
	observation, cancel := context.WithDeadline(ctx, deadline)
	r := &CursorReader{archive: a, binding: binding, data: data, offset: int(offset), first: true, ctx: observation, cancel: cancel, release: release}
	r.mu.Lock()
	r.stop = context.AfterFunc(observation, func() { _ = r.Close() })
	if err := observation.Err(); err != nil {
		r.closeLocked()
		r.mu.Unlock()
		return nil, err
	}
	r.mu.Unlock()
	return r, nil
}

// Next returns a 4-64 KiB page (or its smaller final tail). The current model
// authority is refreshed per page. Byte boundaries may split UTF-8/SSE framing;
// only the operation consumer may reconstruct those protocol events.
func (r *CursorReader) Next(ctx context.Context, maxBytes int) (CursorPage, error) {
	if maxBytes < 4096 || maxBytes > 64<<10 {
		return CursorPage{}, ErrArchiveLimit
	}
	if !r.mu.TryLock() {
		return CursorPage{}, ErrArchiveUnavailable
	}
	defer r.mu.Unlock()
	if r.done {
		return CursorPage{}, io.EOF
	}
	if r.closed {
		return CursorPage{}, ErrArchiveUnavailable
	}
	readCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	defer func() { stop(); cancel() }()
	if r.ctx.Err() != nil {
		cancel()
	}
	principal, err := r.archive.principal(readCtx, r.binding)
	if err != nil {
		r.closeLocked()
		return CursorPage{}, err
	}
	if !principal.Policy.Allows(r.data.Result.Model) {
		r.closeLocked()
		return CursorPage{}, access.ErrUnauthorized
	}
	if r.data.record.ExpiresAt <= time.Now().UnixMilli() {
		r.closeLocked()
		return CursorPage{}, ErrArchiveUnavailable
	}
	end := r.offset + min(maxBytes, len(r.data.Body)-r.offset)
	payload := make([]byte, 9)
	payload[0] = 1
	binary.BigEndian.PutUint64(payload[1:], uint64(end))
	sealed, err := r.archive.keys.Seal(payload, cursorAAD(r.data.record))
	if err != nil {
		r.closeLocked()
		return CursorPage{}, ErrArchiveUnavailable
	}
	page := CursorPage{Body: bytes.Clone(r.data.Body[r.offset:end]), Cursor: base64.RawURLEncoding.EncodeToString(sealed), Done: end == len(r.data.Body)}
	if r.first {
		metadata := r.data.Metadata
		metadata.Values = maps.Clone(metadata.Values)
		page.Metadata = &metadata
	}
	if page.Done {
		result := r.data.Result
		page.Result = &result
		r.done = true
		r.closeLocked()
	} else {
		r.first, r.offset = false, end
	}
	return page, nil
}

func (r *CursorReader) closeLocked() {
	if r.closed {
		return
	}
	r.closed = true
	if r.stop != nil {
		r.stop()
	}
	r.cancel()
	clear(r.data.Body)
	r.data = Recording{}
	r.release()
}

// Close interrupts an in-flight read, joins it via the snapshot lock, drops
// plaintext references and releases capacity. This is not secure erasure.
func (r *CursorReader) Close() error {
	r.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeLocked()
	return nil
}
