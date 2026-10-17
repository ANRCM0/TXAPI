// Package canonical 定义网关内部的「规范格式」——多协议互转的枢纽。
//
// 背景（PLAN 3.2）：N 种客户端协议 × M 种上游协议如果两两直转，是 N×M 条路径，
// 每加一种协议路径数平方增长。让所有协议先转成规范格式、再从规范格式转出去，
// 就只需要 N+M 个转换器。
//
// 为什么基准选 OpenAI Chat Completions：它是事实标准，几乎所有客户端和 SDK
// 都按它实现，文档最全，遇到边界情况最好查。
//
// 规范格式刻意只保留「三家协议都能表达的共同子集」：
// 各家特有的能力（Claude 的 thinking 块、Gemini 的 systemInstruction 位置差异等）
// 不进这里，由各适配器在转换时显式映射、显式降级或明确报错。
// 绝不静默丢弃字段——静默丢弃会让用户遇到「参数在网关后面失效」
// 这种极难排查的问题。
package canonical

import (
	"encoding/json"
	"strings"

	"minigateway/internal/protocol/model"
)

// 内容块类型。取值对齐三家协议的最大公约数：
// OpenAI 的 message.content 分片、Claude 的 content block、Gemini 的 part
// 都能归到这几类。
const (
	PartText       = "text"        // 纯文本
	PartImage      = "image"       // 图片，用 URL 或 data URL 表达
	PartToolUse    = "tool_use"    // 助手发起的工具调用
	PartToolResult = "tool_result" // 工具执行结果
	PartThinking   = "thinking"    // 思考过程（Claude thinking / OpenAI reasoning_content）
)

// 角色常量。取 OpenAI 的词汇表作为规范词汇。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// 结束原因常量。同样取 OpenAI 词汇表，各适配器负责映射到自家取值。
const (
	FinishStop          = "stop"
	FinishLength        = "length"
	FinishToolCalls     = "tool_calls"
	FinishContentFilter = "content_filter"
)

// ChatRequest 是规范格式的对话请求。
type ChatRequest struct {
	Model       string
	Messages    []Message
	MaxTokens   int
	Temperature *float64 // 指针：0 是有效值，不能和「没传」混为一谈
	TopP        *float64
	Stop        []string
	Stream      bool
	Tools       []Tool

	// Extra 保留「规范格式未建模、但客户端确实传了」的字段（原始 JSON）。
	//
	// 出站适配器的处理策略：
	//   - 目标协议能表达的（如 Gemini 的 seed/candidateCount）→ 显式映射
	//   - 目标协议无法表达的（如 Claude 的 frequency_penalty）→ 明确报错
	//   - 其余原样透传给上游，由上游自己决定收不收
	// 三种处理的共同点：不静默丢弃。
	Extra map[string]json.RawMessage
}

// Message 是规范格式的消息。
//
// 注意 OpenAI 把工具调用拆成 assistant.tool_calls 和 role=tool 两条消息，
// Claude 把两者都放进 content 块数组。规范格式统一用 ContentPart 表达，
// 角色只保留 system/user/assistant/tool 四种。
type Message struct {
	Role string
	// Content 统一成块数组。纯文本消息在入站解析时也会包一个 PartText 块，
	// 免得每条转换路径都要特判「字符串还是数组」。
	Content []ContentPart
	// ToolUseID 仅 role=tool 时使用：对应哪次工具调用。
	// OpenAI 叫 tool_call_id，Claude 叫 tool_use_id，Gemini 干脆没有 id。
	ToolUseID string
	// Name 仅 role=tool 时使用：被调用的函数名。
	// OpenAI 的 tool 消息不带函数名（只有 tool_call_id），
	// 但 Gemini 的 functionResponse 强制要求 name——
	// 所以入站解析时要顺着 assistant 消息的 tool_calls 把名字补上，
	// 补不上就让出站适配器明确报错（见 anthropic/gemini 的 BuildRequest）。
	Name string
}

// ContentPart 是统一的内容块。
type ContentPart struct {
	Type string

	// PartText / PartThinking 用
	Text string

	// PartImage 用：http(s) URL 或 data URL（data:<mime>;base64,<data>）
	ImageURL string

	// PartToolUse 用：调用 ID、函数名、入参（JSON 文本）
	ToolUseID string
	Name      string
	Input     json.RawMessage

	// PartToolResult 用：执行结果文本 + 是否出错
	IsError bool
}

// Tool 是规范格式的工具（函数）声明。
type Tool struct {
	Name        string
	Description string
	// Parameters 是 JSON Schema 原文（OpenAI 叫 parameters，Claude 叫 input_schema，
	// Gemini 也叫 parameters，只有包装层级不同，内容可以直接搬）。
	Parameters json.RawMessage
}

// ChatResponse 是规范格式的非流式响应。
type ChatResponse struct {
	ID           string
	Model        string
	Content      []ContentPart
	FinishReason string // 取 Finish* 常量
	Usage        *model.Usage
}

// StreamChunk 是规范格式的流式增量。
//
// 一次增量可能同时带文本、思考、工具调用片段和结束原因，
// 所以字段都是「可选出现」，而不是 choices 数组那种重结构。
type StreamChunk struct {
	ID    string
	Model string

	// Role 仅首帧非空（OpenAI 的首个 delta 带 role）。
	Role string

	// Content 是本次增量的内容块（通常一个）。
	Content []ContentPart

	// ToolCalls 是工具调用的增量。流式里参数是碎片（partial_json），
	// 由客户端自行拼接，网关只负责搬运。
	ToolCalls []ToolCallDelta

	// FinishReason 仅在结束帧非空。
	FinishReason string

	// Usage 仅在带用量的帧非空（一般是最后一帧）。
	Usage *model.Usage
}

// ToolCallDelta 是流式工具调用增量。
//
// ID/Name 只在首帧出现，后续帧只带 Arguments 碎片——
// 这是 OpenAI 流式工具调用的约定，Claude/Gemini 转换时也要遵守。
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// TextOf 取出消息里的全部文本，用换行拼接。
// system 字段提取（Claude 顶层 system）和 systemInstruction 拼装（Gemini）都用它。
func TextOf(msg Message) string {
	var parts []string
	for _, p := range msg.Content {
		if (p.Type == PartText || p.Type == PartToolResult) && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// HasParts 判断消息里是否含有指定类型以外的「有效内容」。
// 用来识别「空消息」——空 content 的消息发给上游大概率被 400。
func HasParts(msg Message) bool { return len(msg.Content) > 0 }
