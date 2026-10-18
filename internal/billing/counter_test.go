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
