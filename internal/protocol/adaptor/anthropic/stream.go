package anthropic

import (
	"encoding/json"

	"minigateway/internal/protocol/canonical"
	"minigateway/internal/protocol/model"
)

// 这个文件实现「Claude SSE 事件流 → OpenAI chunk 流」的翻译。
//
// Claude 的事件流（一行一个 SSE 事件）：
//
//	event: message_start
//	data: {"type":"message_start","message":{...,"usage":{"input_tokens":10}}}
//
//	event: content_block_start
//	data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}
//
//	event: content_block_delta
//	data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你"}}
//
//	event: content_block_delta
//	data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}
//
//	event: message_delta
//	data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}
//
//	event: message_stop
//	data: {"type":"message_stop"}
//
// OpenAI 的 chunk 流：每个 chunk 带 id/model/choices，首帧带 role，
// 中间帧带 delta.content，结束帧带 finish_reason，尾帧带 usage。
//
// 翻译的核心状态（必须在多行之间保持，所以用 translator 结构体）：
//   - message id / model（message_start 时拿到，后面所有 chunk 都要用）
//   - 每个 content block 的类型（text 还是 tool_use，delta 的解析方式不同）
//   - 累积的 usage（input_tokens 在 message_start，output_tokens 在 message_delta）
type translator struct {
	id    string
	model string
	// blockTypes 记录每个 index 的内容块类型：0=text, 1=tool_use
	// 流式里工具调用的参数是 input_json_delta 碎片，要按 index 归位
	blockTypes map[int]string
	// toolCallIndex 记录每个 block index 对应的 tool_calls 数组下标
	toolCallIndex map[int]int
	nextToolIndex int
	// usage 跨事件累积
	usage *canonicalUsage
	// finished 标记已经发过结束帧，避免重复
	finished bool
}

// canonicalUsage 是流式期间的用量累积器。
// Claude 把 input/output tokens 拆在两个事件里，必须合并。
type canonicalUsage struct {
	InputTokens  int
	OutputTokens int
}

// newTranslator 创建一个流翻译器。每次转发会话一个，不能跨请求复用。
func newTranslator() *translator {
	return &translator{
		blockTypes:    map[int]string{},
		toolCallIndex: map[int]int{},
	}
}

// ParseStreamLine 解析一行 Claude SSE，产出 0..n 个 OpenAI chunk。
//
// 返回空切片表示这行不产生客户端可见增量（ping、空行等）。
// 一行可能对应多个 chunk（比如 message_delta 同时带结束原因和 usage）。
func (t *translator) ParseStreamLine(line []byte) ([]*canonical.StreamChunk, error) {
	data, ok := sseData(line)
	if !ok {
		return nil, nil
	}
	if data == "[DONE]" {
		return nil, nil
	}

	var ev claudeStreamEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return nil, err
	}

	switch ev.Type {
	case "message_start":
		return t.onMessageStart(ev)
	case "content_block_start":
		return t.onContentBlockStart(ev)
	case "content_block_delta":
		return t.onContentBlockDelta(ev)
	case "message_delta":
		return t.onMessageDelta(ev)
	case "message_stop":
		return t.onMessageStop(ev)
	case "ping":
		return nil, nil
	case "error":
		// 上游流中途报错：没有客户端可见增量，但必须让上层知道。
		// 返回错误会让 relay 记日志并结束这条流（见 relayStream）。
		if ev.Error != nil {
			return nil, &streamError{message: ev.Error.Message}
		}
		return nil, nil
	default:
		// 未知事件类型：跳过不报错。Claude 会加新事件类型，
		// 网关不应该因为上游升级就挂掉整条流。
		return nil, nil
	}
}

// onMessageStart 处理 message_start：拿到 id/model/input_tokens，
// 发出带 role 的首帧（OpenAI 约定首帧 delta.role="assistant"）。
func (t *translator) onMessageStart(ev claudeStreamEvent) ([]*canonical.StreamChunk, error) {
	if ev.Message != nil {
		t.id = ev.Message.ID
		t.model = ev.Message.Model
		if ev.Message.Usage.InputTokens > 0 {
			t.usage = &canonicalUsage{InputTokens: ev.Message.Usage.InputTokens}
		}
	}
	chunk := &canonical.StreamChunk{
		ID:    t.id,
		Model: t.model,
		Role:  canonical.RoleAssistant,
	}
	return []*canonical.StreamChunk{chunk}, nil
}

