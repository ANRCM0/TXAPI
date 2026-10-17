package store

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"minigateway/internal/model"
)

// GroupRatesSetting 是「渠道分组计费倍率」在 settings 表里的键。
//
// 为什么用 settings KV 而不是单独的表：倍率是「分组 → 一个小数」的
// 映射，分组数量个位数，改一次管很久。为它建表 + 写 CRUD 的代价
// 远大于收益；JSON 存 KV 里，管理接口直接整体替换即可。
const GroupRatesSetting = "billing.group_rates"

// PriceInput 是创建/更新模型价格的入参。
//
// Model 不放在入参里：它来自路径参数（PUT /api/prices/:model），
// 让 body 和路径描述同一份数据的两半容易打架（对不上时以谁为准？）。
type PriceInput struct {
	InputPricePer1K  float64 `json:"input_price_per_1k"`
	OutputPricePer1K float64 `json:"output_price_per_1k"`
}

// Validate 拦负数单价。0 是合法的（免费模型/内部模型），负数没有意义。
//
// 导出是因为校验要发生在 api 层（HTTP 边界）：store 的校验是
// 「数据落库前的最后防线」，api 的校验是「把 400 拦在门外」，
// 两层都要有。
func (in *PriceInput) Validate() error {
	for _, price := range []float64{in.InputPricePer1K, in.OutputPricePer1K} {
		if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 || price > model.MaxPricePer1K {
			return fmt.Errorf("price must be finite and between 0 and 1000000000 per 1K tokens")
		}
	}
	return nil
}

// ModelPrices 列出全部模型价格，按模型名排序（列表接口要求顺序稳定）。
func (s *Store) ModelPrices() ([]model.ModelPrice, error) {
	var rows []model.ModelPrice
	if err := s.db.Order("model ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取价格表失败: %w", err)
	}
	return rows, nil
}

// UpsertModelPrice 写入/更新一个模型的单价（UPSERT）。
//
// 用主键冲突更新而不是「先查再写」：价格由管理接口维护，
// 先查再写在并发改价时会丢更新；UPSERT 一条语句搞定。
func (s *Store) UpsertModelPrice(p *model.ModelPrice) error {
	if p == nil || strings.TrimSpace(p.Model) == "" {
		return fmt.Errorf("price model is required")
	}
	in := PriceInput{InputPricePer1K: p.InputPricePer1K, OutputPricePer1K: p.OutputPricePer1K}
	if err := in.Validate(); err != nil {
		return err
	}
	if err := s.db.Clauses(upsertClause()).Create(p).Error; err != nil {
		return fmt.Errorf("写入模型 %s 的价格失败: %w", p.Model, err)
	}
	return nil
}

// DeleteModelPrice 删除一个模型的价格。删除后该模型按 0 计费并标记
// price_missing——与「从没配过价格」同等对待（SPEC 2.2：宁可漏计费）。
func (s *Store) DeleteModelPrice(modelName string) error {
	res := s.db.Delete(&model.ModelPrice{}, "model = ?", modelName)
	if res.Error != nil {
		return fmt.Errorf("删除模型 %s 的价格失败: %w", modelName, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("模型 %s: %w", modelName, ErrNotFound)
	}
	return nil
}

// GroupRates 读取渠道分组倍率表。JSON 形如 {"default": 1.0, "relay": 0.8}。
//
// 没配过返回空 map（调用方对未配置的分组按 1.0 计）。
// 解析失败返回错误而不是忽略：倍率是钱，静默用错倍数比报错危险。
func (s *Store) GroupRates() (map[string]float64, error) {
	raw, err := s.GetSetting(GroupRatesSetting)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return map[string]float64{}, nil
	}
	var rates map[string]float64
	if err := json.Unmarshal([]byte(raw), &rates); err != nil {
		return nil, fmt.Errorf("解析分组倍率失败（%s）: %w", GroupRatesSetting, err)
	}
	return rates, nil
}

// SetGroupRates 整体替换分组倍率表。
//
// 整体替换而不是按组合并：倍率表是个位数量级，「把整个表交给
// 调用方定义」比「合并语义」简单且不会留下幽灵分组
// （删掉某个分组是常见操作，按组合并做不到）。
func (s *Store) SetGroupRates(rates map[string]float64) error {
	for group, rate := range rates {
		if strings.TrimSpace(group) == "" || math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || rate > model.MaxGroupRate {
			return fmt.Errorf("group rate requires a nonempty name and a finite value in (0, 1000000]")
		}
	}
	encoded, err := json.Marshal(rates)
	if err != nil {
		return fmt.Errorf("序列化分组倍率失败: %w", err)
	}
	return s.SetSetting(GroupRatesSetting, string(encoded))
}

// SetTokenQuota 设置令牌的总额度（不动 used_quota）。
//
// 专门的端点而不是复用 PUT /api/tokens：改额度是高频操作
// （用完加一点），而复用整令牌更新要求调用方重新提交 key——
// key 一旦写错就被 UpdateToken 的「不允许改 key」校验拦下，
// 加额度反而变得很别扭。
func (s *Store) SetTokenQuota(id uint, quota float64) (*model.Token, error) {
	if quota < 0 {
		return nil, fmt.Errorf("额度不能为负数: %v", quota)
	}
	res := s.db.Model(&model.Token{}).Where("id = ?", id).Update("quota", quota)
	if res.Error != nil {
		return nil, fmt.Errorf("更新令牌 %d 额度失败: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("令牌 %d: %w", id, ErrNotFound)
	}
	// 刷缓存：令牌列表接口和鉴权都读缓存，漏了会出现
	// 「库里改了、列表还显示旧额度」的静默故障
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	return s.GetToken(id)
}
