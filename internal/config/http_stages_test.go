package config

import (
	"fmt"
	"testing"
)

func TestHTTPStageTimeoutConfigurationBounds(t *testing.T) {
	cfg, err := Load(writeCfg(t, minimalCfg))
	if err != nil || cfg.Server.HTTPStageTimeouts != (HTTPStageTimeoutsConfig{}) {
		t.Fatalf("stages must default off: %+v %v", cfg, err)
	}
	for _, field := range []string{"headers_seconds", "first_effective_output_seconds", "stream_idle_seconds", "downstream_write_seconds"} {
		for _, seconds := range []string{"-1", "3601", "9223372036854775808", "1.5"} {
			t.Run(field+"/"+seconds, func(t *testing.T) {
				body := fmt.Sprintf("%sserver:\n  http_stage_timeouts:\n    %s: %s\n", minimalCfg, field, seconds)
				if _, err := Load(writeCfg(t, body)); err == nil {
					t.Fatal("invalid timeout accepted")
				}
			})
		}
	}
	cfg, err = Load(writeCfg(t, minimalCfg+"server:\n  http_stage_timeouts:\n    headers_seconds: 1\n    first_effective_output_seconds: 3600\n    stream_idle_seconds: 10\n    downstream_write_seconds: 0\n"))
	want := HTTPStageTimeoutsConfig{HeadersSeconds: 1, FirstEffectiveOutputSeconds: 3600, StreamIdleSeconds: 10}
	if err != nil || cfg.Server.HTTPStageTimeouts != want {
		t.Fatalf("stages not preserved: %+v %v", cfg, err)
	}
	if _, err := Load(writeCfg(t, minimalCfg+"server:\n  http_stage_timeouts:\n    header_seconds: 1\n")); err == nil {
		t.Fatal("misspelled stage silently disabled the budget")
	}
}
