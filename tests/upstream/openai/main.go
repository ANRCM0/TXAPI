// 这是一个假的「上游 LLM 服务」，用来在没有真实 API Key 的情况下
// 验证网关的完整转发链路：鉴权、路由、转换、流式、用量解析、重试。
//
// 它模拟 OpenAI 的行为：
//   - /v1/chat/completions 非流式返回 JSON
//   - stream=true 时返回 SSE
//   - 可以通过路径前缀模拟故障（429/500），用来测重试
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	addr := "127.0.0.1:9911"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	// 记录收到的模型名，用来验证 model_mapping 是否生效
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "missing api key", "type": "auth_error"},
			})
			return
		}

		var req struct {
			Model        string `json:"model"`
			Stream       bool   `json:"stream"`
			ChunkDelayMS int    `json:"smoke_chunk_delay_ms"`
		}
		body := json.NewDecoder(r.Body)
		body.Decode(&req)

		log.Printf("[fake-upstream] 收到请求: model=%s stream=%v auth=%s...", req.Model, req.Stream, auth[:14])

		// 模拟故障：如果 API Key 里带 "bad" 就返回 429。
		// 按 key 而不是模型名判断，才能构造出「前几个渠道坏、最后一个好」的场景，
		// 真正验证重试换渠道能救回来。
		if strings.Contains(auth, "bad") {
			w.WriteHeader(429)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "rate limit exceeded", "type": "rate_limit_error"},
			})
			return
		}

		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			flusher := w.(http.Flusher)
			chunks := []string{"你好", "，这是", "流式", "响应。"}
			delay := 50 * time.Millisecond
			if req.ChunkDelayMS > 0 && req.ChunkDelayMS <= 1000 {
				delay = time.Duration(req.ChunkDelayMS) * time.Millisecond
			}
			for _, ch := range chunks {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", ch)
				flusher.Flush()
				time.Sleep(delay)
			}
			// 结束帧：真实 OpenAI 会发 finish_reason，然后才是 usage 帧。
			// 少了这一帧，客户端拿不到「为什么停」，SDK 的 finish_reason 会是 None。
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			flusher.Flush()
			// 最后一个 chunk 带 usage
			fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18}}\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-fake-123",
			"object":  "chat.completion",
			"model":   req.Model,
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "这是假上游返回的内容，模型名是 " + req.Model}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
		})
	})

	// embeddings 端点（P6 批次 3 加的）。
	//
	// 为什么要单独加：真实 OpenAI 的 /v1/embeddings 和
	// /v1/chat/completions 是两个独立端点，返回结构完全不同
	// （data[].embedding 而不是 choices[].message）。假上游只模拟
	// 对话端点的话，网关的 Mode 透传根本验证不到——请求会被打到
	// /v1/chat/completions 上，拿到一个对话响应冒充向量。
	//
	// usage 刻意只给 {prompt_tokens, total_tokens}：这正是真实
	// embeddings 响应的形状，没有 completion_tokens。网关的计费
	// 要能正确处理这种「只有输入」的用量。
	http.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "missing api key", "type": "auth_error"},
			})
			return
		}

		var req struct {
			Model string `json:"model"`
			Input any    `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		// 按 input 的长度决定 prompt_tokens，让不同长度的输入
		// 在日志里可区分（恒为同一个数的话看不出网关真的读了 input）
		n := 1
		switch v := req.Input.(type) {
		case string:
			n = len([]rune(v))
		case []any:
			n = len(v)
		}
		if n < 1 {
			n = 1
		}

		log.Printf("[fake-upstream] 收到 embeddings 请求: model=%s 输入单元数=%d", req.Model, n)

		if strings.Contains(auth, "bad") {
			w.WriteHeader(429)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "rate limit exceeded", "type": "rate_limit_error"},
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"model":  req.Model,
			"data": []map[string]any{{
				"object":    "embedding",
				"index":     0,
				"embedding": []float64{0.0123, -0.0456, 0.0789, 0.0234},
			}},
			// 注意：没有 completion_tokens——embeddings 只有输入 token
			"usage": map[string]any{"prompt_tokens": n, "total_tokens": n},
		})
	})

	log.Printf("假上游已启动: http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
