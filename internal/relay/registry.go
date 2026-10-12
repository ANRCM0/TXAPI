// Package relay 是转发层的门面：把渠道数据变成可用的适配器，并执行一次转发。
//
// 包内分工：
//   - registry.go  渠道池 + 适配器表（P2 起数据来自 store，支持热更新）
//   - relay.go     单次转发流程 + 失败重试
//   - adaptor/     各厂商适配器实现
package relay

import (
	"log"
	"sort"
	"sync"
	"time"

	"minigateway/internal/model"
	"minigateway/internal/relay/adaptor"
	"minigateway/internal/relay/adaptor/openai"
)

// channel 是运行期的渠道对象，由 model.Channel + 凭据池构建。
//
// key 字段现在是「本次转发实际使用的上游 key」：
// P2 阶段从凭据池里挑第一个可用的，P3 会换成完整的加权随机/冷却/拉黑调度。
// 保留单 key 字段是为了不改动 relay.go 的转发主流程。
type channel struct {
	id      int
	name    string
	typ     string
	baseURL string
	key     string
	models  []string
	mapping map[string]string
	weight  int
	// priority 是渠道优先级，数值越大越先被选中（主备渠道的表达方式）。
	priority int
	// credentialID 是当前使用的那把 Key 的 ID，记账时要写进日志，
	// 否则事后查不出「哪个 Key 出的问题」。
	credentialID uint
}

// supports 判断这个渠道能否服务指定模型。
//
// 支持通配符 "*"，用于「兼容任何模型」的中转渠道。
func (c *channel) supports(modelName string) bool {
	for _, m := range c.models {
		if m == "*" || m == modelName {
			return true
		}
	}
	return false
}

// pickKey 从渠道的凭据池里挑一个可用的上游 key。
//
// P2 的最小实现：取权重最高、且当前可用的第一个凭据。
// P3 会把这里换成 scheduler.SelectCredential()，做加权随机 + 失败冷却 + 自动拉黑。
// 之所以现在就按「可用」过滤：渠道配了 Key 但状态是 disabled 时，
// 继续拿它去请求只会拿到上游的 401，不如直接当成没有可用渠道。
func pickKey(creds []model.Credential, now time.Time) (string, uint, bool) {
	usable := make([]model.Credential, 0, len(creds))
	for _, c := range creds {
		if c.IsUsable(now) && c.APIKey != "" {
			usable = append(usable, c)
		}
	}
	if len(usable) == 0 {
		return "", 0, false
	}
	// 按权重降序，权重相同按 ID 升序，保证选择结果稳定可复现
	sort.SliceStable(usable, func(i, j int) bool {
		if usable[i].Weight != usable[j].Weight {
			return usable[i].Weight > usable[j].Weight
		}
		return usable[i].ID < usable[j].ID
	})
	return usable[0].APIKey, usable[0].ID, true
}

// registry 是运行期的渠道池 + 适配器表。
//
// 用 sync.RWMutex 而不是普通 map：读多写少，
// 渠道热更新（管理 API 增删改）走写锁，转发路径走读锁。
type registry struct {
	mu       sync.RWMutex
	channels map[int]*channel
	adaptors map[string]adaptor.Adaptor
}

var reg *registry

// Init 建好适配器表并装载首批渠道。
//
// P2 的变化：渠道不再来自 YAML，而是来自数据库快照。
// main 在完成 store.Open + SeedFromConfig 之后调用本函数，
// 之后每次渠道变更再调 Reload 刷新。
func Init(channels []model.Channel) error {
	r := &registry{
		channels: make(map[int]*channel, len(channels)),
		adaptors: map[string]adaptor.Adaptor{
			"openai": &openai.Adaptor{},
		},
	}
	if err := r.apply(channels); err != nil {
		return err
	}
	reg = r
	return nil
}

