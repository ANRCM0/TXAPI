package billing

import (
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"minigateway/internal/model"
)

// newTestDB 建一个内存 SQLite，跑真实 AutoMigrate。
//
// 为什么用真库而不是 mock：本文件要验证的核心正是「原子 UPDATE 的
// 并发行为」（RowsAffected、行锁串行化），mock 掉数据库等于把要测
// 的东西测没了。glebarez/sqlite 是纯 Go 驱动，测试不需要 cgo。
//
// 连接池限制为 1：SQLite 单写者，多连接只会互相抢锁；
// busy_timeout 留给极端时序兜底。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:quota_test?mode=memory&cache=shared&_pragma=busy_timeout(5000)"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Token{}, &model.User{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取连接池失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	return db
}

// seedKeySeq 保证同一个测试里建出的令牌 key 不重复
// （tokens.key 有唯一索引，复用 key 会直接撞约束）。
var seedKeySeq int

// seedToken 建一个指定额度的令牌，返回其 ID。
func seedToken(t *testing.T, db *gorm.DB, quota float64) uint {
	t.Helper()
	seedKeySeq++
	tk := &model.Token{
		Name:   t.Name(),
		Key:    t.Name() + "-" + string(rune('a'+seedKeySeq)),
		Quota:  quota,
		Status: model.TokenStatusActive,
	}
	if err := db.Create(tk).Error; err != nil {
		t.Fatalf("建令牌失败: %v", err)
	}
	return tk.ID
}

// tokenUsed 读令牌的已用额度。
func tokenUsed(t *testing.T, db *gorm.DB, id uint) float64 {
	t.Helper()
	var tk model.Token
	if err := db.First(&tk, id).Error; err != nil {
		t.Fatalf("读令牌失败: %v", err)
	}
	return tk.UsedQuota
}

// newTestEngine 建一个带固定价格的引擎（价格表不经存储层）。
func newTestEngine(db *gorm.DB) *Engine {
	return New(db, newTestTable([]model.ModelPrice{
		{Model: "gpt-4o-mini", InputPricePer1K: 0.15, OutputPricePer1K: 0.6},
	}, nil))
}

// TestQuotaExceeded 验证超额拒绝（SPEC 4.2）。
//
// 覆盖两条拒绝路径：
//  1. used_quota 已经 >= quota：显式检查拦截
//  2. 预扣金额 > 剩余额度：guardedDeduct 的 WHERE 条件拦截
//     （并发下这条才是权威判断）
func TestQuotaExceeded(t *testing.T) {
	db := newTestDB(t)
	eng := newTestEngine(db)

	// 路径 1：已用超额
	id1 := seedToken(t, db, 0.001)
	db.Model(&model.Token{}).Where("id = ?", id1).Update("used_quota", 0.002)
	_, err := eng.PreRequest(id1, 0, 500, "gpt-4o-mini")
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("已用超额应拒绝: 得到 %v", err)
	}

	// 路径 2：预扣金额（500×0.6/1k=0.3）> 剩余额度
	id2 := seedToken(t, db, 0.001)
	_, err = eng.PreRequest(id2, 0, 500, "gpt-4o-mini")
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("预扣超额应拒绝: 得到 %v", err)
	}
	// 被拒时不能留下任何扣减痕迹
	if used := tokenUsed(t, db, id2); used != 0 {
		t.Fatalf("被拒的请求不该扣额度: used=%v", used)
	}

	// 额度充足（但没给 max_tokens）：只检查不预扣，放行
	id3 := seedToken(t, db, 1.0)
	pre, err := eng.PreRequest(id3, 0, 0, "gpt-4o-mini")
	if err != nil {
		t.Fatalf("额度充足应放行: %v", err)
	}
	if pre.TokenAmount != 0 {
		t.Fatalf("无 max_tokens 不应预扣: %v", pre.TokenAmount)
	}
	if used := tokenUsed(t, db, id3); used != 0 {
		t.Fatalf("无 max_tokens 不应扣额度: used=%v", used)
	}
}

// TestPreDeductSettle 验证预扣 → 结算的差额处理（SPEC 4.3）。
//
// 关键断言：结算后 used_quota 等于**实际费用**，不是预扣值。
func TestPreDeductSettle(t *testing.T) {
	db := newTestDB(t)
	eng := newTestEngine(db)
	id := seedToken(t, db, 1.0)

	// 预扣：max_tokens=500 → 上限 0.3
	pre, err := eng.PreRequest(id, 0, 500, "gpt-4o-mini")
	if err != nil {
		t.Fatalf("预扣失败: %v", err)
	}
	if pre.TokenAmount != 0.3 {
		t.Fatalf("预扣金额错误: %v, 期望 0.3", pre.TokenAmount)
	}
	if used := tokenUsed(t, db, id); used != 0.3 {
		t.Fatalf("预扣后 used 应等于预扣值: %v", used)
	}

	// 结算：实际费用 0.00585（11 输入 / 7 输出，与 SPEC 4.1 同款）。
	// 浮点比较一律带容差：0.3-0.29415 这类运算在二进制里不精确，
	// 直接 == 会因为尾差（~1e-17）误报。真实金额的累计误差在
	// 1e-15 量级，不影响额度语义。
	actual := 0.00585
	if err := eng.Settle(id, 0, pre, actual); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	// 实际 < 预扣 → 退回差额 → used 等于实际值，不是预扣值
	if used := tokenUsed(t, db, id); math.Abs(used-actual) > 1e-9 {
		t.Fatalf("结算后 used 应等于实际费用: %v, 期望 %v", used, actual)
	}

	// 实际 > 预扣 → 补扣差额
	id2 := seedToken(t, db, 1.0)
	pre2, err := eng.PreRequest(id2, 0, 100, "gpt-4o-mini") // 上限 0.06
	if err != nil {
		t.Fatalf("预扣失败: %v", err)
	}
	if err := eng.Settle(id2, 0, pre2, 0.5); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	if used := tokenUsed(t, db, id2); math.Abs(used-0.5) > 1e-9 {
		t.Fatalf("补扣后 used 应等于实际费用: %v", used)
	}

	// 请求失败 → actual=0 → 全额退回预扣
	id3 := seedToken(t, db, 1.0)
	pre3, err := eng.PreRequest(id3, 0, 500, "gpt-4o-mini")
	if err != nil {
		t.Fatalf("预扣失败: %v", err)
	}
	if err := eng.Settle(id3, 0, pre3, 0); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	if used := tokenUsed(t, db, id3); used != 0 {
		t.Fatalf("失败请求应全额退回预扣: used=%v", used)
	}
}

