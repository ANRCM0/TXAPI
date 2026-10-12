// 这是一个假的「Anthropic Claude 上游」，用来在没有真实 API Key 的情况下
// 验证网关的协议互转链路：OpenAI 客户端协议 → 规范格式 → Claude 协议。
//
// 它模拟 Claude 的真实行为：
//   - POST /v1/messages（鉴权头是 x-api-key + anthropic-version，不是 Bearer）
//   - 非流式返回 content 块数组 + input_tokens/output_tokens 用量
//   - 流式返回 SSE 事件流：
//     message_start → content_block_start → content_block_delta(多个)
//     → content_block_stop → message_delta → message_stop
//   - 会打印收到的请求体，用来验证网关发出的请求转换是否正确
//     （system 是顶层字段、messages 里只有 user/assistant、content 是块数组）
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := "127.0.0.1:9912"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	http.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		// 鉴权校验：Claude 用 x-api-key，不是 Authorization: Bearer
		apiKey := r.Header.Get("x-api-key")
		version := r.Header.Get("anthropic-version")
		if apiKey == "" {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "authentication_error", "message": "missing x-api-key"},
			})
			return
		}
		if version == "" {
			// Claude 强制要求版本头，缺了直接 400——用来验证网关有没有补这个头
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "invalid_request_error", "message": "missing anthropic-version header"},
			})
			return
		}

		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model     string `json:"model"`
			System    string `json:"system"`
			MaxTokens int    `json:"max_tokens"`
			Stream    bool   `json:"stream"`
			Messages  []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "invalid_request_error", "message": "bad json: " + err.Error()},
			})
			return
		}

		// 打印收到的关键字段——这是验证「请求转换正确」的核心证据：
		// 1. system 应该在顶层（不在 messages 里）
		// 2. messages 的 role 应该只有 user/assistant
		// 3. content 应该是块数组
		roleSeq := ""
		for _, m := range req.Messages {
			roleSeq += m.Role + " "
		}
		log.Printf("[fake-claude] 收到请求: model=%s stream=%v system=%q max_tokens=%d 角色序列=[%s] 消息数=%d",
			req.Model, req.Stream, req.System, req.MaxTokens, roleSeq, len(req.Messages))

		reply := fmt.Sprintf("【假Claude上游】模型=%s，system=%q。这是非流式回复。", req.Model, req.System)

		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			flusher := w.(http.Flusher)

			// 按 Claude 真实事件顺序发：message_start（带 input_tokens）
			sendEvent(w, flusher, "message_start", map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id":            "msg_fake_001",
					"type":          "message",
					"role":          "assistant",
					"model":         req.Model,
					"content":       []any{},
					"stop_reason":   nil,
					"stop_sequence": nil,
					"usage":         map[string]any{"input_tokens": 12, "output_tokens": 1},
				},
			})
			time.Sleep(30 * time.Millisecond)

			// content_block_start（text 块，初始为空）
			sendEvent(w, flusher, "content_block_start", map[string]any{
				"type":          "content_block_start",
				"index":         0,
				"content_block": map[string]any{"type": "text", "text": ""},
			})
			time.Sleep(30 * time.Millisecond)

			// content_block_delta × N：文本增量
			chunks := []string{"你好", "，这是", "假Claude", "的流式", "回复。"}
			for _, ch := range chunks {
				sendEvent(w, flusher, "content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": ch},
				})
				time.Sleep(50 * time.Millisecond)
			}

			sendEvent(w, flusher, "content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			})
			time.Sleep(20 * time.Millisecond)

			// message_delta：结束原因 + output_tokens
			sendEvent(w, flusher, "message_delta", map[string]any{
				"type":  "message_delta",
				"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
				"usage": map[string]any{"output_tokens": 9},
			})
			time.Sleep(20 * time.Millisecond)

			sendEvent(w, flusher, "message_stop", map[string]any{"type": "message_stop"})
			return
		}

		// 非流式：Claude 的真实响应形状
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":            "msg_fake_001",
			"type":          "message",
			"role":          "assistant",
			"model":         req.Model,
			"content":       []map[string]any{{"type": "text", "text": reply}},
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 12, "output_tokens": 7},
		})
	})

	log.Printf("假 Claude 上游已启动: http://%s (POST /v1/messages)", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// sendEvent 发一条 Claude SSE 事件（event: 行 + data: 行 + 空行）。
func sendEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}
