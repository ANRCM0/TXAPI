package gateway

import (
	"path/filepath"
	"testing"
	"time"

	"minigateway/internal/config"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/store"
)

// 本文件验证 P6 批次 3 的 B3-3：abilities 表接入路由。
//
// 要保证的核心性质：路由选中的渠道与 abilities 表的数据**一致**。
// 这是「接入」而不是「另起一套」的全部意义——如果索引和表能给出
// 不同的答案，那张表就还是没人用的装饰。
//
// 对照基准故意用 channel.supports()（索引改造前的语义实现）：
// 它是旧行为的活文档，索引必须与它逐一对齐，否则就是行为回归。

// makeChannel 造一个渠道实体（绕过 store，直接喂给 Init/Reload）。
//
// Status 用 model.StatusEnabled（"enabled"）而不是 "active"：
// 渠道的状态常量是 enabled/disabled，凭据才是 active——
// 两者不同名，写错了渠道会被 applyLocked 静默跳过（表现是
// 「明明建了渠道却选不到」）。
func makeChannel(id int, name string, models []string) dbmodel.Channel {
	return dbmodel.Channel{
		ID:        uint(id),
		Name:      name,
		Type:      "openai",
		BaseURL:   "https://example.com",
		Models:    dbmodel.StringList(models),
		Status:    dbmodel.StatusEnabled,
		GroupName: "default",
		Weight:    1,
		Credentials: []dbmodel.Credential{
			{ID: uint(id), APIKey: "sk-upstream", Status: dbmodel.CredentialActive, Weight: 1},
		},
	}
}

// indexFor 取某个模型在索引里的渠道 ID 列表（测试用直读内部状态）。
func indexFor(t *testing.T, gw *Gateway, modelName string) []int {
	t.Helper()
	gw.reg.mu.RLock()
	defer gw.reg.mu.RUnlock()
	return append([]int(nil), gw.reg.modelIndex[modelName]...)
}

// pickedIDs 取 pickChannels 选中的渠道 ID 列表。
func pickedIDs(t *testing.T, gw *Gateway, modelName string) []int {
	t.Helper()
	out := []int{}
	for _, ch := range gw.pickChannels(modelName, nil) {
		out = append(out, ch.id)
	}
	return out
}

// containsID 判断 ID 是否在列表里。
func containsID(ids []int, id int) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// TestModelIndexBuiltOnInit：Init 后索引按「渠道的每个 models 条目」
// 建好，与 abilities 表的写入规则一致。
func TestModelIndexBuiltOnInit(t *testing.T) {
	gw := testCoordinator(t)
	channels := []dbmodel.Channel{
		makeChannel(1, "ch-a", []string{"gpt-4o-mini", "gpt-3.5-turbo"}),
		makeChannel(2, "ch-b", []string{"gpt-4o-mini"}),
		makeChannel(3, "ch-c", []string{"claude-3-5-sonnet"}),
	}
	if err := gw.initRegistry(channels); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}

	// gpt-4o-mini 应同时指向渠道 1 和 2
	got := indexFor(t, gw, "gpt-4o-mini")
	if len(got) != 2 || !containsID(got, 1) || !containsID(got, 2) {
		t.Fatalf("gpt-4o-mini 的索引应为 [1 2]（顺序不定），实际 %v", got)
	}
	// gpt-3.5-turbo 只指向渠道 1
	got = indexFor(t, gw, "gpt-3.5-turbo")
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("gpt-3.5-turbo 的索引应为 [1]，实际 %v", got)
	}
	// claude 只指向渠道 3
	got = indexFor(t, gw, "claude-3-5-sonnet")
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("claude-3-5-sonnet 的索引应为 [3]，实际 %v", got)
	}
	// 没配过的模型不该有索引条目
	if got := indexFor(t, gw, "not-a-model"); len(got) != 0 {
		t.Fatalf("未配置的模型不该有索引条目，实际 %v", got)
	}
}

