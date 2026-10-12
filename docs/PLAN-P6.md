# P6 内核补全方案

> 目标：**不碰前端**，把网关内核从「转发能跑」补到「自用够用、工程可信」。
> 范围：安全、凭据池调度、限流、端点扩展、可观测性、工程化。
> 原则：每一批都能独立上线验证；每批完成即可提交，不留半成品。

---

## 零、起点核查

方案基于对当前代码的实际核查，两处关键事实决定了实现路径：

| 事实 | 位置 | 影响 |
|------|------|------|
| `pickKey` 在**快照构建时**调用，不是每请求 | `registry.go:172` | 凭据在渠道加载时就被固化。P3 不是「加冷却逻辑」，而是要把选择时机从「加载时」搬到「每请求时」——这是一次结构性重构，不是补丁 |
| `Mode` 硬编码为 `ModeChatCompletions` | `relay.go:282` | 端点分发不存在。加 embeddings 需要把 Mode 从路由一路透传到适配器 |
| `fail_count` / `cooldown_until` / `last_error` 三字段全项目无写入点 | `model/channel.go:77-82` | 凭据健康状态是死数据，坏 Key 会被反复选中 |
| `syncAbilities()` 认真维护能力表，但路由走全表扫描 | `store/channel.go` vs `relay/registry.go` | 数据结构与使用方式脱节 |
| `main.go` 未传 `AdminAuth` | `main.go` | `/api/*` 完全无鉴权，`GET /api/channels/:id` 明文回显上游 Key |

---

## 一、批次划分与顺序

```
批次1 安全与凭据管理  ──┐
                        ├─→ 批次2 凭据池调度 ──→ 批次3 限流与端点 ──┐
                        │                                          ├─→ 批次4 可观测性与工程化
                        └──────────────────────────────────────────┘
```

**为什么这个顺序**：
- 批次 1 是**安全底线**，成本最低、收益最高，且必须在网关暴露到 localhost 之外以前完成。
- 批次 2 依赖批次 1 的凭据 CRUD（否则没法配多个 Key 来验证调度）。
- 批次 3 的限流和批次 4 的可观测性相互独立，可以并行（2 个代理）。
- 批次 4 放最后：它改善的是"排查体验"，不影响功能正确性。

---

## 批次 1：安全与凭据管理

**目标**：堵住「管理 API 裸奔」和「凭据无法管理」两个洞。

### B1-1 管理 API 鉴权

- 新增 `internal/server/middleware/admin.go`：`AdminAuth(secret string) gin.HandlerFunc`
- 校验 `X-Admin-Key` 头（或 `Authorization: Bearer`），用 `crypto/subtle.ConstantTimeCompare` 防时序攻击
- 密钥来源：环境变量 `GATEWAY_ADMIN_KEY` 优先，其次 `config.yaml` 的 `admin_key` 字段
- **未配置密钥时拒绝启动**（而不是静默放行）——这是关键：宁可启动失败，也不要一个"以为有鉴权其实没有"的网关
- `main.go` 传入 `AdminAuth`；`server.go` 的 `AdminAuth` 字段从"可选 nil"改为"必填"

**验证**：不带 `X-Admin-Key` 调 `/api/channels` → 401；带错误密钥 → 401；带正确密钥 → 200。`/v1/*` 和 `/healthz` 不受影响。

### B1-2 凭据 CRUD

- `store/credential.go`：`ListCredentials(channelID)` / `CreateCredential` / `UpdateCredential` / `DeleteCredential` / `SetCredentialStatus`
- 复用现有的渠道缓存重载机制（`reloadCache`），改完立即生效
- 新增路由：
  ```
  GET    /api/channels/:id/credentials
  POST   /api/channels/:id/credentials
  PUT    /api/credentials/:id
  DELETE /api/credentials/:id
  POST   /api/credentials/:id/status   # 手动重置 active / 解除拉黑
  ```
- 校验：同一渠道下 key 不允许重复；删除最后一个凭据要拒绝（否则渠道变成空壳）

**验证**：给一个渠道加 3 个 Key，`GET /api/channels/:id/credentials` 返回 3 条；删到只剩 1 条时再删 → 400。

### B1-3 渠道详情脱敏

