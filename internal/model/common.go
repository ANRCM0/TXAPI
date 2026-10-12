// Package model 定义网关的全部持久化实体（GORM 模型）。
//
// 分层位置：这是唯一同时被「存储层 store」和「转发层 relay」引用的包。
// 它只描述数据结构，不含任何业务逻辑——所有「怎么算」的逻辑都在 store 里，
// 所有「怎么用」的逻辑都在 relay 里。
//
// 表结构依据 docs/PLAN.md「2.2 表结构」。渠道与凭据分离（一个渠道挂多个 Key）
// 是本项目区别于 One API 的核心设计，credentials 表的 channel_id 外键就是它的落点。
package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// StringList 是一个以 JSON 文本存储的字符串数组列。
//
// 为什么不建子表：模型列表、令牌模型白名单这类字段，读取时永远整体加载
// （路由时要把整个列表拿来匹配），拆成子表只会让每次读渠道多几次 JOIN；
// 而写路径本来就低频，不构成瓶颈。SQLite 自带 json1 扩展，必要时还能直接
// 对这些列做 JSON 查询。
type StringList []string

// Value 实现 driver.Valuer，写入时序列化成 JSON 文本。
// nil 统一写成 "[]" 而不是 NULL：上层代码拿到的永远是数组，不用到处判空。
func (s StringList) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	b, err := json.Marshal([]string(s))
	if err != nil {
		return nil, fmt.Errorf("序列化字符串列表失败: %w", err)
	}
	return string(b), nil
}

// Scan 实现 sql.Scanner，读出时把 JSON 文本还原成数组。
func (s *StringList) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return fmt.Errorf("StringList 无法识别的数据库类型 %T", src)
	}
	if len(raw) == 0 {
		*s = nil
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("反序列化字符串列表失败: %w", err)
	}
	*s = out
	return nil
}

// StringMap 与 StringList 同理，只是存的是 JSON 对象（模型名映射）。
type StringMap map[string]string

// Value 实现 driver.Valuer。
func (m StringMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(map[string]string(m))
	if err != nil {
		return nil, fmt.Errorf("序列化字符串映射失败: %w", err)
	}
	return string(b), nil
}

// Scan 实现 sql.Scanner。
func (m *StringMap) Scan(src any) error {
	if src == nil {
		*m = nil
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return fmt.Errorf("StringMap 无法识别的数据库类型 %T", src)
	}
	if len(raw) == 0 {
		*m = nil
		return nil
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("反序列化字符串映射失败: %w", err)
	}
	*m = out
	return nil
}
