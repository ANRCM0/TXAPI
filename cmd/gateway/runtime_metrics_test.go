package main

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/metrics"
)

func TestRuntimeMetricsDisabledWSAndLiveAdmission(t *testing.T) {
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 4, User: 2, Key: 1, Channel: 1})
	if err != nil {
		t.Fatal(err)
	}
	registry := metrics.New()
	registerRuntimeMetrics(registry, capacity, nil)
	request, err := capacity.AcquireRequest(context.Background(), access.Principal{UserID: 1, TokenID: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer request()
	attempt, err := capacity.AcquireAttempt(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer attempt()
	for _, active := range []int{1, 0} {
		var out strings.Builder
		if err := registry.Render(&out); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]int{"gateway_admission_requests_active": active, "gateway_admission_attempts_active": active, "gateway_native_ws_enabled": 0, "gateway_native_ws_connections_active": 0, "gateway_native_ws_rounds_active": 0} {
			if !strings.Contains(out.String(), "\n"+name+" "+strconv.Itoa(value)+"\n") {
				t.Fatalf("missing %s=%d: %s", name, value, out.String())
			}
		}
		attempt()
		request()
	}
}

func TestRuntimeMetricsSocketRoundCompletionAndDisconnect(t *testing.T) {
	for _, cancelRound := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelRound), func(t *testing.T) {
			f := newWSAssemblyFixture(t)
			client := &http.Client{Timeout: 2 * time.Second}
			wait := func(connections, rounds int) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				var last string
				for {
					response, err := client.Get(f.front.URL + "/metrics")
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(io.LimitReader(response.Body, 128<<10))
					_ = response.Body.Close()
					if err != nil || response.StatusCode != http.StatusOK {
						t.Fatalf("metrics read failed: %d %v", response.StatusCode, err)
					}
					last = string(body)
					matched := true
					for name, value := range map[string]int{"gateway_admission_requests_active": rounds, "gateway_admission_attempts_active": rounds, "gateway_native_ws_enabled": 1, "gateway_native_ws_connections_active": connections, "gateway_native_ws_rounds_active": rounds} {
						matched = matched && strings.Contains(last, "\n"+name+" "+strconv.Itoa(value)+"\n")
					}
					if matched {
						return
					}
					if time.Now().After(deadline) {
						t.Fatal("runtime metrics did not reach expected occupancy", connections, rounds, last)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			wait(0, 0)
			peer := f.dial(t)
			wait(1, 0)
			f.send(t, peer, "", "block", false)
			wsWait(t, f.blocked)
			wait(1, 1)
			if cancelRound {
				_ = peer.CloseNow()
			} else {
				close(f.release)
				f.receiveRound(t, peer)
				wait(1, 0)
				_ = peer.CloseNow()
			}
			wait(0, 0)
			if f.attempts.Load() != 1 || f.httpCalls.Load() != 0 {
				t.Fatal("observing metrics changed native execution")
			}
		})
	}
}
