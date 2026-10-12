# SPEC-P5：计费与用量统计

> 前置依赖：**P2 必须已完成**（需要 `logs`、`model_prices`、`tokens` 表）
> 目标：按 token 用量计费、扣减额度、提供用量查询

---

## 一、背景

网关如果不计费，就只是一个转发器。计费的价值：

1. **成本可见**：知道每个渠道/模型/令牌花了多少钱
2. **额度控制**：给令牌设额度上限，用完自动拒绝（防止 Key 被刷爆）
3. **用量分析**：哪个模型最贵、哪个渠道最慢

自用阶段不需要「充值」，但**需要额度控制**——因为自用的 Key 被刷爆也是真实损失。

---

## 二、核心设计

### 2.1 计费公式

```
cost = (prompt_tokens / 1000) × 输入单价
     + (completion_tokens / 1000) × 输出单价
     × 渠道分组倍率
     × 用户分组倍率
```

**倍率的用途**：同一模型在不同渠道成本不同（官方 vs 中转），
用倍率统一折算成「标准计价单位」。

### 2.2 价格表

```sql
model_prices(
  model,                    -- 模型名，主键
  input_price_per_1k,       -- 输入单价（每 1000 token）
  output_price_per_1k,      -- 输出单价
  updated_at
)
```

**必须支持**：
- 通配符匹配：`gpt-4*` 匹配 `gpt-4o`、`gpt-4-turbo`
- 未配置价格的模型：**按 0 计费但记录日志**，不要报错
  （宁可漏计费，不要因为价格表缺一项就拒绝服务）

### 2.3 扣减时机（关键决策）

| 方案 | 说明 | 取舍 |
|------|------|------|
| **预扣** | 请求前按预估扣，响应后按实际修正 | 能防超额，但预估不准会误拒 |
| **后扣** | 响应后按实际 usage 扣 | 准确，但可能超额 |
| **预扣+结算**（推荐） | 请求前预扣一个保守上限，响应后按实际调整 | 兼顾两者 |

**推荐做法**：
1. 请求前检查：`used_quota < quota`（超了就拒绝，返回 403）
2. 请求前预扣：按 `max_tokens × 输出单价` 预扣一个上限
3. 响应后结算：按实际 usage 计算，把预扣和实际的差额退回

**流式的处理**：流式响应的 usage 在最后一个 chunk 里（需要客户端传
`stream_options.include_usage`）。如果上游没返回 usage，用 tokenizer 估算
或按字符数近似。**估算值要在日志里标记为估算**，便于事后核对。

### 2.4 日志记录

每次请求都要写一条 `logs`，包含：

```
request_id, user_id, token_id,
channel_id, credential_id,
model, is_stream,
prompt_tokens, completion_tokens, total_tokens,
cost, latency_ms,
status,          -- success / error
error_message,   -- 失败时的错误
is_estimated,    -- usage 是否为估算值
created_at
```

**为什么每条都记**：日志是排查问题和分析用量的唯一依据。
宁可日志表大，不要事后无法追溯。

**异步写入**：日志写入不能阻塞响应。用 channel + 后台 goroutine 批量写，
或者干脆 `go func()` 异步写。但要注意：
- 异步写失败要记到标准日志，不能静默丢失
- 进程退出时要 flush 未写入的日志

---

## 三、交付物

```
internal/billing/
├── price.go       价格表查询（含通配符匹配 + 缓存）
├── counter.go     token 计数（含估算降级）
├── quota.go       额度检查与扣减
└── settle.go      预扣 → 结算 流程
internal/store/
├── log.go         日志写入（异步批量）
└── price.go       价格表 CRUD
internal/server/api/
├── log.go         日志查询 API
└── price.go       价格表管理 API
internal/relay/
└── relay.go       改造：请求前检查额度 → 响应后结算 + 记日志
```

### API

```
GET  /api/logs?token_id=&channel_id=&model=&limit=&offset=   查询日志
GET  /api/stats/summary?range=7d                             用量汇总
GET  /api/stats/by-model?range=7d                            按模型统计
GET  /api/stats/by-channel?range=7d                          按渠道统计
GET  /api/prices                                             价格表
PUT  /api/prices/:model                                      更新价格
POST /api/tokens/:id/quota                                   设置额度
```

---

## 四、验收标准

### 4.1 基础计费
1. 配一个价格：`gpt-4o-mini` 输入 $0.15/1k，输出 $0.6/1k
2. 发一次请求，假上游返回 usage（11 输入 / 7 输出）
3. 验证：日志里的 cost = 11/1000×0.15 + 7/1000×0.6 = 0.00585

### 4.2 额度控制
1. 给令牌设额度 0.001
2. 发请求直到超额
3. 验证：超额后返回 403，且不再转发到上游（省 Key 额度）

### 4.3 预扣与结算
1. 预扣一个上限
2. 响应后验证：`used_quota` 等于实际值，不是预扣值

### 4.4 流式用量
1. 发流式请求，假上游在最后 chunk 带 usage
2. 验证：用量被正确提取并计费

### 4.5 价格缺失
1. 请求一个价格表里没有的模型
2. 验证：**不报错**，正常转发，日志里 cost=0 且有标记

### 4.6 日志不阻塞
1. 发 100 个并发请求
2. 验证：响应时间不受日志写入影响，且 100 条日志都落库了

---

## 五、风险与注意事项

| 风险 | 说明 | 应对 |
|------|------|------|
| 异步日志丢失 | 进程退出时缓冲未 flush | 优雅关闭时 flush |
| 流式无 usage | 上游不返回 usage | 估算 + 标记 `is_estimated` |
| 价格表缺失 | 新模型没配价格 | 记 0 分不报错 |
| 并发扣减竞态 | 多请求同时扣同一令牌 | 数据库原子更新（`used_quota = used_quota + ?`）|
| 预扣不准 | 预扣过高导致误拒 | 预扣要保守，宁可少扣 |

---

## 六、测试要求

- `TestCalculateCost`：计费公式正确性（含倍率）
- `TestPriceWildcard`：通配符匹配（`gpt-4*` 匹配 `gpt-4o`）
- `TestQuotaExceeded`：超额拒绝
- `TestPreDeductSettle`：预扣与实际结算的差额处理
- `TestUsageEstimate`：无 usage 时的估算降级
- `TestConcurrentDeduct`：并发扣减不丢失（跑 100 个 goroutine）
