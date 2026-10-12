package store

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"minigateway/internal/model"
)

// TokenInput 是创建/更新令牌的入参。
type TokenInput struct {
	Name   string   `json:"name"`
	Key    string   `json:"key"`
	Models []string `json:"models"`
	// ExpiresInSeconds 是「多少秒后过期」，0 表示永不过期。
	// 用相对值而不是绝对时间戳：调用方不用关心服务器时区，也更容易表达「30 天有效」。
	ExpiresInSeconds int64   `json:"expires_in_seconds"`
	Quota            float64 `json:"quota"`
}

// validate 填充默认值并校验。传进来的 in 会被就地修改。
func (in *TokenInput) validate(now time.Time) error {
	if in.Key == "" {
		return errors.New("令牌 key 不能为空")
	}
	if in.Models == nil {
		in.Models = []string{}
	}
	if in.ExpiresInSeconds < 0 {
		return errors.New("expires_in_seconds 不能为负数")
	}
	return nil
}

// toModel 把入参转成实体。expires_at 在这里才算出来，保持「相对时长」的语义。
func (in *TokenInput) toModel(now time.Time) *model.Token {
	var expiresAt int64
	if in.ExpiresInSeconds > 0 {
		expiresAt = now.Add(time.Duration(in.ExpiresInSeconds) * time.Second).Unix()
	}
	return &model.Token{
		Name:      in.Name,
		Key:       in.Key,
		Models:    model.StringList(in.Models),
		Quota:     in.Quota,
		ExpiresAt: expiresAt,
		Status:    model.TokenStatusActive,
	}
}

// CreateToken 新建令牌。
func (s *Store) CreateToken(in *TokenInput) (*model.Token, error) {
	now := time.Now()
	if err := in.validate(now); err != nil {
		return nil, err
	}
	tk := in.toModel(now)
	// 先查重再插入：SQLite 的唯一索引当然也会拦住重复 key，
	// 但那时报的是底层约束错误，调用方拿到的是一串看不懂的 SQL 约束名。
	//
	// 注意 Unscoped()：Token 带软删除字段，默认查询会过滤掉已删除的行。
	// 但唯一索引对软删除的行**仍然生效**——不查它们的话，预检通不过、
	// 真正插入时才被数据库拦下，预检就白做了。
	var dup int64
	if err := s.db.Unscoped().Model(&model.Token{}).Where("key = ?", in.Key).Count(&dup).Error; err != nil {
		return nil, fmt.Errorf("检查令牌重复失败: %w", err)
	}
	if dup > 0 {
		return nil, fmt.Errorf("令牌 key 已存在: %s", in.Key)
	}
	if err := s.db.Create(tk).Error; err != nil {
		return nil, fmt.Errorf("创建令牌失败: %w", err)
	}
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	return tk, nil
}

// UpdateToken 更新令牌。
//
// key 不允许改：令牌 key 是客户端写死在配置里的，改了等于所有客户端集体失效。
// 真要换 key 就删旧建新。
func (s *Store) UpdateToken(id uint, in *TokenInput) (*model.Token, error) {
	now := time.Now()
	if err := in.validate(now); err != nil {
		return nil, err
	}
	var tk model.Token
	if err := s.db.First(&tk, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("令牌 %d: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("读取令牌 %d 失败: %w", id, err)
	}
	if in.Key != tk.Key {
		return nil, fmt.Errorf("不允许修改令牌 key（原 key=%s）；如需更换请删除后重建", tk.Key)
	}

	updated := in.toModel(now)
	updates := map[string]any{
		"name":       updated.Name,
		"models":     updated.Models,
		"quota":      updated.Quota,
		"expires_at": updated.ExpiresAt,
	}
	if err := s.db.Model(&model.Token{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("更新令牌失败: %w", err)
	}
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	return s.GetToken(id)
}

// DeleteToken 吊销令牌（物理删除）。
func (s *Store) DeleteToken(id uint) error {
	res := s.db.Delete(&model.Token{}, id)
	if res.Error != nil {
		return fmt.Errorf("删除令牌失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("令牌 %d: %w", id, ErrNotFound)
	}
	return s.reloadCache()
}

// GetToken 按 ID 读令牌。
func (s *Store) GetToken(id uint) (*model.Token, error) {
	var tk model.Token
	if err := s.db.First(&tk, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("令牌 %d: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("读取令牌 %d 失败: %w", id, err)
	}
	return &tk, nil
}

// ListTokens 列出所有令牌（从缓存读）。
func (s *Store) ListTokens() []model.Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Token, 0, len(s.tokens))
	for _, t := range s.tokens {
		out = append(out, *t)
	}
	// 按 ID 排序：遍历 map 的顺序是随机的，不排序的话同样的请求
	// 两次返回不同顺序，前端 diff 会抖
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CountTokens 返回令牌总数。
func (s *Store) CountTokens() (int64, error) {
	var n int64
	if err := s.db.Model(&model.Token{}).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("统计令牌数失败: %w", err)
	}
	return n, nil
}
