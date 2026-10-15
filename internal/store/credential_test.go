package store

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/model"
)

// 本文件覆盖凭据 CRUD 的全部规则：增删改查、同渠道 key 查重、
// 删除最后一个凭据被拒、状态手动设置（含 active 时清调度状态）。
//
// 为什么在 store 层而不是 API 层测：查重、末位保护、缓存刷新都是
// store 的职责，API 层只是转发。在这里测能用最小组件覆盖全部规则，
// API 层的路由拼接另有 curl 手工验收。

// newCredTestStore 建一个带一个渠道（含 1 个默认凭据）的测试库。
//
// 渠道是凭据的宿主，凭据的每个操作都要先有渠道，所以统一在这里建好。
func newCredTestStore(t *testing.T) *Store {
	t.Helper()
	st := openTestStore(t, "cred")
	if _, err := st.CreateChannel(&ChannelInput{
		Name:    "cred-test",
		Type:    "openai",
		BaseURL: "http://127.0.0.1:9911",
		Models:  []string{"gpt-4o-mini"},
		APIKey:  "sk-orig-key-0001",
	}); err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}
	return st
}

// TestCredentialCRUD 覆盖「加 3 把 Key → 列出来 → 改 → 删」的主干路径。
//
// 同时验证缓存刷新：ListCredentials 读的是内存缓存，创建/更新/删除
// 之后必须立刻能看到变化——这就是「不重启网关就生效」的那一环。
func TestCredentialCRUD(t *testing.T) {
	st := newCredTestStore(t)
	defer st.Close()

	// 渠道自带 1 个默认凭据
	if creds := st.ListCredentials(1); len(creds) != 1 {
		t.Fatalf("新渠道应有 1 个凭据, 实际 %d", len(creds))
	}

	// 加两把（渠道已有 1 把，共 3 把）
	for _, key := range []string{"sk-second-0002", "sk-third-0003"} {
		if _, err := st.CreateCredential(1, &CredentialInput{Name: key, APIKey: key, Weight: 2}); err != nil {
			t.Fatalf("创建凭据 %s 失败: %v", key, err)
		}
	}
	creds := st.ListCredentials(1)
	if len(creds) != 3 {
		t.Fatalf("应有 3 个凭据, 实际 %d", len(creds))
	}
	// 缓存里的排序是 weight DESC, id ASC：新建的两个 weight=2
	// 应排在默认凭据（weight 继承渠道的 1）之前
	if creds[0].Weight != 2 {
		t.Fatalf("排序错误: 第一条 weight 应为 2, 实际 %d", creds[0].Weight)
	}

	// 改一把：换 key + 改权重
	updated, err := st.UpdateCredential(creds[0].ID, &CredentialInput{
		Name:   "renamed",
		APIKey: "sk-second-0002", // 等于自己的旧值，不算重复
		Weight: 9,
	})
	if err != nil {
		t.Fatalf("更新凭据失败: %v", err)
	}
	if updated.Weight != 9 || updated.Name != "renamed" {
		t.Fatalf("更新没生效: %+v", updated)
	}
	// 缓存必须立刻反映新权重（否则转发层还用旧权重分流）
	if got := st.ListCredentials(1); got[0].Weight != 9 {
		t.Fatalf("更新后缓存没刷新: 第一条 weight=%d", got[0].Weight)
	}

	// 删一把：还剩两把，允许
	if err := st.DeleteCredential(creds[1].ID); err != nil {
		t.Fatalf("删除凭据失败: %v", err)
	}
	if got := st.ListCredentials(1); len(got) != 2 {
		t.Fatalf("删除后应有 2 个凭据, 实际 %d", len(got))
	}
}

// TestCredentialDuplicateKeyRejected 验证同渠道 api_key 不允许重复。
//
// 为什么必须拒：两把相同的 Key 会被调度器当成两个独立凭据轮换，
// 故障时按凭据排查会得到「同一把 Key 一半成功一半失败」的假象。
// 但同一把 Key 出现在**不同渠道**是合法的（一个 Key 供两个上游
// 中转用的场景确实存在），所以查重必须带 channel_id 条件。
func TestCredentialDuplicateKeyRejected(t *testing.T) {
	st := newCredTestStore(t)
	defer st.Close()

	dup := "sk-orig-key-0001" // 渠道 1 的默认凭据就是这把
	_, err := st.CreateCredential(1, &CredentialInput{APIKey: dup})
	if err == nil {
		t.Fatal("同渠道重复 api_key 应被拒绝")
	}
	// 更新路径同样要拒：换成同渠道另一把凭据的 key
	creds := st.ListCredentials(1)
	other := creds[0]
	if _, err := st.CreateCredential(1, &CredentialInput{APIKey: "sk-unique-9999"}); err != nil {
		t.Fatalf("加第二把失败: %v", err)
	}
	creds = st.ListCredentials(1)
	if _, err := st.UpdateCredential(creds[1].ID, &CredentialInput{APIKey: dup}); err == nil {
		t.Fatal("更新成同渠道已有的 key 应被拒绝")
	}
	_ = other

	// 跨渠道不重复即可：渠道 2 可以用渠道 1 的 key
	if _, err := st.CreateChannel(&ChannelInput{
		Name:    "cred-test-2",
		Type:    "openai",
		BaseURL: "http://127.0.0.1:9911",
		Models:  []string{"gpt-4o-mini"},
		APIKey:  dup,
	}); err != nil {
		t.Fatalf("渠道 2 用相同 key 应合法, 实际被拒: %v", err)
	}
}

