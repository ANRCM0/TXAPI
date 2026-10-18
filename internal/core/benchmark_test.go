package core_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/core"
	"minigateway/internal/gateway"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/meta"
)

type benchWriter struct {
	header http.Header
	delay  time.Duration
}

func (w *benchWriter) Header() http.Header { return w.header }
func (w *benchWriter) WriteHeader(int)     {}
func (w *benchWriter) Write(p []byte) (int, error) {
	if w.delay > 0 {
		time.Sleep(w.delay)
	}
	return len(p), nil
}
func (w *benchWriter) Flush()                           {}
func (w *benchWriter) SetWriteDeadline(time.Time) error { return nil }

func BenchmarkExecutor(b *testing.B) {
	for _, scenario := range []struct {
		name, protocol string
		stream         bool
		slow           time.Duration
		concurrency    int
	}{
		{"same-json", "openai", false, 0, 1},
		{"same-sse", "openai", true, 0, 1},
		{"cross-gemini-sse", "gemini", true, 0, 1},
		{"slow-client-sse", "openai", true, time.Millisecond, 1},
		{"concurrent-sse", "openai", true, 0, 4},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			const upstreamDelay = 5 * time.Millisecond
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				time.Sleep(upstreamDelay)
				if !scenario.stream {
					io.WriteString(w, `{"id":"bench","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":16,"completion_tokens":8,"total_tokens":24}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for i := 0; i < 8; i++ {
					if scenario.protocol == "gemini" {
						fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\n")
					} else {
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
					}
					w.(http.Flusher).Flush()
				}
				if scenario.protocol == "gemini" {
					fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":16,\"candidatesTokenCount\":8,\"totalTokenCount\":24}}\n\n")
				} else {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":16,\"completion_tokens\":8,\"total_tokens\":24}}\n\ndata: [DONE]\n\n")
				}
			}))
			defer upstream.Close()
			a, _ := protocol.Resolve(scenario.protocol)
			payload := []byte(fmt.Sprintf(`{"model":"m","stream":%t,"messages":[{"role":"user","content":"%s"}]}`, scenario.stream, strings.Repeat("x", 512)))
			input, err := protocol.Prepare(a, payload, &meta.Meta{ActualModel: "m", BaseURL: upstream.URL, APIKey: "bench-key", IsStream: scenario.stream, Mode: meta.ModeChatCompletions})
			if err != nil {
				b.Fatal(err)
			}
			input.Timeout = 2 * time.Second
			e := core.New(core.Options{})
			defer e.Close()
			samples := make([]time.Duration, b.N)
			b.Logf("go=%s os=%s arch=%s logicalCPUs=%d payload=%dB concurrency=%d upstreamDelay=%s slowWrite=%s", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), len(payload), scenario.concurrency, upstreamDelay, scenario.slow)
			b.ReportAllocs()
			b.ResetTimer()
			var next atomic.Int64
			var group sync.WaitGroup
			for worker := 0; worker < scenario.concurrency; worker++ {
				group.Add(1)
				go func() {
					defer group.Done()
					for {
						index := int(next.Add(1) - 1)
						if index >= b.N {
							return
						}
						w := &benchWriter{header: make(http.Header), delay: scenario.slow}
						started := time.Now()
						result := e.Execute(context.Background(), w, gateway.ToCoreInput(input))
						samples[index] = time.Since(started)
						if result.StatusCode != 200 {
							b.Errorf("result=%+v", result)
							return
						}
					}
				}()
			}
			group.Wait()
			b.StopTimer()
			if b.Failed() {
				return
			}
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			p95 := b.N * 95 / 100
			if p95 >= b.N {
				p95 = b.N - 1
			}
			p99 := b.N * 99 / 100
			if p99 >= b.N {
				p99 = b.N - 1
			}
			b.ReportMetric(float64(samples[p95].Nanoseconds())/1e6, "p95-ms")
			b.ReportMetric(float64(samples[p99].Nanoseconds())/1e6, "p99-ms")
			b.ReportMetric(float64(scenario.concurrency), "concurrency")
		})
	}
}