- `GET /api/channels/:id` 的 `api_key` 默认返回掩码（`sk-...abcd`）
- 加 `?reveal=1` 显式要求才回显明文——让"我要看 Key"成为一个有意识的操作

**验证**：默认请求看不到完整 Key；`?reveal=1` 能看到。

---

## 批次 2：凭据池调度（项目核心卖点）

**目标**：把 `credentials` 表的四个死字段变成活的调度状态。

### B2-1 结构性重构：选择时机后移

**这是本批次最大的一步，单独做、单独验证。**

现状：
```go
// registry.go —— 加载时就把 key 选死了
type channel struct {
    key          string
    credentialID uint
    ...
}
```

改为：
```go
type channel struct {
    credentials []model.Credential   // 持有全部凭据，不再预选
    ...
}
```
- `pickKey` 从 `registry.go` 移除，选择逻辑搬到 `scheduler` 包
- 转发时（`relay.go` 构建 Meta 前）调用 `scheduler.Pick(channel, model, sessionID)`
- 重试换渠道时**重新选凭据**（现在换渠道会连带换凭据，逻辑要理顺）

**验证**：一个渠道配 2 个 Key，连发 10 次请求，日志里 `credential_id` 应出现两个值（而不是恒定一个）。

### B2-2 调度器：加权随机 + 过滤

新增 `internal/scheduler/`：
- `selector.go`：`Pick(creds []Credential, now time.Time) (Credential, bool)`
  - 过滤：`status == active && cooldown_until < now`
  - 加权随机：在 `[0, Σweight)` 掷点，落在谁区间就是谁
- **为什么用加权随机而不是"按权重排序取第一个"**：排序取第一个会让权重低的凭据永远拿不到流量，权重退化成优先级。加权随机才能真正按比例分流。

**验证**：权重 10:5:1 的三个凭据各发 1000 次，流量比应接近 10:5:1（容差 ±20%）。

### B2-3 健康状态迁移

- `scheduler/health.go`：
  ```
  成功: fail_count = 0, cooldown_until = 0, status = active
  失败: fail_count++
        if fail_count >= 阈值(默认5): status = banned
        else:                        status = cooldown
                                     cooldown_until = now + base × 2^(fail_count-1)  // 封顶 1h
  ```
- **关键：区分"该罚"的失败**。400/401（参数错、Key 无效）与 429/5xx（上游问题）要分开处理——把用户的参数错误算到凭据头上，会把好 Key 拉黑。
- 状态更新写 DB，但走**异步**（复用 P5 的异步写思路），不能阻塞响应
- 更新后触发缓存重载，让下次选择看到新状态

**验证**：配 1 个好 Key + 1 个坏 Key，连发 20 次：坏 Key 的 `fail_count` 递增 → 进入 `cooldown` → 冷却到期后恢复 → 再失败到阈值 → `banned`。好 Key 的 `fail_count` 恒为 0。

### B2-4 会话亲和

- `scheduler/affinity.go`：`PickWithAffinity(creds, sessionID)`
- 会话 ID 来源：请求头 `X-Session-Id`，缺省时用「令牌 + 首条消息哈希」
- 一致性哈希：`hash(sessionID) % Σweight` 定位，**不因凭据增删而整体重排**
- **为什么重要**：上游按账号缓存 prompt，同一对话固定打同一 Key 才能吃到缓存命中——省钱又提速。这不是"避免上下文错乱"，是成本优化。
- 亲和命中的凭据不可用时，降级到普通加权随机（不能因为亲和把请求卡死）

**验证**：同一 sessionID 连发 10 次，`credential_id` 恒定；换 sessionID 后可能变化；把亲和命中的凭据设为 banned，请求仍成功（降级生效）。

### B2-5 后台恢复任务

- 定时（默认 30s）扫描 `cooldown_until` 到期的凭据，重置为 `active`
- `banned` 的**不自动恢复**（需人工，符合 PLAN 3.3）
- 进程退出时停止（复用优雅关闭链路）

**验证**：设一个凭据 `cooldown_until = now+5s`，等 10s，状态自动回到 `active`。

### B2-6 单元测试（本批次必须写）

