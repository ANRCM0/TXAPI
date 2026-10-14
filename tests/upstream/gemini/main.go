// 这是一个假的「Gemini 上游」，用来验证网关的协议互转链路：
// OpenAI 客户端协议 → 规范格式 → Gemini 协议。
//
// 它模拟 Gemini 的真实行为：
//   - POST /v1beta/models/{model}:generateContent?key=xxx（key 在 URL 上）
//   - 流式是 :streamGenerateContent?alt=sse
//   - 非流式返回 candidates[{content, finishReason}] + usageMetadata
//   - 流式返回 SSE 累计快照：每条 data 包含截至当前的完整文本
//   - 会打印收到的请求体，验证转换是否正确：
//     systemInstruction 是顶层字段、role 只有 user/model、
//     contents[].parts 是分片数组
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	addr := "127.0.0.1:9913"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	// Gemini 的模型名在 URL path 里，用前缀匹配
	http.HandleFunc("/v1beta/models/", func(w http.ResponseWriter, r *http.Request) {
		// 鉴权：key 在 URL query 上，不是鉴权头
		key := r.URL.Query().Get("key")
		if key == "" {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": 401, "message": "missing key in URL query", "status": "UNAUTHENTICATED"},
			})
			return
		}

		// 从 path 解析模型名和动作：/v1beta/models/{model}:generateContent
		path := strings.TrimPrefix(r.URL.Path, "/v1beta/models/")
		modelName := path
		stream := false
		if i := strings.Index(path, ":"); i >= 0 {
			modelName = path[:i]
			action := path[i+1:]
			if action == "streamGenerateContent" {
				stream = true
			}
		}

		body, _ := io.ReadAll(r.Body)
		var req struct {
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
			SystemInstruction *struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
			GenerationConfig *struct {
				MaxOutputTokens int `json:"maxOutputTokens"`
			} `json:"generationConfig"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": 400, "message": "bad json: " + err.Error(), "status": "INVALID_ARGUMENT"},
			})
			return
		}

		// 打印关键字段——验证请求转换的核心证据：
		// 1. systemInstruction 应该在顶层
		// 2. role 应该只有 user/model
		// 3. parts 应该是分片数组
		roleSeq := ""
		for _, c := range req.Contents {
			roleSeq += c.Role + " "
		}
		sysText := ""
		if req.SystemInstruction != nil {
			for _, p := range req.SystemInstruction.Parts {
				sysText += p.Text
			}
		}
		maxTok := 0
		if req.GenerationConfig != nil {
			maxTok = req.GenerationConfig.MaxOutputTokens
		}
		log.Printf("[fake-gemini] 收到请求: model=%s(URL路径) stream=%v key=%s... systemInstruction=%q maxOutputTokens=%d 角色序列=[%s] contents数=%d",
			modelName, stream, key[:min(10, len(key))], sysText, maxTok, roleSeq, len(req.Contents))

		reply := fmt.Sprintf("【假Gemini上游】模型=%s，system=%q。这是非流式回复。", modelName, sysText)

		if stream {
			// Gemini 流式：alt=sse，每条 data 是累计快照，不是文本增量
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			flusher := w.(http.Flusher)

			chunks := []string{"你好", "世界", "，", "stream", "!"}
			full := ""
			for i, ch := range chunks {
				full += ch
				snap := map[string]any{
					"candidates": []map[string]any{{
						"content": map[string]any{
							"role":  "model",
							"parts": []map[string]any{{"text": full}},
						},
						// 最后一条快照带 finishReason
					}},
				}
				if i == len(chunks)-1 {
					snap["candidates"].([]map[string]any)[0]["finishReason"] = "STOP"
					// usageMetadata 通常只在最后一条出现
					snap["usageMetadata"] = map[string]any{
						"promptTokenCount":     12,
						"candidatesTokenCount": 9,
						"totalTokenCount":      21,
					}
				}
				data, _ := json.Marshal(snap)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				time.Sleep(50 * time.Millisecond)
			}
			return
		}

		// 非流式：Gemini 真实响应形状
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"role":  "model",
					"parts": []map[string]any{{"text": reply}},
				},
				"finishReason": "STOP",
			}},
			"usageMetadata": map[string]any{
				"promptTokenCount":     12,
				"candidatesTokenCount": 7,
				"totalTokenCount":      19,
			},
		})
	})

	log.Printf("假 Gemini 上游已启动: http://%s (POST /v1beta/models/{model}:generateContent?key=...)", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
