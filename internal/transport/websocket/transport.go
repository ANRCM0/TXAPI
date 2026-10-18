// Package websocket owns bounded WebSocket I/O, not application protocol,
// authentication, generations or billing. Callers authenticate before Accept
// and must keep one reader running to process peer control/close frames.
package websocket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	ws "github.com/coder/websocket"
)

var (
	ErrClosed         = errors.New("websocket transport is closed")
	ErrCapacity       = errors.New("websocket connection capacity reached")
	ErrOrigin         = errors.New("websocket origin is not allowed")
	ErrHandshake      = errors.New("websocket handshake failed")
	ErrMessageLimit   = errors.New("websocket message size limit exceeded")
	ErrInvalidMessage = errors.New("invalid websocket message")
	ErrConcurrentRead = errors.New("websocket already has a reader")
)

type MessageType int

const (
	Text   MessageType = 1
	Binary MessageType = 2
)

type Options struct {
	Connections      int
	MaxMessageBytes  int64
	HandshakeTimeout time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	MaxLifetime      time.Duration
	// Exact http(s) origins, not wildcard patterns. Empty means same-origin
	// browsers only; native clients without an Origin still require outer auth.
	Origins []string
}

type Manager struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	closed  bool
	pending int
	conns   map[*Conn]struct{}
	wg      sync.WaitGroup
	done    chan struct{}
	opts    Options
	origins map[string]bool
}

type Snapshot struct{ Active, Pending int }

func New(ctx context.Context, opts Options) (*Manager, error) {
	if ctx == nil || opts.Connections < 1 || opts.Connections > 4096 || opts.MaxMessageBytes < 1 || opts.MaxMessageBytes > 20<<20 ||
		int64(opts.Connections)*opts.MaxMessageBytes > 32<<20 || opts.HandshakeTimeout <= 0 || opts.HandshakeTimeout > 30*time.Second ||
		opts.ReadTimeout <= 0 || opts.ReadTimeout > 5*time.Minute || opts.WriteTimeout <= 0 || opts.WriteTimeout > 30*time.Second ||
		opts.MaxLifetime < opts.ReadTimeout || opts.MaxLifetime > 24*time.Hour || len(opts.Origins) > 32 {
		return nil, fmt.Errorf("invalid websocket transport configuration")
	}
	origins := make(map[string]bool)
	for _, raw := range opts.Origins {
		origin, ok := canonicalOrigin(raw)
		if !ok {
			return nil, fmt.Errorf("websocket origins must be exact http(s) scheme and host")
		}
		origins[origin] = true
	}
	root, cancel := context.WithCancel(ctx)
	opts.Origins = nil
	m := &Manager{ctx: root, cancel: cancel, conns: make(map[*Conn]struct{}), done: make(chan struct{}), opts: opts, origins: origins}
	go func() {
		<-root.Done()
		m.mu.Lock()
		m.closed = true
		conns := make([]*Conn, 0, len(m.conns))
		for conn := range m.conns {
			conns = append(conns, conn)
		}
		m.mu.Unlock()
		for _, conn := range conns {
			conn.Close()
		}
		m.wg.Wait()
		close(m.done)
	}()
	return m, nil
}

func canonicalOrigin(raw string) (string, bool) {
	if len(raw) == 0 || len(raw) > 256 || strings.ContainsAny(raw, "*?#\\\r\n\t ,") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}

func (m *Manager) allowedOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 {
		return false
	}
	origin, ok := canonicalOrigin(values[0])
	if !ok {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return origin == strings.ToLower(scheme+"://"+r.Host) || m.origins[origin]
}

