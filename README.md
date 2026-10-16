# TXAPI

TXAPI 是一个自用的 LLM API 网关：把多个上游渠道（OpenAI / Anthropic / Gemini / 任何
OpenAI 兼容端点）收敛到一个 OpenAI 兼容入口后面，做**令牌鉴权、凭据池调度、
协议互转、计费额度、限流、可观测**。

定位见 [docs/PLAN.md](docs/PLAN.md)：**自用为主、留多用户扩展口**，单二进制，
零第三方可观测性依赖，凭据池调度是技术亮点。

- 语言/版本：Go 1.23
- 存储：SQLite（默认，单文件 + WAL）；接口留着 PostgreSQL 的位置
- Go module/import 路径仍为 `minigateway`，保持现有代码与依赖路径兼容；项目对外名称为 TXAPI

---

多团队功能开发先阅读 [协作框架](docs/COLLABORATION.md) 和 [贡献约定](CONTRIBUTING.md)。
前后端契约见 [OpenAPI 0.2.0](docs/openapi.json)；框架接口与业务阶段分别验收。

## 架构

```
客户端 (Cherry Studio / NextChat / Cursor / 任何 OpenAI SDK)
   │  OpenAI 兼容协议 + Bearer <令牌>
   ▼
┌──────────────────────────────────────────────────────────────┐
│ HTTP 层   internal/server + middleware                       │
│   RequestID → IP限流 → CORS → Auth(令牌) → 令牌限流 → Relay    │
│   路由: /v1/* 转发   /api/* 管理   /metrics   /healthz        │
├──────────────────────────────────────────────────────────────┤
│ 转发层   gateway 编排 + core 执行 + protocol 转换             │
│   模型路由(索引) → 选渠道+选凭据 → 额度预扣 → 协议转换          │
│   → 转发(失败换目标重试) → 结算 → 落库+埋指标                   │
├──────────────────────────────────────────────────────────────┤
│ 业务层                                                         │
│   scheduler  凭据池调度(加权随机/冷却/拉黑/亲和/恢复)           │
│   billing    计费(价格表/预扣→结算/原子扣减)                    │
│   metrics    Prometheus 指标(手写 exposition，零依赖)           │
│   logging    结构化日志(log/slog，级别可配)                     │
├──────────────────────────────────────────────────────────────┤
│ 存储层   internal/store                                        │
│   SQLite + GORM + 内存缓存(渠道/令牌) + 异步批量日志写入          │
└──────────────────────┬───────────────────────────────────────┘
                       │ 渠道 Key（每个渠道可配多把，构成凭据池）
                       ▼
        上游: OpenAI / Anthropic / Gemini / DeepSeek / ...
```

目录结构：

```
.
├── cmd/gateway/                 入口：装配依赖 + 启动 + 优雅关闭（极薄）
├── Dockerfile                   多阶段构建（golang:1.23-alpine → alpine）
├── docker-compose.yml           网关 + 命名数据卷
├── Makefile                     init / build / run / check / smoke
├── configs/gateway.example.yaml 可提交的配置模板（不含真实密钥）
├── internal/
│   ├── config/                  YAML + 环境变量 + 启动期校验
│   ├── logging/                 log/slog 初始化（级别来自配置）
│   ├── metrics/                 Prometheus 指标（手写 exposition 格式）
│   ├── model/                   GORM 数据模型
│   ├── store/                   数据访问 + 内存缓存 + 异步日志 + 清理
│   ├── billing/                 价格表、额度预扣/结算、原子扣减
│   ├── scheduler/               凭据池调度：选择/健康/亲和/恢复/清理
│   ├── core/                    单次 HTTP 执行、连接复用、取消与提交状态（纯标准库）
│   ├── gateway/                 实例化请求编排、调度/重试、结算与记账
│   ├── protocol/                请求/响应及每请求 SSE 转换（不依赖 Gin/数据库）
│   │   ├── canonical/           厂商中立的请求/响应格式
│   │   └── adaptor/             openai / anthropic / gemini
│   └── server/
│       ├── server.go            中间件链 + 路由注册
│       ├── api/                 管理 API（渠道/凭据/令牌/日志/价格 CRUD）
│       └── middleware/          Recovery/RequestID/CORS/Auth/限流/AdminAuth
├── tests/
│   ├── configs/                 各场景独立数据库的手工测试配置
│   ├── upstream/                假上游（无 Key 跑通全链路）
│   └── smoke/                   冒烟检查与可选 SDK 检查
├── docs/                        架构、开发计划、开发指南、P6 方案
└── bin/、.local/                本地生成物，不提交
```

