# 网关开发方案与目标架构

> 定位：**自用为主、留多用户扩展口的 LLM API 网关**
> 原则：单二进制、MIT 许可、凭据池调度为技术亮点

---

## 一、总体架构

### 1.1 分层

```
┌─────────────────────────────────────────────────────┐
│  客户端 (Cherry Studio / NextChat / Cursor / SDK)     │
└──────────────────────┬──────────────────────────────┘
                       │ OpenAI / Claude / Gemini 原生协议
┌──────────────────────▼──────────────────────────────┐
│  HTTP 层  internal/server                            │
│  中间件: RequestID → CORS → Auth → RateLimit → Trace │
│  路由:   /v1/* 转发入口   /api/* 管理接口              │
├─────────────────────────────────────────────────────┤
│  转发层  internal/relay          ⭐ 核心              │
│   路由(能力表) → 调度(选凭据) → 转换 → 转发 → 记账      │
├─────────────────────────────────────────────────────┤
│  业务层                                              │
│   scheduler 凭据池调度   billing 计费   log 日志       │
├─────────────────────────────────────────────────────┤
│  存储层  internal/store                              │
│   SQLite(默认) / PostgreSQL(可选)  +  内存缓存         │
└──────────────────────┬──────────────────────────────┘
                       │ 渠道 Key（可多个，构成凭据池）
┌──────────────────────▼──────────────────────────────┐
│  上游: OpenAI / Anthropic / Gemini / DeepSeek / ...   │
└─────────────────────────────────────────────────────┘
```

### 1.2 目录结构（目标形态）

```
gateway/
├── cmd/gateway/main.go            入口（极薄）
├── internal/
│   ├── config/                    配置：YAML + 环境变量 + 数据库设置
│   ├── model/                     ⭐ 数据模型（GORM 定义）
│   ├── store/                     数据访问层（含内存缓存）
│   ├── relay/                     ⭐ 转发核心
│   │   ├── adaptor/               协议适配器接口
│   │   │   ├── openai/            OpenAI 协议
│   │   │   ├── anthropic/         Claude 协议
│   │   │   ├── gemini/            Gemini 协议
│   │   │   └── compatible/        OpenAI 兼容厂商（DeepSeek 等）
│   │   ├── canonical/             ⭐ 规范格式（内部统一表示）
│   │   ├── meta/                  单次转发上下文
│   │   ├── pipeline.go            转发流水线
│   │   └── registry.go            适配器注册表
│   ├── scheduler/                 ⭐ 凭据池调度（差异化卖点）
│   │   ├── selector.go            加权随机选择
│   │   ├── health.go              冷却 / 拉黑 / 恢复
│   │   └── affinity.go            会话亲和
│   ├── billing/                   计费：token 计数 + 价格表
│   ├── server/
│   │   ├── router.go              路由注册
│   │   ├── middleware/            鉴权 / 限流 / 追踪
│   │   └── api/                   管理接口（渠道/令牌/日志 CRUD）
│   ├── cache/                     内存缓存 + 可选 Redis
│   └── pkg/                       通用工具（日志/错误/ID 生成）
├── web/                           前端（构建后 embed 进二进制）
├── docs/                          文档
└── deploy/                        Docker / compose
```

---

## 二、数据模型（架构的地基）

### 2.1 关键决策：渠道与凭据分离

这是比 One API 更进一步的**核心设计**。

One API 里「渠道 = 一个 Key」，想用 5 个 Key 就得建 5 个渠道，
无法表达「这 5 个 Key 是同一家的、应该一起调度」。

**本项目的设计**：

```
渠道 (Channel) = 上游服务商配置
  ├── 凭据 1 (Key A, 权重 10, 健康)
  ├── 凭据 2 (Key B, 权重 5, 冷却中)
  └── 凭据 3 (Key C, 权重 1, 已拉黑)
```

一个渠道下挂多个凭据，调度器在凭据之间做负载均衡。
这是实现「多 Key 轮换 / 失败冷却 / 自动拉黑 / 会话亲和」的前提。

### 2.2 表结构

