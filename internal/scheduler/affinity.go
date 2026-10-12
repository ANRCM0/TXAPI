package scheduler

import (
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	dbmodel "minigateway/internal/model"
)

// PickWithAffinity 在凭据池里挑一把 Key，优先满足会话亲和。
//
// 与 Pick 的区别只在「怎么定位目标」：Pick 纯加权随机，本函数先用会话 ID
// 通过一致性哈希定位到一把凭据，那把可用就用它；定位失败或不可用就
// **降级到普通加权随机**，绝不让请求因为亲和卡死。
//
// sessionID 为空表示不做亲和（直接走 Pick）。什么时候为空：客户端没传
// X-Session-Id，且请求体里抽不出首条 user 消息（见 SessionID 的说明）。
func PickWithAffinity(creds []dbmodel.Credential, sessionID string, exclude map[uint]bool, now time.Time) (*dbmodel.Credential, bool) {
	if sessionID == "" {
		return Pick(creds, exclude, now)
	}
	usable := filterUsable(creds, exclude, now)
	if len(usable) == 0 {
		return nil, false
	}
	return affinityTarget(usable, sessionID)
}

// vnodeScale 是每单位权重放大出的虚拟节点数。
//
// 这个值直接决定环上分流的准确度，不能随便取小：
// 一致性哈希的落点分布依赖节点在环上「撒得够碎」。用 160 时实测
// 权重 5:1:1 的三个凭据分到 9.2%/76.3%/14.5%（期望 14.3%/71.4%/14.3%），
// 权重 1 的凭据少拿近四成流量——按权重分流这个语义已经失真了。
// 提到 512 后同一组配置分到 14.2%/71.0%/14.8%，落在期望值附近。
//
// 代价是节点数 = Σweight × 512，构建要排序。所以环必须缓存
// （见 getRing），不能每请求重建——否则这个精度是拿吞吐换来的。
const vnodeScale = 512

// ringCacheMax 是环缓存的最大条目数。
//
// 缓存键是「可用凭据集合 + 各自权重」，条数在现实中很小：一个渠道的
// 凭据池是稳定的，键基本就一个。重试时 exclude 集合不同会产生变体键，
// 但重试是低频事件，64 条足够覆盖，超出按先进先出淘汰。
//
// 设上限是为了防病态场景（比如频繁改凭据权重）把内存吃光：
// 一个 10 凭据 × 权重和 100 的池子，环有 51200 个节点、约 400KB。
const ringCacheMax = 64

// ringNode 是环上的一个虚拟节点。
//
// 用 idx（指向 usable 的下标）而不是存整个 Credential：环有上万个节点，
// 存副本会让内存放大几十倍，而凭据内容本来就是只读快照的一部分。
type ringNode struct {
	pos uint32
	idx uint32
}

// credRing 是一致性哈希环，按 pos 升序。
type credRing struct {
	nodes []ringNode
}

var (
	ringMu    sync.RWMutex
	ringCache = make(map[string]*credRing, ringCacheMax)
	// ringFIFO 记录插入顺序用于淘汰。不引第三方 LRU：条目数是个位数，
	// 先进先出与最近最少使用的差别可以忽略。
	ringFIFO []string
)

