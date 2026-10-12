package store

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"minigateway/internal/model"
)

// GetSetting 读取一个系统设置，不存在时返回空字符串（不报错）。
func (s *Store) GetSetting(key string) (string, error) {
	var st model.Setting
	err := s.db.First(&st, "key = ?", key).Error
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("读取设置 %s 失败: %w", key, err)
	}
	return st.Value, nil
}

// SetSetting 写入一个系统设置（存在则覆盖）。
func (s *Store) SetSetting(key, value string) error {
	st := model.Setting{Key: key, Value: value}
	// OnConflict 走 SQLite 的 UPSERT，避免「先查再写」的竞态
	if err := s.db.Clauses(upsertClause()).Create(&st).Error; err != nil {
		return fmt.Errorf("写入设置 %s 失败: %w", key, err)
	}
	return nil
}

// ListSettings 列出全部设置。
func (s *Store) ListSettings() ([]model.Setting, error) {
	var rows []model.Setting
	if err := s.db.Order("key ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取设置失败: %w", err)
	}
	return rows, nil
}

// upsertClause 返回「主键冲突时更新全部字段」的 UPSERT 子句。
//
// 抽成函数是因为 settings 和 model_prices 两处都要用，语义完全一样。
// 冲突目标交给 GORM 按主键推断：两张表的主键都是单列且明确。
func upsertClause() clause.OnConflict {
	return clause.OnConflict{
		UpdateAll: true,
	}
}

// isNotFound 统一判断 GORM 的「记录不存在」。
func isNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}

// ModelPrices 列出全部模型价格。
func (s *Store) ModelPrices() ([]model.ModelPrice, error) {
	var rows []model.ModelPrice
	if err := s.db.Order("model ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取价格表失败: %w", err)
	}
	return rows, nil
}

// UpsertModelPrice 写入/更新一个模型的单价。
func (s *Store) UpsertModelPrice(p *model.ModelPrice) error {
	if err := s.db.Clauses(upsertClause()).Create(p).Error; err != nil {
		return fmt.Errorf("写入模型 %s 的价格失败: %w", p.Model, err)
	}
	return nil
}

// Ping 探活，供健康检查用。
func (s *Store) Ping() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("获取连接池失败: %w", err)
	}
	return sqlDB.Ping()
}