// Reload 用新的渠道快照替换渠道池，实现「不重启网关就生效」。
//
// 关键设计：适配器表不重建。适配器是无状态的，重建纯属浪费；
// 而且万一某个适配器的构造函数变重（比如要读证书文件），
// 热更新一次就付一次代价。只换 channels，锁的持有时间也最短。
func Reload(channels []model.Channel) error {
	if reg == nil {
		return Init(channels)
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	// 整表替换而不是逐个 diff：替换期间读到的是「替换前」或「替换后」的
	// 完整快照，不会出现「一半新一半旧」的中间态
	next := make(map[int]*channel, len(channels))
	saved := reg.channels
	reg.channels = next
	if err := reg.applyLocked(channels); err != nil {
		reg.channels = saved // 失败回滚，别把好数据换成坏数据
		return err
	}
	return nil
}

// apply 在写锁保护下重建渠道池（首次 Init 用）。
func (r *registry) apply(channels []model.Channel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applyLocked(channels)
}

// applyLocked 真正的装载逻辑，调用方必须已持有写锁。
//
// 单独拆出来是因为 Reload 需要「先换 map 再装载，失败时回滚」，
// 而 Init 直接装载即可。两者都要用同一套转换逻辑，不能抄两份。
func (r *registry) applyLocked(channels []model.Channel) error {
	now := time.Now()
	skipped := 0
	for _, ch := range channels {
		if !ch.IsEnabled() {
			skipped++
			continue
		}
		if _, ok := r.adaptors[ch.Type]; !ok {
			// 不直接返回错误：一个渠道类型写错不该让整个网关起不来。
			// 跳掉它并告警，其余渠道照常服务；运维看到日志再去改。
			log.Printf("[渠道装载] 渠道 %d(%s) 使用了未知类型 %q，已跳过（可用: openai）", ch.ID, ch.Name, ch.Type)
			skipped++
			continue
		}
		key, credID, ok := pickKey(ch.Credentials, now)
		if !ok {
			log.Printf("[渠道装载] 渠道 %d(%s) 没有可用凭据，已跳过", ch.ID, ch.Name)
			skipped++
			continue
		}
		r.channels[int(ch.ID)] = &channel{
			id:           int(ch.ID),
			name:         ch.Name,
			typ:          ch.Type,
			baseURL:      ch.BaseURL,
			key:          key,
			models:       []string(ch.Models),
			mapping:      map[string]string(ch.ModelMapping),
			weight:       ch.Weight,
			priority:     ch.Priority,
			credentialID: credID,
		}
	}
	if skipped > 0 {
		log.Printf("[渠道装载] 共 %d 个渠道，跳过 %d 个（停用/类型未知/无可用凭据）", len(channels), skipped)
	}
	// 注意：这里**不**再要求「至少一个渠道」。
	// P2 起渠道可以全部通过 API 动态增删，空渠道池是合法状态
	// （表现为所有请求返回「没有可用渠道」），不该阻止网关启动。
	return nil
}

// getAdaptor 按渠道类型取适配器实例。
func (r *registry) getAdaptor(channelType string) (adaptor.Adaptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adaptors[channelType]
	return a, ok
}

// allChannels 返回全部渠道的副本，供路由层筛选。
func (r *registry) allChannels() []*channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*channel, 0, len(r.channels))
	for _, ch := range r.channels {
		out = append(out, ch)
	}
	return out
}

// ChannelCount 返回当前可用渠道数，供健康检查用。
func ChannelCount() int {
	if reg == nil {
		return 0
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return len(reg.channels)
}

// ensureInit 在 reg 为 nil 时兜底初始化，避免 nil map 写入 panic。
//
// 什么时候会走到：单元测试直接调 pickChannels 而没有先 Init。
// 生产路径上 main 一定会先 Init，这里只是防御。
func ensureInit() {
	if reg == nil {
		reg = &registry{
			channels: map[int]*channel{},
			adaptors: map[string]adaptor.Adaptor{"openai": &openai.Adaptor{}},
		}
	}
}

// Snapshot 返回当前渠道池的可读快照（调试/健康检查用）。
func Snapshot() []map[string]any {
	ensureInit()
	out := make([]map[string]any, 0)
	for _, ch := range reg.allChannels() {
		out = append(out, map[string]any{
			"id":       ch.id,
			"name":     ch.name,
			"type":     ch.typ,
			"models":   ch.models,
			"weight":   ch.weight,
			"priority": ch.priority,
		})
	}
	return out
}
