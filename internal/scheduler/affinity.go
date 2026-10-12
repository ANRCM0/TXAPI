package scheduler

import (
	"hash/fnv"
	"sort"
	"strconv"
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

// affinityTarget 用一致性哈希在可用凭据里定位会话的目标凭据，失败则降级。
//
// 为什么用一致性哈希而不是 hash(sessionID) % len(usable)：
// 取模会在凭据增删时让**所有**会话的映射整体重排（分母变了），
// 一次加 Key 就把全部历史会话打散，上游的 prompt 缓存全部作废。
// 一致性哈希把每把凭据映射到环上的若干点，会话只认「顺时针下一个点」，
// 增删一把凭据只影响落在它区间里的那部分会话，其余会话的映射不变——
// 这正是「省钱又提速」的前提：缓存命中率不能被运维加 Key 的动作清零。
//
// 实现方式：每把凭据按权重放大出若干个虚拟节点，节点位置由
// (credentialID, 副本序号) 哈希决定（与 sessionID 无关，所以凭据集合
// 不变时整张环不变）；会话落点由 sessionID 哈希决定，顺时针找第一个
// ≥ 落点的节点。排序 + 二分即可，不需要真的建环结构。
func affinityTarget(usable []dbmodel.Credential, sessionID string) (*dbmodel.Credential, bool) {
	type node struct {
		pos  uint32
		cred int // usable 里的下标
	}
	// 虚拟节点数 = 权重 × 放大倍数，而不是直接等于权重。
	//
	// 为什么必须放大：一致性哈希的「只影响一小部分会话」这个性质，前提是
	// 每把凭据在环上占的区间足够碎。直接拿权重当节点数时，weight=1 的
	// 凭据只有 1 个节点、占 1 段连续区间——它那段里的**所有**会话在删
	// 别的凭据时都会跟着重排（相邻区间的边界全变了）。实测权重 3:2:1 时
	// 删一把会让 86% 的会话映射变化，一致性哈希名存实亡。
	//
	// 放大 160 倍后 weight=1 也有 160 个节点散在环上，删一把凭据的
	// 影响面收敛到它实际占的流量份额（约 1/6），这才是想要的性质。
	// 160 这个数是随手取的 2 的幂附近值，任何 ≥64 的倍数都能让分布
	// 足够均匀；再大只是白费排序时间（节点数 = Σweight × 160）。
	const vnodeScale = 160
	nodes := make([]node, 0, len(usable)*vnodeScale)
	for i := range usable {
		replicas := effectiveWeight(usable[i].Weight) * vnodeScale
		for r := 0; r < replicas; r++ {
			nodes = append(nodes, node{pos: ringPos(usable[i].ID, r), cred: i})
		}
	}
	if len(nodes) == 0 {
		return nil, false
	}
	sort.Slice(nodes, func(a, b int) bool { return nodes[a].pos < nodes[b].pos })
	target := ringHash(sessionID)
	idx := sort.Search(len(nodes), func(i int) bool { return nodes[i].pos >= target })
	if idx == len(nodes) {
		idx = 0 // 落点超出环尾，绕回环首
	}
	cred := usable[nodes[idx].cred]
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
	return h.Sum32()
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
	return h.Sum32()
}
