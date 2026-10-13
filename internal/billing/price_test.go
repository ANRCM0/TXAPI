package billing

import (
	"math"
	"testing"

	"minigateway/internal/model"
	relaymodel "minigateway/internal/relay/model"
)

// newTestTable 构造一个直接给定价格和倍率的价格表（不经存储层），
// 让计费公式的测试不依赖数据库。
func newTestTable(prices []model.ModelPrice, rates map[string]float64) *PriceTable {
	t := NewPriceTable(nil, 0)
	for _, p := range prices {
		if containsStar(p.Model) {
			t.wild = append(t.wild, p)
			continue
		}
		t.exact[p.Model] = p
	}
	if rates != nil {
		t.rates = rates
	}
	return t
}

func containsStar(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '*' {
			return true
		}
	}
	return false
}

// TestCalculateCost 验证计费公式（含倍率）。
//
// 基准用例与 SPEC 4.1 一致：gpt-4o-mini 输入 $0.15/1k、输出 $0.6/1k，
// usage 为 11 输入 / 7 输出：
//
//	cost = 11/1000×0.15 + 7/1000×0.6 = 0.00165 + 0.0042 = 0.00585
func TestCalculateCost(t *testing.T) {
	table := newTestTable([]model.ModelPrice{
		{Model: "gpt-4o-mini", InputPricePer1K: 0.15, OutputPricePer1K: 0.6},
	}, nil)

	usage := &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}

	// 无倍率（未配置分组 → 1.0）
	cost, ok := table.Cost(usage, "gpt-4o-mini", "default")
	if !ok {
		t.Fatal("有价格却返回未命中")
	}
	if math.Abs(cost-0.00585) > 1e-12 {
		t.Fatalf("基础计费错误: 得到 %v, 期望 0.00585", cost)
	}

	// 倍率 2.0：整个公式（输入项+输出项）同比例放大
	table2 := newTestTable([]model.ModelPrice{
		{Model: "gpt-4o-mini", InputPricePer1K: 0.15, OutputPricePer1K: 0.6},
	}, map[string]float64{"relay": 2.0})
	cost2, ok := table2.Cost(usage, "gpt-4o-mini", "relay")
	if !ok {
		t.Fatal("有价格却返回未命中")
	}
	if math.Abs(cost2-0.0117) > 1e-12 {
		t.Fatalf("倍率计费错误: 得到 %v, 期望 0.0117", cost2)
	}

	// 未知分组按 1.0 计（倍率的意义是折算成本差异，没配=按标准价）
	cost3, _ := table2.Cost(usage, "gpt-4o-mini", "unknown-group")
	if math.Abs(cost3-0.00585) > 1e-12 {
		t.Fatalf("未知分组应按 1.0 计: 得到 %v", cost3)
	}

	// 零用量 → 零费用
	cost4, _ := table.Cost(&relaymodel.Usage{}, "gpt-4o-mini", "default")
	if cost4 != 0 {
		t.Fatalf("零用量应计 0: 得到 %v", cost4)
	}

	// 价格缺失 → (0, false)，调用方据此打 price_missing 标记
	cost5, ok5 := table.Cost(usage, "no-such-model", "default")
	if ok5 || cost5 != 0 {
		t.Fatalf("价格缺失应返回 (0, false): 得到 (%v, %v)", cost5, ok5)
	}

	// nil usage → (0, false)
	cost6, ok6 := table.Cost(nil, "gpt-4o-mini", "default")
	if ok6 || cost6 != 0 {
		t.Fatalf("nil usage 应返回 (0, false): 得到 (%v, %v)", cost6, ok6)
	}
}

// TestPriceWildcard 验证通配符匹配。
//
// SPEC 2.2 的要求：gpt-4* 匹配 gpt-4o、gpt-4-turbo。
func TestPriceWildcard(t *testing.T) {
	table := newTestTable([]model.ModelPrice{
		{Model: "gpt-4*", InputPricePer1K: 1, OutputPricePer1K: 2},
	}, nil)

	for _, name := range []string{"gpt-4o", "gpt-4-turbo", "gpt-4", "gpt-4o-mini"} {
		if _, ok := table.Price(name); !ok {
			t.Errorf("gpt-4* 应匹配 %s", name)
		}
	}
	// 注意 "gpt-4" 本身也在匹配列表里（* 可以匹配空串），
	// 所以这里只列真正不该命中的
	for _, name := range []string{"gpt-3.5-turbo", "claude-3", ""} {
		if _, ok := table.Price(name); ok {
			t.Errorf("gpt-4* 不应匹配 %q", name)
		}
	}

	// 精确名优先于通配
	exactFirst := newTestTable([]model.ModelPrice{
		{Model: "gpt-4*", InputPricePer1K: 1, OutputPricePer1K: 2},
		{Model: "gpt-4o", InputPricePer1K: 9, OutputPricePer1K: 9},
	}, nil)
	p, _ := exactFirst.Price("gpt-4o")
	if p.InputPricePer1K != 9 {
		t.Fatalf("精确匹配应优先: 得到 %v", p.InputPricePer1K)
	}

	// 更具体的通配优先：gpt-4o* 比 gpt-4* 字面量长
	specific := newTestTable([]model.ModelPrice{
		{Model: "gpt-4*", InputPricePer1K: 1, OutputPricePer1K: 2},
		{Model: "gpt-4o*", InputPricePer1K: 9, OutputPricePer1K: 9},
	}, nil)
	p2, _ := specific.Price("gpt-4o-mini")
	if p2.InputPricePer1K != 9 {
		t.Fatalf("更具体的通配应胜出: 得到 %v", p2.InputPricePer1K)
	}

	// 前缀/后缀/中间通配
	mid := newTestTable([]model.ModelPrice{
		{Model: "*4o*", InputPricePer1K: 5, OutputPricePer1K: 5},
	}, nil)
	if _, ok := mid.Price("gpt-4o-mini"); !ok {
		t.Error("*4o* 应匹配 gpt-4o-mini")
	}
	if _, ok := mid.Price("gpt-3.5"); ok {
		t.Error("*4o* 不应匹配 gpt-3.5")
	}
}

// TestEstimateUpperBound 验证预扣上限的估算（只按输出价，不含倍率）。
func TestEstimateUpperBound(t *testing.T) {
	table := newTestTable([]model.ModelPrice{
		{Model: "gpt-4o-mini", InputPricePer1K: 0.15, OutputPricePer1K: 0.6},
	}, nil)

	// 500 max_tokens × 0.6/1k = 0.3
	if got := table.EstimateUpperBound(500, "gpt-4o-mini"); math.Abs(got-0.3) > 1e-12 {
		t.Fatalf("上限估算错误: 得到 %v, 期望 0.3", got)
	}
	// 没给 max_tokens 或价格缺失 → 0（不预扣）
	if got := table.EstimateUpperBound(0, "gpt-4o-mini"); got != 0 {
		t.Fatalf("无 max_tokens 应返回 0: 得到 %v", got)
	}
	if got := table.EstimateUpperBound(500, "no-price"); got != 0 {
		t.Fatalf("价格缺失应返回 0: 得到 %v", got)
	}
}