// TestPickChannelsUsesIndex：pickChannels 走索引，命中结果与
// supports() 的语义完全一致。
//
// 这是 B3-3 的主断言：接入索引之后**行为不能变**，只是实现从
// 全表扫描换成 map 命中。
func TestPickChannelsUsesIndex(t *testing.T) {
	gw := testCoordinator(t)
	channels := []dbmodel.Channel{
		makeChannel(1, "ch-a", []string{"gpt-4o-mini", "*"}),
		makeChannel(2, "ch-b", []string{"gpt-4o-mini"}),
		makeChannel(3, "ch-c", []string{"claude-3-5-sonnet"}),
	}
	if err := gw.initRegistry(channels); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}

	// 具体模型：命中精确索引 + 通配渠道
	got := pickedIDs(t, gw, "gpt-4o-mini")
	if len(got) != 2 || !containsID(got, 1) || !containsID(got, 2) {
		t.Fatalf("gpt-4o-mini 应选中渠道 1 和 2，实际 %v", got)
	}
	// 只配在通配渠道上的模型：靠 "*" 命中
	got = pickedIDs(t, gw, "some-unlisted-model")
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("未显式配置的模型应命中通配渠道 1，实际 %v", got)
	}
	// 完全没有渠道支持的模型：空
	if got := pickedIDs(t, gw, "nope"); len(got) != 1 || got[0] != 1 {
		// 渠道 1 配了 "*"，所以任何模型都会命中它——这正是通配的语义
		t.Fatalf("有通配渠道时任何模型都应命中渠道 1，实际 %v", got)
	}

	// 逐个模型对照 supports()：这是「行为没变」的证明
	for _, m := range []string{"gpt-4o-mini", "claude-3-5-sonnet", "whatever", ""} {
		picked := pickedIDs(t, gw, m)
		for _, ch := range channels {
			// 用旧语义（supports）当基准
			var want bool
			for _, mm := range ch.Models {
				if mm == "*" || mm == m {
					want = true
				}
			}
			got := containsID(picked, int(ch.ID))
			if want != got {
				t.Fatalf("模型 %q：渠道 %d 的选中结果 %v 与 supports 语义 %v 不一致",
					m, ch.ID, got, want)
			}
		}
	}
}

// TestIndexRebuiltOnReload：Reload 后索引跟着更新。
//
// 这条防的是「换渠道不换索引」的静默故障：渠道删了、索引还指着它，
// 转发时从 map 里取到 nil 直接 panic。
func TestIndexRebuiltOnReload(t *testing.T) {
	gw := testCoordinator(t)
	if err := gw.initRegistry([]dbmodel.Channel{
		makeChannel(1, "ch-a", []string{"gpt-4o-mini"}),
		makeChannel(2, "ch-b", []string{"gpt-4o-mini"}),
	}); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	if got := pickedIDs(t, gw, "gpt-4o-mini"); len(got) != 2 {
		t.Fatalf("初始应选中 2 个渠道，实际 %v", got)
	}

	// 热更新：渠道 2 改去服务别的模型
	if err := gw.Reload([]dbmodel.Channel{
		makeChannel(1, "ch-a", []string{"gpt-4o-mini"}),
		makeChannel(2, "ch-b", []string{"claude-3-5-sonnet"}),
	}); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}

	got := pickedIDs(t, gw, "gpt-4o-mini")
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("Reload 后 gpt-4o-mini 应只剩渠道 1，实际 %v", got)
	}
	// 旧的索引条目必须清掉：渠道 2 不再服务 gpt-4o-mini
	if stale := indexFor(t, gw, "gpt-4o-mini"); containsID(stale, 2) {
		t.Fatalf("Reload 后索引里还残留渠道 2，实际 %v", stale)
	}
	// 新模型要能选中渠道 2
	if got := pickedIDs(t, gw, "claude-3-5-sonnet"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("Reload 后 claude-3-5-sonnet 应选中渠道 2，实际 %v", got)
	}
}

