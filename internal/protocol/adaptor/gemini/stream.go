package gemini

import (
	"encoding/json"
	"errors"
	"fmt"

	"minigateway/internal/protocol/canonical"
)

// 这个文件实现「Gemini SSE → OpenAI chunk」的翻译。
//
// Gemini 流式的关键特性（和 Claude 完全不同）：
//   - 每条 SSE data 携带新生成的文本增量，不按累计快照差分或去重。
//   - 没有 message_start 之类的生命周期事件，也没有独立的 usage 事件
//     （usageMetadata 通常只出现在最后一个 chunk 里）。
//   - finishReason 直接出现在 chunk 的 candidate 上。
//
// 翻译策略：将每条响应的文本完整转为 OpenAI 增量。
//   - finishReason：出现时发结束帧。
//   - usage：出现时发 usage 尾帧。
//
// 单条 chunk 里 candidates 可能有多个（候选数 > 1），本项目只处理
// 第一个候选（和 OpenAI 单候选假设一致）。
type translator struct {
	nextToolIndex int
	id            string
	model         string
	finished      bool
	// roleSent 首帧是否已发过 role（OpenAI 约定首帧 delta.role="assistant"）
	roleSent bool
}

// newTranslator 创建流翻译器（每个会话一个）。
func newTranslator() *translator {
	return &translator{}
}

// ParseStreamLine 解析一行 Gemini SSE，产出 0..n 个 OpenAI chunk。
func (t *translator) ParseStreamLine(line []byte) ([]*canonical.StreamChunk, error) {
	data, ok := sseData(line)
	if !ok {
		return nil, nil
	}
	if data == "[DONE]" {
		return nil, nil
	}

	var chunk geminiResponse
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil, err
	}

	if chunk.Error != nil {
		return nil, errors.New("Gemini 上游流报告错误")
	}

	var chunks []*canonical.StreamChunk

	// 首帧补 role：OpenAI SDK 依赖首帧 delta.role="assistant" 开始渲染。
	// Gemini 没有「开始」事件，所以第一条带内容的 chunk 前
	// 先插一个纯 role 帧。
	if !t.roleSent && len(chunk.Candidates) > 0 && len(chunk.Candidates[0].Content.Parts) > 0 {
		t.roleSent = true
		chunks = append(chunks, &canonical.StreamChunk{
			ID:    t.id,
			Model: t.model,
			Role:  canonical.RoleAssistant,
		})
	}

	// 注意产出顺序：文本增量 → finish 帧 → usage 帧。
	// Gemini 最后一条 chunk 可能同时带 parts + finishReason + usageMetadata，
	// 如果先把 usage 帧发出去，客户端会在收到 finish_reason 之前
	// 先收到「流已结束」的 usage——OpenAI SDK 按 chunk 顺序处理，
	// usage 帧必须排在 finish 帧之后。
	if len(chunk.Candidates) == 0 {
		// 被安全拦截的流：candidates 为空，promptFeedback.blockReason 有值。
		// 发一个带 content_filter 的结束帧，让客户端知道流为什么停了。
		if chunk.PromptFeedback != nil && chunk.PromptFeedback.BlockReason != "" && !t.finished {
			t.finished = true
			chunks = append(chunks, &canonical.StreamChunk{
				ID:    t.id,
				Model: t.model,
				Content: []canonical.ContentPart{{
					Type: canonical.PartText,
					Text: "[被 Gemini 安全策略拦截: " + chunk.PromptFeedback.BlockReason + "]",
				}},
				FinishReason: canonical.FinishContentFilter,
			})
		}
		if chunk.Usage != nil {
			chunks = append(chunks, t.usageChunk(chunk.Usage))
		}
		return chunks, nil
	}

	cand := chunk.Candidates[0]

	for _, p := range cand.Content.Parts {
		if p.Text != "" {
			chunks = append(chunks, &canonical.StreamChunk{
				ID:      t.id,
				Model:   t.model,
				Content: []canonical.ContentPart{{Type: canonical.PartText, Text: p.Text}},
			})
		}
	}

	// 函数调用：Gemini 流式里 functionCall 通常完整出现在某条 chunk
	//（不是碎片流）。整条发出，客户端按完整工具调用处理。
	for _, p := range cand.Content.Parts {
		if p.FunctionCall != nil {
			id := p.FunctionCall.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", t.nextToolIndex)
			}
			chunks = append(chunks, &canonical.StreamChunk{
				ID:    t.id,
				Model: t.model,
				ToolCalls: []canonical.ToolCallDelta{{
					Index:     t.nextToolIndex,
					ID:        id,
					Name:      p.FunctionCall.Name,
					Arguments: string(p.FunctionCall.Args),
				}},
			})
			t.nextToolIndex++
		}
	}

	// finishReason：结束帧（必须排在 usage 帧之前）
	if cand.FinishReason != "" && !t.finished {
		t.finished = true
		reason := mapFinishReason(cand.FinishReason)
		if t.nextToolIndex > 0 && reason == canonical.FinishStop {
			reason = canonical.FinishToolCalls
		}
		chunks = append(chunks, &canonical.StreamChunk{
			ID:           t.id,
			Model:        t.model,
			FinishReason: reason,
		})
	}

	// Usage snapshots are cumulative; later events replace earlier observations.
	if chunk.Usage != nil {
		chunks = append(chunks, t.usageChunk(chunk.Usage))
	}

	return chunks, nil
}

// usageChunk 产出带用量的尾帧。
func (t *translator) usageChunk(u *geminiUsage) *canonical.StreamChunk {
	return &canonical.StreamChunk{
		ID:    t.id,
		Model: t.model,
		Usage: u.toUsage(),
	}
}

// SetID 设置 message id（relay 在流开始时调用，Gemini 响应不带 id）。
func (t *translator) SetID(id, model string) {
	t.id = id
	t.model = model
}