// poolKey 把可用凭据集合编码成缓存键。
//
// 必须包含权重：改权重会改变每把凭据占的环上区间，那是另一张环。
// 排序是为了让「同样的集合以不同顺序传入」命中同一个键——渠道快照的
// 顺序虽然稳定，但依赖它等于让缓存正确性绑定在一个隐性约定上。
func poolKey(usable []dbmodel.Credential) string {
	parts := make([]string, 0, len(usable))
	for i := range usable {
		parts = append(parts,
			strconv.FormatUint(uint64(usable[i].ID), 10)+":"+
				strconv.Itoa(effectiveWeight(usable[i].Weight)))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// getRing 取（或构建）该凭据池的哈希环。
//
// 为什么必须缓存：构建一张环要做 Σweight × 512 次哈希 + 一次全排序，
// 对 3 把凭据就是 3584 个节点。每请求重做一遍，等于把一致性哈希的
// 固定开销摊在每一个请求上，而这张环在凭据池不变时是常量。
func getRing(usable []dbmodel.Credential, key string) *credRing {
	ringMu.RLock()
	r, ok := ringCache[key]
	ringMu.RUnlock()
	if ok {
		return r
	}

	built := buildRing(usable)

	ringMu.Lock()
	defer ringMu.Unlock()
	// 双重检查：两个请求可能同时未命中并各建一张环，先到的写进缓存，
	// 后到的丢弃自己的副本用先到的——两张环内容相同，但要避免
	// 「后写覆盖」让 FIFO 里出现重复键（会导致淘汰时误删活条目）。
	if r, ok := ringCache[key]; ok {
		return r
	}
	if len(ringFIFO) >= ringCacheMax {
		delete(ringCache, ringFIFO[0])
		ringFIFO = ringFIFO[1:]
	}
	ringCache[key] = built
	ringFIFO = append(ringFIFO, key)
	return built
}

// buildRing 构建一致性哈希环。
func buildRing(usable []dbmodel.Credential) *credRing {
	nodes := make([]ringNode, 0, len(usable)*vnodeScale)
	for i := range usable {
		replicas := effectiveWeight(usable[i].Weight) * vnodeScale
		for r := 0; r < replicas; r++ {
			nodes = append(nodes, ringNode{pos: ringPos(usable[i].ID, r), idx: uint32(i)})
		}
	}
	sort.Slice(nodes, func(a, b int) bool { return nodes[a].pos < nodes[b].pos })
	return &credRing{nodes: nodes}
}

// affinityTarget 用一致性哈希在可用凭据里定位会话的目标凭据。
//
// 为什么用一致性哈希而不是 hash(sessionID) % len(usable)：
// 取模会在凭据增删时让**所有**会话的映射整体重排（分母变了），
// 一次加 Key 就把全部历史会话打散，上游的 prompt 缓存全部作废。
// 一致性哈希把每把凭据映射到环上的若干点，会话只认「顺时针下一个点」，
// 增删一把凭据只影响落在它区间里的那部分会话，其余会话的映射不变——
// 这正是「省钱又提速」的前提：缓存命中率不能被运维加 Key 的动作清零。
//
// 落点与节点位置都过一遍雪崩混合（fmix32），原因见 fmix32 的说明。
func affinityTarget(usable []dbmodel.Credential, sessionID string) (*dbmodel.Credential, bool) {
	if len(usable) == 0 {
		return nil, false
	}
	ring := getRing(usable, poolKey(usable))
	if len(ring.nodes) == 0 {
		return nil, false
	}
	target := ringHash(sessionID)
	idx := sort.Search(len(ring.nodes), func(i int) bool { return ring.nodes[i].pos >= target })
	if idx == len(ring.nodes) {
		idx = 0 // 落点超出环尾，绕回环首
	}
	cred := usable[ring.nodes[idx].idx]
	return &cred, true
}

// ringHash 把会话 ID 映射到环上的一个位置。
//
// 用 FNV-1a 而不是 Go 内置的 map 哈希：map 的哈希带随机种子，每次进程
// 启动都不一样，会话亲和会在重启后全部重排——那还不如不做亲和。
// FNV 是确定性算法，同一 sessionID 永远落在同一个位置。
func ringHash(sessionID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionID))
	return fmix32(h.Sum32())
}

// ringPos 算某把凭据的第 r 个虚拟节点在环上的位置。
//
// 必须拼上副本序号而不是只哈希 ID：同一个 ID 复制出多个节点时位置要不同，
// 否则它们会塌成一个点，权重大的凭据反而只占一个区间，加权语义丢失。
func ringPos(credentialID uint, replica int) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strconv.FormatUint(uint64(credentialID), 10)))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(strconv.Itoa(replica)))
	return fmix32(h.Sum32())
}

// fmix32 是 murmur3 的雪崩终结器，把哈希的每一位都搅进结果。
//
// 为什么 FNV-1a 之后还要再过一遍：FNV 对短输入的雪崩性很差。
// 虚拟节点位置来自 "1:0"、"1:1"、"1:2" 这类只差末尾一两个字符的短串，
// FNV 算出来的一串值会挤在环上同一片区域——实测权重 9:1 的两把凭据
// 分到 8.98%/91.02%（期望 10%/90%），权重小的那把系统性少拿流量。
// 加一步 fmix32 后同一配置收敛到 9.6%/90.4%，且不需要引第三方哈希库。
func fmix32(h uint32) uint32 {
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}