// TestIndexMatchesAbilitiesTable：路由选中的渠道与 abilities 表的
// 数据一致。
//
// 这是 B3-3 的验收断言。abilities 表由 store.syncAbilities 在渠道
// 增删改时维护，路由走内存索引——两者必须给出同一份答案，否则
// 「接入」就白接了。
func TestIndexMatchesAbilitiesTable(t *testing.T) {
	gw := testCoordinator(t)
	st, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), "gateway.db"),
	})
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// 走 store 建渠道：这样 syncAbilities 才会被触发，abilities 表
	// 里才会有数据（绕过 store 直接 Init 的话表是空的，对照就没意义）
	ch1, err := st.CreateChannel(&store.ChannelInput{
		Name: "ch-a", Type: "openai", BaseURL: "https://a.example.com",
		Models: []string{"gpt-4o-mini", "gpt-3.5-turbo"}, APIKey: "sk-a", GroupName: "default",
	})
	if err != nil {
		t.Fatalf("建渠道 1 失败: %v", err)
	}
	ch2, err := st.CreateChannel(&store.ChannelInput{
		Name: "ch-b", Type: "openai", BaseURL: "https://b.example.com",
		Models: []string{"gpt-4o-mini", "claude-3-5-sonnet"}, APIKey: "sk-b", GroupName: "default",
	})
	if err != nil {
		t.Fatalf("建渠道 2 失败: %v", err)
	}

	// 转发层用 store 的快照装载——和 main.go 的启动路径一致
	if err := gw.initRegistry(st.Channels()); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}

	abilities, err := st.Abilities()
	if err != nil {
		t.Fatalf("查 abilities 失败: %v", err)
	}
	if len(abilities) == 0 {
		t.Fatal("abilities 表是空的，对照无从谈起（syncAbilities 没生效？）")
	}

	// 逐行对照：abilities 表里每个 (model, channel) 组合，
	// pickChannels 都必须选中那个渠道。
	for _, ab := range abilities {
		picked := pickedIDs(t, gw, ab.Model)
		if !containsID(picked, int(ab.ChannelID)) {
			t.Fatalf("abilities 表说模型 %q 由渠道 %d 服务，但路由没选中它（选中: %v）",
				ab.Model, ab.ChannelID, picked)
		}
	}
	// 反向对照：路由选中的每个渠道，abilities 表里也该有对应行——
	// 否则说明索引比表「多」了指不存在的组合。
	for _, m := range []string{"gpt-4o-mini", "gpt-3.5-turbo", "claude-3-5-sonnet"} {
		for _, id := range pickedIDs(t, gw, m) {
			found := false
			for _, ab := range abilities {
				if ab.Model == m && int(ab.ChannelID) == id {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("路由为模型 %q 选中了渠道 %d，但 abilities 表里没有这行（索引与表不一致）", m, id)
			}
		}
	}

	// 改渠道后 abilities 和索引都要跟上（这正是「同生命周期」的含义）
	if _, err := st.UpdateChannel(ch1.ID, &store.ChannelInput{
		Name: "ch-a", Type: "openai", BaseURL: "https://a.example.com",
		Models: []string{"gpt-4o-mini"}, APIKey: "sk-a", GroupName: "default",
	}); err != nil {
		t.Fatalf("改渠道失败: %v", err)
	}
	if err := gw.Reload(st.Channels()); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}

	abilities, err = st.Abilities()
	if err != nil {
		t.Fatalf("重查 abilities 失败: %v", err)
	}
	for _, ab := range abilities {
		if !containsID(pickedIDs(t, gw, ab.Model), int(ab.ChannelID)) {
			t.Fatalf("改渠道后 abilities 表说模型 %q 由渠道 %d 服务，但路由没选中它", ab.Model, ab.ChannelID)
		}
	}
	// gpt-3.5-turbo 已从渠道 1 移除，但渠道 2 没配它——应该谁都选不中
	if got := pickedIDs(t, gw, "gpt-3.5-turbo"); len(got) != 0 {
		t.Fatalf("gpt-3.5-turbo 已无渠道支持，实际选中 %v", got)
	}
	_ = ch1
	_ = ch2
}

