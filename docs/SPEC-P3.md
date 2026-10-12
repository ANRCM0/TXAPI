# SPEC-P3：凭据池调度

> 前置依赖：**P2 必须已完成**（需要 `credentials` 表和 `store` 层）
> 目标：实现网关的差异化卖点——多 Key 轮换、失败冷却、自动拉黑、会话亲和

---

## 一、背景：为什么要做这个

P1/P2 里「一个渠道 = 一个 Key」。实际使用中，同一个上游服务商通常有多个 Key
（多个账号、多个额度包）。如果每个 Key 都要单独建渠道，会带来三个问题：

1. **无法统一调度**：无法表达「这 5 个 Key 是同一家的，应该一起做负载均衡」
2. **无法统一故障处理**：一个 Key 挂了，无法自动切到同渠道的其他 Key
3. **无法做会话亲和**：同一会话可能打到不同 Key，破坏上游 prompt 缓存

**P3 的核心改动：把「渠道下的多个 Key」建模为「凭据池」，并实现调度算法。**

```
渠道 (Channel) = 上游服务商
  ├── 凭据 1 (Key A, 权重 10, active)
  ├── 凭据 2 (Key B, 权重 5,  cooldown 中)
  └── 凭据 3 (Key C, 权重 1,  banned)
```

---

## 二、四个机制（核心交付）

### 2.1 加权随机选择

**目标**：按权重把流量分配到不同凭据。

**算法**：
```
1. 过滤出可用凭据：
     status == active
     && cooldown_until < now
     && 凭据所属渠道支持请求的模型
2. 计算权重总和 total = Σ weight
3. 在 [0, total) 掷一个随机点 n
4. 遍历凭据，n -= weight，n < 0 时选中该凭据
```

**为什么不用轮询**：权重让运维能按比例压流量（新凭据先给 10% 试水），
轮询做不到。这是 GPT-Load 的做法，也是本项目要抄的核心。

### 2.2 失败冷却（指数退避）

**目标**：失败的凭据暂时下线，给上游恢复时间，避免持续打挂。

**状态迁移**：
```
成功:
  fail_count = 0
  cooldown_until = 0
  status = active

失败:
  fail_count += 1
  if fail_count >= 阈值(默认 5):
      status = banned            // 永久禁用，需人工或定时任务恢复
  else:
      status = cooldown
      cooldown_until = now + base × 2^(fail_count - 1)
      // base 默认 30 秒 → 30s, 60s, 120s, 240s...
      // 必须封顶，比如最多 30 分钟，否则退避到几天没意义
```

**关键细节**：
- **退避必须封顶**。不封顶的话，第 10 次失败会冷却 8.5 小时，等于永久禁用
- **冷却到期自动恢复**：需要一个后台定时任务（比如每 30 秒扫一次），
  把 `cooldown_until < now` 的凭据改回 `active`。不做这个的话，
  凭据冷却后就再也不会被选中，等于永久禁用
- **成功要重置 fail_count**。否则一个偶发失败的凭据会累积计数，最终被误拉黑

### 2.3 自动拉黑

**目标**：连续失败的凭据自动禁用并告警，防止它持续消耗请求。

**触发**：`fail_count >= 阈值`（默认 5，可配）

**与冷却的区别**：
- 冷却 = 临时，到期自动恢复
- 拉黑 = 需要人工介入，或由更长的定时任务恢复（比如 1 小时后）

**必须提供恢复手段**：管理 API 要能手动把 banned 改回 active，
否则运维遇到误判只能改数据库。

### 2.4 会话亲和（重要，且容易做错）

**目标**：同一个会话固定使用同一个凭据。

**为什么重要**——两个理由：

1. **成本优化（主要理由）**：上游（OpenAI / Anthropic）按账号缓存 prompt 前缀。
   同一会话打到不同凭据 = 缓存命中率 0 = 每次全价 + 更高延迟。
   固定到同一凭据，缓存命中能显著省钱提速。
2. **上下文连续性**：某些上游对多轮对话有会话级状态，换账号可能报错。

**实现**：一致性哈希
```
凭据 = hash(会话ID) % 可用凭据数
```

**会话 ID 从哪来**：
- 优先取请求头 `X-Session-Id`（客户端显式传）
- 退化方案：取 `user_id + 首条 user 消息的哈希`（同一对话内容稳定）
- 再退化：不做亲和（按纯加权随机）

**必须注意**：
- 亲和的凭据如果不可用（冷却/拉黑），要**回退到加权随机**，
  不能直接报错
- 亲和不是强绑定，只是「优先选择」

---

## 三、交付物

### 3.1 新增/修改文件

```
internal/scheduler/
├── selector.go      凭据选择（加权随机 + 亲和）
├── health.go        状态迁移（成功/失败 → active/cooldown/banned）
├── affinity.go      会话亲和（一致性哈希）
└── recovery.go      后台定时恢复冷却到期的凭据
internal/store/
└── credential.go    凭据的 CRUD（P2 已建表，这里补数据访问方法）
internal/server/api/
└── credential.go    凭据管理 API
internal/relay/
└── relay.go         改造：选凭据 → 失败换凭据 → 再换渠道
```

