package gemini

import (
	"encoding/json"

	"minigateway/internal/relay/canonical"
)

// 这个文件实现「Gemini SSE → OpenAI chunk」的翻译。
//
// Gemini 流式的关键特性（和 Claude 完全不同）：
//   - 每条 SSE data 是一条完整的 GenerateContentResponse 快照，
//     不是 delta 增量。同一条流里，第 N 条快照的文本包含前 N-1 条的全部内容。
//   - 没有 message_start 之类的生命周期事件，也没有独立的 usage 事件
//     （usageMetadata 通常只出现在最后一条快照里）。
//   - finishReason 直接出现在快照的 candidate 上。
//
// 翻译策略：把「快照」当增量用。
//   - 文本：用 lastTextLen 记录上一条快照的文本长度，本次只发新增部分。
//     这是整个翻译的核心——不做这个去重，客户端会看到重复文本。
//   - finishReason：出现时发结束帧。
//   - usage：出现时发 usage 尾帧。
//
// 单条快照里 candidates 可能有多个（候选数 > 1），本项目只处理
// 第一个候选（和 OpenAI 单候选假设一致）。
type translator struct {
	id    string
	model string
	// lastTextLen 是上一条快照的文本总长度，用于计算增量
	lastTextLen int
	finished    bool
	// usageSent 防止重复发 usage 帧
	usageSent bool
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

	var snap geminiResponse
	if err := json.Unmarshal([]byte(data), &snap); err != nil {
		return nil, err
	}

	var chunks []*canonical.StreamChunk

	// 首帧补 role：OpenAI SDK 依赖首帧 delta.role="assistant" 开始渲染。
	// Gemini 的快照没有「开始」事件，所以第一条带内容的快照前
	// 先插一个纯 role 帧。
	if !t.roleSent && len(snap.Candidates) > 0 && len(snap.Candidates[0].Content.Parts) > 0 {
		t.roleSent = true
		chunks = append(chunks, &canonical.StreamChunk{
			ID:    t.id,
			Model: t.model,
			Role:  canonical.RoleAssistant,
		})
	}

	// 注意产出顺序：文本增量 → finish 帧 → usage 帧。
	// Gemini 最后一条快照同时带 parts + finishReason + usageMetadata，
	// 如果先把 usage 帧发出去，客户端会在收到 finish_reason 之前
	// 先收到「流已结束」的 usage——OpenAI SDK 按 chunk 顺序处理，
	// usage 帧必须排在 finish 帧之后。
	if len(snap.Candidates) == 0 {
		// 被安全拦截的流：candidates 为空，promptFeedback.blockReason 有值。
		// 发一个带 content_filter 的结束帧，让客户端知道流为什么停了。
		if snap.PromptFeedback != nil && snap.PromptFeedback.BlockReason != "" && !t.finished {
			t.finished = true
			chunks = append(chunks, &canonical.StreamChunk{
				ID:    t.id,
				Model: t.model,
				Content: []canonical.ContentPart{{
					Type: canonical.PartText,
					Text: "[被 Gemini 安全策略拦截: " + snap.PromptFeedback.BlockReason + "]",
				}},
				FinishReason: canonical.FinishContentFilter,
			})
		}
		return chunks, nil
	}

	cand := snap.Candidates[0]

	// 文本增量：拼接所有 text part，算增量
	var text string
	for _, p := range cand.Content.Parts {
		text += p.Text
	}
	if len(text) > t.lastTextLen {
		delta := text[t.lastTextLen:]
		t.lastTextLen = len(text)
		chunks = append(chunks, &canonical.StreamChunk{
			ID:      t.id,
			Model:   t.model,
			Content: []canonical.ContentPart{{Type: canonical.PartText, Text: delta}},
		})
	}

	// 函数调用：Gemini 流式里 functionCall 通常是完整出现在某条快照
	//（不是碎片流）。整条发出，客户端按完整工具调用处理。
	for _, p := range cand.Content.Parts {
		if p.FunctionCall != nil {
			chunks = append(chunks, &canonical.StreamChunk{
				ID:    t.id,
				Model: t.model,
				ToolCalls: []canonical.ToolCallDelta{{
					Index:     0,
					ID:        p.FunctionCall.Name,
					Name:      p.FunctionCall.Name,
					Arguments: string(p.FunctionCall.Args),
				}},
			})
		}
	}

	// finishReason：结束帧（必须排在 usage 帧之前）
	if cand.FinishReason != "" && !t.finished {
		t.finished = true
		chunks = append(chunks, &canonical.StreamChunk{
			ID:           t.id,
			Model:        t.model,
			FinishReason: mapFinishReason(cand.FinishReason),
		})
	}

	// usage：通常只在最后一条快照里出现，最后发
	if snap.Usage != nil && !t.usageSent {
		t.usageSent = true
		chunks = append(chunks, t.usageChunk(snap.Usage))
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