> 入口在 `cmd/gateway`，不是仓库根目录：`go run ./cmd/gateway` / `go build ./cmd/gateway`。
> 根目录的 `config.yaml` 是个人配置，被 Git 忽略；可提交的模板在 `configs/`。

### S4 控制台与交付验收

启动后访问网关根路径 `/`，使用已初始化的管理员或管理员创建的用户登录。
控制台使用原生 ES Modules，随 `go build` 嵌入二进制；**不需要 Node 构建、CDN 或额外前端服务**。

- 管理员：创建/停用用户、模型授权、渠道与多凭据管理、全局日志、模型策略和价格设置。
- 用户：创建/限制/吊销自己的 Key、查看可用模型和本人用量、发送一次真实的非流式测试请求。
- 渠道/凭据与价格/倍率写入后展示 desired/applied 版本；发布失败保留旧运行快照，可以在设置页重试。
- 登录会话只保存在页面内存中，刷新页面需要重新登录。调用 Key 只在创建时展示；测试调用不保存 Key。
- 用量来自可丢弃、有保留周期的访问日志，**不是 S5 严格预算或可靠账本**。

2026-10-16，收口提交 `97bdc61` 已通过 push/PR 完整 CI 和独立容器交付演练：全新安装、非 root/健康检查、数据卷重启、S3→S4 升级、在途 SSE 正常关闭及备份恢复/旧镜像回滚。验收范围和运行链接见 [开发计划 S4](docs/PLAN.md#s4-管理配置与交付)，集成状态见 [PR #4](https://github.com/ANRCM0/TXAPI/pull/4)。这不等于已经升级真实业务库或完成生产上线。

---

## 功能矩阵

| 能力 | 状态 | 说明 |
|------|------|------|
| 令牌鉴权 | ✅ | Bearer 令牌，可按令牌限制可访问的模型 |
| 渠道路由 | ✅ | 按模型名索引命中（O(1)，支持 `*` 通配渠道），按权重分流 |
| 凭据池调度 | ✅ | 每请求选 Key：加权随机 + 一致性哈希亲和 |
| 健康状态机 | ✅ | 失败分类 → 指数退避冷却（30s 起，封顶 1h）→ 连续失败拉黑 |
| 自动恢复 | ✅ | 冷却到期自动回 active（每 30s 扫描）；banned 需人工解封 |
| 失败重试 | ✅ | 重试单元是 (渠道, 凭据) 对；400/404/422 不重试也不罚凭据 |
| 协议转换 | ✅ | OpenAI 入站 → OpenAI / Anthropic / Gemini 上游（经规范格式枢纽） |
| 流式转发 | ✅ | SSE 逐行翻译，打字机效果保留；anthropic/gemini 也支持 |
| embeddings | ✅ | 与对话共用整条链路，只按输入 token 计费 |
| 基础计费 | ✅ | 价格表（支持通配符/分组倍率）、估算预扣→结算；不是 S5 严格预算/可靠账本 |
| 限流 | ✅ | 令牌级 RPM/TPM（滑动窗口）+ IP 级兜底；429 + `Retry-After` |
| 结构化日志 | ✅ | log/slog，级别可配（debug/info/warn/error），七个统一字段 |
| Prometheus 指标 | ✅ | `GET /metrics`，请求/尝试/凭据/日志队列指标；手写 exposition，零新依赖 |
| 日志清理 | ✅ | `logs` 表按保留天数每日分批删除（每批 1000 行） |
| 管理 API | ✅ | 新 `/api/admin/*` 使用管理员登录会话；旧 `/api/*` 继续使用独立管理密钥 |
| Docker | ✅ | 多阶段构建 + compose + 数据卷；密钥走环境变量 |
| CI | ✅ | build/vet/race/格式/契约/冒烟，以及独立容器安装、升级与备份恢复演练 |
| 用户登录/隔离 | ✅ | 一次性管理员初始化；管理员创建用户；独立登录会话、本人 Key/日志/用量与模型权限交集 |
| 内嵌管理/用户控制台 | ✅ | 使用真实会话 API，管理员与本人资源隔离；交付验收见 PLAN |
| 配置发布可见性 | ✅ | 独立渠道/价格快照的 desired/applied 版本、失败原因及重试；版本仅在当前进程有效 |
| 公开注册 | ❌ | 首版只允许管理员创建用户 |
| 支付/充值 | ❌ | 不在当前阶段范围 |
| images/audio/rerank | ❌ | 适配器接口已留扩展位，等真有需求 |
| Redis 限流 | ❌ | 单机内存版够自用；接口留位置，需要时替换实现 |
| PostgreSQL | ❌ | 未接入；当前默认单实例 SQLite，S4 不要求外部数据库 |

> P5/P6 的计费与请求级凭据调度已交付；S0、S1 于 2026-10-15 通过 CI 验收。S2 与 F0 已合并到 `master`；S3 全部实现并通过审查、完整 CI，已合并到 `master`，运行约定见 [开发指南](docs/DEVELOPMENT.md#s3-并发准入与用户请求速率)，阶段验收见
> [docs/PLAN.md](docs/PLAN.md)。

S4 已完成实现、完整 CI 和容器交付验收；S5 仍未开始。Docker、升级和恢复的通过证据来自隔离 CI 环境，不承诺真实上游兼容性或崩溃时无损恢复。

---

## 快速开始

### 方式一：本机编译（无需 Docker）

```bash
export PATH=$PATH:/usr/local/go/bin

make init          # 只在缺少 config.yaml 时从 configs/ 复制模板
# 编辑 config.yaml：填 admin_key、替换上游地址与密钥占位符
make build
# 首次使用先按下方说明初始化管理员
make run
```

不使用 Make 时：

```bash
mkdir -p bin .local/data
test -f config.yaml || cp configs/gateway.example.yaml config.yaml
go build -o bin/gateway ./cmd/gateway
./bin/gateway -config config.yaml
```

**管理密钥是必填项**：`admin_key`（或环境变量 `GATEWAY_ADMIN_KEY`）缺失时
网关拒绝启动。管理 API 能改渠道、看上游 Key，裸奔的代价是「谁连上 localhost
就能拿走全部凭据」。

### S2 管理员初始化与升级注意

客户端 Key 现在只保存 SHA-256 摘要，仅创建时返回原文。旧库升级会执行版本 2 数据迁移；**`user_id=0` 的旧 Key 默认不可调用，必须由管理员显式认领，不会自动绑定为超级凭证。升级前先停止旧进程并备份数据库和 WAL。**

首次管理员无默认密码，通过环境输入初始化（密码 12–72 字节）：

```bash
export TXAPI_ADMIN_USERNAME=admin
read -rsp '管理员密码: ' TXAPI_ADMIN_PASSWORD; echo
export TXAPI_ADMIN_PASSWORD
./bin/gateway -config config.yaml -init-admin
unset TXAPI_ADMIN_PASSWORD
```

之后启动服务，通过 `POST /api/auth/login` 获取 24 小时 Bearer 会话。管理员创建用户；用户登录后通过 `POST /api/me/keys` 生成自己的调用 Key。管理会话不能用于 `/v1/*`，调用 Key 也不能管理用户。

新安装建议不在 YAML 中配置客户端 `tokens`，登录后生成；若仍导入 YAML Key，它们同样处于无主隔离状态。迁移认领、API 示例和回滚步骤见 [开发指南](docs/DEVELOPMENT.md#s2-身份权限与升级)。

### 方式二：Docker Compose

```bash
# 密钥全部走环境变量（不写进镜像、不写进 compose 文件）
export GATEWAY_ADMIN_KEY=dev-secret
export GATEWAY_CHANNEL_KEY_1=sk-your-real-key

# 首次安装先按开发指南在临时容器初始化管理员，再启动
docker compose up -d
curl http://127.0.0.1:8080/healthz
```

- 配置文件用 volume 挂载：改渠道不用重新 build
- SQLite 落在命名卷 `gateway-data`：`docker compose down` 不丢数据，
  `docker compose down -v` 才清
- 日志级别/保留天数也可用环境变量覆盖：
  `GATEWAY_LOG_LEVEL=debug`、`GATEWAY_LOG_RETENTION_DAYS=7`
- 容器内监听地址由 `GATEWAY_ADDR` 覆盖为 `0.0.0.0:8080`；端口默认只绑
  `127.0.0.1`（见 `docker-compose.yml` 的注释）
- Compose 显式设置 `GATEWAY_DATABASE_PATH=/app/data/gateway.db`，保证数据库位于命名卷。
  **旧部署若使用其他路径，不要直接切换：先停写、一致备份并恢复到目标卷。** 新环境变量不会自动搬迁旧库。

### 配置项

| 字段 | 默认 | 环境变量 | 说明 |
|------|------|----------|------|
| `server.addr` | `127.0.0.1:8080` | `GATEWAY_ADDR` | 监听地址（容器内必须 0.0.0.0） |
| `server.retry_times` | `2` | — | 首次失败后最多再试几次；`0` 表示不重试 |
| `server.timeout_seconds` | `120` | — | 单次上游请求超时（含完整流） |
| `server.total_timeout_seconds` | 同单次超时 | — | 全部重试共享的总时间预算，1–600 秒 |
| `server.ip_rpm_limit` | `0`（不限） | — | IP 级限流，防无令牌的探测请求 |
| `admin_key` | 必填 | `GATEWAY_ADMIN_KEY` | 管理 API 密钥，缺失拒绝启动 |
| `log_level` | `info` | `GATEWAY_LOG_LEVEL` | `debug`/`info`/`warn`/`error` |
| `log_retention_days` | `30` | `GATEWAY_LOG_RETENTION_DAYS` | `logs` 表保留天数，`0` 关闭清理 |
| `database.driver` | `sqlite` | — | 目前只支持 sqlite |
| `database.path` | `data/gateway.db` | — | SQLite 文件路径 |
| `database.path` 覆盖 | — | `GATEWAY_DATABASE_PATH` | 部署环境优先路径；Compose 固定在数据卷内，不自动搬迁旧库 |
| `channels[].key` | — | `GATEWAY_CHANNEL_KEY_<id>` | 渠道上游 Key，环境变量优先 |

> **环境变量优先于 YAML** 是全局规则：部署时改配置不用重新打镜像/改文件，
> 密钥尤其应该走环境变量（不进 git）。

HTTP 请求体上限为 20 MiB，超限请求返回 `413`。

> **渠道和令牌不是运行期配置**：`channels`/`tokens` 段只在**数据库为空时**
> 导入一次（种子数据），之后一律通过管理 API 增删改，改完立即生效、不用重启。

---

## API 一览

### 转发面（客户端用，Bearer 令牌鉴权）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/chat/completions` | 对话补全（支持 `stream: true`） |
| POST | `/v1/embeddings` | 文本转向量（只按输入 token 计费） |
| GET | `/v1/models` | 该令牌可访问的模型清单（客户端用来填下拉框） |

### 管理面（`/api/*`，`X-Admin-Key` 头鉴权）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/channels` | 渠道列表 |
| POST | `/api/channels` | 建渠道 |
| GET | `/api/channels/:id` | 渠道详情（已有上游 Key 始终掩码） |
| PUT | `/api/channels/:id` | 改渠道 |
| DELETE | `/api/channels/:id` | 删渠道 |
| GET | `/api/channels/:id/credentials` | 凭据列表（含健康状态） |
| POST | `/api/channels/:id/credentials` | 加一把 Key |
| PUT | `/api/credentials/:id` | 改凭据（权重等） |
| DELETE | `/api/credentials/:id` | 删凭据（最后一把会拒绝） |
| POST | `/api/credentials/:id/status` | 手动重置 active / 解除拉黑 |
| GET/POST/PUT/DELETE | `/api/tokens[/:id]` | 令牌 CRUD |
| GET | `/api/logs` | 请求流水（支持按渠道/令牌/模型过滤 + 分页） |
| GET | `/api/stats/...` | 用量统计（汇总/按模型/按渠道） |
| GET/POST/PUT/DELETE | `/api/prices[/:model]` | 价格表 CRUD |
| GET | `/api/abilities` | 能力表（模型 → 渠道，路由索引的数据源） |

### 运维面（免鉴权）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/healthz` | 健康检查（不依赖数据库和上游，给探活用） |
| GET | `/metrics` | Prometheus exposition 格式指标 |

**指标族**：

| 指标 | 类型 | 标签 |
|------|------|------|
| `gateway_requests_total` | counter | `model,channel,status` |
| `gateway_request_duration_seconds` | histogram | `model,channel`（桶 0.05~60s + `+Inf`） |
| `gateway_tokens_total` | counter | `model,channel,type`(prompt/completion) |
| `gateway_cost_total` | counter | `model,channel` |
| `gateway_credentials` | gauge | `channel,status`(active/cooldown/banned/disabled) |
| `gateway_credential_failures_total` | counter | `channel,credential` |
| `gateway_attempts_total` | counter | `channel,status,termination` |
| `gateway_attempt_duration_seconds` | histogram | `channel` |
| `gateway_log_queue_depth` / `gateway_log_queue_capacity` | gauge | 无标签 |
| `gateway_logs_dropped_total` / `gateway_logs_failed_total` | counter | 无标签，普通访问日志丢弃/最终写入失败 |

> `/metrics` 和 `/healthz` **不鉴权也不限流**。理由：抓取方是 Prometheus，
> 它没有也不该有客户端令牌；指标只有聚合计数和标签（模型名/渠道名），
> 不含请求内容、不含任何 Key。要在公网暴露时，正确做法是在反向代理层按
> 来源 IP 放行，而不是在应用层加一套监控专用鉴权（那意味着密钥进了监控
> 系统的配置，暴露面更大）。完整理由见 `internal/server/server.go` 的注释。

**日志级别**（`log_level` 或 `GATEWAY_LOG_LEVEL`）：

| 级别 | 内容 |
|------|------|
| `error` | 网关自身故障：panic、结算失败、额度预检失败 |
| `warn` | 上游返回错误、请求被拒（4xx/5xx）、限流触发、渠道装载跳过 |
| `info`（默认） | 请求摘要一行一条 + 启动/关闭/清理等生命周期事件 |
| `debug` | 上述全部 + 选凭据细节（渠道/凭据/权重/会话/第几次尝试）、冷却判定、上游原始用量 |

统一字段（info 及以上的请求日志都带）：`request_id` `token_id` `channel_id`
`credential_id` `model` `latency_ms` `status`。

---

## 设计决策

### 1. 渠道与凭据分离

「渠道」是上游服务商的连接信息（base_url、协议类型、模型列表），
「凭据」是该渠道下的一把 Key。一个渠道可以挂多把 Key，构成**凭据池**。

为什么分开：实际使用中同一个服务商通常有多个 Key（多个账号、多个额度包）。
如果「一个渠道 = 一个 Key」，就没法表达「这 5 把 Key 是同一家的，应该一起
做负载均衡」，也无法在 Key 级别做故障隔离。

代价是调度复杂了一层（选渠道 → 再选渠道内的凭据），收益是单渠道挂 3 把 Key
真的等于有 3 份容量——而不是配了不用。详见 [docs/PLAN.md](docs/PLAN.md)。

### 2. 规范格式枢纽

协议转换走「客户端协议 → 规范格式 → 上游协议」两段式，而不是 N×M 个直连
适配器。

新增一个上游协议只需要：实现规范格式 → 该协议的反向转换。已支持的客户端
协议自动获得它，已支持的上游协议也自动能被它调用。N+M 而不是 N×M。

客户端协议当前固定 OpenAI（网关只暴露 `/v1/*` OpenAI 兼容入口）；上游协议
已支持 openai / anthropic / gemini。

### 3. 请求体透明透传

请求体只解析 `model`/`stream`/`max_tokens` 三个字段，其余**原样转发**。

为什么不用结构体完整建模：LLM 请求参数在快速演进（reasoning、tools、
response_format…）。用结构体反序列化再序列化，任何未声明的字段都会被静默
丢掉，用户会遇到「某个参数在网关后面失效了」这种极难排查的问题。
`max_tokens` 是唯一例外——预扣额度需要它算费用上限。

### 4. 原子额度扣减

额度检查 + 预扣发生在**转发之前**，结算在响应之后修正差额。

- 超额时直接回 403，一个字节都不发给上游——转发注定被拒的请求等于白烧钱
  （上游 Key 是按量收费的）
- 扣减走 gorm 的原子 `UPDATE ... SET used = used + ?`，并发下不丢更新
- 预扣→结算用 flag + defer 保证「无论 Relay 正常返回还是 panic 都要结算」：
  预扣了钱不结账，令牌额度会被永久占用（表现为「没用几次就 403」）
- 结算失败只告警不阻断：用户请求已经成功了，网关因为自己的账本坏了就报错
  是不讲道理的

### 5. 一致性哈希亲和

会话亲和通过 `server.session_affinity: true` 显式开启（生产默认关闭）；会话 ID（`X-Session-Id` 头，缺省用「用户 + 令牌 + 首条消息哈希」）经一致性哈希
定位到一把凭据，同一会话固定打同一把 Key。

**为什么重要**：上游按账号缓存 prompt，同一对话固定打同一把 Key 才能吃到
缓存命中——省钱又提速。这不是「避免上下文错乱」，是成本优化。

用一致性哈希而不是 `hash % N`：凭据增删时只有相邻的一小段会话会重映射，
不会整体重排。亲和目标不可用时在当前可用池重新映射；无会话或超过环容量时回退加权随机。启用前须在真实部署环境验证缓存命中率、延迟或成本收益，仓库回归不等于真实上游收益证明。

### 6. 可观测性零新增依赖

结构化日志用标准库 `log/slog`，Prometheus 指标**手写 exposition 纯文本格式**，
都不引第三方库。

取舍：项目从 P1 起就把第三方依赖压在 gin/gorm/sqlite/yaml 四个，每一个新
依赖都是供应链面和升级面；而所需指标全是「带标签的 counter /
histogram / gauge」，exposition 格式是个几十行的纯文本协议。手写版只覆盖
「本网关自己埋的点」，不需要官方库的 collector 注册表、默认运行时指标、
推送网关那些能力——等真要多协议暴露或上 Pushgateway 时再换，替换成本很低
（`internal/metrics` 由 gateway 与 server 消费）。取舍理由写在
`internal/metrics/metrics.go` 的头注里。

### 7. 注入而不是直接依赖

计费引擎、限流器、日志落库、凭据选择、健康反馈、渠道重载——全部由 `main`
在启动时注入，`internal/` 之间不横向 import 业务包。

`relay` 不知道 store 存在，`scheduler` 不知道 relay 存在，`metrics` 不知道
任何业务存在。这让「转发层保持纯粹」（换存储甚至改成外发日志都不用改
relay），也让单元测试能不注入就不注入（nil = 关闭该功能）。

---

## 测试

```bash
export PATH=$PATH:/usr/local/go/bin
make check            # build + vet + go test
make smoke            # 假上游 + 临时库，端到端验证 OpenAI 链路

go test -race -count=1 ./...
gofmt -l .            # 应无输出
```

测试覆盖计费端到端、凭据调度、限流、指标、日志清理、配置校验，以及 S0 的
管理鉴权、Gemini 增量流、逐请求转换状态、异常流终止、密钥脱敏和请求资源限制。
全量测试需使用 `go test -race -count=1 ./...` 执行；当前 S0 验收状态见开发计划。

`make smoke` 用 Python 标准库管理本地假 OpenAI 上游和临时数据库，验证启动、
鉴权、模型列表/映射、非流式与流式转发的完整文本/结束原因/用量，不调用外部
模型、不使用真实密钥。

> **为什么 `-race` 是硬要求**：凭据调度、限流计数、指标埋点全是并发路径，
> 而 Go 的 data race 在不开 race detector 时表现为「偶发的奇怪数字」——
> 亲和分流偏斜和 RPM/TPM 共桶缺陷都是这样逃过普通测试的。CI 里这条不能省。

---

## 文档索引

| 文档 | 内容 |
|------|------|
| [docs/COLLABORATION.md](docs/COLLABORATION.md) | 多团队目录边界、扩展接口、迁移与验收流程 |
| [docs/openapi.json](docs/openapi.json) | 可导入前端工具的 HTTP 契约，CI 检查路由/鉴权/DTO |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 目标架构：模块边界、管理员—用户模型、接口原则 |
| [docs/PLAN.md](docs/PLAN.md) | 开发基线与分阶段任务（S0–S5）、验收标准 |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | 构建、测试、配置和目录约定 |
| [docs/PLAN-P6.md](docs/PLAN-P6.md) | 已交付的 P6 内核补全方案：安全、调度、限流、可观测性 |

---

## 许可

MIT
