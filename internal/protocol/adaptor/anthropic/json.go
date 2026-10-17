package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"minigateway/internal/protocol/canonical"
)

// 这个文件放 anthropic 包自己的 JSON 辅助函数。
// 不放进公共包的原因：extractExtra/mergeExtra 的「已建模字段列表」
// 每个协议都不一样，公共包没法知道各协议的字段全集。
// sseData 是纯 SSE 解析，三家共用一份实现即可（在 adaptor 包）。

// extractExtra 从请求体里摘出「Claude 协议未建模」的字段。
func extractExtra(body []byte, modeled ...string) map[string]json.RawMessage {
	var all map[string]json.RawMessage
	if json.Unmarshal(body, &all) != nil {
		return nil
	}
	for _, k := range modeled {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil
	}
	return all
}

// Unsupported fields are request errors, not a log-only lossy conversion.
func mergeExtra(body []byte, extra map[string]json.RawMessage, supported map[string]bool) ([]byte, error) {
	if len(extra) == 0 {
		return body, nil
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(body, &all) != nil {
		return body, nil
	}
	for k, v := range extra {
		if _, exists := all[k]; exists {
			continue
		}
		if supported[k] {
			all[k] = v
			continue
		}
		return nil, fmt.Errorf("%w: field %q cannot be preserved", ErrUnsupported, k)
	}
	return json.Marshal(all)
}

// sseData 取出一行 SSE 的 data 载荷。
func sseData(line []byte) (string, bool) {
	s := strings.TrimSpace(string(line))
	if s == "" || strings.HasPrefix(s, ":") {
		return "", false
	}
	if !strings.HasPrefix(s, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(s, "data:")), true
}

// buildClaudeStreamLine 把规范 chunk 写成 Claude 的一行 SSE。
//
// 未来场景（Claude 客户端 ← 其他上游）才用到，当前项目客户端固定 OpenAI。
// 保留完整实现是为了 ProtocolAdaptor 接口的完整性。
func buildClaudeStreamLine(chunk *canonical.StreamChunk) ([]byte, error) {
	// 首帧：message_start
	if chunk.Role != "" {
		ev := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            chunk.ID,
				"type":          "message",
				"role":          "assistant",
				"model":         chunk.Model,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
			},
		}
		if u := chunk.Usage; u != nil {
			ev["message"].(map[string]any)["usage"] = map[string]any{
				"input_tokens":  u.PromptTokens,
				"output_tokens": 0,
			}
		}
		return json.Marshal(ev)
	}

	// 文本增量：content_block_delta
	var texts []string
	for _, p := range chunk.Content {
		if p.Type == canonical.PartText {
			texts = append(texts, p.Text)
		}
	}
	if len(texts) > 0 {
		ev := map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": strings.Join(texts, ""),
			},
		}
		return json.Marshal(ev)
	}

	// 工具调用增量
	for _, tc := range chunk.ToolCalls {
		ev := map[string]any{
			"type":  "content_block_delta",
			"index": tc.Index,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": tc.Arguments,
			},
		}
		return json.Marshal(ev)
	}

	// 结束帧：message_delta
	if chunk.FinishReason != "" {
		ev := map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   unmapStopReason(chunk.FinishReason),
				"stop_sequence": nil,
			},
		}
		if u := chunk.Usage; u != nil {
			ev["usage"] = map[string]any{"output_tokens": u.CompletionTokens}
		}
		return json.Marshal(ev)
	}

	// usage 尾帧
	if chunk.Usage != nil {
		ev := map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": chunk.Usage.CompletionTokens},
		}
		return json.Marshal(ev)
	}
	return nil, nil
}
