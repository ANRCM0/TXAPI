package store

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"minigateway/internal/model"
)

// 本文件是凭据（渠道下的一个上游 Key）的 CRUD。
//
// 与渠道 CRUD 的边界：渠道描述「上游在哪、支持什么模型」，
// 凭据描述「用哪个 Key 访问它」。两者分开维护后，加一个 Key 不再
// 需要重建渠道（那会顺带重置能力表和优先级），轮换 Key 的成本
// 从「改渠道」降到「加一条凭据」。

// ListCredentials 列出某渠道下的全部凭据。
//
// 从缓存读而不是查库：管理接口的读不该给 SQLite 加压，而每次写
// 之后都刷过缓存（见各写方法末尾的 reloadCache），一致性靠
// 「写后即刷」保证。渠道不存在时返回空列表而不是报错——「列出
// 不存在渠道的凭据」和「列出没有凭据的渠道」对调用方是同一个
// 意思：没有东西可列。
func (s *Store) ListCredentials(channelID uint) []model.Credential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.channels {
		if s.channels[i].ID == channelID {
			out := make([]model.Credential, len(s.channels[i].Credentials))
			copy(out, s.channels[i].Credentials)
			return out
		}
	}
	return nil
}

// CreateCredential 给渠道新增一个凭据。
//
// 与 CreateChannel 里的首个凭据不同，这里不碰渠道本身：models、
// priority、能力表都不受影响，只有凭据池变长。这正是把 Key 从
// 渠道里拆出来的目的。
func (s *Store) CreateCredential(channelID uint, in *CredentialInput) (*model.Credential, error) {
	if err := s.validateCredentialInput(channelID, in, 0); err != nil {
		return nil, err
	}
	cred := &model.Credential{
		ChannelID: channelID,
		Name:      in.Name,
		APIKey:    in.APIKey,
		Weight:    in.Weight,
		Status:    model.CredentialActive,
	}
	if err := s.db.Create(cred).Error; err != nil {
		return nil, fmt.Errorf("创建凭据失败: %w", err)
	}
	// 与渠道 CRUD 同样的纪律：写后即刷缓存。漏了这一步会出现
	// 「API 显示加成功了、转发还在用旧凭据池」的静默故障。
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	return cred, nil
}