// onContentBlockStart 处理 content_block_start：记录块类型。
// text 块的首帧不带内容（Claude 的 text 初始为空），
// tool_use 块要记下 id/name，供后续 input_json_delta 归位。
func (t *translator) onContentBlockStart(ev claudeStreamEvent) ([]*canonical.StreamChunk, error) {
	var blk struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if len(ev.Delta) > 0 {
		// content_block_start 的 data 里 content_block 是对象，
		// 但事件结构里我们把它放在 Delta 字段（RawMessage）
		if err := json.Unmarshal(ev.Delta, &blk); err == nil {
			t.blockTypes[ev.Index] = blk.Type
			if blk.Type == "tool_use" {
				idx := t.nextToolIndex
				t.nextToolIndex++
				t.toolCallIndex[ev.Index] = idx
				// 工具调用首帧：带 id/name，参数为空
				chunk := &canonical.StreamChunk{
					ID:    t.id,
					Model: t.model,
					ToolCalls: []canonical.ToolCallDelta{{
						Index: idx,
						ID:    blk.ID,
						Name:  blk.Name,
					}},
				}
				return []*canonical.StreamChunk{chunk}, nil
			}
		}
	}
	// text 块开始：不产生增量（内容在后续 delta 里）
	return nil, nil
}

// onContentBlockDelta 处理 content_block_delta：文本或工具参数碎片。
func (t *translator) onContentBlockDelta(ev claudeStreamEvent) ([]*canonical.StreamChunk, error) {
	var delta claudeContentDelta
	if err := json.Unmarshal(ev.Delta, &delta); err != nil {
		return nil, err
	}
	switch delta.Type {
	case "text_delta":
		if delta.Text == "" {
			return nil, nil
		}
		chunk := &canonical.StreamChunk{
			ID:      t.id,
			Model:   t.model,
			Content: []canonical.ContentPart{{Type: canonical.PartText, Text: delta.Text}},
		}
		return []*canonical.StreamChunk{chunk}, nil
	case "input_json_delta":
		// 工具参数碎片：归到对应的 tool_calls 下标
		idx, ok := t.toolCallIndex[ev.Index]
		if !ok {
			// 没记录过这个块的起始事件（异常情况）：按新工具调用处理
			idx = t.nextToolIndex
			t.nextToolIndex++
			t.toolCallIndex[ev.Index] = idx
		}
		chunk := &canonical.StreamChunk{
			ID:    t.id,
			Model: t.model,
			ToolCalls: []canonical.ToolCallDelta{{
				Index:     idx,
				Arguments: delta.PartialJSON,
			}},
		}
		return []*canonical.StreamChunk{chunk}, nil
	case "thinking_delta":
		// 思考增量：映射成 PartThinking，出站时由客户端适配器决定
		// 是显示还是丢弃（OpenAI 协议没有标准思考字段）
		chunk := &canonical.StreamChunk{
			ID:      t.id,
			Model:   t.model,
			Content: []canonical.ContentPart{{Type: canonical.PartThinking, Text: delta.Text}},
		}
		return []*canonical.StreamChunk{chunk}, nil
	case "signature_delta":
		// 思考块的签名：只有回传思考历史时才需要，流式输出里
		// 客户端用不到，跳过（不丢内容，只丢签名）
		return nil, nil
	default:
		return nil, nil
	}
}

// onMessageDelta 处理 message_delta：结束原因 + output_tokens。
// 结束帧和 usage 可能在同一事件里，产出两个 chunk。
func (t *translator) onMessageDelta(ev claudeStreamEvent) ([]*canonical.StreamChunk, error) {
	var chunks []*canonical.StreamChunk

	// 结束帧
	var delta struct {
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(ev.Delta, &delta); err == nil && delta.StopReason != "" && !t.finished {
		t.finished = true
		chunks = append(chunks, &canonical.StreamChunk{
			ID:           t.id,
			Model:        t.model,
			FinishReason: mapStopReason(delta.StopReason),
		})
	}

	// usage 帧
	if ev.Usage != nil {
		if t.usage == nil {
			t.usage = &canonicalUsage{}
		}
		t.usage.OutputTokens = ev.Usage.OutputTokens
		chunks = append(chunks, t.usageChunk())
	}
	return chunks, nil
}

// onMessageStop 处理 message_stop：流正常结束。
// 结束帧已在 message_delta 发过；这里只补一个 usage 尾帧（如果还没发）。
func (t *translator) onMessageStop(ev claudeStreamEvent) ([]*canonical.StreamChunk, error) {
	// message_stop 不带 usage，usage 已在 message_delta 处理。
	// 什么都不发——OpenAI 的 [DONE] 由 relayStream 统一补。
	return nil, nil
}

// usageChunk 产出带用量的尾帧。
func (t *translator) usageChunk() *canonical.StreamChunk {
	u := &canonicalUsage{}
	if t.usage != nil {
		u = t.usage
	}
	return &canonical.StreamChunk{
		ID:    t.id,
		Model: t.model,
		Usage: &model.Usage{
			PromptTokens:     u.InputTokens,
			CompletionTokens: u.OutputTokens,
			TotalTokens:      u.InputTokens + u.OutputTokens,
		},
	}
}

// streamError 包装上游流中途的错误。
type streamError struct {
	message string
}

func (e *streamError) Error() string { return "上游流错误: " + e.message }
