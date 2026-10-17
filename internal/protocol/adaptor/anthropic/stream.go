package anthropic

import (
	"encoding/json"
	"fmt"

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
	id            string
	model         string
	blocks        map[int]*streamBlock
	toolIDs       map[string]bool
	toolIDBytes   int
	nextToolIndex int
	// usage 跨事件累积
	usage *canonicalUsage
	// finished 标记已经发过结束帧，避免重复
	finished bool
}

const (
	maxStreamBlocks = 1024
	maxToolIDBytes  = 64 * 1024
)

type streamBlock struct {
	kind         string
	toolIndex    int
	initialInput bool
	hasArguments bool
	closed       bool
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
		blocks:  map[int]*streamBlock{},
		toolIDs: map[string]bool{},
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
	case "content_block_stop":
		return t.onContentBlockStop(ev)
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

// Start content is independent of later deltas and must not be discarded.
func (t *translator) onContentBlockStart(ev claudeStreamEvent) ([]*canonical.StreamChunk, error) {
	var blk struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Text  string          `json:"text"`
		Input json.RawMessage `json:"input"`
	}
	if t.finished || ev.Index < 0 || len(t.blocks) >= maxStreamBlocks {
		return nil, fmt.Errorf("invalid or excessive content block start")
	}
	if _, exists := t.blocks[ev.Index]; exists {
		return nil, fmt.Errorf("duplicate content block index %d", ev.Index)
	}
	if err := json.Unmarshal(ev.ContentBlock, &blk); err != nil {
		return nil, err
	}
	state := &streamBlock{kind: blk.Type}
	switch blk.Type {
	case "tool_use":
		if blk.ID == "" || blk.Name == "" || t.toolIDs[blk.ID] {
			return nil, fmt.Errorf("tool block has missing or duplicate identity")
		}
		if len(blk.ID) > maxToolIDBytes-t.toolIDBytes {
			return nil, fmt.Errorf("tool identity bytes exceed the stream limit")
		}
		arguments := ""
		if len(blk.Input) > 0 {
			var object map[string]json.RawMessage
			if json.Unmarshal(blk.Input, &object) != nil || object == nil {
				return nil, fmt.Errorf("initial tool input must be a JSON object")
			}
			if len(object) > 0 {
				arguments = string(blk.Input)
				state.initialInput, state.hasArguments = true, true
			}
		}
		state.toolIndex = t.nextToolIndex
		t.nextToolIndex++
		t.toolIDs[blk.ID] = true
		t.toolIDBytes += len(blk.ID)
		t.blocks[ev.Index] = state
		return []*canonical.StreamChunk{{ID: t.id, Model: t.model, ToolCalls: []canonical.ToolCallDelta{{Index: state.toolIndex, ID: blk.ID, Name: blk.Name, Arguments: arguments}}}}, nil
	case "text", "thinking":
		t.blocks[ev.Index] = state
		if blk.Type == "text" && blk.Text != "" {
			return []*canonical.StreamChunk{{ID: t.id, Model: t.model, Content: []canonical.ContentPart{{Type: canonical.PartText, Text: blk.Text}}}}, nil
		}
	default:
		return nil, fmt.Errorf("unsupported content block type %q", blk.Type)
	}
	return nil, nil
}

// onContentBlockDelta 处理 content_block_delta：文本或工具参数碎片。
func (t *translator) onContentBlockDelta(ev claudeStreamEvent) ([]*canonical.StreamChunk, error) {
	state, ok := t.blocks[ev.Index]
	if !ok || state.closed || t.finished {
		return nil, fmt.Errorf("delta without an open content block at index %d", ev.Index)
	}
	var delta claudeContentDelta
	if err := json.Unmarshal(ev.Delta, &delta); err != nil {
		return nil, err
	}
	switch delta.Type {
	case "text_delta":
		if state.kind != "text" {
			return nil, fmt.Errorf("text delta does not match its content block")
		}
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
		if state.kind != "tool_use" || state.initialInput {
			return nil, fmt.Errorf("tool delta cannot follow a non-tool block or complete initial input")
		}
		state.hasArguments = state.hasArguments || delta.PartialJSON != ""
		chunk := &canonical.StreamChunk{
			ID:    t.id,
			Model: t.model,
			ToolCalls: []canonical.ToolCallDelta{{
				Index:     state.toolIndex,
				Arguments: delta.PartialJSON,
			}},
		}
		return []*canonical.StreamChunk{chunk}, nil
	case "thinking_delta":
		if state.kind != "thinking" {
			return nil, fmt.Errorf("thinking delta does not match its content block")
		}
		// 思考增量：映射成 PartThinking，出站时由客户端适配器决定
		// 是显示还是丢弃（OpenAI 协议没有标准思考字段）
		chunk := &canonical.StreamChunk{
			ID:      t.id,
			Model:   t.model,
			Content: []canonical.ContentPart{{Type: canonical.PartThinking, Text: delta.Text}},
		}
		return []*canonical.StreamChunk{chunk}, nil
	case "signature_delta":
		if state.kind != "thinking" {
			return nil, fmt.Errorf("signature delta does not match its content block")
		}
		// 思考块的签名：只有回传思考历史时才需要，流式输出里
		// 客户端用不到，跳过（不丢内容，只丢签名）
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported content block delta %q", delta.Type)
	}
}

func (t *translator) onContentBlockStop(ev claudeStreamEvent) ([]*canonical.StreamChunk, error) {
	state, ok := t.blocks[ev.Index]
	if !ok || state.closed || t.finished {
		return nil, fmt.Errorf("stop without an open content block at index %d", ev.Index)
	}
	state.closed = true
	if state.kind == "tool_use" && !state.hasArguments {
		return []*canonical.StreamChunk{{ID: t.id, Model: t.model, ToolCalls: []canonical.ToolCallDelta{{Index: state.toolIndex, Arguments: "{}"}}}}, nil
	}
	return nil, nil
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
		for index, block := range t.blocks {
			if !block.closed {
				return nil, fmt.Errorf("message ended before content block %d stopped", index)
			}
		}
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
