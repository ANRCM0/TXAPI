// Package upstreamhttp owns bounded OAuth wire calls through the existing core.
package upstreamhttp

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"time"

	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

type Caller struct{ executor *core.Executor }

// A supplied client/transport is trusted assembly input, not runtime config.
func NewCaller(client *http.Client) *Caller {
	owned := http.Client{}
	if client != nil {
		owned = *client
	}
	transport := owned.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if original, ok := transport.(*http.Transport); ok {
		copy := original.Clone()
		copy.MaxResponseHeaderBytes = 16 * 1024
		copy.MaxConnsPerHost = 4
		copy.DisableKeepAlives = true
		copy.ForceAttemptHTTP2 = false
		copy.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		copy.TLSHandshakeTimeout = 10 * time.Second
		copy.ResponseHeaderTimeout = 15 * time.Second
		owned.Transport = copy
	}
	owned.Jar = nil
	owned.Timeout = 20 * time.Second
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Caller{executor: core.New(core.Options{Client: &owned, MaxResponseBytes: auth.MaxResponseBytes})}
}

func (c *Caller) Call(ctx context.Context, request auth.Request) auth.Response {
	result := auth.Response{Phase: execution.NotDispatched}
	if c == nil || c.executor == nil || ctx == nil || len(request.Body.Value()) > auth.MaxRequestBytes || !validRequest(request) {
		return result
	}
	header := request.Header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	header.Set("User-Agent", "TXAPI")
	wire := c.executor.ExecuteRaw(ctx, core.RawInput{Method: request.Method, URL: request.URL,
		Header: header, Body: []byte(request.Body.Value()), Timeout: 20 * time.Second})
	return auth.Response{Phase: execution.Phase(wire.Phase), Status: wire.StatusCode,
		Header: wire.Header, Body: auth.NewSecret(string(wire.Body)), Complete: wire.Complete}
}

func (c *Caller) Close() {
	if c != nil && c.executor != nil {
		c.executor.Close()
	}
}

func validRequest(request auth.Request) bool {
	models := request.Method == http.MethodGet && request.URL == "https://api.openai.com/v1/models"
	switch request.Method {
	case http.MethodPost:
		if request.URL != "https://auth.openai.com/api/accounts/oauth/token" ||
			request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			return false
		}
	case http.MethodGet:
		if (!models && request.URL != "https://auth.openai.com/.well-known/jwks.json") || request.Body.Value() != "" {
			return false
		}
	default:
		return false
	}
	if models && !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
		return false
	}
	for key, values := range request.Header {
		if len(values) != 1 {
			return false
		}
		// Bearer has one required separator; validate the token separately so
		// the general no-whitespace header check does not reject that space.
		if key != "Authorization" && !auth.ValidWireText(values[0], 128) {
			return false
		}
		switch strings.ToLower(key) {
		case "authorization":
			if !models || !strings.HasPrefix(values[0], "Bearer ") || !auth.ValidWireText(strings.TrimPrefix(values[0], "Bearer "), auth.MaxTokenBytes) {
				return false
			}
		case "accept":
			if values[0] != "application/json" {
				return false
			}
		case "content-type":
			if request.Method != http.MethodPost || values[0] != "application/x-www-form-urlencoded" {
				return false
			}
		default:
			return false
		}
		if key != http.CanonicalHeaderKey(key) {
			return false
		}
	}
	return true
}
