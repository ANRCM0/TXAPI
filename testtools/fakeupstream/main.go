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
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
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
			for _, ch := range chunks {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", ch)
				flusher.Flush()
				time.Sleep(50 * time.Millisecond)
			}
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

	log.Printf("假上游已启动: http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
