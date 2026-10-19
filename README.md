# TXAPI

一个按 **core 中转内核、业务层、统一兼容层** 分工的轻量 AI API 网关。管理员集中配置上游，用户用自己的 API Key 调用。默认形态为 Go 模块化单体、单二进制、单实例 SQLite，内嵌原生 ES Modules 管理/用户控制台。

## 当前能做什么

核对日期：2026-10-19。本次从已验收 `master/290ba5d` 整合保留分支，提供 R04a 本地账号命令和 R06c 账本展示；精确本地/CI/合并状态见 [整合记录](docs/history/branch-integration-20261019.md)。当前 [OpenAPI](docs/openapi.json) 为 **0.11.0 / 111 个操作**，数据库 schema v17。详细状态与后续门槛统一见 [开发计划](docs/PLAN.md) 和 [R00–R07 状态矩阵](docs/reference/reference-adoption-status.md)。

| 能力 | 已交付范围 | 配置与限制 |
|---|---|---|
| 身份与控制台 | 管理员/用户会话、用户权限、调用 Key 生命周期、配置保存/发布状态 | 会话与调用 Key 分开；调用 Key 摘要存储，仅创建时展示 |
| Chat / embeddings | Chat JSON/SSE、文本/function tools 往返、OpenAI-compatible embeddings | Chat 可转到已支持的 OpenAI/Anthropic/Gemini 组合；不支持的工具/转换显式拒绝 |
| 调度与准入 | 多静态 Key、权重/优先级、有限重试、健康、四级并发、用户 RPM | 亲和、路由诊断和 shadow 对比均可选；shadow 不控制真实路由 |
| Responses HTTP / 历史 | 原生 JSON/SSE、幂等、短期私有回放、加密历史、Conversations/items 与续链 | 默认关闭，legacy/disabled 可用，strict 拒绝；见 [HTTP](docs/reference/responses-http.md) 与 [历史](docs/reference/responses-history.md) |
| 原生后台 | 持久受理、原生提交/查询/取消、事件序号恢复、任务控制台与故障恢复 | 默认关闭，仅 disabled、自包含文本/函数工具；store=false 仍临时保留内容，见 [后台契约](docs/reference/responses-background.md) |
| 原生 WebSocket | 双端原生连接、单连接串行文本/函数轮次、逐轮授权、连接内最新父 ID 续链 | 默认关闭且仅 disabled；不含收费、持久重连或任意多路复用，见 [WS 契约](docs/reference/responses-websocket.md) |
| 用量与财务读取 | 原生缓存/推理 Token 明细、版本化售价报价、持久前台 HTTP 用量和个人账本读取 | 均有明确 opt-in/权限范围；观察与报价不等于入账，见 [用量](docs/reference/usage-history.md)、[报价](docs/reference/usage-quotations.md)、[账本](docs/reference/owner-billing.md) |
| 上游账号命令 | 本地授权/重新授权、元数据列表、单次刷新及停用，独立加密保存 | operator-only，不参与推理路由或自动刷新；候选契约和真实账号范围见 [指南](docs/reference/upstream-accounts.md) |
| strict 账务 | 定点价格版本、用户/Key 双预留、持久结算、对账和强杀恢复 | S5 仍为 5/6，完整厂商成本上界未完成，原生 Responses/后台/WS 收费尚未开放 |

A1–A4、S6/M1、FC-001 四层地基与 A01/A02/M2 已验收合并；FC-002 与 R03 只完成各自明确的后台/WS 子集。完整 S5、A03–A06 和 R00–R07 目标仍开放。账号授权工具仅提供本地运维命令，尚未成为推理凭据；支付、媒体、分布式和外部 SDK/Runtime 不属于已交付能力。

已有受控官方 Python SDK、假上游和 CI 证据不代表真实 Provider、OAuth、SDK WS 或所有客户端兼容，也不代表新版本发布、镜像发布或生产收费就绪。

## 最终框架

产品职责划分为中转内核、业务层和兼容层；生产依赖遵守已验收的 **L0 内核、L1 基础框架、L2 兼容适配、L3 功能** 四层契约。L0/L1/L2 v1 已冻结，功能通过 [登记公开入口与独立接入样例](docs/reference/foundation-integration-v1.md) 扩展。

```text
客户端 / 控制台
       |
兼容层：HTTP / SSE / WS、操作与上游编解码、应用 API
       |
业务层：身份 -> 准入 -> 路由 -> attempt -> 状态/账务 -> 收尾
       |
core HTTP / WS 执行与传输 I/O
       |
上游服务

支撑与组装：store / secrets / config / cmd/gateway
```

连接、生成轮次和持久任务分别拥有有界寿命；各入口复用同一授权、准入、执行与账务权威。core 不承载用户、会话或收费策略。完整职责、扩展规则与剩余目标见 [架构](docs/ARCHITECTURE.md) 和 [活动计划](docs/PLAN.md)。