// TestUnlimitedQuota 验证 quota=0（不限额度）的语义：
// 不检查、不扣减，used_quota 保持 0，账本以 logs 表为准。
func TestUnlimitedQuota(t *testing.T) {
	db := newTestDB(t)
	eng := newTestEngine(db)
	id := seedToken(t, db, 0)

	pre, err := eng.PreRequest(id, 0, 500, "gpt-4o-mini")
	if err != nil {
		t.Fatalf("不限额度应放行: %v", err)
	}
	if pre.TokenLimited || pre.TokenAmount != 0 {
		t.Fatalf("不限额度不该标记限额/扣减: %+v", pre)
	}
	if used := tokenUsed(t, db, id); used != 0 {
		t.Fatalf("不限额度不该写 used_quota: %v", used)
	}
	// 结算对不限额目标也是 no-op
	if err := eng.Settle(id, 0, pre, 999); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	if used := tokenUsed(t, db, id); used != 0 {
		t.Fatalf("不限额度结算后仍不该写 used_quota: %v", used)
	}
}

// TestConcurrentDeduct 验证并发扣减不丢失（SPEC 六：100 个 goroutine）。
//
// 场景：额度 0.5，每次扣 0.0078125（=2^-7）→ 恰好 64 次成功、
// 36 次失败，且 used_quota 恰好等于 0.5。读-改-写的实现会让最终值
// 对不上（丢失更新），原子 UPDATE 的 WHERE 守卫保证不丢。
//
// 为什么用 2 的幂次小数而不是 0.01：0.01 在二进制浮点里不精确，
// 累加到边界时 used+0.01<=0.5 会因尾差少放行一次（第一次跑这个
// 测试就栽在这）。真实金额的累计误差在 1e-15 量级、不影响额度
// 语义，但「恰好卡满额度」的断言需要精确可表示的数。
//
// 必须用 go test -race 跑：既验数据库语义，也验 Go 侧无数据竞争。
func TestConcurrentDeduct(t *testing.T) {
	db := newTestDB(t)
	eng := newTestEngine(db)
	id := seedToken(t, db, 0.5)

	const goroutines = 100
	const each = 0.0078125 // 2^-7，二进制精确表示

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ok, err := eng.guardedDeduct(quotaTarget{tokensTable, id}, each)
			if err != nil {
				t.Errorf("扣减出错: %v", err)
				return
			}
			if ok {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if success != 64 {
		t.Fatalf("并发扣减成功数错误: %d, 期望 64（0.5/0.0078125）", success)
	}
	used := tokenUsed(t, db, id)
	if math.Abs(used-0.5) > 1e-12 {
		t.Fatalf("并发扣减后 used_quota 错误: %v, 期望 0.5（一次都不能丢）", used)
	}
}

// TestConcurrentPreRequest 验证 Engine 层的并发预扣：限额 0.5、
// 每次预扣 0.3，最多只允许 1 个请求通过预扣。
//
// 这是 check-then-act 竞态的真实场景：如果「读 used」和「写 used」
// 是两条独立语句，多个 goroutine 会同时读到 used=0 并全部通过。
func TestConcurrentPreRequest(t *testing.T) {
	db := newTestDB(t)
	eng := newTestEngine(db)
	id := seedToken(t, db, 0.5)

	const goroutines = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		passed  int
		rejects int
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := eng.PreRequest(id, 0, 500, "gpt-4o-mini") // 每次预扣 0.3
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				passed++
			} else if errors.Is(err, ErrQuotaExceeded) {
				rejects++
			} else {
				t.Errorf("意外的错误类型: %v", err)
			}
		}()
	}
	wg.Wait()

	// 0.5 额度、每次 0.3：第一个扣完剩 0.2，第二个的守卫
	// （0.2 + 0.3 <= 0.5 不成立）必须被拒
	if passed != 1 || rejects != goroutines-1 {
		t.Fatalf("并发预扣结果错误: passed=%d rejects=%d, 期望 1/%d", passed, rejects, goroutines-1)
	}
	if used := tokenUsed(t, db, id); used != 0.3 {
		t.Fatalf("并发预扣后 used_quota 错误: %v, 期望 0.3", used)
	}
}
