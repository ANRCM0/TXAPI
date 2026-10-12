package store

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"minigateway/internal/model"
)

// ChannelInput 是创建/更新渠道时的入参。
//
// 为什么 API 层不直接收 model.Channel：model.Channel 带 GORM 的审计字段和
// 软删除字段，直接暴露会让调用方能改 created_at 这种不该改的东西。
// 用一个独立的入参结构体，边界更干净。
type ChannelInput struct {
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	BaseURL      string            `json:"base_url"`
	Models       []string          `json:"models"`
	ModelMapping map[string]string `json:"model_mapping"`
	GroupName    string            `json:"group_name"`
	Priority     int               `json:"priority"`
	Weight       int               `json:"weight"`
	Status       string            `json:"status"`
	// APIKey 是该渠道的第一个凭据。创建渠道时至少给一个 Key，
	// 否则这个渠道建出来也没法用（P3 会提供单独的凭据 CRUD）。
	APIKey string `json:"api_key"`
}

// CredentialInput 是创建/更新凭据时的入参。
type CredentialInput struct {
	Name   string `json:"name"`
	APIKey string `json:"api_key"`
	Weight int    `json:"weight"`
}

// validate 在落库前拦住不合理的渠道配置。
//
// 宁可在这里报错，也不要等转发时才发现 base_url 是空的——
// 那种错误的现场是「客户端拿到 500」，排查成本高得多。
func (in *ChannelInput) validate() error {
	if in.Name == "" {
		return errors.New("渠道名不能为空")
	}
	if in.BaseURL == "" {
		return fmt.Errorf("渠道 %s 缺少 base_url", in.Name)
	}
	if len(in.Models) == 0 {
		return fmt.Errorf("渠道 %s 没有配置任何模型", in.Name)
	}
	if in.Weight <= 0 {
		in.Weight = 1
	}
	if in.Type == "" {
		in.Type = "openai"
	}
	if in.Status == "" {
		in.Status = model.StatusEnabled
	}
	if in.GroupName == "" {
		in.GroupName = "default"
	}
	if in.Status != model.StatusEnabled && in.Status != model.StatusDisabled {
		return fmt.Errorf("未知的渠道状态 %q", in.Status)
	}
	return nil
}

// toModel 把入参转成实体。APIKey 单独处理，因为它属于凭据而不是渠道。
func (in *ChannelInput) toModel() *model.Channel {
	return &model.Channel{
		Name:         in.Name,
		Type:         in.Type,
		BaseURL:      in.BaseURL,
		Models:       model.StringList(in.Models),
		ModelMapping: model.StringMap(in.ModelMapping),
		GroupName:    in.GroupName,
		Priority:     in.Priority,
		Weight:       in.Weight,
		Status:       in.Status,
	}
}

// CreateChannel 新建渠道，并在同一事务里建好它的第一个凭据和能力表条目。
//
// 为什么要包在一个事务里：渠道、凭据、能力表三者必须一致。
// 半途失败留下「有渠道没凭据」的数据，转发时表现为莫名其妙的 401。
func (s *Store) CreateChannel(in *ChannelInput) (*model.Channel, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if in.APIKey == "" {
		return nil, fmt.Errorf("渠道 %s 至少需要一个 api_key", in.Name)
	}

	ch := in.toModel()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ch).Error; err != nil {
			return fmt.Errorf("创建渠道失败: %w", err)
		}
		cred := &model.Credential{
			ChannelID: ch.ID,
			Name:      "default",
			APIKey:    in.APIKey,
			Weight:    ch.Weight,
			Status:    model.CredentialActive,
		}
		if err := tx.Create(cred).Error; err != nil {
			return fmt.Errorf("创建渠道凭据失败: %w", err)
		}
		ch.Credentials = []model.Credential{*cred}
		// 能力表在同一个事务里同步，避免出现「渠道建好了但路由不到」
		return syncAbilities(tx, ch)
	})
	if err != nil {
		return nil, err
	}
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	return s.GetChannel(ch.ID)
}

// UpdateChannel 更新渠道。
//
// 关键点：能力表必须跟着重建。渠道改了 models 或 group_name 之后，
// 旧的能力表条目会让请求继续路由到已经不支持该模型的渠道上。
// 做法是先按 channel_id 删光旧条目再重建，而不是逐条 diff——
// 一个渠道的能力条目最多几十条，全删重建比维护 diff 逻辑简单且不易错。
func (s *Store) UpdateChannel(id uint, in *ChannelInput) (*model.Channel, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	existing, err := s.GetChannel(id)
	if err != nil {
		return nil, err
	}

	ch := in.toModel()
	ch.ID = id
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 用 Updates + map 而不是 Save：Save 会把零值字段也写进去，
		// 导致「只想改 weight」的请求把 priority 冲成 0。
		updates := map[string]any{
			"name":          ch.Name,
			"type":          ch.Type,
			"base_url":      ch.BaseURL,
			"models":        ch.Models,
			"model_mapping": ch.ModelMapping,
			"group_name":    ch.GroupName,
			"priority":      ch.Priority,
			"weight":        ch.Weight,
			"status":        ch.Status,
		}
		if err := tx.Model(&model.Channel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return fmt.Errorf("更新渠道失败: %w", err)
		}
		// 传了新的 api_key 就替换该渠道下的凭据池：自用场景下一个渠道一把 Key，
		// 多 Key 的精细管理是 P3 的事，这里先把单 Key 的更新路径打通。
		if in.APIKey != "" {
			if err := tx.Where("channel_id = ?", id).Delete(&model.Credential{}).Error; err != nil {
				return fmt.Errorf("清理旧凭据失败: %w", err)
			}
			cred := &model.Credential{
				ChannelID: id,
				Name:      "default",
				APIKey:    in.APIKey,
				Weight:    ch.Weight,
				Status:    model.CredentialActive,
			}
			if err := tx.Create(cred).Error; err != nil {
				return fmt.Errorf("创建新凭据失败: %w", err)
			}
			ch.Credentials = []model.Credential{*cred}
		} else {
			ch.Credentials = existing.Credentials
		}
		return syncAbilities(tx, ch)
	})
	if err != nil {
		return nil, err
	}
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	return s.GetChannel(id)
}

