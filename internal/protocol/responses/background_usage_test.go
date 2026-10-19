package responses

import (
	"fmt"
	"strings"
	"testing"

	bg "minigateway/internal/foundation/contracts/background/v1"
	bgv2 "minigateway/internal/foundation/contracts/background/v2"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

func TestBackgroundUsageJSONAndSSEPreserveOptionalQuantities(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, sample := range []struct {
			name, usage string
			complete    v2.Completeness
			input       *int64
		}{
			{"missing", "", v2.Unknown, nil},
			{"null", `,"usage":null`, v2.Unknown, nil},
			{"partial", `,"usage":{"input_tokens":0,"input_tokens_details":{"cached_tokens":0}}`, v2.Partial, bgQuantity(0)},
			{"exact", `,"usage":{"input_tokens":9007199254740993,"output_tokens":1,"total_tokens":9007199254740994,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":0}}`, v2.Complete, bgQuantity(9007199254740993)},
		} {
			t.Run(fmt.Sprintf("%t/%s", stream, sample.name), func(t *testing.T) {
				p, problem := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: bg.RemoteSubmit, Body: []byte(fmt.Sprintf(`{"model":"m","background":true,"stream":%t,"input":"x"}`, stream))}, backgroundTarget())
				if problem != nil {
					t.Fatal(problem)
				}
				body := fmt.Sprintf(`{"id":"resp_native","object":"response","status":"incomplete","output":[]%s}`, sample.usage)
				if stream {
					frames, err := backgroundFrame(p.Input.Decoder.NewStream(), fmt.Sprintf(`{"type":"response.incomplete","sequence_number":0,"response":%s}`, body))
					if err != nil || len(frames) != 1 || !frames[0].Complete {
						t.Fatal(frames, err)
					}
				} else if _, _, err := p.Input.Decoder.Decode([]byte(body)); err != nil {
					t.Fatal(err)
				}
				detailed := p.Progress.(bgv2.Observer).DrainUsage()
				if detailed.State != bg.RemoteIncomplete || detailed.Usage == nil || detailed.Usage.Completeness != sample.complete || detailed.Usage.Validate() != nil {
					t.Fatal("state/usage lost", detailed)
				}
				got := detailed.Usage.Counts.InputTokens
				if (got == nil) != (sample.input == nil) || got != nil && *got != *sample.input {
					t.Fatal("unknown/zero/large quantity collapsed", got)
				}
				if sample.name == "partial" && (detailed.Observation.Usage != nil || detailed.Usage.Counts.TotalTokens != nil || detailed.Usage.Breakdown.CachedInputTokens == nil || *detailed.Usage.Breakdown.CachedInputTokens != 0) {
					t.Fatal("partial quantities inferred", detailed)
				}
				if old := p.Progress.Drain(); old.RemoteID != "" || old.Usage != nil {
					t.Fatal("v1 drain replayed detailed observation", old)
				}
			})
		}
	}
}

func bgQuantity(n int64) *int64 { return &n }

func TestBackgroundUsageRejectsInvalidDetailsBeforeObservation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, usage := range []string{
			`{"input_tokens":-1}`, `{"input_tokens":2,"input_tokens_details":{"cached_tokens":3}}`,
			`{"output_tokens":1,"output_tokens_details":{"reasoning_tokens":2}}`, `{"input_tokens_details":{"cache_write_tokens":-1}}`,
			`{"input_tokens":2,"input_tokens_details":{"cached_tokens":1.5}}`, `{"total_tokens":9223372036854775808}`,
		} {
			p, _ := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: bg.RemoteSubmit, Body: []byte(fmt.Sprintf(`{"model":"m","background":true,"stream":%t,"input":"x"}`, stream))}, backgroundTarget())
			body := fmt.Sprintf(`{"id":"resp_native","object":"response","status":"completed","output":[],"usage":%s}`, usage)
			var err error
			if stream {
				_, err = backgroundFrame(p.Input.Decoder.NewStream(), fmt.Sprintf(`{"type":"response.completed","sequence_number":0,"response":%s}`, body))
			} else {
				_, _, err = p.Input.Decoder.Decode([]byte(body))
			}
			if err == nil {
				t.Fatal("invalid usage accepted", stream, usage)
			}
			observation := p.Progress.(bgv2.Observer).DrainUsage()
			if observation.RemoteID != "" || observation.Usage != nil || strings.Contains(string(observation.Document), "completed") {
				t.Fatal("rejected usage reached observer", observation)
			}
		}
	}
}
