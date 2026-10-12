// Package scheduler 是凭据池调度器：在多把上游 Key 之间做加权随机分流，
// 并根据转发结果维护每把 Key 的健康状态（失败冷却、连续失败拉黑）。
//
// 这个包是本项目与 One API 最本质的差异所在：One API 里「渠道 == 一个
// Key」，想用 5 个 Key 就得建 5 个渠道，无法表达「这 5 个 Key 是同一家的、
// 应该一起调度」。这里渠道只描述「上游在哪」，Key 全部挂在 credentials
// 表上，由本包统一调度。
//
// 依赖方向：本包只依赖 internal/model，是叶子包。它**不**知道 store 和
// relay 的存在——状态持久化通过注入点（SetHealthPersister）由 main 接上
// store，与 relay 的 SetLogRecorder 同一套路。这样调度算法可以在单元
// 测试里裸跑（不建库、不起 HTTP），而生产环境又能落库。
package scheduler

import (
	"math/rand"
	"time"

	dbmodel "minigateway/internal/model"
)

// Pick 在凭据池里按权重随机挑一把当前可用的 Key。
//
// 参数：
//   - creds 渠道下的全部凭据（由调用方从渠道快照里取）
//   - exclude 本次请求已经试过、不想再试的凭据 ID 集合（nil 表示不排除）。
//     重试时靠它避免「换一把 Key 重试又换回刚才失败的那把」。
//   - now 当前时间，用于判断冷却是否到期。传进来而不是内部取 time.Now()：
//     冷却判定必须和调用方看到的是同一个时刻，否则测试无法构造
//     「刚好到期」的边界。
//
// 返回 nil, false 表示没有可用凭据（全在冷却/拉黑/被排除，或 Key 为空）。
func Pick(creds []dbmodel.Credential, exclude map[uint]bool, now time.Time) (*dbmodel.Credential, bool) {
	usable := filterUsable(creds, exclude, now)
	if len(usable) == 0 {
		return nil, false
	}
	return weightedFrom(usable, rand.Intn)
}

// PickWithRNG 与 Pick 等价，但随机源由调用方提供。
//
// 存在的唯一理由是让测试可复现：加权随机用全局 rand 时，同一份测试在
// 不同次运行里会采到不同的样本，1000 次采样下偶发越出 ±20% 容差，CI
// 就变成「随机红」——这种失败没人会认真查，最终只会把容差越放越松，
// 测试也就抓不住「压根没分流」这类真 bug 了。
//
// 生产代码一律走 Pick：全局 rand 在多 goroutine 下本就并发安全，而每次
// 选择都新建一个 *rand.Rand 反而要付构造开销，还会丢掉全局种子的好处。
func PickWithRNG(creds []dbmodel.Credential, exclude map[uint]bool, now time.Time, rng *rand.Rand) (*dbmodel.Credential, bool) {
	usable := filterUsable(creds, exclude, now)
	if len(usable) == 0 {
		return nil, false
	}
	return weightedFrom(usable, func(n int) int { return rng.Intn(n) })
}

// filterUsable 挑出「当前可用」的凭据，返回副本。
//
// 过滤条件与 PLAN 3.3 的选择算法一致：
//   - IsUsable：状态 active 且冷却已到期（见 model.Credential.IsUsable）
//   - APIKey 非空：空 Key 请求必然 401，而 401 又会被算成「这把 Key 坏了」
//     把它拖进冷却——用一个本来就没配 Key 的条目去污染健康状态，越调越乱
//   - 不在 exclude 里：本次请求已经试过的不再试
//
// 为什么返回副本而不是原切片的下标：调用方（affinity）会取地址返回结果，
// 指向共享只读快照的元素会让「快照不可变」的约定失效，也会在 -race 下
// 和别的请求的读撞上。
func filterUsable(creds []dbmodel.Credential, exclude map[uint]bool, now time.Time) []dbmodel.Credential {
	out := make([]dbmodel.Credential, 0, len(creds))
	for i := range creds {
		c := &creds[i]
		if exclude[c.ID] || !c.IsUsable(now) || c.APIKey == "" {
			continue
		}
		out = append(out, *c)
	}
	return out
}

// weightedFrom 在已过滤的凭据里按权重随机挑一个。
//
// randIntn 是 [0, n) 上的随机数函数，注入而不是直接用 rand.Intn：
// 这样测试能传固定种子的随机源（见 PickWithRNG），生产传 rand.Intn。
func weightedFrom(usable []dbmodel.Credential, randIntn func(n int) int) (*dbmodel.Credential, bool) {
	if len(usable) == 0 {
		return nil, false
	}
	total := 0
	for i := range usable {
		total += effectiveWeight(usable[i].Weight)
	}
	// 为什么用加权随机而不是「按权重降序排序取第一个」：
	// 排序取第一个会让权重低的凭据**永远**拿不到流量，权重退化成优先级
	// （只有权重最高的那把在服务，其余全是摆设）。运维配 weight=5 的
	// 意图是「这把 Key 分到 5 份流量」，不是「这把 Key 优先」——两者
	// 语义完全不同。加权随机才是唯一能兑现「按比例分流」的算法。
	//
	// 具体做法：把 Σweight 看成一条数轴，每把凭据占一段等长的区间，
	// 在 [0, Σweight) 里掷一个点，落在谁区间就是谁。
	n := randIntn(total)
	for i := range usable {
		n -= effectiveWeight(usable[i].Weight)
		if n < 0 {
			return &usable[i], true
		}
	}
	// 取整不会走到这里（各区间接缝无缺口），兜底返回最后一个，
	// 保证「选不出」只发生在真的一把都没有时
	return &usable[len(usable)-1], true
}

// effectiveWeight 把配置里的权重折算成实际参与分流的份数。
//
// 权重 ≤ 0 按 1 处理，而不是按 0 排除：管理接口和种子数据都可能漏配
// 权重（DB 默认值是 1，但历史数据或手写 SQL 可能留下 0），按 0 算会让
// 这把凭据永远选不中——比没有它还糟，因为它占了渠道里一个名额却不干活。
// 同时避免 Σweight 为 0 时 rand.Intn(0) 直接 panic。
func effectiveWeight(w int) int {
	if w <= 0 {
		return 1
	}
	return w
}
