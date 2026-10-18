package websocket

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"

	ws "github.com/coder/websocket"
)

// Dial owns only the handshake context. A successful socket belongs to the
// manager lifetime and shares its capacity with accepted connections.
func (m *Manager) Dial(parent context.Context, rawURL string, header http.Header) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if parent == nil || err != nil || len(rawURL) > 2048 || strings.ContainsAny(rawURL, "\r\n\\") ||
		u == nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrHandshake
	}
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if len(m.conns)+m.pending >= m.opts.Connections {
		m.mu.Unlock()
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
	ctx, cancel := context.WithTimeout(parent, m.opts.HandshakeTimeout)
	stop := context.AfterFunc(m.ctx, cancel)
	defer func() { stop(); cancel() }()
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: m.opts.HandshakeTimeout}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: m.opts.HandshakeTimeout, ResponseHeaderTimeout: m.opts.HandshakeTimeout,
		MaxResponseHeaderBytes: 16 << 10, MaxConnsPerHost: 1, DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	raw, response, err := ws.Dial(ctx, rawURL, &ws.DialOptions{HTTPClient: client, HTTPHeader: header.Clone(), CompressionMode: ws.CompressionDisabled})
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, ErrHandshake
	}
	raw.SetReadLimit(m.opts.MaxMessageBytes)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil || ctx.Err() != nil {
		_ = raw.CloseNow()
		return nil, ErrClosed
	}
	lifetime, lifetimeCancel := context.WithTimeout(m.ctx, m.opts.MaxLifetime)
	conn := &Conn{raw: raw, owner: m, ctx: lifetime, cancel: lifetimeCancel, done: make(chan struct{}), writer: make(chan struct{}, 1)}
	m.pending--
	m.conns[conn] = struct{}{}
	registered = true
	context.AfterFunc(lifetime, conn.Close)
	return conn, nil
}