// UpdateCredential 更新凭据的 name / api_key / weight。
//
// 为什么 status 不在可改范围里：状态是调度器的地盘（P3 会由
// cooldown/banned 逻辑驱动），让管理接口能顺手改会造成「调度器
// 刚拉黑的 Key 被一次 PUT 改回 active」的竞态。要人工干预状态走
// SetCredentialStatus，语义明确且留下操作痕迹。
func (s *Store) UpdateCredential(id uint, in *CredentialInput) (*model.Credential, error) {
	var existing model.Credential
	if err := s.db.First(&existing, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("凭据 %d: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("读取凭据 %d 失败: %w", id, err)
	}
	// excludeID 传自己：更新时 api_key 没变（等于自己的旧值）不能算重复
	if err := s.validateCredentialInput(existing.ChannelID, in, id); err != nil {
		return nil, err
	}
	updates := map[string]any{
		"name":    in.Name,
		"api_key": in.APIKey,
		"weight":  in.Weight,
	}
	if err := s.db.Model(&model.Credential{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("更新凭据失败: %w", err)
	}
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	return s.GetCredential(id)
}

// DeleteCredential 删除凭据。
//
// 拒绝删掉渠道的最后一个凭据：没有 Key 的渠道是空壳，路由还能命中它，
// 转发必然 401，而这种故障在列表里看不出异常（渠道状态还是
// enabled）。要停用整个渠道应该改渠道 status，语义更准确。
func (s *Store) DeleteCredential(id uint) error {
	var cred model.Credential
	if err := s.db.First(&cred, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("凭据 %d: %w", id, ErrNotFound)
		}
		return fmt.Errorf("读取凭据 %d 失败: %w", id, err)
	}
	var total int64
	if err := s.db.Model(&model.Credential{}).Where("channel_id = ?", cred.ChannelID).Count(&total).Error; err != nil {
		return fmt.Errorf("统计渠道 %d 的凭据数失败: %w", cred.ChannelID, err)
	}
	if total <= 1 {
		return fmt.Errorf("渠道 %d 只剩最后一个凭据，不能删除（否则渠道变成没有 Key 的空壳）；要停用渠道请改用 status=disabled", cred.ChannelID)
	}
	if err := s.db.Delete(&model.Credential{}, id).Error; err != nil {
		return fmt.Errorf("删除凭据失败: %w", err)
	}
	return s.reloadCache()
}

// SetCredentialStatus 手动设置凭据状态，用于「重置为 active」和「解除拉黑」。
//
// 为什么单独一个方法而不是复用 Update：这个操作要连带清理调度状态。
// 设回 active 时如果不清 cooldown_until 和 fail_count，凭据虽然状态是
// active 却仍被 IsUsable 判为冷却中——「解封了但没用」是最难排查的
// 一类故障。反过来设 banned 时保留这些字段，方便事后复盘失败历史。
func (s *Store) SetCredentialStatus(id uint, status string) (*model.Credential, error) {
	switch status {
	case model.CredentialActive, model.CredentialCooldown, model.CredentialBanned, model.CredentialDisabled:
	default:
		return nil, fmt.Errorf("未知的凭据状态 %q（可选: %s/%s/%s/%s）",
			status, model.CredentialActive, model.CredentialCooldown, model.CredentialBanned, model.CredentialDisabled)
	}
	updates := map[string]any{"status": status}
	if status == model.CredentialActive {
		// 人工解封 = 「这个 Key 我确认是好的」，调度状态一并清零，
		// 否则下次转发还会被旧的 cooldown_until 挡住
		updates["cooldown_until"] = 0
		updates["fail_count"] = 0
	}
	res := s.db.Model(&model.Credential{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return nil, fmt.Errorf("更新凭据状态失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("凭据 %d: %w", id, ErrNotFound)
	}
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	return s.GetCredential(id)
}

// GetCredential 按 ID 读单个凭据。
func (s *Store) GetCredential(id uint) (*model.Credential, error) {
	var cred model.Credential
	if err := s.db.First(&cred, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("凭据 %d: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("读取凭据 %d 失败: %w", id, err)
	}
	return &cred, nil
}

// HealthUpdate 是调度器算好、交给 store 落库的凭据健康状态。
//
// 与 model.Credential 的区别：只带调度器负责的那几个字段。用独立结构体
// 而不是直接收 *model.Credential，是为了让「调度器不拥有整条记录」
// 这件事在类型上就成立——它改不了 name/api_key/weight，那三个是管理
// 接口的地盘。
type HealthUpdate struct {
	CredentialID uint
	FailCount    int
	// CooldownUntil 是冷却截止时间，Unix 秒；0 表示不在冷却中。
	CooldownUntil int64
	Status        string
	LastError     string
	LastUsedAt    int64
}

// UpdateCredentialHealth 写入调度器算出的凭据健康状态，并刷新缓存。
//
// 这是 credentials 表 fail_count / cooldown_until / last_error 三个字段
// 的第一个（也是唯一一个）写入点——在 P6 批次 2 之前它们是死数据，
// 表建了、字段定义了、IsUsable 也读它们，但没有任何代码写过。
//
// 为什么保留 fail_count 而不是每次覆盖式写入：调度器传什么就写什么，
// 退避的连续性由调度器负责（见 scheduler.backoff），这里不做额外判断。
//
// 写后即刷缓存（与其它写方法同一纪律）：漏了这一步会出现「库里已经
// cooldown、转发还在用它」的窗口，那个窗口里每个请求都会重复踩同一个
// 坏 Key。调用方（scheduler 的 worker goroutine）随后会再调一次 Reload
// 把新状态发布到转发层。
func (s *Store) UpdateCredentialHealth(u HealthUpdate) error {
	if u.Status != model.CredentialActive &&
		u.Status != model.CredentialCooldown &&
		u.Status != model.CredentialBanned {
		return fmt.Errorf("凭据 %d: 非法的调度状态 %q（可选: %s/%s/%s）",
			u.CredentialID, u.Status,
			model.CredentialActive, model.CredentialCooldown, model.CredentialBanned)
	}
	updates := map[string]any{
		"fail_count":     u.FailCount,
		"cooldown_until": u.CooldownUntil,
		"status":         u.Status,
		"last_error":     u.LastError,
	}
	// last_used_at 只在真有时才写：调度器对「状态没变」的更新会跳过它
	// （见 scheduler.HealthUpdate.Changed），传 0 意味着「这次不动它」，
	// 而不是「把它清成 1970」——后者会让排查时看到一把从未用过的 Key。
	if u.LastUsedAt > 0 {
		updates["last_used_at"] = u.LastUsedAt
	}
	res := s.db.Model(&model.Credential{}).Where("id = ?", u.CredentialID).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("更新凭据 %d 健康状态失败: %w", u.CredentialID, res.Error)
	}
	if res.RowsAffected == 0 {
		// 凭据被删了（管理接口删 Key 和调度器写状态并发时会发生）。
		// 不算错误：记录没了，状态无处可写，本来就没有可做的。
		return nil
	}
	if err := s.reloadCache(); err != nil {
		return err
	}
	return nil
}

// RecoverCooledDownCredentials 把冷却已到期的凭据恢复成 active，返回恢复了几把。
//
// 查询条件刻意写成「status = cooldown 且 cooldown_until 已过」，三个条件
// 缺一不可：
//   - 只判 cooldown_until 会把 banned 的一起放行（拉黑后 cooldown_until
//     被清成 0，但 status 还是 banned）——那是自动解封，本方案明确禁止；
//   - 只判 status 会把设了未来冷却时间的凭据提前放行，退避失去意义；
//   - cooldown_until > 0 是防御：0 表示「不在冷却中」，一条 status 已经是
//     cooldown 但时间为 0 的记录是脏数据，交给它不如留给下次人工排查。
//
// 保留 fail_count：退避曲线要连续，见 scheduler.StartRecovery 的说明。
// 不返回被恢复的 ID 列表而是返回条数：调用方（后台任务）只需要知道
// 「有没有事发生」来决定要不要打日志。
func (s *Store) RecoverCooledDownCredentials() (int, error) {
	now := time.Now().Unix()
	res := s.db.Model(&model.Credential{}).
		Where("status = ? AND cooldown_until > 0 AND cooldown_until <= ?", model.CredentialCooldown, now).
		Updates(map[string]any{
			"status":         model.CredentialActive,
			"cooldown_until": 0,
		})
	if res.Error != nil {
		return 0, fmt.Errorf("恢复冷却到期的凭据失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return 0, nil
	}
	if err := s.reloadCache(); err != nil {
		return int(res.RowsAffected), err
	}
	return int(res.RowsAffected), nil
}

// validateCredentialInput 在落库前拦住不合理的凭据入参。
//
// excludeID 是「查重时排除的凭据 ID」：更新路径传自己的 ID，
// 这样 api_key 没变（值等于自己的旧值）不会被误判成重复；
// 创建路径传 0（ID 从 1 开始，0 匹配不到任何行）。
//
// 查重按渠道做，因为「同一把 Key 出现在两个不同渠道」是合法的
// （同一个 Key 给两个上游用的场景确实存在），只有同渠道内重复
// 才会让调度器把它当成两个独立凭据轮换，流量统计和故障定位全乱。
func (s *Store) validateCredentialInput(channelID uint, in *CredentialInput, excludeID uint) error {
	var n int64
	if err := s.db.Model(&model.Channel{}).Where("id = ?", channelID).Count(&n).Error; err != nil {
		return fmt.Errorf("检查渠道 %d 是否存在失败: %w", channelID, err)
	}
	if n == 0 {
		return fmt.Errorf("渠道 %d: %w", channelID, ErrNotFound)
	}
	if in.APIKey == "" {
		return errors.New("凭据 api_key 不能为空")
	}
	if in.Weight <= 0 {
		// 默认 1 而不是报错：调用方不关心权重时不该被迫传一个数，
		// 而 0 权重会让这个凭据永远选不中（比没有它还糟）
		in.Weight = 1
	}
	if in.Name == "" {
		in.Name = "cred"
	}
	var dup int64
	if err := s.db.Model(&model.Credential{}).
		Where("channel_id = ? AND api_key = ? AND id <> ?", channelID, in.APIKey, excludeID).
		Count(&dup).Error; err != nil {
		return fmt.Errorf("检查凭据重复失败: %w", err)
	}
	if dup > 0 {
		return fmt.Errorf("渠道 %d 下已存在相同的 api_key: %s", channelID, in.APIKey)
	}
	return nil
}
