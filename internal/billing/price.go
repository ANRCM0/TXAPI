// Package billing 是计费与额度控制的核心。
//
// 包内分工（按「纯逻辑」和「带 IO 的编排」分层）：
//   - price.go    价格表查询：通配符匹配 + 缓存 + 渠道分组倍率 + 计费公式
//   - counter.go  token 计数：上游没返回 usage 时的字符数估算降级
//   - quota.go    额度检查与原子扣减（令牌 / 用户两个目标）
//   - settle.go   请求前预扣 → 响应后结算的编排（Engine）
//
// 为什么独立成包：计费公式、通配符匹配是纯函数，必须能脱离数据库单测；
// 额度扣减依赖 SQLite 的原子 UPDATE，又需要和转发流程解耦。
// 这个边界让两类关注点各自可测。
package billing

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"minigateway/internal/model"
	relaymodel "minigateway/internal/protocol/model"
)

// PriceLoader 从存储层加载价格表和分组倍率。
//
// 用函数而不是接口注入：billing 不该知道 store 的存在（避免
// billing → store → billing 的潜在环），而「读两张表」这件事
// 用函数签名表达已经足够。
type PriceLoader func() (prices []model.ModelPrice, rates map[string]float64, err error)

// PriceTable 是价格表的内存缓存。
//
// 为什么缓存：转发路径上每个请求都要查价，直连 SQLite 会给
// 单写者的数据库平添读压力。价格表读多写极少（只有管理接口改），
// 缓存 + TTL 过期 + 写后失效（见 Engine.ReloadPrices）三重保障。
type PriceTable struct {
	mu sync.RWMutex
	// exact 是精确模型名 → 价格。
	exact map[string]model.ModelPrice
	// wild 是带 "*" 的通配条目，命中时按「字面量最长」优先。
	wild []model.ModelPrice
	// rates 是渠道分组 → 计费倍率。查不到的分组按 1.0 计。
	rates map[string]float64

	loader   PriceLoader
	ttl      time.Duration
	loadedAt time.Time
}

// DefaultPriceTTL 是价格表缓存的有效期。
//
// 写后失效（管理接口改价格时主动 ReloadPrices）已经能保证及时性，
// TTL 只是兜底——防止有人绕过 API 直接改库，或者 reload 调用漏掉。
const DefaultPriceTTL = 5 * time.Minute

// NewPriceTable 创建价格表缓存。loader 为 nil 时退化为空表（查什么都是「没有价格」）。
func NewPriceTable(loader PriceLoader, ttl time.Duration) *PriceTable {
	if ttl <= 0 {
		ttl = DefaultPriceTTL
	}
	return &PriceTable{
		exact:  map[string]model.ModelPrice{},
		wild:   nil,
		rates:  map[string]float64{},
		loader: loader,
		ttl:    ttl,
	}
}

// Price 查询模型的单价，第二个返回值表示是否找到价格。
//
// 匹配顺序：精确名 → 通配符（字面量最长的胜出）。
// 例：表里有 gpt-4* 和 gpt-4o*，查 gpt-4o-mini 命中后者（更具体）。
func (t *PriceTable) Price(modelName string) (model.ModelPrice, bool) {
	t.refreshIfStale()
	t.mu.RLock()
	defer t.mu.RUnlock()
	if p, ok := t.exact[modelName]; ok {
		return p, true
	}
	bestScore := -1
	var found model.ModelPrice
	for _, p := range t.wild {
		if !wildcardMatch(p.Model, modelName) {
			continue
		}
		if score := wildcardScore(p.Model); score > bestScore {
			bestScore = score
			found = p
		}
	}
	return found, bestScore >= 0
}

// Rate 返回渠道分组的计费倍率。未配置的分组按 1.0 计——
// 倍率的意义是「折算成本差异」，没配就等于「按标准价」。
func (t *PriceTable) Rate(group string) float64 {
	t.refreshIfStale()
	t.mu.RLock()
	defer t.mu.RUnlock()
	if r, ok := t.rates[group]; ok && r > 0 {
		return r
	}
	return 1.0
}

