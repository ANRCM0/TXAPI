package adaptor

import (
	"minigateway/internal/relay/canonical"
)

// ProtocolAdaptor 是「协议层」的适配器接口，对应 PLAN 3.2 的协议互转设计。
//
// 与 Adaptor 的分工：
//   - Adaptor      面向「一次转发」：URL、鉴权头、请求体改写、响应回写。
//     它是 relay 主流程的直接依赖，可以包含 HTTP 层的处理。
//   - ProtocolAdaptor 面向「协议翻译」：本协议 ←→ 规范格式的双向转换，
//     不关心 HTTP 细节（body 进、body 出）。
//
// 为什么要拆两层：relay 需要 URL/鉴权/流式回写这些 HTTP 关注点，
// 而协议互转需要的是纯函数式的转换。把两者混在一个接口里，
// 每个厂商适配器都要实现一遍 HTTP 样板代码，而且没法单测转换逻辑。
// 现在 anthropic/gemini 适配器 = 一个 ProtocolAdaptor（纯转换）
// + 一个薄薄的 Adaptor 外壳（调 ProtocolAdaptor 干活）。
type ProtocolAdaptor interface {
	// Name 返回协议名，如 "openai" / "anthropic" / "gemini"。
	Name() string

	// ParseRequest 把本协议的请求体解析成规范格式。
	ParseRequest(body []byte) (*canonical.ChatRequest, error)

	// BuildRequest 把规范格式的请求构造成本协议的请求体。
	BuildRequest(req *canonical.ChatRequest) ([]byte, error)

	// ParseResponse 把本协议的非流式响应体解析成规范格式。
	ParseResponse(body []byte) (*canonical.ChatResponse, error)

	// BuildResponse 把规范格式的响应构造成本协议的响应体。
	BuildResponse(resp *canonical.ChatResponse) ([]byte, error)

	// ParseStreamLine 把上游发来的一行 SSE 数据解析成规范 chunk。
	//
	// 返回 nil 表示这一行不产生客户端可见的增量（比如 Claude 的
	// ping 事件、message_start 这种只有状态没有内容的行）。
	// 一行也可能对应多个 chunk（比如 Gemini 一条 data 里带
	// functionCall + finishReason），所以返回切片。
	ParseStreamLine(line []byte) ([]*canonical.StreamChunk, error)

	// BuildStreamLine 把规范 chunk 序列化成本协议的一行 SSE 数据。
	//
	// 返回 nil 表示这个 chunk 不需要发给客户端。
	// 是否附 "data: [DONE]" 之类的终止标记由各协议自己决定。
	BuildStreamLine(chunk *canonical.StreamChunk) ([]byte, error)

	// DefaultBaseURL 是该协议的官方默认地址，配置里没写 base_url 时兜底。
	DefaultBaseURL() string

	// AuthHeader 返回该协议的鉴权头名和值。
	// OpenAI: Authorization / Bearer ***
	// Anthropic: x-api-key / <key>（另需 anthropic-version 头，见适配器）
	// Gemini: 不用头，key 拼在 URL 上（返回空字符串表示「走 URL」）
	AuthHeader(apiKey string) (name, value string)
}

// SessionStreamer 是「带会话状态的流式解析」扩展接口。
//
// 为什么需要它：流式转换是有状态的。Claude 的 translator 要跨行记住
// message id（message_start 才有）、每个 content block 的类型、
// 累积的 usage（input/output tokens 拆在两个事件里）。
// ProtocolAdaptor.ParseStreamLine 是无状态的（每次调用独立），
// 满足不了这个需求。
//
// 设计取舍：不把 sessionID 直接塞进 ProtocolAdaptor.ParseStreamLine 签名——
// 那样 OpenAI 这种无状态协议也得为一个用不到的参数改签名。
// 用可选接口（Go 的类型断言模式）：需要状态的协议（Claude/Gemini）
// 实现它，无状态的（OpenAI）不用管。
//
// 实现方的义务：sessionID 相同时必须返回同一个 translator 实例，
// 且必须并发安全（不同会话的流可能同时进行）。
type SessionStreamer interface {
	// ParseStreamLineSession 在指定会话上下文里解析一行 SSE。
	ParseStreamLineSession(line []byte, sessionID string) ([]*canonical.StreamChunk, error)
}

// SessionReleaser 是「流结束后释放会话状态」的可选接口。
//
// 为什么需要：带状态的流翻译器（translator map）如果只创建不清理，
// 长跑网关会内存泄漏。流正常结束或客户端断开时，relay 调这个接口
// 让适配器把对应 sessionID 的 translator 从 map 里删掉。
type SessionReleaser interface {
	ReleaseStreamSession(sessionID string)
}