// TestCredentialDeleteLastRejected 验证删掉渠道最后一个凭据会被拒绝。
//
// 没有 Key 的渠道是空壳：路由还能命中它，转发必然 401，
// 而列表里渠道状态还是 enabled，看不出异常——这种故障排查起来
// 成本极高，所以在 store 层就拦住。
func TestCredentialDeleteLastRejected(t *testing.T) {
	st := newCredTestStore(t)
	defer st.Close()

	creds := st.ListCredentials(1)
	if len(creds) != 1 {
		t.Fatalf("初始应有 1 个凭据, 实际 %d", len(creds))
	}
	err := st.DeleteCredential(creds[0].ID)
	if err == nil {
		t.Fatal("删除最后一个凭据应被拒绝")
	}
	// 被拒之后凭据必须还在（不能删一半失败留残局）
	if got := st.ListCredentials(1); len(got) != 1 {
		t.Fatalf("拒绝删除后凭据应仍在, 实际 %d", len(got))
	}
	// 加到两个之后再删：删到剩一个为止，最后一个仍然拒
	if _, err := st.CreateCredential(1, &CredentialInput{APIKey: "sk-second-0002"}); err != nil {
		t.Fatalf("加第二把失败: %v", err)
	}
	creds = st.ListCredentials(1)
	for _, c := range creds[:1] {
		if err := st.DeleteCredential(c.ID); err != nil {
			t.Fatalf("删除非末位凭据失败: %v", err)
		}
	}
	rest := st.ListCredentials(1)
	if len(rest) != 1 {
		t.Fatalf("应剩 1 个, 实际 %d", len(rest))
	}
	if err := st.DeleteCredential(rest[0].ID); err == nil {
		t.Fatal("最后一个凭据仍应被拒绝")
	}
}

// TestCredentialSetStatus 验证手动状态设置，重点是「设回 active
// 必须连带清掉调度状态」。
//
// 为什么重要：cooldown_until 和 fail_count 是调度器的冷却字段。
// 人工解封时只改 status 不清这两个，凭据虽然状态是 active 却仍被
// IsUsable 判为冷却中——「解封了但流量还是不打过去」是最难排查的
// 一类故障。反过来设 banned 时保留它们，方便事后复盘失败历史。
func TestCredentialSetStatus(t *testing.T) {
	st := newCredTestStore(t)
	defer st.Close()

	creds := st.ListCredentials(1)
	id := creds[0].ID

	// 先手工把凭据打成「冷却中 + 有失败计数」，模拟调度器罚过它
	future := time.Now().Add(time.Hour).Unix()
	if err := st.DB().Model(&model.Credential{}).Where("id = ?", id).
		Updates(map[string]any{"status": model.CredentialCooldown, "cooldown_until": future, "fail_count": 3}).Error; err != nil {
		t.Fatalf("预制冷却状态失败: %v", err)
	}

	// 解封回 active
	back, err := st.SetCredentialStatus(id, model.CredentialActive)
	if err != nil {
		t.Fatalf("设 active 失败: %v", err)
	}
	if back.Status != model.CredentialActive {
		t.Fatalf("状态没改成 active: %s", back.Status)
	}
	if back.CooldownUntil != 0 || back.FailCount != 0 {
		t.Fatalf("解封应清掉调度状态, 实际 cooldown_until=%d fail_count=%d", back.CooldownUntil, back.FailCount)
	}
	if !back.IsUsable(time.Now()) {
		t.Fatal("解封后的凭据应可用")
	}
	// 缓存也要刷新：转发层读的是缓存，不清的话解封对转发无效
	if got := st.ListCredentials(1); !got[0].IsUsable(time.Now()) {
		t.Fatal("缓存里的凭据应已解封")
	}

	// 拉黑：状态变了，但 fail_count/cooldown_until 原样保留（复盘用）
	if err := st.DB().Model(&model.Credential{}).Where("id = ?", id).
		Updates(map[string]any{"fail_count": 5}).Error; err != nil {
		t.Fatalf("预制失败计数失败: %v", err)
	}
	banned, err := st.SetCredentialStatus(id, model.CredentialBanned)
	if err != nil {
		t.Fatalf("设 banned 失败: %v", err)
	}
	if banned.Status != model.CredentialBanned || banned.FailCount != 5 {
		t.Fatalf("拉黑不应动失败计数: %+v", banned)
	}
	if banned.IsUsable(time.Now()) {
		t.Fatal("banned 的凭据不应可用")
	}

	// 未知状态直接报错（拼错状态名会静默写坏数据，必须拦）
	if _, err := st.SetCredentialStatus(id, "activ"); err == nil {
		t.Fatal("未知状态应报错")
	}
	// 不存在的凭据 → ErrNotFound
	if _, err := st.SetCredentialStatus(9999, model.CredentialActive); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的凭据应返回 ErrNotFound, 实际 %v", err)
	}
}

