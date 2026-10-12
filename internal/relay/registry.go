// Package relay 是转发层的门面：把配置变成可用的适配器，并执行一次转发。
//
// 包内分工：
//   - registry.go  渠道选择 + 适配器查找
//   - relay.go     单次转发流程 + 失败重试
//   - adaptor/     各厂商适配器实现
package relay

import (
	"fmt"
	"sync"

	"minigateway/internal/config"
	"minigateway/internal/relay/adaptor"
	"minigateway/internal/relay/adaptor/openai"
)

// channel 是运行期的渠道对象，由 ChannelConfig 构建。
type channel struct {
	id      int
	name    string
	typ     string
	baseURL string
	key     string
	models  []string
	mapping map[string]string
	weight  int
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

// registry 是运行期的渠道池 + 适配器表。
//
// 用 sync.RWMutex 而不是普通 map：读多写少，
// 后续做渠道热更新时可以直接加写锁，不用改结构。
type registry struct {
	mu       sync.RWMutex
	channels map[int]*channel
	adaptors map[string]adaptor.Adaptor
}

var reg *registry

// Init 由 main 调用一次，把配置里的渠道装载成运行期对象。
//
// 适配器在这里按渠道类型实例化并缓存：适配器本身是无状态的，
// 可以安全地在多个请求间共享同一个实例。
func Init(cfg *config.Config) error {
	r := &registry{
		channels: make(map[int]*channel, len(cfg.Channels)),
		adaptors: map[string]adaptor.Adaptor{
			"openai": &openai.Adaptor{},
		},
	}
	for _, cc := range cfg.Channels {
		if !cc.IsEnabled() {
			continue
		}
		if _, ok := r.adaptors[cc.Type]; !ok {
			return fmt.Errorf("渠道 %d(%s) 使用了未知类型 %q，可用类型: openai",
				cc.ID, cc.Name, cc.Type)
		}
		r.channels[cc.ID] = &channel{
			id:      cc.ID,
			name:    cc.Name,
			typ:     cc.Type,
			baseURL: cc.BaseURL,
			key:     cc.Key,
			models:  cc.Models,
			mapping: cc.ModelMapping,
			weight:  cc.Weight,
		}
	}
	if len(r.channels) == 0 {
		return fmt.Errorf("没有可用的渠道（可能全部被 enabled: false 关闭了）")
	}
	reg = r
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
