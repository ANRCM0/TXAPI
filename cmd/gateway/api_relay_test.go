package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minigateway/internal/config"
)

func relayCommand(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], args...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TXAPI_SMOKE_MAIN=") && !strings.HasPrefix(value, "TXAPI_SMOKE_UPSTREAM=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "TXAPI_SMOKE_MAIN=1", "TXAPI_SMOKE_UPSTREAM=127.0.0.1:1")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("relay entry point did not terminate", ctx.Err())
	}
	return output, err
}

func TestAPIKeyRelayHasNoOAuthCommand(t *testing.T) {
	output, err := relayCommand(t, "-help")
	if err != nil || !strings.Contains(string(output), "-config") || strings.Contains(string(output), "upstream-account") {
		t.Fatalf("API relay command surface: %v\n%s", err, output)
	}
	for _, action := range []string{"authorize", "reauthorize", "refresh"} {
		t.Run(action, func(t *testing.T) {
			output, err := relayCommand(t, "-config", filepath.Join(t.TempDir(), "missing.yaml"), "-upstream-account", action)
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 2 || !strings.Contains(string(output), "flag provided but not defined: -upstream-account") {
				t.Fatalf("removed command was not rejected before configuration/store/network work: %v\n%s", err, output)
			}
		})
	}
}

func TestStrictLeaseCoversEnabledConsumerLifetime(t *testing.T) {
	c := &config.Config{}
	c.Server.TotalTimeoutSeconds = 30
	if lease := strictBillingLease(c); lease != 35*time.Second {
		t.Fatal("HTTP lease", lease)
	}
	c.ResponsesWS.Enabled = true
	if lease := strictBillingLease(c); lease <= 120*time.Second {
		t.Fatal("WS round exceeds reservation lifetime", lease)
	}
	c.Responses.Background.Enabled = true
	if lease := strictBillingLease(c); lease <= 5*time.Minute {
		t.Fatal("background deadline exceeds reservation lifetime", lease)
	}
	c.Server.TotalTimeoutSeconds = 600
	if lease := strictBillingLease(c); lease != 605*time.Second {
		t.Fatal("longer HTTP lease was shortened", lease)
	}
}