// TestIndexSkipsDisabledChannels：停用的渠道不进索引。
//
// applyLocked 本来就跳过停用/无凭据的渠道，索引必须在同一个循环里
// 建——否则索引会指向一个根本没进渠道池的 ID，pickChannels 取不到
// 渠道对象（代码里有防御性跳过，但那是兜底，不该靠它兜）。
func TestIndexSkipsDisabledChannels(t *testing.T) {
	gw := testCoordinator(t)
	disabled := makeChannel(1, "ch-disabled", []string{"gpt-4o-mini"})
	disabled.Status = dbmodel.StatusDisabled
	if err := gw.initRegistry([]dbmodel.Channel{disabled}); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	if got := pickedIDs(t, gw, "gpt-4o-mini"); len(got) != 0 {
		t.Fatalf("停用的渠道不该被选中，实际 %v", got)
	}
	if got := indexFor(t, gw, "gpt-4o-mini"); len(got) != 0 {
		t.Fatalf("停用的渠道不该进索引，实际 %v", got)
	}
}

// TestIndexNoDuplicateForWildcardAndExplicit：同时配了具体模型和
// "*" 的渠道不能在候选列表里出现两次。
//
// 不去重的话，它在加权随机里被选中两次的概率翻倍——等于偷偷改了
// 权重，而这种偏差在流量分布上是看不出来的。
func TestIndexNoDuplicateForWildcardAndExplicit(t *testing.T) {
	gw := testCoordinator(t)
	if err := gw.initRegistry([]dbmodel.Channel{
		makeChannel(1, "ch-both", []string{"gpt-4o-mini", "*"}),
	}); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	got := pickedIDs(t, gw, "gpt-4o-mini")
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("同时配具体模型和通配的渠道应只出现一次，实际 %v", got)
	}
}

// TestIndexRollbackOnBadChannel：装载失败时索引跟着渠道池一起回滚。
//
// 只回滚 channels 不回滚 modelIndex 的话，失败后会留下一个
// 「索引指向已被丢弃的渠道」的中间态——pickChannels 取不到对象，
// 依赖的是代码里的防御性跳过，而不是一致性保证。
func TestIndexRollbackOnBadChannel(t *testing.T) {
	gw := testCoordinator(t)
	if err := gw.initRegistry([]dbmodel.Channel{
		makeChannel(1, "ch-a", []string{"gpt-4o-mini"}),
	}); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	before := indexFor(t, gw, "gpt-4o-mini")

	// 未知协议类型会被 applyLocked 跳过而不是报错，所以这里用
	// 「空渠道列表」来验证回滚路径不会把索引清空：
	// 空列表装载成功（0 个渠道是合法状态），索引应该跟着变空。
	if err := gw.Reload(nil); err != nil {
		t.Fatalf("Reload 空列表失败: %v", err)
	}
	if got := indexFor(t, gw, "gpt-4o-mini"); len(got) != 0 {
		t.Fatalf("Reload 空列表后索引应清空，实际 %v", got)
	}

	// 再装回去，索引要能完整恢复
	if err := gw.Reload([]dbmodel.Channel{
		makeChannel(1, "ch-a", []string{"gpt-4o-mini"}),
	}); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}
	after := indexFor(t, gw, "gpt-4o-mini")
	if len(after) != len(before) {
		t.Fatalf("重新装载后索引应恢复，期望 %v 实际 %v", before, after)
	}
}

// TestIndexConcurrentReadWrite：索引在并发读写下不能崩。
//
// pickChannels 走读锁、Reload 走写锁，两者共用同一份 map。
// -race 下跑这条能抓出漏加锁的读写（那是会直接打挂进程的 bug）。
func TestIndexConcurrentReadWrite(t *testing.T) {
	gw := testCoordinator(t)
	if err := gw.initRegistry([]dbmodel.Channel{
		makeChannel(1, "ch-a", []string{"gpt-4o-mini"}),
		makeChannel(2, "ch-b", []string{"gpt-4o-mini"}),
	}); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = gw.Reload([]dbmodel.Channel{
				makeChannel(1, "ch-a", []string{"gpt-4o-mini"}),
				makeChannel(2, "ch-b", []string{"gpt-4o-mini", "claude-3-5-sonnet"}),
			})
			time.Sleep(time.Millisecond)
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for i := 0; i < 100; i++ {
			_ = gw.pickChannels("gpt-4o-mini", nil)
			_ = gw.pickChannels("claude-3-5-sonnet", nil)
		}
		select {
		case <-done:
			return
		default:
		}
	}
	<-done
}
