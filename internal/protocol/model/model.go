// Package model 存放网关内部流转的数据结构。
//
// 这里刻意只定义「网关自己要用」的字段：路由需要的 model/stream，
// 计费需要的 usage，错误处理需要的错误体。
// 请求正文本身不做完整建模，而是以 json.RawMessage 原样透传（见 relay 包），
// 这样上游新增的任何字段都不会被网关吃掉。
package model

import execv1 "minigateway/internal/foundation/contracts/execution/v1"

// ModelRequest is retained only by the legacy Relay compatibility boundary.
// New coordination uses protocol.Request and operation-specific payloads.
// MaxTokens is a legacy Chat estimate, never a strict cost-bound proof.
type ModelRequest struct {
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	MaxTokens int    `json:"max_tokens"`
}

// Usage 是上游返回的 token 用量，是计费的唯一依据。
type Usage = execv1.Usage

// ErrorDetail 是 OpenAI 风格的错误体，也是网关对外统一的错误格式。
type ErrorDetail = execv1.ErrorDetail

// ErrorWithStatusCode 把 HTTP 状态码和错误体绑在一起。
//
// 为什么要绑在一起：重试逻辑要先看状态码决定「要不要换渠道重试」，
// 再看错误消息决定「给客户端回什么」。两者分开传会到处漏判。
type ErrorWithStatusCode = execv1.Error