// TestCredentialNotFoundPaths 验证操作不存在的凭据/渠道时的错误形态。
//
// 统一约定：不存在 → ErrNotFound（API 层映射成 404），
// 而不是 400 或 500——调用方要知道的是「你要改的东西不存在」，
// 这是客户端问题，不是服务端故障。
func TestCredentialNotFoundPaths(t *testing.T) {
	st := newCredTestStore(t)
	defer st.Close()

	if _, err := st.CreateCredential(9999, &CredentialInput{APIKey: "sk-x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("往不存在的渠道加凭据应 ErrNotFound, 实际 %v", err)
	}
	if _, err := st.UpdateCredential(9999, &CredentialInput{APIKey: "sk-x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在的凭据应 ErrNotFound, 实际 %v", err)
	}
	if err := st.DeleteCredential(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除不存在的凭据应 ErrNotFound, 实际 %v", err)
	}
	if _, err := st.GetCredential(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("读不存在的凭据应 ErrNotFound, 实际 %v", err)
	}
	// 不存在渠道的凭据列表返回空而不是报错：「没东西可列」不是错误
	if creds := st.ListCredentials(9999); len(creds) != 0 {
		t.Fatalf("不存在渠道的凭据列表应为空, 实际 %d", len(creds))
	}
}
func TestCredentialHealthCannotOverwriteManualStatus(t *testing.T) {
	st := newCredTestStore(t)
	defer st.Close()
	id := st.ListCredentials(1)[0].ID
	if _, err := st.SetCredentialStatus(id, model.CredentialDisabled); err != nil {
		t.Fatal(err)
	}
	err := st.UpdateCredentialHealth(HealthUpdate{CredentialID: id, Status: model.CredentialCooldown, FailCount: 2, CooldownUntil: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCredential(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.CredentialDisabled {
		t.Fatalf("stale health update overwrote manual disable: %+v", got)
	}
}

func TestCredentialHealthGenerationFencesManualEdits(t *testing.T) {
	st := newCredTestStore(t)
	defer st.Close()
	source := st.ListCredentials(1)[0]
	if _, err := st.SetCredentialStatus(source.ID, model.CredentialActive); err != nil {
		t.Fatal(err)
	}
	stale := HealthUpdate{CredentialID: source.ID, Status: model.CredentialCooldown, FailCount: 3, CooldownUntil: time.Now().Add(time.Hour).Unix(), ExpectedGeneration: source.HealthGeneration, ExpectedUpdatedAt: source.UpdatedAt}
	if err := st.UpdateCredentialHealth(stale); err != nil {
		t.Fatal(err)
	}
	current, _ := st.GetCredential(source.ID)
	if current.Status != model.CredentialActive || current.FailCount != 0 || current.HealthGeneration != source.HealthGeneration+1 {
		t.Fatalf("stale event crossed manual reset: %+v", current)
	}
	stale.ExpectedGeneration = current.HealthGeneration
	stale.ExpectedUpdatedAt = current.UpdatedAt
	if err := st.UpdateCredentialHealth(stale); err != nil {
		t.Fatal(err)
	}
	cooled, _ := st.GetCredential(source.ID)
	if cooled.Status != model.CredentialCooldown || cooled.FailCount != 3 {
		t.Fatalf("current event not applied: %+v", cooled)
	}
	stale.Status = model.CredentialActive
	stale.FailCount = 0
	if err := st.UpdateCredentialHealth(stale); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := st.GetCredential(source.ID)
	if unchanged.Status != model.CredentialCooldown {
		t.Fatal("stale update raced past compare-and-swap")
	}
}

func TestS3ConcurrentDeleteRetainsLastCredential(t *testing.T) {
	st := newCredTestStore(t)
	defer st.Close()
	first := st.ListCredentials(1)[0]
	second, err := st.CreateCredential(1, &CredentialInput{APIKey: "second"})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	var success atomic.Int64
	group.Add(2)
	for _, id := range []uint{first.ID, second.ID} {
		go func(id uint) {
			defer group.Done()
			if st.DeleteCredential(id) == nil {
				success.Add(1)
			}
		}(id)
	}
	group.Wait()
	if success.Load() != 1 {
		t.Fatalf("deleted=%d", success.Load())
	}
	var count int64
	st.DB().Model(&model.Credential{}).Where("channel_id=1").Count(&count)
	if count != 1 {
		t.Fatalf("concurrent deletes emptied channel: count=%d", count)
	}
}