调度是最容易写错的地方，测试是硬要求：
- `TestWeightedDistribution` —— 权重分流比例
- `TestFilterUnavailable` —— cooldown/banned/disabled 被排除
- `TestCooldownBackoff` —— 退避序列 1×2×4×8… 且封顶
- `TestBanThreshold` —— 达到阈值转 banned
- `TestAffinityStable` —— 同会话恒定、凭据增删后尽量稳定
- `TestAffinityFallback` —— 亲和目标不可用时降级
- `TestErrorClassification` —— 400 不惩罚凭据、429 惩罚

### B2-7 可选：Key 池探测

- 定时用 `GET /v1/models` 轻量探活每个凭据，失败的提前冷却
- **建议延后**：真实流量已经在做健康检测，探活是锦上添花

---

## 批次 3：限流与端点扩展

### B3-1 限流中间件

- `internal/server/middleware/ratelimit.go`：滑动窗口计数器（内存）
- 两个维度：
  - **令牌级**：RPM（每分钟请求数）+ TPM（每分钟 token 数）
  - **IP 级**：兜底防「没有令牌的暴力探测」
- 令牌上加 `rpm_limit` / `tpm_limit` 字段（0 = 不限，与额度语义一致）
- 超额返回 429 + `Retry-After` 头
- **为什么需要**：P5 的额度扣减是"响应后结算"，防不住「1000 并发在结算前全打到上游」——限流是请求前的闸门，两者互补
- 实现取舍：先做单机内存版（自用够），接口留出 Redis 实现的位置

**验证**：令牌限 10 RPM，连发 15 次 → 前 10 次 200，后 5 次 429 且带 `Retry-After`。

### B3-2 embeddings 端点

- 路由 `POST /v1/embeddings`，handler 传入 `ModeEmbeddings`
- `relay.go` 的硬编码 `ModeChatCompletions` 改为从 handler 参数传入
- `openai` 适配器的 `GetRequestURL` 已支持 embeddings，主要是打通 Mode 透传 + 响应解析（embeddings 无 completion_tokens）
- 计费：embeddings 只算输入 token

**验证**：`curl /v1/embeddings` 返回向量；日志 `prompt_tokens` 有值、`completion_tokens=0`、`cost` 只按输入计价。

### B3-3 能力表：接入或删除

两条路，二选一：
- **接入**：`registry` 加载时从 `abilities` 表构建 `model → []channelID` 索引，选渠道走索引
- **删除**：移除 `abilities` 表和 `syncAbilities`，承认"渠道数量少，全表扫描够用"

**建议接入**：表已经建了、数据已经维护了，接上去只是加一个内存索引，收益（O(1) 路由）与成本（一次加载）都合理。

**验证**：`GET /api/abilities` 的数据与路由实际选中的渠道一致。

---

## 批次 4：可观测性与工程化

### B4-1 结构化日志

- 标准库 `log` → `log/slog`（Go 1.21+，无第三方依赖）
- 统一字段：`request_id` / `token_id` / `channel_id` / `credential_id` / `model` / `latency_ms` / `status`
- 日志级别从配置读（`debug` / `info` / `warn` / `error`）
- 请求日志按级别分流：正常请求 `info`，上游失败 `warn`，内部错误 `error`

**验证**：`debug` 级别能看到选凭据、冷却判定等细节；`info` 级别只有请求摘要。

### B4-2 Prometheus 指标

- `GET /metrics`，暴露：
  - `gateway_requests_total{model,channel,status}`
  - `gateway_request_duration_seconds{model,channel}`（histogram）
  - `gateway_tokens_total{model,channel,type}`（prompt/completion）
  - `gateway_cost_total{model,channel}`
  - `gateway_credentials{channel,status}`（各状态凭据数）
  - `gateway_credential_failures_total{channel,credential}`
- 用 `prometheus/client_golang`
- **可选项**：如果不想引依赖，先做 `/metrics` 的纯文本 exposition 格式手写版

**验证**：`curl /metrics` 能看到计数随请求增长；histogram 有分桶。

### B4-3 日志表清理

- `logs` 表无限增长，SQLite 单文件会越来越慢
- 策略：配置 `log_retention_days`（默认 30），后台定时（每日）删除过期行
- 删除要**分批**（每次 1000 行），避免长事务锁表
- 可选：清理前先汇总到 `stats_daily` 表，保留长期统计