### 3.2 调度器接口（建议）

```go
// Scheduler 负责在渠道的凭据池里选一个可用凭据。
type Scheduler interface {
    // Select 为一次请求选一个凭据。
    // sessionID 用于会话亲和，空字符串表示不做亲和。
    Select(channelID int, model string, sessionID string) (*model.Credential, error)

    // ReportSuccess 报告本次请求成功，重置凭据的失败计数。
    ReportSuccess(credentialID int)

    // ReportFailure 报告本次请求失败，推进凭据的状态。
    // statusCode 用于判断是否算「凭据的错」（401/429 算，400 不算）。
    ReportFailure(credentialID int, statusCode int, errMsg string)
}
```

⚠️ **重要**：`ReportFailure` 必须区分错误类型。
- **算凭据的错**：401（Key 失效）、403（权限不足）、429（额度/限流）
- **不算凭据的错**：400（请求参数错）、404（模型不存在）
  → 请求本身有问题，换凭据也一样错，不该惩罚凭据

不区分的话，一个客户端的错误请求会把所有凭据全部拉黑。

### 3.3 凭据管理 API

```
GET    /api/channels/:id/credentials     列出渠道下的凭据
POST   /api/channels/:id/credentials     新增凭据
PUT    /api/credentials/:id              修改（权重/启用状态）
DELETE /api/credentials/:id              删除
POST   /api/credentials/:id/reset        手动重置状态（banned → active）
GET    /api/credentials/:id/stats        查看失败次数、冷却剩余时间
```

⚠️ **API 返回凭据时不能回传完整 Key**，要脱敏（如 `sk-abc...xyz`）。

---

## 四、验收标准

必须用**真实运行**证明，不接受「应该能跑」：

### 4.1 加权随机
配 2 个凭据（权重 9:1），发 100 次请求，统计分布应接近 90:10。

### 4.2 失败冷却
1. 配 3 个凭据，其中 1 个故意用坏 Key（会返回 401）
2. 连发 20 次请求
3. 验证：坏凭据的 `fail_count` 递增，最终进入 cooldown
4. 验证：其余 2 个凭据正常分担流量

### 4.3 自动拉黑
把失败阈值调成 2，连发 5 次，验证坏凭据 `status` 变成 `banned`。

### 4.4 冷却自动恢复
1. 让凭据进入 cooldown（base 设成 2 秒）
2. 等待 3 秒
3. 验证：后台任务把它改回了 `active`

### 4.5 会话亲和
1. 用同一个 `X-Session-Id` 发 10 次请求
2. 验证：全部落到同一个凭据（查日志）
3. 换一个 session id，验证落到另一个凭据

### 4.6 错误类型区分（关键）
1. 发一个参数错误（400）的请求
2. 验证：凭据的 `fail_count` **没有**增加

---

## 五、测试要求

**调度算法必须写单元测试**——这是最容易写错、也最难在集成测试里发现的地方。

建议测试：
- `TestSelectWeighted`：权重分布是否符合预期（跑 10000 次统计）
- `TestCooldownBackoff`：退避序列是否为 30/60/120/240，且封顶
- `TestBannedAfterThreshold`：达到阈值是否正确拉黑
- `TestAffinityStable`：同一 sessionID 是否稳定命中同一凭据
- `TestAffinityFallback`：亲和凭据不可用时是否回退而非报错
- `TestFailureClassification`：400 不惩罚、401/429 惩罚

---

## 六、风险与注意事项

| 风险 | 说明 | 应对 |
|------|------|------|
| 退避不封顶 | 失败多了冷却到几天，等于永久禁用 | 必须设上限（如 30 分钟） |
| 不恢复冷却 | 冷却后永不恢复，凭据池逐渐枯竭 | 必须有后台定时任务 |
| 错误分类不当 | 客户端 400 把凭据全拉黑 | 只惩罚 401/403/429 |
| 亲和写死 | 亲和凭据挂了就报错 | 必须回退到加权随机 |
| 并发竞态 | 多个请求同时更新 fail_count | 用数据库原子更新或加锁 |
| 内存与库不一致 | P2 的内存缓存没同步 | 状态变更要同时更新缓存 |

---

## 七、参考实现

- **GPT-Load**（`/root/gpt-load`）：本阶段的主要参考，它的凭据调度最完整。
  重点看 `internal/control/credential_stages.go` 和 `internal/execution/`。
- **One API**（`/root/one-api`）：看 `middleware/distributor.go` 的渠道选择，
  但它是渠道级不是凭据级，参考思路即可。

⚠️ 不要抄 New API 的代码（AGPL-3.0）。只借鉴功能设计。
