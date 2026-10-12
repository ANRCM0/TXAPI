package store

import (
	"path/filepath"
	"testing"
	"time"

	"minigateway/internal/config"
	"minigateway/internal/model"
)

// openTestStore 建一个用临时文件的 Store。
//
// 用文件而不是 :memory:：本文件要验证「Close 时 flush」——
// 关掉之后要重新打开同一个库文件来数日志，内存库一关就没了。
func openTestStore(t *testing.T, name string) *Store {
	t.Helper()
	st, err := Open(config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), name+".db"),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	return st
}

// waitForLogs 轮询等待日志数达到 n。
//
// 日志是异步写入的（见 asyncLogWriter），刚写完请求就查库可能还没
// 落盘——这正是异步语义，不是 bug。测试等它落，而不是绕过异步。
func waitForLogs(t *testing.T, st *Store, n int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, err := st.CountLogs()
		if err != nil {
			t.Fatalf("数日志失败: %v", err)
		}
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := st.CountLogs()
	t.Fatalf("等待日志落库超时: 期望 %d 条, 实际 %d 条", n, got)
}

// TestAsyncLogFlushOnClose 验证优雅关闭不丢日志（SPEC 2.4 的
// 「进程退出时要 flush 未写入的日志」）。
//
// 手法：立刻写 50 条、立刻 Close（赶在 200ms 的批次 ticker 之前），
// 然后用**新的 Store 重新打开同一个库文件**数日志——异步队列里的
// 积压必须被 Close 排空落库，否则这里只能数到 0。
func TestAsyncLogFlushOnClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flush.db")

	st, err := Open(config.DatabaseConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	const n = 50
	for i := 0; i < n; i++ {
		st.WriteLog(&model.RequestLog{
			RequestID:    "flush-test",
			OriginModel:  "gpt-4o-mini",
			PromptTokens: 11,
			Cost:         0.00585,
		})
	}
	// 不 sleep：故意让日志全部堆在 channel 里，验证 Close 的 flush
	if err := st.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	// 重新打开同一个库文件来数——上一个 Store 已经关闭，
	// 只能换个连接查
	st2, err := Open(config.DatabaseConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatalf("重开测试库失败: %v", err)
	}
	defer st2.Close()
	got, err := st2.CountLogs()
	if err != nil {
		t.Fatalf("数日志失败: %v", err)
	}
	if got != n {
		t.Fatalf("优雅关闭后日志丢失: 期望 %d 条, 实际 %d 条", n, got)
	}
}

// TestLogQueryFilters 验证日志查询的过滤和分页。
func TestLogQueryFilters(t *testing.T) {
	st := openTestStore(t, "query")
	defer st.Close()

	// 直接走 DB 同步插（WriteLog 是异步的，查询测试要可控的时序）
	rows := []model.RequestLog{
		{RequestID: "r1", TokenID: 1, ChannelID: 10, OriginModel: "gpt-4o-mini", Cost: 1},
		{RequestID: "r2", TokenID: 1, ChannelID: 20, OriginModel: "gpt-4o", Cost: 2},
		{RequestID: "r3", TokenID: 2, ChannelID: 10, OriginModel: "gpt-4o-mini", Cost: 3},
	}
	for i := range rows {
		if err := st.DB().Create(&rows[i]).Error; err != nil {
			t.Fatalf("插入日志失败: %v", err)
		}
	}

	// 按 token 过滤
	logs, err := st.ListLogs(LogQuery{TokenID: 1})
	if err != nil || len(logs) != 2 {
		t.Fatalf("按 token 过滤: 得到 %d 条, 期望 2 (err=%v)", len(logs), err)
	}
	// 新的在前
	if logs[0].RequestID != "r2" {
		t.Fatalf("排序错误: 第一条应是 r2, 实际 %s", logs[0].RequestID)
	}

	// 按渠道 + 模型组合过滤
	logs, err = st.ListLogs(LogQuery{ChannelID: 10, Model: "gpt-4o-mini"})
	if err != nil || len(logs) != 2 {
		t.Fatalf("组合过滤: 得到 %d 条, 期望 2 (err=%v)", len(logs), err)
	}

	// 分页：limit=1&offset=1 → 跳过最新的 r3，拿到 r2
	logs, err = st.ListLogs(LogQuery{Limit: 1, Offset: 1})
	if err != nil || len(logs) != 1 || logs[0].RequestID != "r2" {
		t.Fatalf("分页: 得到 %+v (err=%v)", logs, err)
	}

	// limit 超上限被截断到 MaxLogLimit（查询本身要成功）
	logs, err = st.ListLogs(LogQuery{Limit: 99999})
	if err != nil || len(logs) != 3 {
		t.Fatalf("超大 limit 应截断: 得到 %d 条 (err=%v)", len(logs), err)
	}
}