**验证**：插入 40 天前的日志，跑清理任务，过期行被删、近期行保留。

### B4-4 Dockerfile + compose

- 多阶段构建：`golang:1.23-alpine` 编译 → `alpine` 运行
- `docker-compose.yml`：网关 + 数据卷（持久化 SQLite）
- `.dockerignore`
- 环境变量注入管理密钥和上游 Key（不写进镜像）

**验证**：`docker compose up` 一条命令跑起来，数据持久化到卷。

### B4-5 CI

- `.github/workflows/ci.yml`：`go build` + `go vet` + `go test -race` + `gofmt -l` 检查
- **为什么必需**：P5 的 `priceMissing` 反转 bug 是集成测试抓到的。没有 CI，下次同类 bug 就靠运气了。

**验证**：推送后 CI 跑绿；故意引入一个测试失败，CI 变红。

### B4-6 README 重写

- 现状 README 写的是"没有数据库、没有计费系统、约 900 行"，实际 10,000 行、有 SQLite 和完整计费
- 重写内容：架构图、功能矩阵、快速开始（含 Docker）、API 一览、设计决策说明
- 补 `docs/` 下的设计文档索引

---

## 二、明确不做（附理由）

| 不做 | 理由 |
|------|------|
| ❌ 用户注册/登录/JWT | 自用场景「令牌即租户」。`users` 表留着做多用户扩展口即可，现在建全套登录是过度设计 |
| ❌ 支付/充值/邀请码 | PLAN 已明确排除，商业系统才需要 |
| ❌ images / audio / rerank 端点 | 先做最常用的 embeddings；其余等真有需求，适配器接口已留好扩展位 |
| ❌ Redis 限流 | 单机内存版够自用；接口留位置，需要时替换实现 |
| ❌ PostgreSQL 支持 | PLAN 说"可选"，但当前 `Open()` 对非 SQLite 直接报错。等真要上生产再补，属于部署阶段（P7） |
| ❌ 前端 | 本次明确排除 |
| ❌ 集群/多机 | 单二进制是优势，别拆 |

---

## 三、风险与应对

| 风险 | 应对 |
|------|------|
| **B2-1 是结构性重构**，可能影响现有转发链路和 P5 的记账 | 先只改「持有全部凭据 + 每请求选择」，行为等价（还是选第一个可用的）；跑通 P5 全部集成测试后再换加权随机。分两步提交 |
| 健康状态写 DB 可能阻塞响应 | 走异步写（复用 P5 的异步日志模式）；失败不影响响应 |
| 凭据状态更新与缓存重载有竞态 | 状态更新后由单一 goroutine 串行重载；用 `-race` 测试验证 |
| 限流计数在并发下有竞态 | 分片锁或 `atomic`；必须 `-race` 测试 |
| 亲和哈希在凭据增删时重排 | 用一致性哈希；测试覆盖"删除一个凭据后其余会话映射尽量不变" |
| 引入 Prometheus 依赖 | 如果只想保持零依赖，用纯文本 exposition 手写版 |

---

## 四、验收总表

每批次完成后，以下全部必须为真：

| 项 | 标准 |
|----|------|
| 构建 | `go build ./...` 通过 |
| 静态检查 | `go vet ./...` 通过，`gofmt -l` 无输出 |
| 测试 | `go test -race ./...` 全部通过 |
| 回归 | P5 的 6 个验收场景仍然通过（不能被新功能打破） |
| 手动 | 每批次的「验证」条目用 curl 实测通过 |
| 提交 | 每批次一个独立 commit，信息说清"做了什么 + 为什么" |

---

## 五、工作量估算

| 批次 | 内容 | 相对量 |
|------|------|--------|
| 1 | 安全与凭据管理 | 小 |
| 2 | 凭据池调度 | **大**（本方案主体） |
| 3 | 限流与端点 | 中 |
| 4 | 可观测性与工程化 | 中 |

**执行方式**：批次 1 → 批次 2 串行（有依赖）；批次 3 与批次 4 可并行（2 个代理）。每批次完成即提交、即汇报，不攒到最后。