// DeleteChannel 删除渠道，连带删掉它的凭据和能力表条目。
//
// 用事务保证三者一起消失：漏掉能力表条目的后果最严重——
// 路由表里还留着这个渠道 ID，选渠道时拿到一个已删除的渠道，转发必然失败。
func (s *Store) DeleteChannel(id uint) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("channel_id = ?", id).Delete(&model.Credential{}).Error; err != nil {
			return fmt.Errorf("删除渠道凭据失败: %w", err)
		}
		if err := tx.Where("channel_id = ?", id).Delete(&model.Ability{}).Error; err != nil {
			return fmt.Errorf("删除能力表条目失败: %w", err)
		}
		res := tx.Delete(&model.Channel{}, id)
		if res.Error != nil {
			return fmt.Errorf("删除渠道失败: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("渠道 %d: %w", id, ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// 必须刷缓存：缓存是列表接口和转发热更新的数据源，
	// 漏了这一步会出现「库里删了、列表还显示、请求还能路由过去」的静默故障
	return s.reloadCache()
}

// GetChannel 按 ID 读渠道（含凭据池）。
func (s *Store) GetChannel(id uint) (*model.Channel, error) {
	var ch model.Channel
	err := s.db.Preload("Credentials").First(&ch, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("渠道 %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("读取渠道 %d 失败: %w", id, err)
	}
	return &ch, nil
}

// ListChannels 列出所有渠道。
//
// 这里读的是缓存而不是数据库：管理接口的读请求不该给 SQLite 加压，
// 而缓存已经在每次写操作后刷新过，数据一致性靠「写后即刷」保证。
func (s *Store) ListChannels() []model.Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Channel, len(s.channels))
	copy(out, s.channels)
	return out
}

// CountChannels 返回渠道总数，用于判断数据库是否为空（决定要不要导入种子数据）。
func (s *Store) CountChannels() (int64, error) {
	var n int64
	if err := s.db.Model(&model.Channel{}).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("统计渠道数失败: %w", err)
	}
	return n, nil
}

// syncAbilities 按渠道当前的 models 重建它在能力表里的全部条目。
//
// 能力表的作用是路由加速（PLAN 2.2）：选渠道时一次索引命中拿到候选列表，
// 不用扫描 channels 表再解析 JSON 模型列表。
//
// 通配符处理：渠道配了 "*" 时，能力表里存的就是 model="*" 这一条，
// 不展开成具体模型名——「这个渠道支持所有模型」这个语义只有 "*" 能表达，
// 展开反而会在新增模型时漏掉。
//
// enabled 列冗余了渠道的 status：路由时只查 enabled=true 的条目，
// 不必再回表 join channels 判断渠道是否被停用。
func syncAbilities(tx *gorm.DB, ch *model.Channel) error {
	if err := tx.Where("channel_id = ?", ch.ID).Delete(&model.Ability{}).Error; err != nil {
		return fmt.Errorf("清理渠道 %d 的旧能力表条目失败: %w", ch.ID, err)
	}
	if len(ch.Models) == 0 {
		return nil
	}
	enabled := ch.IsEnabled()
	seen := make(map[string]bool, len(ch.Models))
	rows := make([]*model.Ability, 0, len(ch.Models))
	for _, m := range ch.Models {
		// models 里重复写同一个模型会撞复合主键，去重后再插入
		if seen[m] {
			continue
		}
		seen[m] = true
		rows = append(rows, &model.Ability{
			GroupName: ch.GroupName,
			Model:     m,
			ChannelID: ch.ID,
			Enabled:   enabled,
			Priority:  ch.Priority,
			Weight:    ch.Weight,
		})
	}
	if err := tx.Create(&rows).Error; err != nil {
		return fmt.Errorf("写入渠道 %d 的能力表失败: %w", ch.ID, err)
	}
	return nil
}

// RebuildAllAbilities 全量重建能力表。
//
// 用途：种子导入后跑一次，把 YAML 里所有渠道一次性同步进能力表。
// 逐个渠道调 syncAbilities 也可以，但放在一个事务里更快也更一致。
func (s *Store) RebuildAllAbilities() error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM abilities").Error; err != nil {
			return fmt.Errorf("清空能力表失败: %w", err)
		}
		var channels []model.Channel
		if err := tx.Find(&channels).Error; err != nil {
			return fmt.Errorf("读取渠道失败: %w", err)
		}
		for i := range channels {
			if err := syncAbilities(tx, &channels[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// Abilities 返回能力表全部条目，供管理接口和调试用。
func (s *Store) Abilities() ([]model.Ability, error) {
	var rows []model.Ability
	if err := s.db.Order("model ASC, channel_id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取能力表失败: %w", err)
	}
	return rows, nil
}
