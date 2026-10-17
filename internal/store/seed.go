package store

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/model"
)

// seedVersionSetting 记录种子数据导入时的配置版本，用于判断是否需要重新导入。
const seedVersionSetting = "seed.config_version"

// SeedFromConfig 在数据库为空时，把 YAML 里的渠道和令牌导入数据库。
//
// 为什么要这一步：P1 的配置全在 YAML 里，P2 把数据源换成数据库后，
// 老用户的 config.yaml 不能直接作废。这个函数就是那座桥——
// 第一次用新版本启动时自动把旧配置搬进数据库，之后一切以数据库为准。
//
// 幂等性：只在 channels 和 tokens 都为空时才导入。导入过一次之后，
// 即使 YAML 还在，也不会再动数据库——否则用户在界面上改过的渠道
// 会在下次重启时被 YAML 覆盖回去，这是最容易被投诉的行为。
func (s *Store) SeedFromConfig(cfg *config.Config) (imported bool, err error) {
	if cfg == nil || !cfg.HasSeedData() {
		return false, nil
	}
	nCh, err := s.CountChannels()
	if err != nil {
		return false, err
	}
	nTk, err := s.CountTokens()
	if err != nil {
		return false, err
	}
	if nCh > 0 || nTk > 0 {
		// 数据库非空，跳过导入。记一条日志说明原因，避免「我改了 YAML 怎么不生效」的困惑。
		//
		// 用 slog 而不是 fmt.Printf：这条日志和进程里其他日志一样要受
		// log_level 控制，fmt.Printf 会绕过级别过滤，在 info 级别下
		// 把种子导入的状态和请求摘要混在一起刷屏。
		slog.Info("[种子导入] 数据库非空，跳过 YAML 导入", "channels", nCh, "tokens", nTk)
		return false, nil
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		for _, cc := range cfg.Channels {
			if !cc.IsEnabled() {
				continue
			}
			ch := &model.Channel{
				ID:           uint(cc.ID),
				Name:         cc.Name,
				Type:         cc.Type,
				BaseURL:      cc.BaseURL,
				Models:       model.StringList(cc.Models),
				ModelMapping: model.StringMap(cc.Mapping),
				GroupName:    "default",
				Priority:     0,
				Weight:       cc.Weight,
				Status:       model.StatusEnabled,
			}
			if err := tx.Create(ch).Error; err != nil {
				return fmt.Errorf("导入渠道 %s 失败: %w", cc.Name, err)
			}
			// YAML 里的渠道 key 变成该渠道的第一个凭据：
			// P2 起「渠道」和「Key」是两个概念，直接过渡到新模型
			cred := &model.Credential{
				ChannelID: ch.ID,
				Name:      "default",
				APIKey:    cc.Key,
				Weight:    cc.Weight,
				Status:    model.CredentialActive,
			}
			if err := s.createCredential(tx, cred); err != nil {
				return fmt.Errorf("导入渠道 %s 的凭据失败: %w", cc.Name, err)
			}
			if err := syncAbilities(tx, ch); err != nil {
				return err
			}
		}

		for _, tc := range cfg.Tokens {
			policy := access.Legacy(tc.Models)
			tk := &model.Token{
				Name:      tc.Name,
				KeyHash:   access.Hash(tc.Key),
				KeyPrefix: keyPrefix(tc.Key),
				ModelMode: policy.Mode,
				Models:    model.StringList(policy.Models),
				Quota:     tc.Quota,
				RPMLimit:  tc.RPMLimit,
				TPMLimit:  tc.TPMLimit,
				Status:    model.TokenStatusActive,
			}
			if err := tx.Create(tk).Error; err != nil {
				return fmt.Errorf("导入令牌 %s 失败: %w", tc.Name, err)
			}
		}
		return nil
	})
	if err != nil {
		// 事务回滚后数据库仍然是空的，下次启动会重试导入，不会留下半截数据
		return false, err
	}
	if err := s.reloadCache(); err != nil {
		return false, err
	}
	slog.Info("[种子导入] 已从 YAML 导入种子数据",
		"channels", len(cfg.Channels), "tokens", len(cfg.Tokens))
	return true, nil
}
