package websocket

import (
	"context"
	"net/http"

	ws "github.com/coder/websocket"
)

func (m *Manager) CheckOrigin(r *http.Request) error {
	if r == nil || !m.allowedOrigin(r) {
		return ErrOrigin
	}
	return nil
}

// CloseStatus uses only fixed close codes and an empty reason. Its one bounded
// worker stays in manager capacity and shutdown accounting after caller timeout.
func (c *Conn) CloseStatus(ctx context.Context, code uint16) error {
	if ctx == nil {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch code {
	case 1000, 1001, 1003, 1008, 1009, 1011, 1013:
	default:
		return ErrInvalidMessage
	}
	if !c.statusClosing.CompareAndSwap(false, true) {
		return ErrClosed
	}
	m := c.owner
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return ErrClosed
	}
	m.pending++
	m.wg.Add(1)
	m.mu.Unlock()
	finished := make(chan error, 1)
	go func() {
		err := c.raw.Close(ws.StatusCode(code), "")
		c.Close()
		m.mu.Lock()
		m.pending--
		m.mu.Unlock()
		m.wg.Done()
		if err != nil {
			err = ErrClosed
		}
		finished <- err
	}()
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