// Cost 按计费公式算一次请求的费用。
//
//	cost = (prompt_tokens / 1000) × 输入单价
//	     + (completion_tokens / 1000) × 输出单价
//	     × 分组倍率
//
// 倍率乘在整个括号外（不是只乘输出项）：SPEC 2.1 的语义是
// 「同一模型在不同渠道成本不同，用倍率统一折算成标准计价单位」，
// 输入和输出要同比例折算，否则折算后的单价不再自洽。
//
// 价格表缺失时返回 (0, false)：按 0 计费但标记出来。宁可漏计费，
// 也不要因为价格表缺一项就把请求拒了——服务可用性优先（SPEC 2.2）。
func (t *PriceTable) Cost(u *relaymodel.Usage, modelName, group string) (float64, bool) {
	if u == nil {
		return 0, false
	}
	p, ok := t.Price(modelName)
	if !ok {
		return 0, false
	}
	cost := float64(u.PromptTokens)/1000*p.InputPricePer1K +
		float64(u.CompletionTokens)/1000*p.OutputPricePer1K
	return cost * t.Rate(group), true
}

// EstimateUpperBound 估算一次请求的费用上限：max_tokens × 输出单价。
//
// 只用输出价，两个原因：
//  1. max_tokens 约束的就是输出长度，它是唯一「请求前就知道的上限」
//  2. 输入（prompt）大小在预扣时无法预知，强行估高会误拒小额度令牌
//     （SPEC 五：预扣要保守，宁可少扣——少扣的部分由结算补上）
//
// 不含分组倍率：预扣发生在选渠道之前，还不知道会走哪个分组。
// 按 1.0 估算，结算时用真实倍率修正。
func (t *PriceTable) EstimateUpperBound(maxTokens int, modelName string) float64 {
	if maxTokens <= 0 {
		return 0
	}
	p, ok := t.Price(modelName)
	if !ok {
		return 0
	}
	return float64(maxTokens) / 1000 * p.OutputPricePer1K
}

// ReloadPrices 强制从存储重读价格表和倍率。
//
// 失败时保留旧缓存：价格查不到最多是少收钱，请求失败是真故障。
// 这个取舍和「价格缺失不报错」是同一条原则。
func (t *PriceTable) ReloadPrices() error {
	if t.loader == nil {
		return nil
	}
	prices, rates, err := t.loader()
	if err != nil {
		slog.Warn("[价格表] 重读失败，继续用旧缓存", "err", err)
		return err
	}
	exact := make(map[string]model.ModelPrice, len(prices))
	var wild []model.ModelPrice
	for _, p := range prices {
		if strings.Contains(p.Model, "*") {
			wild = append(wild, p)
			continue
		}
		exact[p.Model] = p
	}
	t.mu.Lock()
	t.exact = exact
	t.wild = wild
	if rates != nil {
		t.rates = rates
	}
	t.loadedAt = time.Now()
	t.mu.Unlock()
	return nil
}

// refreshIfStale 缓存过期时重读。双重检查锁的轻量版：
// 先在读锁下判断是否过期，过期再走 ReloadPrices（自己拿写锁）。
// 极端并发下可能多个 goroutine 同时重读，但价格表重读是幂等的，
// 多读一次的代价远小于为此维护一把专用加载锁的复杂度。
func (t *PriceTable) refreshIfStale() {
	t.mu.RLock()
	stale := t.loader != nil && (t.loadedAt.IsZero() || time.Since(t.loadedAt) > t.ttl)
	t.mu.RUnlock()
	if stale {
		_ = t.ReloadPrices()
	}
}

// wildcardMatch 判断模型名是否匹配通配条目。只支持 "*"（任意长度任意字符）。
//
// 为什么不用 path.Match：它会把 "?"、"[]" 也当元字符。模型名里
// 出现这些字符时行为出乎意料（比如 "gpt[4]" 会被当成字符类），
// 而价格表里这些写法没有意义——只认 "*" 更可预期。
//
// 实现是「分段按序出现」：gpt-4* → ["gpt-4", ""]，要求前缀是
// gpt-4、后面随便；*4o* → ["", "4o", ""]，只要求包含 4o。
// pattern 以 * 开头时第一段是「包含」匹配而不是前缀匹配——
// 这是最容易写错的地方，单独用 startsWithStar 标记。
// 左起第一个匹配位置是最优的（给后续段留最多空间）。
func wildcardMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	startsWithStar := strings.HasPrefix(pattern, "*")
	rest := name
	for i, part := range strings.Split(pattern, "*") {
		if part == "" {
			continue
		}
		if i == 0 && !startsWithStar {
			if !strings.HasPrefix(rest, part) {
				return false
			}
			rest = rest[len(part):]
			continue
		}
		idx := strings.Index(rest, part)
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(part):]
	}
	return true
}

// wildcardScore 是通配条目的「具体度」分数：字面量字符越多越具体。
// 同分时条目长的胜出，保证排序确定（否则 map 遍历顺序会让匹配结果随机）。
func wildcardScore(pattern string) int {
	return len(pattern) - strings.Count(pattern, "*")
}