// TestLogStats 验证用量统计的三条聚合查询。
func TestLogStats(t *testing.T) {
	st := openTestStore(t, "stats")
	defer st.Close()

	now := time.Now()
	rows := []model.RequestLog{
		{RequestID: "s1", ChannelID: 1, OriginModel: "gpt-4o-mini", PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18, Cost: 0.00585, Status: 200, LatencyMs: 100, CreatedAt: now},
		{RequestID: "s2", ChannelID: 1, OriginModel: "gpt-4o-mini", PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18, Cost: 0.00585, Status: 200, LatencyMs: 200, CreatedAt: now},
		{RequestID: "s3", ChannelID: 2, OriginModel: "gpt-4o", PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, Cost: 1.5, Status: 429, LatencyMs: 50, CreatedAt: now},
		// 一条超出统计窗口的旧数据（应被 range=7d 排除）
		{RequestID: "old", ChannelID: 1, OriginModel: "gpt-4o-mini", Cost: 100, Status: 200, CreatedAt: now.Add(-30 * 24 * time.Hour)},
	}
	for i := range rows {
		if err := st.DB().Create(&rows[i]).Error; err != nil {
			t.Fatalf("插入日志失败: %v", err)
		}
	}

	since, err := ParseSince("7d", now)
	if err != nil {
		t.Fatalf("解析 range 失败: %v", err)
	}

	summary, err := st.LogSummary(since)
	if err != nil {
		t.Fatalf("汇总失败: %v", err)
	}
	if summary.TotalRequests != 3 || summary.SuccessRequests != 2 || summary.ErrorRequests != 1 {
		t.Fatalf("请求数统计错误: %+v", summary)
	}
	if summary.TotalTokens != 186 { // 18+18+150
		t.Fatalf("token 统计错误: %d", summary.TotalTokens)
	}
	if summary.TotalCost < 1.5117-1e-9 || summary.TotalCost > 1.5117+1e-9 { // 0.00585×2+1.5
		t.Fatalf("费用统计错误: %v", summary.TotalCost)
	}

	byModel, err := st.StatsByModel(since)
	if err != nil || len(byModel) != 2 {
		t.Fatalf("按模型统计: 得到 %d 组 (err=%v)", len(byModel), err)
	}
	// 按费用降序：gpt-4o(1.5) 应在 gpt-4o-mini(0.0117) 之前
	if byModel[0].Model != "gpt-4o" {
		t.Fatalf("按模型排序错误: %+v", byModel)
	}

	byChannel, err := st.StatsByChannel(since)
	if err != nil || len(byChannel) != 2 {
		t.Fatalf("按渠道统计: 得到 %d 组 (err=%v)", len(byChannel), err)
	}
	if byChannel[0].ChannelID != 2 { // 渠道 2 花了 1.5
		t.Fatalf("按渠道排序错误: %+v", byChannel)
	}

	// range=all 不过滤时间：旧数据也进来
	all, err := st.LogSummary(time.Time{})
	if err != nil {
		t.Fatalf("全量汇总失败: %v", err)
	}
	if all.TotalRequests != 4 {
		t.Fatalf("all 范围应含旧数据: %+v", all)
	}
}

// TestParseSince 验证 range 参数解析。
func TestParseSince(t *testing.T) {
	now := time.Now()
	if s, err := ParseSince("24h", now); err != nil || !s.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("24h 解析错误: %v %v", s, err)
	}
	if s, err := ParseSince("", now); err != nil || !s.Equal(now.Add(-7*24*time.Hour)) {
		t.Fatalf("默认 7d 解析错误: %v %v", s, err)
	}
	if s, err := ParseSince("all", now); err != nil || !s.IsZero() {
		t.Fatalf("all 应返回零值时间: %v %v", s, err)
	}
	if _, err := ParseSince("abc", now); err == nil {
		t.Fatal("非法 range 应报错")
	}
	if _, err := ParseSince("7x", now); err == nil {
		t.Fatal("非法单位应报错")
	}
}

// TestGroupRates 验证分组倍率的读写（计费倍率的配置面）。
func TestGroupRates(t *testing.T) {
	st := openTestStore(t, "rates")
	defer st.Close()

	// 没配过 → 空 map，调用方按 1.0 计
	rates, err := st.GroupRates()
	if err != nil || len(rates) != 0 {
		t.Fatalf("初始倍率应为空: %v %v", rates, err)
	}

	if err := st.SetGroupRates(map[string]float64{"default": 1.0, "relay": 0.8}); err != nil {
		t.Fatalf("写倍率失败: %v", err)
	}
	rates, err = st.GroupRates()
	if err != nil || rates["relay"] != 0.8 {
		t.Fatalf("读倍率错误: %v %v", rates, err)
	}

	// 非正倍率被拦：倍率是钱，静默用错比报错危险
	if err := st.SetGroupRates(map[string]float64{"relay": -1}); err == nil {
		t.Fatal("负倍率应报错")
	}
}

// TestTokenQuota 验证令牌额度的专门更新端点。
func TestTokenQuota(t *testing.T) {
	st := openTestStore(t, "quota")
	defer st.Close()

	tk, err := st.CreateToken(&TokenInput{Key: "sk-q", Name: "q"})
	if err != nil {
		t.Fatalf("建令牌失败: %v", err)
	}
	updated, err := st.SetTokenQuota(tk.ID, 0.5)
	if err != nil {
		t.Fatalf("设额度失败: %v", err)
	}
	if updated.Quota != 0.5 {
		t.Fatalf("额度没写进去: %v", updated.Quota)
	}
	// used_quota 不能被动到
	if updated.UsedQuota != 0 {
		t.Fatalf("used_quota 被误改: %v", updated.UsedQuota)
	}
	// 不存在的令牌 → ErrNotFound
	if _, err := st.SetTokenQuota(9999, 1); err == nil {
		t.Fatal("不存在的令牌应报错")
	}
	// 负数额度被拦
	if _, err := st.SetTokenQuota(tk.ID, -1); err == nil {
		t.Fatal("负额度应报错")
	}
}
