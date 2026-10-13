package billing

import (
	"fmt"

	relaymodel "minigateway/internal/relay/model"
)

// PreDeduct 是请求前额度处理的快照，响应后原样传给 Settle。
//
// 为什么需要这个结构体而不是只传一个金额：结算时必须知道
// 「哪些目标在请求前真的被扣过」。额度不限（quota=0）的目标
// 请求前没扣，结算时也不该动它的 used_quota——否则「不限额度」
// 的令牌每个请求都要多写两次库，白白给 SQLite 的单写者加压。
type PreDeduct struct {
	// TokenAmount / UserAmount 是各目标实际预扣的金额。
	TokenAmount float64
	UserAmount  float64
	// TokenLimited / UserLimited 记录各目标是否设了额度。
	TokenLimited bool
	UserLimited  bool
}

// PreRequest 在转发前做两件事：检查额度、预扣一个费用上限。
//
// 这是「预扣+结算」方案（SPEC 2.3 推荐）的前半段：
//  1. 限额目标先查 used_quota < quota，超了直接 ErrQuotaExceeded
//  2. 再按 max_tokens × 输出单价预扣一个上限（guardedDeduct 原子完成
//     「判断+扣减」，并发下不会两个请求同时扣穿额度）
//
// maxTokens <= 0（客户端没给）时不预扣：猜一个默认值会把小额度
// 令牌全部误拒，违背「预扣要保守，宁可少扣」的原则；检查仍然执行，
// 结算按实际费用补扣。
func (e *Engine) PreRequest(tokenID, userID uint, maxTokens int, modelName string) (*PreDeduct, error) {
	pre := &PreDeduct{}
	// 预扣上限只按输出价估（见 PriceTable.EstimateUpperBound 的注释），
	// 且不含倍率：预扣在选渠道之前，还不知道走哪个分组。
	amount := 0.0
	if e.prices != nil {
		amount = e.prices.EstimateUpperBound(maxTokens, modelName)
	}

	if tokenID != 0 {
		limited, deducted, err := e.checkAndPreDeduct(quotaTarget{tokensTable, tokenID}, amount)
		if err != nil {
			return pre, err
		}
		pre.TokenLimited = limited
		pre.TokenAmount = deducted
	}

	if userID != 0 {
		limited, deducted, err := e.checkAndPreDeduct(quotaTarget{usersTable, userID}, amount)
		if err != nil {
			// 用户侧失败时把令牌侧的预扣退回去：账本两边必须同步。
			// 只回一半的状态（令牌扣了、用户没扣）会让两边永久对不上，
			// 且令牌额度被白占。
			if pre.TokenAmount > 0 {
				if rerr := e.refund(quotaTarget{tokensTable, tokenID}, pre.TokenAmount); rerr != nil {
					// 退款失败只能告警：请求反正要被拒，不能因为
					// 退款失败把错误吞掉换成 500
					return pre, fmt.Errorf("%v（退回令牌预扣也失败: %v）", err, rerr)
				}
				pre.TokenAmount = 0
			}
			return pre, err
		}
		pre.UserLimited = limited
		pre.UserAmount = deducted
	}
	return pre, nil
}

// checkAndPreDeduct 对一个目标做「检查 + 预扣」。
//
// 返回 (是否限额, 实际预扣金额, 错误)。额度不足时返回包装了
// ErrQuotaExceeded 的错误，调用方用 errors.Is 判断。
func (e *Engine) checkAndPreDeduct(t quotaTarget, amount float64) (limited bool, deducted float64, err error) {
	quota, used, err := e.quotaState(t)
	if err != nil {
		return false, 0, err
	}
	if quota <= 0 {
		// 不限额度：不检查不扣减
		return false, 0, nil
	}
	limited = true
	// 先显式检查：amount 可能为 0（没价格/没 max_tokens），
	// 这时扣减路径根本不会执行，检查不能省
	if used >= quota {
		return true, 0, fmt.Errorf("%s %d %w（已用 %.6f / 总额 %.6f）", t.table, t.id, ErrQuotaExceeded, used, quota)
	}
	if amount <= 0 {
		return true, 0, nil
	}
	ok, err := e.guardedDeduct(t, amount)
	if err != nil {
		return true, 0, err
	}
	if !ok {
		// 并发抢额度失败：另一个请求刚刚把额度扣穿了。
		// used >= quota 的检查通过不代表扣得到——这正是
		// 「把判断放进 UPDATE 的 WHERE」要解决的窗口。
		return true, 0, fmt.Errorf("%s %d %w（并发扣减未抢到额度）", t.table, t.id, ErrQuotaExceeded)
	}
	return true, amount, nil
}

// Settle 响应后结算：按实际费用修正预扣差额。
//
//	实际 > 预扣 → 补扣差额（addUsed，不加额度保护）
//	实际 < 预扣 → 退回差额（refund，有负数保护）
//	请求失败   → actual 传 0，即全额退回预扣
//
// 无论成败都要结清：预扣了钱不结账，令牌额度会被永久占用
// （表现为「没用几次就被 403」）。
func (e *Engine) Settle(tokenID, userID uint, pre *PreDeduct, actual float64) error {
	if pre == nil {
		return nil
	}
	if pre.TokenLimited && tokenID != 0 {
		if err := e.adjust(quotaTarget{tokensTable, tokenID}, actual-pre.TokenAmount); err != nil {
			return err
		}
	}
	if pre.UserLimited && userID != 0 {
		if err := e.adjust(quotaTarget{usersTable, userID}, actual-pre.UserAmount); err != nil {
			return err
		}
	}
	return nil
}

// adjust 结算单个目标的差额：正数补扣，负数退回。
func (e *Engine) adjust(t quotaTarget, delta float64) error {
	if delta == 0 {
		return nil
	}
	if delta > 0 {
		return e.addUsed(t, delta)
	}
	return e.refund(t, -delta)
}

// PriceOf 按用量和模型算费。
//
// 查价顺序：上游真实模型名（映射后的 ActualModel）优先，找不到再
// 按客户端请求名（OriginModel）查一次——模型映射会让两个名字不同，
// 价格表配哪个都不该漏计费。
//
// 返回 (费用, 是否找到价格)。找不到时费用为 0，调用方应把
// price_missing 标记进日志（SPEC 4.5：不报错，正常转发）。
func (e *Engine) PriceOf(u *relaymodel.Usage, actualModel, originModel, group string) (float64, bool) {
	if e.prices == nil || u == nil {
		return 0, false
	}
	if cost, ok := e.prices.Cost(u, actualModel, group); ok {
		return cost, true
	}
	if actualModel != originModel {
		if cost, ok := e.prices.Cost(u, originModel, group); ok {
			return cost, true
		}
	}
	return 0, false
}