```sql
-- 用户（自用阶段只有 1 个 admin，但表先建好，为多用户留口子）
users(id, username, password_hash, role, quota, used_quota, status, created_at)

-- 渠道 = 上游服务商
channels(
  id, name, type,              -- type: openai/anthropic/gemini
  base_url,                    -- 上游地址
  models,                      -- JSON 数组，支持 "*" 通配
  model_mapping,               -- JSON 对象，模型名映射
  group_name,                  -- 渠道分组（用于分组倍率/权限）
  priority,                    -- 优先级（高优先级先选）
  weight,                      -- 渠道级权重
  status,                      -- enabled/disabled
  created_at, updated_at
)

-- ⭐ 凭据 = 渠道下的一个 Key
credentials(
  id, channel_id, name,
  api_key,
  weight,                      -- 凭据级权重
  status,                      -- active/cooldown/banned/disabled
  fail_count,                  -- 连续失败次数
  cooldown_until,              -- 冷却截止时间戳
  last_used_at,
  last_error,                  -- 最近一次错误（便于排查）
  created_at
)

-- 令牌 = 客户端访问网关的凭证
tokens(
  id, user_id, name, key,
  models,                      -- 允许访问的模型（空=不限制）
  quota, used_quota,           -- 额度（自用阶段可设 0=不限）
  expires_at, status, created_at
)

-- ⭐ 能力表 = 路由加速器
abilities(
  group_name, model, channel_id,
  enabled, priority, weight,
  PRIMARY KEY(group_name, model, channel_id)
)
-- 作用：选渠道时直接查这张表，一次索引命中，
--      不用去扫描 channels 表解析 JSON 模型列表

-- 请求日志
logs(
  id, request_id, user_id, token_id,
  channel_id, credential_id,
  model, is_stream,
  prompt_tokens, completion_tokens, total_tokens,
  cost, latency_ms,
  status, error_message, created_at
)

-- 价格表（用于计费）
model_prices(model, input_price_per_1k, output_price_per_1k, updated_at)

-- 系统设置
settings(key, value)
```

---

## 三、四个核心机制的设计

### 3.1 转发流水线

```
① 接收       读 body，只解析 model/stream，其余原样保留
                ↓
② 路由       查能力表 → 得到候选渠道列表（按 priority 排序）
                ↓
③ 调度       每个候选渠道 → 调度器选一个可用凭据
                ↓
④ 入站转换   客户端协议 → 规范格式        (如 Claude → 规范)
                ↓
⑤ 出站转换   规范格式 → 上游协议          (如 规范 → OpenAI)
                ↓
⑥ 转发       发请求（带超时 / 请求 ID / 追踪）
                ↓
⑦ 响应转换   上游协议 → 规范格式 → 客户端协议
                ↓
⑧ 记账       抽 usage → 计费 → 写日志 → 更新凭据健康状态
                ↓
⑨ 失败处理   换凭据重试 → 换渠道重试 → 返回错误
```

### 3.2 多协议互转：规范格式（关键洞察）

**朴素做法**（N × M 爆炸）：
```
OpenAI 客户端 → OpenAI 上游    ┐
OpenAI 客户端 → Claude 上游    │ 3 种协议 = 9 条转换路径
OpenAI 客户端 → Gemini 上游    │ 5 种协议 = 25 条
Claude 客户端 → OpenAI 上游    │ 每加一种协议，路径数平方增长
...                            ┘
```

**本项目的做法**（N + M）：
```
        入站适配器              出站适配器
OpenAI  ─┐                 ┌─  OpenAI
Claude  ─┼→ [规范格式] ────┼─  Claude
Gemini  ─┘                 └─  Gemini

3 种协议 = 3 + 3 = 6 个转换器（而不是 9 个）
5 种协议 = 5 + 5 = 10 个（而不是 25 个）
```

**规范格式选谁**：选 OpenAI Chat Completions 格式。
理由：它是事实标准，几乎所有客户端和 SDK 都用它，文档最全。

**规范格式长什么样**（`internal/relay/canonical/`）：

```go
type ChatRequest struct {
    Model       string
    Messages    []Message      // 统一的消息结构
    MaxTokens   int
    Temperature *float64
    Stream      bool
    Tools       []Tool         // 函数调用
    // ... 只保留各协议都能表达的共同子集
}

type Message struct {
    Role    string        // system / user / assistant / tool
    Content []ContentPart // 统一的多模态内容
}

type ContentPart struct {
    Type string  // text / image / tool_use / tool_result
    Text string
    // ...
}
```

**适配器接口**（每个协议实现一次）：

