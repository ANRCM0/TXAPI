package gemini

import (
	"encoding/json"
	"fmt"
	"strings"

	"minigateway/internal/protocol/canonical"
)

// 这个文件放 gemini 包自己的 JSON 辅助函数。
// 和 anthropic 的同名文件结构一致，但「已建模字段列表」不同。

// extractExtra 从请求体里摘出「Gemini 协议未建模」的字段。
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

// mergeExtra 把未建模字段合并回出站请求体。
// Unsupported fields fail before dispatch instead of silently changing semantics.
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

// buildGeminiStreamLine 把规范 chunk 写成 Gemini 的一行 SSE。
// 未来场景（Gemini 客户端出站）用，当前项目未启用。
// 每次调用仅编码当前规范 chunk，不在适配器内部拼接跨事件状态。
func buildGeminiStreamLine(chunk *canonical.StreamChunk) ([]byte, error) {
	var text string
	for _, p := range chunk.Content {
		if p.Type == canonical.PartText {
			text += p.Text
		}
	}

	cand := geminiCandidate{
		Content:      geminiContent{Role: "model"},
		FinishReason: unmapFinishReason(chunk.FinishReason),
	}
	if text != "" {
		cand.Content.Parts = append(cand.Content.Parts, geminiPart{Text: text})
	}
	for _, tc := range chunk.ToolCalls {
		cand.Content.Parts = append(cand.Content.Parts, geminiPart{
			FunctionCall: &geminiFunctionCall{Name: tc.Name, Args: json.RawMessage(tc.Arguments)},
		})
	}

	snap := geminiResponse{Candidates: []geminiCandidate{cand}}
	if u := chunk.Usage; u != nil {
		snap.Usage = &geminiUsage{
			PromptTokenCount:     u.PromptTokens,
			CandidatesTokenCount: u.CompletionTokens,
			TotalTokenCount:      u.TotalTokens,
		}
	}
	return json.Marshal(snap)
}
