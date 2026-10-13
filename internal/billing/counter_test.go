package billing

import (
	"testing"
)

// TestEstimateTokens 验证字符数估算：英文约 4 字符/token，中文约 1 字符/token。
func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"hello world", 3}, // 11 ASCII 字符 × 0.25 = 2.75 → 向上取整 3
		{"你好世界", 6},        // 4 CJK × 1.5 = 6
		{"你好，世界！", 9},      // 6 个 CJK 字符（含全角标点）× 1.5 = 9
	}
	for _, c := range cases {
		if got := EstimateTokens(c.text); got != c.want {
			t.Errorf("EstimateTokens(%q) = %d, 期望 %d", c.text, got, c.want)
		}
	}
}

// TestEstimateUsage 验证完整用量估算（prompt + completion）。
func TestEstimateUsage(t *testing.T) {
	u := EstimateUsage("hello world", "你好")
	if u.PromptTokens != 3 || u.CompletionTokens != 3 || u.TotalTokens != 6 {
		t.Fatalf("估算用量错误: %+v", u)
	}
}

// TestPromptTextFromBody 验证从请求体抽取对话文本：
// JSON 语法符号不该被计入 prompt token。
func TestPromptTextFromBody(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o-mini",
		"messages": [
			{"role": "user", "content": "hello world"},
			{"role": "assistant", "content": "你好"},
			{"role": "user", "content": [{"type": "text", "text": "多模态文本"}]}
		],
		"temperature": 0.7
	}`)
	got := PromptTextFromBody(body)
	// 三段文本 + 换行：hello world / 你好 / 多模态文本
	if got != "hello world\n你好\n多模态文本\n" {
		t.Fatalf("抽取文本错误: %q", got)
	}
	// 只含文本，不含字段名/引号/转义——按整个 body 估会把 JSON 语法
	// 当成几百个假 token
	if got := PromptTextFromBody([]byte(`not json`)); got != "" {
		t.Fatalf("非法 JSON 应返回空串: %q", got)
	}
}

// TestCompletionTextFromResponse 验证从响应抽取回答文本（非流式 + 流式）。
func TestCompletionTextFromResponse(t *testing.T) {
	// 非流式 JSON
	jsonResp := []byte(`{"choices":[{"message":{"content":"这是回答"}}],"usage":{"prompt_tokens":11}}`)
	if got := CompletionTextFromResponse(jsonResp); got != "这是回答" {
		t.Fatalf("非流式抽取错误: %q", got)
	}

	// 流式 SSE：逐行拼接 delta.content
	sseResp := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"，世界\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"！\"}}]}\n\n" +
		"data: [DONE]\n\n")
	if got := CompletionTextFromResponse(sseResp); got != "你好，世界！" {
		t.Fatalf("流式抽取错误: %q", got)
	}

	// 空响应
	if got := CompletionTextFromResponse(nil); got != "" {
		t.Fatalf("空响应应返回空串: %q", got)
	}
}