```go
type ProtocolAdaptor interface {
    Name() string

    // 请求方向
    ParseRequest(body []byte) (*canonical.ChatRequest, error)   // 本协议 → 规范
    BuildRequest(req *canonical.ChatRequest) ([]byte, error)     // 规范 → 本协议

    // 响应方向
    ParseResponse(body []byte) (*canonical.ChatResponse, error)
    BuildResponse(resp *canonical.ChatResponse) ([]byte, error)

    // 流式（最难的部分）
    ParseStreamLine(line []byte) (*canonical.StreamChunk, error)
    BuildStreamLine(chunk *canonical.StreamChunk) ([]byte, error)

    // 元信息
    DefaultBaseURL() string
    AuthHeader(apiKey string) (name, value string)
}
```

⚠️ **必须正视的问题：协议能力不对等**

格式转换是**有损的**。举例：
- Claude 的 `system` 是独立顶层字段；OpenAI 是 `messages` 里的 `role=system`
- Gemini 的 role 只有 `user`/`model`，没有 `assistant`/`system`
- 函数调用（tools）三家格式完全不同
- Claude 有 `thinking`（扩展思考），OpenAI 用 `reasoning_effort`

**策略：能转则转，不能转则明确降级或报错，绝不静默丢弃。**
静默丢弃会产生「参数在网关后面失效」这类极难排查的问题。

**流式转换是最难的一环**：客户端说 OpenAI 协议、上游说 Claude 协议时，
必须把 Claude 的 SSE 事件流实时翻译成 OpenAI 的 chunk 流。
这块要单独设计并重点测试。

### 3.3 凭据池调度（差异化卖点）

**四件事**：

| 机制 | 作用 | 实现要点 |
|------|------|---------|
| **加权随机** | 按权重分配流量 | 在 `[0, Σweight)` 掷点，落在谁区间就是谁 |
| **失败冷却** | 失败的凭据暂时下线 | `cooldown = base × 2^(fail_count-1)`，指数退避 + 封顶 |
| **自动拉黑** | 连续失败的永久禁用 | `fail_count >= 阈值` → `status=banned`，并告警 |
| **会话亲和** | 同一会话固定同一凭据 | 会话 ID 哈希 → 固定凭据 |

**选择算法**：

```
1. 过滤:  status == active
       && cooldown_until < now
       && 凭据所属渠道支持该模型
2. 加权随机选一个
3. 记录 last_used_at
```

**成功/失败的状态迁移**：

```
成功:  fail_count = 0, cooldown_until = 0, status = active
失败:  fail_count++
       if fail_count >= 阈值(默认5):  status = banned（需人工或定时任务恢复）
       else:                          status = cooldown
                                      cooldown_until = now + base × 2^(fail_count-1)
```

**会话亲和为什么重要**（这是很多人忽略的点）：
上游（OpenAI / Anthropic）会按账号缓存 prompt。
如果同一个多轮对话反复打到不同凭据，缓存命中率是 0；
固定打到同一凭据，缓存命中能**省钱又提速**。
所以亲和不只是「避免上下文错乱」，更是**成本优化**。

### 3.4 计费与用量

```
计费公式:
  cost = prompt_tokens/1000 × 输入单价 + completion_tokens/1000 × 输出单价
        × 渠道分组倍率 × 用户分组倍率

扣减流程:
  1. 请求前: 预估扣减（可选，防止超额）
  2. 响应后: 按实际 usage 结算（以实际为准）
  3. 流式:   从最后一个带 usage 的 chunk 取用量
```

---

## 四、开发阶段

| 阶段 | 内容 | 交付物 | 预计 |
|------|------|--------|------|
| **P1** ✅ | 极简内核 | 转发链路跑通（已完成） | — |
| **P2** | 持久化地基 | SQLite + GORM + 渠道/令牌/日志表 + 管理 REST API | 1-2 周 |
| **P3** | 凭据池调度 | 凭据表 + 权重/冷却/拉黑/亲和 | 1-2 周 |
| **P4** | 多协议互转 | 规范格式 + Anthropic/Gemini 适配器 | 2-3 周 |
| **P5** | 计费与日志 | 价格表 + 额度扣减 + 日志查询 | 1 周 |
| **P6** | 管理界面 | React + 内嵌二进制 | 2-3 周 |
| **P7** | 发布 | Docker + 文档 + 测试 | 1 周 |

### P2 详细：持久化地基

这是**最关键的一步**——所有后续功能都依赖它。

