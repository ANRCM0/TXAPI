package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
)

// The smoke harness runs the real main/lifecycle with a loopback-only network
// fixture. Neither this environment switch nor the dialer enters go build.
func TestMain(m *testing.M) {
	if os.Getenv("TXAPI_SMOKE_MAIN") != "1" {
		os.Exit(m.Run())
	}
	address := os.Getenv("TXAPI_SMOKE_UPSTREAM")
	transport, err := smokeTransport(address)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	http.DefaultTransport = transport
	defer transport.CloseIdleConnections()
	main()
}

func smokeTransport(address string) (*http.Transport, error) {
	host, port, err := net.SplitHostPort(address)
	number, numberErr := strconv.Atoi(port)
	if err != nil || host != "127.0.0.1" || numberErr != nil || number < 1 || number > 65535 {
		return nil, errors.New("smoke upstream must be a literal loopback address and port")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("smoke harness forbids other destinations")
	}
	transport.DialTLSContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != "api.openai.com:443" {
			return nil, errors.New("smoke harness forbids other destinations")
		}
		// No TLS assertion: this is the isolated fake provider's HTTP socket.
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return transport, nil
}

func TestSmokeTransportCannotReachExternalDestinations(t *testing.T) {
	for _, address := range []string{"api.openai.com:443", "localhost:1234", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:80/path"} {
		if _, err := smokeTransport(address); err == nil {
			t.Fatalf("unsafe fixture address: %s", address)
		}
	}
	transport, err := smokeTransport("127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	if transport.Proxy != nil {
		t.Fatal("smoke inherited a deployment proxy")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "api.openai.com:80"); err == nil {
		t.Fatal("plain HTTP bypassed the fixture")
	}
	if _, err := transport.DialTLSContext(context.Background(), "tcp", "other.invalid:443"); err == nil {
		t.Fatal("unregistered TLS destination bypassed the fixture")
	}
}