## 运行与使用

- 语言基线：Go 1.23；Go module/import 名仍为 `minigateway`。
- 存储：SQLite；没有已交付的 PostgreSQL/Redis 或多实例实现。
- 前端：原生 ES Modules，`go build` 时嵌入；运行不需要 Node、CDN 或前端构建服务。
- Python 3.10+、Node 20+、Make 用于相应开发验证，不是网关运行依赖。

首次安装按以下顺序操作，命令、环境变量、容器和 Windows 说明见 [开发指南](docs/DEVELOPMENT.md)：

1. 复制 `configs/gateway.example.yaml` 到被 Git 忽略的个人 `config.yaml`，配置监听地址、数据库与管理密钥；清理不需要的示例渠道/令牌。
2. 构建入口为 `go build -o bin/gateway ./cmd/gateway`，不是仓库根目录；也可使用 `make init` / `make build`。
3. 通过 `TXAPI_ADMIN_USERNAME` / `TXAPI_ADMIN_PASSWORD` 与 `-init-admin` 一次性初始化管理员，不传命令行明文密码；旧管理 API 仍要求单独的 `GATEWAY_ADMIN_KEY`。
4. 正常启动，访问根路径 `/`；管理员配置上游并创建用户，用户登录后创建调用 Key，再调用可用模型。

配置、密钥和数据库不提交；实际数据库路径取决于配置/环境和工作目录，示例路径不是自动迁移规则。已有部署先核对实际路径并备份，不清空数据库“修复”。