// Accept must follow authentication and identity-specific connection admission.
// The HTTP context owns only the handshake, never the upgraded connection.
// A writer supporting response-controller write deadlines is required, so an
// uncooperative handshake cannot silently bypass the configured deadline.
func (m *Manager) Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !m.allowedOrigin(r) {
		http.Error(w, "websocket origin is not allowed", http.StatusForbidden)
		return nil, ErrOrigin
	}
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		http.Error(w, "websocket transport is closed", http.StatusServiceUnavailable)
		return nil, ErrClosed
	}
	if len(m.conns)+m.pending >= m.opts.Connections {
		m.mu.Unlock()
		http.Error(w, "websocket capacity reached", http.StatusServiceUnavailable)
		return nil, ErrCapacity
	}
	m.pending++
	m.wg.Add(1)
	m.mu.Unlock()
	registered := false
	defer func() {
		if !registered {
			m.mu.Lock()
			m.pending--
			m.mu.Unlock()
			m.wg.Done()
		}
	}()
	if r.Context().Err() != nil {
		return nil, ErrHandshake
	}
	control := http.NewResponseController(w)
	if err := control.SetWriteDeadline(time.Now().Add(m.opts.HandshakeTimeout)); err != nil {
		http.Error(w, "websocket handshake deadlines are unavailable", http.StatusServiceUnavailable)
		return nil, ErrHandshake
	}
	deadlineDone := make(chan struct{})
	stop := context.AfterFunc(m.ctx, func() {
		defer close(deadlineDone)
		_ = control.SetWriteDeadline(time.Now())
	})
	origins := make([]string, 0, len(m.origins))
	for origin := range m.origins {
		origins = append(origins, strings.NewReplacer("[", "\\[", "]", "\\]").Replace(origin))
	}
	raw, err := ws.Accept(w, r, &ws.AcceptOptions{OriginPatterns: origins, CompressionMode: ws.CompressionDisabled})
	if !stop() {
		<-deadlineDone // Do not let a late cancellation overwrite the reset.
	}
	resetErr := control.SetWriteDeadline(time.Time{})
	if err != nil {
		return nil, ErrHandshake
	}
	if resetErr != nil {
		_ = raw.CloseNow()
		return nil, ErrHandshake
	}
	raw.SetReadLimit(m.opts.MaxMessageBytes)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		_ = raw.CloseNow()
		return nil, ErrClosed
	}
	ctx, cancel := context.WithTimeout(m.ctx, m.opts.MaxLifetime)
	conn := &Conn{raw: raw, owner: m, ctx: ctx, cancel: cancel, done: make(chan struct{}), writer: make(chan struct{}, 1)}
	m.pending--
	m.conns[conn] = struct{}{}
	registered = true
	context.AfterFunc(ctx, conn.Close)
	return conn, nil
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Snapshot{Active: len(m.conns), Pending: m.pending}
}

// Close aborts transport connections and joins pending handshakes. It does not
// join application workers: the assembler must separately cancel/join them
// before closing gateway/store. Timeout does not imply cleanup has finished.
func (m *Manager) Close(ctx context.Context) error {
	m.cancel()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Conn struct {
	raw           *ws.Conn
	owner         *Manager
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	once          sync.Once
	reading       atomic.Bool
	statusClosing atomic.Bool
	writer        chan struct{}
}

func (c *Conn) Context() context.Context { return c.ctx }
func (c *Conn) Done() <-chan struct{}    { return c.done }

func (c *Conn) operationContext(parent context.Context, timeout time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}

func (c *Conn) Read(ctx context.Context) (MessageType, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	if !c.reading.CompareAndSwap(false, true) {
		return 0, nil, ErrConcurrentRead
	}
	defer c.reading.Store(false)
	ctx, cancel := c.operationContext(ctx, c.owner.opts.ReadTimeout)
	defer cancel()
	typ, data, err := c.raw.Read(ctx)
	if err != nil {
		failure := ioError(ctx, err)
		c.Close()
		return 0, nil, failure
	}
	if typ == ws.MessageText && !utf8.Valid(data) {
		c.Close()
		return 0, nil, ErrInvalidMessage
	}
	return MessageType(typ), data, nil
}

func (c *Conn) Write(ctx context.Context, typ MessageType, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if typ != Text && typ != Binary || typ == Text && !utf8.Valid(data) {
		return ErrInvalidMessage
	}
	if int64(len(data)) > c.owner.opts.MaxMessageBytes {
		return ErrMessageLimit
	}
	ctx, cancel := c.operationContext(ctx, c.owner.opts.WriteTimeout)
	defer cancel()
	select {
	case c.writer <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.writer }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.raw.Write(ctx, ws.MessageType(typ), data); err != nil {
		failure := ioError(ctx, err)
		c.Close()
		return failure
	}
	return nil
}

func ioError(ctx context.Context, err error) error {
	if errors.Is(err, ws.ErrMessageTooBig) {
		return ErrMessageLimit
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrClosed
}

// Close deliberately aborts without a close-handshake wait. No dynamic peer
// reason, header, URL or payload is returned through transport errors.
func (c *Conn) Close() {
	c.once.Do(func() {
		c.cancel()
		_ = c.raw.CloseNow()
		c.owner.mu.Lock()
		delete(c.owner.conns, c)
		c.owner.mu.Unlock()
		c.owner.wg.Done()
		close(c.done)
	})
}