- [ ] 引入 GORM，建 SQLite（默认）/ PostgreSQL（可选）
- [ ] 迁移：users / channels / credentials / tokens / abilities / logs
- [ ] `store/` 层：渠道和令牌的 CRUD + 内存缓存
- [ ] 能力表自动维护（渠道增删改时同步更新）
- [ ] 管理 API（先做无鉴权或简单鉴权）：
  - `GET/POST/PUT/DELETE /api/channels`
  - `GET/POST/DELETE /api/tokens`
  - `GET /api/logs`
- [ ] 启动时从数据库加载，替代现在的 YAML 配置

**验证标准**：能通过 REST API 增删渠道，不重启网关就生效。

### P3 详细：凭据池调度

- [ ] `credentials` 表 + 渠道下的凭据 CRUD
- [ ] `scheduler/selector.go`：加权随机 + 过滤不可用凭据
- [ ] `scheduler/health.go`：成功/失败的状态迁移 + 指数退避
- [ ] `scheduler/affinity.go`：会话亲和（一致性哈希）
- [ ] 后台定时任务：恢复冷却到期的凭据
- [ ] 单元测试：调度算法必须测（这是最容易写错的地方）

**验证标准**：配 3 个凭据，其中 1 个故意用坏 Key，
连发 20 次请求，坏凭据应被冷却，其余两个按权重分流。

### P4 详细：多协议互转

- [ ] `canonical/` 定义规范格式
- [ ] 重构现有 openai 适配器 → 实现 `ProtocolAdaptor` 接口
- [ ] 新增 anthropic 适配器（`x-api-key` 鉴权、`messages` 格式、SSE 事件流）
- [ ] 新增 gemini 适配器（URL 带 key、`generateContent` 格式）
- [ ] 流式转换（最难，单独测试）
- [ ] 能力不对等的降级策略 + 明确报错

**验证标准**：用 OpenAI SDK 调 Claude 模型，能正常对话（含流式）。

### P6 详细：管理界面

- [ ] React + Vite + TypeScript
- [ ] 页面：概览 / 渠道 / 凭据 / 令牌 / 日志 / 设置
- [ ] `go:embed` 把构建产物打进二进制
- [ ] 单文件部署

---

## 五、明确不做（避免过度设计）

| 不做 | 理由 |
|------|------|
| ❌ 一上来支持 40 个厂商 | 用 `compatible` 适配器兜底，先做 3 个协议 |
| ❌ 支付/充值/邀请码 | New API 有，但那是商业系统；自用不需要 |
| ❌ OAuth / Passkey / 2FA | AxonHub 有，但会拖慢几个月 |
| ❌ 插件系统 | New API 的 JS 插件很酷，但先有核心 |
| ❌ GraphQL | AxonHub 用 gqlgen，但 REST 对新手友好 10 倍 |
| ❌ 微服务拆分 | 单二进制就是优势，别拆 |
| ❌ 集群/多机部署 | 自用场景用不上，等真需要再说 |

---

## 六、技术栈

| 层 | 选择 | 理由 |
|----|------|------|
| 语言 | Go 1.23 | 单二进制、并发模型适合 IO 密集转发 |
| HTTP | Gin | 生态成熟、中间件模型清晰、资料最多 |
| ORM | GORM | 比 Ent 上手快，代码生成少 |
| 数据库 | SQLite 默认 + PG 可选 | SQLite 零配置跑起来，PG 留给生产 |
| 缓存 | 内存 + 可选 Redis | 别一上来就上 Redis |
| 前端 | React + Vite + TS | 生态大、AI 辅助写代码资料多 |
| 部署 | Docker + 单二进制内嵌前端 | 一个命令跑起来 |

---

## 七、参考资料对照

| 抄什么 | 抄自 | 许可证 |
|--------|------|--------|
| 项目骨架、Adaptor 接口思路、能力表 | One API | MIT |
| 凭据池调度（权重/冷却/拉黑/亲和） | GPT-Load | MIT |
| 工程分层、目录组织 | AxonHub | Apache-2.0 |
| 规范格式做枢纽的思路 | LiteLLM | MIT（注意 enterprise/ 是商业授权） |
| 插件架构、crate 分层 | codex-proxy-rs | Apache-2.0 |
| 前端内嵌、单二进制交付 | GPT-Load | MIT |

⚠️ **避开 New API 的代码**：它是 AGPL-3.0，抄了会传染。
只借鉴它的功能设计思路，不复制代码。