控制台会话仅保存在页面内存，刷新需重新登录；测试页使用用户输入的调用 Key，会实际消耗上游资源。配置发布状态见 [控制台与发布说明](docs/DEVELOPMENT.md#s4-控制台与配置发布)。

### 计费与安全边界

`legacy` 是兼容模式，不具备 strict 的持久预算保证；`disabled` 显式关闭计费但不关闭权限/准入；当前 `strict` 为尚未完成 S5 验收的受限模式。strict 只允许已登记官方输入档案的受限文本 Chat（`n=1`、单一显式总输出限制与默认服务等级）与文本 embeddings，缺价或未知计价维度拒绝。S5-C 已增加重复 JSON 键/非文本输入拒绝、转换后输出限制检查，以及独立上游成本契约和累计重试预算；`max_completion_tokens` 暂不支持跨协议转换。strict 执行须显式配置精确目标的上游契约与档案，无契约或无档案不发送；每次发送前原子记录保守上限，失败/未知尝试不退还重试预算。输入上限须满足已登记档案，独立最高价格及收费假设仍须核实；当前没有内置已验收的完整生产成本契约，也不具备可靠 tokenizer。用户收费封顶不等于厂商绝对成本保证；缺失/异常用量保留 pending，不视为免费。细节见 [S5 运行约定](docs/DEVELOPMENT.md#s5-定点价格与严格结算首批)。

发送前还会用请求固定的**售价与目标分组倍率**计算声明输入/输出包络的收费上界；用户预留不足或计算溢出时返回 400 `billing_fee_limit`，不因上游价便宜而放行。重试逐目标检查；没有执行就释放双占用，先前未知执行则继续 pending。此守卫依赖契约边界成立，不替代真实模型计量证明。

strict 会复核转换后的最终正文，而非只检查入口：保留三协议文本计价白名单检查，但通过白名单不等于目标获准执行，仍必须通过输入档案门槛。工具/媒体、显式缓存或 thinking 配置、非默认服务等级及未知字段会在 attempt/网络调用前被拒绝。embeddings 不能在转换后变为 token 数组或混合批次。拒绝原因保留在管理员账本，不反馈上游健康；legacy/disabled 的透传行为保持。该白名单不证明模型默认推理、缓存或真实 Token 计量边界。

strict 执行现已强制使用[已登记的输入边界档案](docs/reference/input-bound-profiles.md)：没有档案的旧契约仍可加载以支持历史读取与恢复，但新尝试返回 503 `provider_input_unproven`，不发上游、不反馈健康、不继续换凭据。当前可执行范围仅为档案登记的官方 OpenAI Chat/embeddings 目标；Anthropic/Gemini、兼容代理及未登记模型在 strict 下不再仅凭运营声明放行，legacy/disabled 的兼容路径不变。档案核对真实 URL、正文模型和整请求包络；最终正文输出限制必须与占用成本时的限制完全一致。发送还须通过[费率档案与凭据绑定](docs/reference/provider-tariff-profiles.md)：显式标准 USD 档案、费率不得低于已登记下限，凭据 ID 与实际发送的上游 Key 摘要必须匹配。缺费率档案返回 `provider_tariff_unproven`，换 Key/新凭据或实际鉴权头不匹配返回 `provider_credential_unproven`；已核对的标准价下限不替代逐凭据最高适用价、失败收费和未来变更依据，完整 S5 仍未验收。

普通访问日志可丢弃且会过期，页面用量只是日志聚合，不是权威余额或恢复账本。客户端调用 Key 已摘要存储；上游 API Key 支持挂载密钥的静态加密，生产安全基线必须启用，durable runtime 强制核对 store 已加密。HTTP 明文兼容模式不计入该安全验收；管理掩码不是存储保护。意图/输出内容加密与私有 sealed 字节游标已交付，S7-A02 加密历史及 FC-002 disabled 后台/原生事件子批已独立验收；后台 store=false 仍按声明临时保留恢复内容。密钥轮换、旧备份/WAL/内存等限制见 [密钥约定](docs/reference/secret-storage.md)，公网仍须 TLS 与边界防护。

## HTTP 契约与目录

唯一机器可读契约为 [OpenAPI 0.11.0](docs/openapi.json)，共 111 个操作。路由、权限与 DTO 一致性进入 CI；A02 的 0.4.0 / 91 操作是历史验收基线。版本与操作数不表示覆盖完整官方协议。

| 入口 | 当前用途 |
|---|---|
| `/v1/models`、`/v1/chat/completions`、`/v1/embeddings` | 模型列表与基础推理，使用调用 Key |
| `POST /v1/responses` | 可选前台 HTTP/SSE 与 disabled 原生后台 |
| `GET /v1/responses` Upgrade | 可选 disabled 原生 WS，使用 Authorization 调用 Key |
| Responses 历史/Conversations/items 路由 | 已存历史、检索/删除与续链，owner/Key 隔离 |
| `/api/auth/*`、`/api/admin/*`、`/api/me/*` | 会话、管理及本人资源；新增读取/控制操作以 OpenAPI 为准 |
| 旧 `/api/channels` 等 | 兼容期管理 API，使用独立管理密钥 |
| `/`、`/ui/*`、`/healthz`、`/metrics` | 控制台、静态资源、健康与指标 |

```text
cmd/gateway/           实例组装与有序关闭
internal/core/         标准库 HTTP / WS 单次执行与 I/O 事实
internal/foundation/   版本化公开契约与框架桥
internal/compat/       HTTP / WS / 管理表示与编解码门面
internal/gateway/      请求协调、路由、attempt、重试及统一收尾
internal/protocol/     操作/厂商转换与逐流状态
internal/generation/   加密意图/输出、领取/fencing、原生后台与恢复
internal/responses/    前台/幂等/历史/会话与后台应用
internal/responsesws/  原生 WS 会话及轮次应用
internal/transport/    独立有界 WS 传输
internal/billing/      价格、预算、结算、对账和报价
internal/store/        统一持久化与迁移
internal/server/       HTTP、管理与个人 API 注册
web/                   内嵌控制台
docs/                  活动计划、运行契约与历史证据
```

## 文档与开发顺序

| 文档 | 职责 |
|---|---|
| [开发计划](docs/PLAN.md) / [能力矩阵](docs/reference/reference-adoption-status.md) | 当前已验收范围、未完成代码与进入条件 |
| [架构](docs/ARCHITECTURE.md) / [地基接入](docs/reference/foundation-integration-v1.md) | 职责、依赖、公开端口、扩展与版本规则 |
| [开发指南](docs/DEVELOPMENT.md) / [协作约定](docs/COLLABORATION.md) | 命令、运行/恢复、目录与集成责任 |
| [贡献约定](CONTRIBUTING.md) / [代理约束](AGENTS.md) | 开发执行与验收规则 |
| [批次索引](docs/PLAN.md#批次与验收记录) / [吸收记录](REFERENCE-ADOPTION-PROGRESS.md) | 精确提交、失败、命令、CI 与验收边界 |
| [固定上游快照](docs/reference/upstream-snapshots.md) / [来源通知](THIRD_PARTY_NOTICES.md) | 历史阅读版本、实际复用来源和许可 |
| [保留分支整合](docs/history/branch-integration-20261019.md) / [账号命令](docs/reference/upstream-accounts.md) / [账本含义](docs/reference/billing-console.md) | 原 WIP 的差异取舍、恢复点、operator-only 账号与账本展示 |
| [分支与文档整理记录](docs/history/repository-cleanup-20261019.md) | 本地归档、保留工作树与本次整理范围 |

后续先收口正在进行的独立后端批次；成本证明、真实 Provider/客户端、前端恢复及外部 Runtime 分别按 PLAN 的进入条件推进。开发遵循“批量开发 → 集中验证 → 集中修复 → 合并”，不因局部编辑重复全量测试；最终合并保留完整 CI，纯说明文档只检查差异、链接与一致性。
