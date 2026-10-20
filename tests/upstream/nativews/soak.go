package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const soakRequestCap = 8192

type soakInput struct {
	Seconds  int `json:"seconds"`
	Workers  int `json:"workers"`
	Protocol int `json:"protocol"`
}

type soakReport struct {
	Configuration soakInput        `json:"configuration"`
	RequestCap    int              `json:"request_cap"`
	Started       int              `json:"started"`
	Completed     int              `json:"completed"`
	ElapsedNS     int64            `json:"elapsed_ns"`
	Samples       []baselineSample `json:"samples"`
	Errors        []string         `json:"errors"`
	ActiveAfter   int              `json:"active_after"`
	TimingScope   string           `json:"timing_scope"`
}

func (f *fixture) soakHandler(directory string) (http.HandlerFunc, error) {
	ca, err := os.ReadFile(filepath.Join(directory, "ca.pem"))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("soak CA unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var input soakInput
		raw, readErr := io.ReadAll(io.LimitReader(r.Body, 1025))
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if r.Method != http.MethodPost || readErr != nil || len(raw) > 1024 || decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF ||
			input.Seconds < 5 || input.Seconds > 1800 || input.Workers < 1 || input.Workers > 2 || input.Protocol < 1 || input.Protocol > 2 {
			http.Error(w, "seconds 5..1800, workers 1..2, protocol 1|2 required", 400)
			return
		}
		if !f.soakActive.CompareAndSwap(false, true) {
			http.Error(w, "soak already active", 409)
			return
		}
		defer f.soakActive.Store(false)
		bit := uint32(1 << input.Protocol)
		if f.soakProfiles.Load()&bit != 0 {
			http.Error(w, "one soak per protocol and fixture", 409)
			return
		}
		f.soakProfiles.Or(bit)
		transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: input.Protocol == 2,
			TLSClientConfig:     &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second, MaxConnsPerHost: input.Workers}
		if input.Protocol == 1 {
			transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
			transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
		}
		defer transport.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(input.Seconds+5)*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if http.NewResponseController(w).Flush() != nil {
			return
		}
		report := f.measureSoak(ctx, &http.Client{Transport: transport}, "https://127.0.0.1:8443", input, time.Duration(input.Seconds)*time.Second)
		_ = json.NewEncoder(w).Encode(report)
	}, nil
}

func (f *fixture) measureSoak(ctx context.Context, client *http.Client, base string, input soakInput, duration time.Duration) soakReport {
	report := soakReport{Configuration: input, RequestCap: soakRequestCap,
		TimingScope: "all started requests retained; monotonic client/provider event intervals; actual gateway and TLS reverse proxy; provider HTTP2 required; 100ms fixture event pacing"}
	if input.Workers < 1 || input.Workers > 2 || input.Protocol < 1 || input.Protocol > 2 || duration <= 0 || duration > 1800*time.Second {
		report.Errors = []string{"invalid bounded soak configuration"}
		return report
	}
	run := &baselineRun{records: make(map[string]*baselineRecord), delay: 100 * time.Millisecond}
	f.mu.Lock()
	f.baseline = run
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.baseline = nil
		f.mu.Unlock()
	}()
	start := time.Now()
	until := start.Add(duration)
	var mu sync.Mutex
	var workers sync.WaitGroup
	stop := false
	for worker := 0; worker < input.Workers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				mu.Lock()
				if stop || time.Now().After(until) || ctx.Err() != nil {
					mu.Unlock()
					return
				}
				if report.Started >= soakRequestCap {
					report.Errors = append(report.Errors, "request cap reached before requested duration")
					stop = true
					mu.Unlock()
					return
				}
				sequence := report.Started
				report.Started++
				mu.Unlock()
				tag := fmt.Sprintf("h2-baseline-soak-%d-%d", input.Protocol, sequence)
				run.mu.Lock()
				run.records[tag] = &baselineRecord{drained: make(chan struct{})}
				run.mu.Unlock()
				sample := run.callProtocolCompletion(ctx, client, base, baselineClientKey, "alias", tag, input.Protocol, true)
				sample.Pair, sample.Target = sequence, "gateway"
				run.mu.Lock()
				if sample.Drained {
					delete(run.records, tag)
				}
				run.mu.Unlock()
				mu.Lock()
				report.Samples = append(report.Samples, sample)
				if sample.Error == "" {
					report.Completed++
				} else {
					report.Errors = append(report.Errors, fmt.Sprintf("request %d: %s", sequence, sample.Error))
					stop = true
				}
				mu.Unlock()
			}
		}()
	}
	workers.Wait()
	report.ElapsedNS = time.Since(start).Nanoseconds()
	run.mu.Lock()
	report.ActiveAfter = len(run.records)
	run.mu.Unlock()
	if report.ElapsedNS < duration.Nanoseconds() || report.Started == 0 || report.Started != report.Completed || report.ActiveAfter != 0 {
		report.Errors = append(report.Errors, "soak interval, completion or drain mismatch")
	}
	return report
}

func soakCall(ctx context.Context, client *http.Client, base, payload string, output io.Writer) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/soak", strings.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil || len(raw) > 32<<20 || response.StatusCode != 200 || !json.Valid(raw) {
		return errors.New("bounded soak response unavailable or invalid")
	}
	_, err = output.Write(raw)
	return err
}

// Runs inside the non-root gateway container. It observes PID 1 only and never
// opens its environment, command arguments, database, configuration or keys.
func processResources(output io.Writer) error {
	raw, err := os.ReadFile("/proc/1/stat")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(raw)[strings.LastIndex(string(raw), ")")+1:])
	if len(fields) < 20 {
		return errors.New("proc stat unavailable")
	}
	user, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return err
	}
	system, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return err
	}
	status, err := os.ReadFile("/proc/1/status")
	if err != nil {
		return err
	}
	result := map[string]any{"cpu_ticks": user + system, "process_identity": fields[19]}
	for _, line := range strings.Split(string(status), "\n") {
		values := strings.Fields(line)
		if len(values) >= 2 && values[0] == "VmRSS:" {
			value, err := strconv.ParseUint(values[1], 10, 64)
			if err != nil {
				return err
			}
			result["rss_bytes"] = value * 1024
		}
	}
	fd, err := os.ReadDir("/proc/1/fd")
	if err != nil {
		return err
	}
	result["fd_count"] = len(fd)
	return json.NewEncoder(output).Encode(result)
}
