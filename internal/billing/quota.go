package billing

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrQuotaExceeded 表示额度不足。
//
// 调用方应回 403 且**不要**转发上游：超额的请求打到上游只会烧
// 真实 Key 的钱，而且注定要被拒——转发它没有任何收益（SPEC 4.2）。
var ErrQuotaExceeded = errors.New("额度不足")

// 额度所在的表名。用常量而不是拼接调用方传入的字符串：
// 表名不可能来自用户输入，但集中定义能保证「拼错表名」这类错误
// 在编译审视时就暴露，而不是运行时静默查错表。
const (
	tokensTable = "tokens"
	usersTable  = "users"
)

// quotaTarget 是一次额度操作的目标（令牌或用户）。
//
// 为什么抽象成「表名 + ID」而不是两个方法：SPEC 2.3 的额度是
// 「先扣令牌，再扣用户」的双层结构，两层的判断和扣减逻辑完全
// 同构，写成两份代码必然漂移（改了一边忘了另一边）。
type quotaTarget struct {
	table string
	id    uint
}

// Engine 是计费编排器：请求前预扣、响应后结算、价格查询。
//
// 持有 *gorm.DB 而不是 store.Store：额度扣减要的是「一条原子
// UPDATE」这个能力，整个 store 包对它来说太重；billing 也不该
// 依赖存储层的缓存实现。
type Engine struct {
	db     *gorm.DB
	prices *PriceTable
}

// New 创建计费引擎。prices 为 nil 时退化为「不查价、不计费」
// （测试或关闭计费的场景）。
func New(db *gorm.DB, prices *PriceTable) *Engine {
	return &Engine{db: db, prices: prices}
}

// Prices 暴露价格表，供管理接口在改价后主动失效缓存。
func (e *Engine) Prices() *PriceTable { return e.prices }

// quotaState 读目标的额度状态。
//
// 带 deleted_at IS NULL：tokens/users 都有软删除字段，默认查询
// 不过滤它们，但一个已删除的目标不该再被扣额度。
func (e *Engine) quotaState(t quotaTarget) (quota, used float64, err error) {
	var row struct {
		Quota     float64
		UsedQuota float64
	}
	err = e.db.Table(t.table).
		Select("quota, used_quota").
		Where("id = ? AND deleted_at IS NULL", t.id).
		Scan(&row).Error
	if err != nil {
		return 0, 0, fmt.Errorf("读取 %s %d 的额度失败: %w", t.table, t.id, err)
	}
	return row.Quota, row.UsedQuota, nil
}

// isLimited 判断目标是否设了额度。
//
// quota = 0 的语义是「不限额度」（沿用 P2 的约定，见 Token 注释）。
// 不限额度的目标不检查也不扣减——账本以 logs 表为准，used_quota
// 只在限额场景下有意义，这样能避免不限额度的令牌每次请求都写两次库。
func (e *Engine) isLimited(t quotaTarget) (bool, error) {
	quota, _, err := e.quotaState(t)
	if err != nil {
		return false, err
	}
	return quota > 0, nil
}

// guardedDeduct 带额度保护的原子扣减，返回是否真正扣到了。
//
// 为什么必须是一条 UPDATE 而不是「先 SELECT 再 UPDATE」：
// 并发下 100 个请求可能同时读到 used_quota=0，各自算出「没超额」，
// 然后各自写回 used_quota=各自的值——99 次扣减凭空消失
// （典型的 check-then-act 竞态）。把条件写进 WHERE 子句，让数据库
// 在一条语句内完成「判断 + 写入」，行锁天然串行化并发更新，
// RowsAffected=0 就说明这次扣减没抢到额度。
//
// 条件里 quota = 0 OR ... 是不限额目标的放行：调用方通常已经用
// isLimited 过滤，但防御一下，免得未来有人直接调它。
func (e *Engine) guardedDeduct(t quotaTarget, amount float64) (bool, error) {
	res := e.db.Exec(
		"UPDATE "+t.table+" SET used_quota = used_quota + ? "+
			"WHERE id = ? AND (quota = 0 OR used_quota + ? <= quota)",
		amount, t.id, amount)
	if res.Error != nil {
		return false, fmt.Errorf("扣减 %s %d 额度失败: %w", t.table, t.id, res.Error)
	}
	return res.RowsAffected > 0, nil
}

// addUsed 直接加 used_quota，不做额度保护。
//
// 用于结算补扣：请求已经成功响应过了，把真实费用记准比「防止
// 超额」重要——防止超额是请求前检查的职责，这里再拦一次只会
// 让账本永久少记（而且拦了也没用，钱已经花了）。
func (e *Engine) addUsed(t quotaTarget, amount float64) error {
	res := e.db.Exec("UPDATE "+t.table+" SET used_quota = used_quota + ? WHERE id = ?", amount, t.id)
	if res.Error != nil {
		return fmt.Errorf("补扣 %s %d 额度失败: %w", t.table, t.id, res.Error)
	}
	return nil
}

// refund 退回金额，且不允许把 used_quota 退成负数。
//
// 为什么要下限保护：浮点累计误差 + 并发退款可能让「退回」超过
// 「已扣」。退成负数的 used_quota 会让后续检查（used < quota）
// 永远通过，额度保护直接失效。
func (e *Engine) refund(t quotaTarget, amount float64) error {
	res := e.db.Exec(
		"UPDATE "+t.table+" SET used_quota = used_quota - ? "+
			"WHERE id = ? AND used_quota - ? >= 0",
		amount, t.id, amount)
	if res.Error != nil {
		return fmt.Errorf("退回 %s %d 额度失败: %w", t.table, t.id, res.Error)
	}
	return nil
}
