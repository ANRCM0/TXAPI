# TXAPI

FC-002 顺序推进中：A03-01 已验收合并至 `master/c1b8264`；当前 A03-02 的 disabled 原生后台/查询/取消/终态删除和 OpenAPI 0.5.0 / 92 操作为候选，尚未验收。默认关闭，`store=false` 仍有临时内容保留，事件恢复另由下一批实现；不标记完整 A03/M3 或成本证明通过。见 [后台运行约定](docs/reference/responses-background.md) 与 [精确批次记录](docs/history/implementation-fc002.md)。

一个按**core 中转内核、业务层、统一对外兼容层**分工的轻量 AI API 网关。管理员集中配置上游，用户使用自己的 API Key 调用；业务层负责授权、调度、计费及状态处理，兼容层覆盖客户端/上游协议、扩展和前端接入。

默认形态是 **Go 模块化单体、单二进制、单实例 SQLite**，附带内嵌管理/用户控制台。不以复制所有上游项目的功能为目标，不预建微服务、组织租户或动态插件平台。

## 当前能做什么

用户已批准的 [FC-001 四层地基提案](docs/proposals/FC-001-foundation-contracts-v1.md) 已按 FND-01 至 FND-05 完成；代码基线 `master/889ecbd` 的完整 CI 通过，L0/L1/L2 v1 已冻结。上层按 [公开入口和独立接入样例](docs/reference/foundation-integration-v1.md) 开发；A1–A4/S6/M2 功能验收保留，后台/原生 WS 与 S5 成本门槛仍单独交付。活动排期见 [PLAN](docs/PLAN.md#地基符合性收口fc-001)。

2026-10-18，S7-A02 已完成独立验收：PR #12 将功能头 `2084bcd`（基线 `04fbfa6`）合并为 `master/765501a`，功能头 push/PR 与合并后 master 完整 CI 均通过。M2 的 HTTP/SSE 与持久会话已交付，OpenAPI 0.4.0 / 91 个操作；下一产品批次为 A03 原生后台任务，上述地基收口现已完成。运行、容量与权限规则见 [历史契约](docs/reference/responses-history.md)，精确提交和验证见 [A02 验收](docs/history/implementation-s7-a02.md#2026-10-18-s7-a02-functional-acceptance)。

2026-10-18，S7-A01 已完成独立验收：PR #10 将功能头 `3195d0923d957eb5f41dde5412912d10e72259f7`（基线 `bea97fa`）合并为 `master/a925140ab86fca780a71ef6956a45e0e6af38658`；push `39073160376`、PR `39073164111` 与合并后 master `39073532710` 均为 check + delivery 全部成功。精确修复、命令与范围见 [S7-A01 功能验收](docs/history/implementation-s7-a01.md#2026-10-18-s7-a01-functional-acceptance)。默认关闭的前台 Responses 在 legacy/disabled 可用；尚无已证明的 strict 子集，全部 Responses 在计费前拒绝。该段保留 A01 原验收范围；A02 现已由 PR #12 独立验收合并为 `master/765501a`，M2 已完成。后台/原生 WS 与 S5 成本门槛继续独立验收。

核对日期：2026-10-18，S6/M1 已验收并经 PR #8 合并为 `master/ca009c2`，功能头 `9a6eaa2` 的 push/PR 与合并后 master 完整 CI 均通过。精确提交与验证记录见 [PLAN](docs/PLAN.md) 和 [S6 批次记录](docs/history/implementation-s6.md)。A1–A4 的原验收基线 `master/ccbfe29` 和证据保留在 [基础层记录](docs/history/implementation-s5-a3.md#2026-10-18-a3-05a4-最终验收与共同基线)。

- 管理员初始化、登录、创建/停用用户，设置用户模型与限额；管理会话和模型调用 Key 分离。
- 用户创建、限制、吊销自己的 Key，查看本人可用模型、访问日志和日志聚合用量；调用 Key 仅创建时展示，服务端摘要存储。
- 使用 OpenAI 兼容的 Chat Completions JSON/SSE 和 embeddings；Chat 可转到 OpenAI、Anthropic、Gemini 上游。embeddings 不据此宣称支持全部提供商。
- 管理渠道与多上游 API Key 凭据池，按优先级/权重调度、有限重试、健康冷却/恢复；会话亲和是默认关闭的可选优化，不是持久会话。
- 执行实例/用户/Key/渠道并发限制、用户跨 Key RPM、Key RPM 与估算 TPM；查看请求/尝试指标和有界访问日志。
- 通过内嵌控制台操作配置；区分“已保存/已应用”，发布失败继续使用旧运行快照。
- 已合并定点价格版本、用户/Key 双预算预留、持久账本、幂等结算、管理员对账与强杀恢复切片；**完整成本上界仍未完成**。

| 范围 | 状态与边界 |
|---|---|
| S0–S4 与 F0 | 已有阶段验收并合并 master；F0 是初始团队接线基线，不是最终统一兼容框架 |
| 已合并功能基线 | 四层地基为 PR #18 / `master/889ecbd`，FND-01 至 FND-05 均经完整 CI/合并验收，v1 已冻结；保留 PR #12 的 A02/M2、PR #10 的 A01、PR #8 的 S6/M1 与 PR #6 的 A1–A4 原产品范围，不代表完整 S5/S7 产品完成 |
| S5 | 6 个验收项中 5 个通过，S5-03 未完成；实现已合并，完整阶段尚未验收，不承诺已具备可靠商业收费全部条件 |
| 统一兼容框架 | S6 已合并现有推理/管理 API、构造扩展与前端的契约闭环，以及真实 API/浏览器验收链；复用 A1–A4 共享业务链和默认关闭的加密持久 runtime。支持矩阵见兼容契约清单，M1 已完成验收，精确证据见 PLAN/批次记录 |
| Responses / 持久会话 / 后台任务 / WebSocket | 本轮主线必须交付；S7-A01 原生 Responses HTTP/SSE、持久幂等元数据和私有加密短期重放已验收并由 PR #10 合并为 `master/a925140`，默认关闭；GET/续链/会话已通过 A02 独立 CI 并由 PR #12 合并验收；后台/原生 WS 未实现，A01 短期重放仍不是历史权威 |
| 原生 Messages/Gemini 入站、订阅账号池、支付、分布式部署 | 不属于当前已交付能力；本轮不新增支付 |

现有假上游、SDK 模拟及 CI 记录不等于任意客户端/真实上游全兼容，也不等于已完成生产部署。每种能力分别记录“协议可转发、客户端实测兼容、strict 可计价”，不以品牌名称或路由存在代替验收。

## 最终框架

以下是目标分层，**不是所有层都已实现**：

```text
客户端 / 前端 / 扩展
        |
统一兼容层                                 <- S6 现有支持范围已实现并合并
  接入适配 / 应用契约 / 能力与错误映射
        |
业务层：统一协调 gateway 与业务服务
  授权 -> 准入 -> 调度 -> attempt -> 收尾
        |                      |
单次 HTTP 执行 core            独立 WS I/O    <- 产品 WS 执行接线待开发
        |
上游服务

上游协议编解码亦属于兼容层，由业务协调按操作调用
业务扩展：Responses 前台已验收合并 / 会话历史 A02 已验收合并 / 后台待开发
支撑与组装：store / secrets / config / cmd/gateway
```

- **core 中转内核**：当前以标准库执行一次 HTTP 尝试，负责连接/流式读写、超时、取消、背压及提交事实；业务策略通过外层协调。
- **业务层**：统一执行授权、限额、路由、计费、任务状态及收尾；HTTP、worker 和后续 WS 消费同一组业务规则。
- **兼容层**：按操作适配客户端与上游协议，提供公开 API、业务扩展和前端消费契约。协议、传输和页面保留各自子边界，前端经应用 API 使用业务能力。
- **扩展顺序**：先用现有推理、管理 API 和前端验收内核/兼容框架，再按明确的 API、客户端、配置或页面功能目标吸收同类项目能力；Responses、会话、原生后台和 WS 按独立批次接入。

完整边界、当前实现差距与状态型设计见 [架构](docs/ARCHITECTURE.md)。

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

普通访问日志可丢弃且会过期，页面用量只是日志聚合，不是权威余额或恢复账本。客户端调用 Key 已摘要存储；上游 API Key 支持挂载密钥的静态加密，生产安全基线必须启用，durable runtime 强制核对 store 已加密。HTTP 明文兼容模式不计入该安全验收；管理掩码不是存储保护。意图/输出内容加密与私有 sealed 字节游标已交付，S7-A02 加密历史已验收，后台/原生事件仍未交付。密钥轮换、旧备份/WAL/内存等限制见 [密钥约定](docs/reference/secret-storage.md)，公网仍须 TLS 与边界防护。

## HTTP 契约与目录

已验收 [OpenAPI 0.4.0](docs/openapi.json) 登记 91 个操作，包含 A02 新增的 11 个历史/会话操作；A01 历史版本 0.3.0 / 80 个操作与 S6 基线 0.2.3 / 79 个操作保留。路由、鉴权与 DTO 一致性进入 CI；版本不表示覆盖完整官方协议或已通过真实 SDK/Codex 验收。

| 入口 | 当前用途 |
|---|---|
| `/v1/models`、`/v1/chat/completions`、`/v1/embeddings` | 模型列表与现有推理能力，使用调用 Key |
| `POST /v1/responses` | 已验收原生 JSON/SSE 前台生成，使用调用 Key；默认关闭，strict 在计费前拒绝 |
| `/api/auth/*` | 登录/登出等会话操作 |
| `/api/admin/*` | 管理员用户、渠道、配置、价格与账本等操作 |
| `/api/me/*` | 本人资料、Key、模型与日志/用量 |
| 旧 `/api/channels` 等 | 兼容期管理 API，仍受独立管理密钥保护；新前端不使用 |
| `/`、`/ui/*` | 内嵌控制台与静态资源 |
| `/healthz`、`/metrics` | 健康检查与指标；部署访问策略见开发指南 |

```text
cmd/gateway/          实例组装与有序关闭
internal/core/        标准库单次 HTTP 执行
internal/gateway/     请求协调、attempt、重试、统一收尾
internal/protocol/    操作/厂商转换与每请求流状态
internal/access/      身份、权限与准入基础
internal/generation/  worker、加密意图/输出、持久领取和 sealed 字节游标
internal/responses/   A01 前台/幂等/私有重放；A02 已验收加密历史/会话/续链
internal/transport/   独立 WS 传输 I/O（无生产 WS 应用/上游接线）
internal/model/       持久化实体与中立端口数据
internal/scheduler/   渠道/凭据选择与健康
internal/billing/     价格、预算、持久结算与对账
internal/store/       持久化、迁移、快照与访问日志
internal/server/      HTTP、管理与个人 API
web/                  内嵌控制台
configs/              无真实密钥的配置模板
tests/                假上游、冒烟、协议与交付验证
docs/                 现行文档、历史验收与参考资料
.local/               被忽略的本地数据与参考仓库
```

WS transport 已位于 `internal/transport/websocket`；`internal/responses` 已有前台真实消费者，经端口使用统一 gateway 与 store，不直接依赖 GORM。新增目录须在真实功能批次评审依赖边界，不预建空模块。

## 文档与开发顺序

当前 Responses 配置与限制见 [Responses HTTP 运行约定](docs/reference/responses-http.md)，本批实现/验证/验收状态见 [S7-A01 记录](docs/history/implementation-s7-a01.md)。启用须挂载内容密钥并启用实际 store 凭据加密；默认关闭不改变已有部署。`store` 省略保持原生默认 true，true 时额外保存加密私有短期重放缓存；false 不存正文，完成后重试返回 409 而非再次执行。持久回执按 owner/Key 隔离，未知执行不会因 TTL 删除或自动重发；这些能力不等于 GET/续链/会话/后台/WS，也不改变提供商原生存储语义。

| 文档 | 职责 |
|---|---|
| [开发计划](docs/PLAN.md) | 当前状态、底层架构优先与后续功能补全的实施和验收顺序 |
| [架构](docs/ARCHITECTURE.md) | 最终分层、内核/框架/功能边界及当前差距 |
| [开发指南](docs/DEVELOPMENT.md) | 运行命令、配置、验证节奏、升级和恢复 |
| [协作约定](docs/COLLABORATION.md) | 团队目录、共享入口、契约与集成门槛 |
| [贡献约定](CONTRIBUTING.md) / [代理约束](AGENTS.md) | 开发执行规则 |
| [运行组件约定](docs/reference/generation-runtime.md) | worker、加密意图/输出、持久领取/游标、WS I/O 的端口/寿命/恢复 |
| [兼容契约清单](docs/reference/compatibility-contracts.md) / [S6 批次](docs/history/implementation-s6.md) | 三层实际消费者、支持/拒绝矩阵、扩展接入与精确功能/集成验收证据 |
| [阶段历史](docs/history/acceptance-s0-s5.md) / [S5–A3 批次](docs/history/implementation-s5-a3.md) | 原始提交、命令、CI、验收范围和合并事实，不另行排期 |
| [上游固定快照](docs/reference/upstream-snapshots.md) | 历史阅读的参考库 SHA、文件与复用边界；本地副本不随 Git/worktree 分发 |
| [早期 P6 方案](docs/history/plan-p6.md) | 已停止用于排期的历史设计，不作为现行缺陷或功能清单 |

A1–A4 底座和 S6 内核/兼容框架功能均已合并。S6/M1 已完成集成验收；S7-A01 已通过现有业务与兼容契约接入 Responses HTTP/SSE，并完成本批独立 CI/合并验收；A02 已独立验收合并，M2 已完成，后续为 A03 后台；不以历史 CI 代替新批次证据；同类项目能力按明确目标有选择地吸收。S5 成本证明单独收口，strict 开放仍遵守原有门槛。本轮不新增支付。

开发遵循“批量开发 → 集中验证 → 集中修复 → 合并”；不因每次编辑/汇报重复跑全量测试，最终合并仍需通过完整现有 CI。纯说明文档整理只验证差异、链接与一致性。历史 CI 范围和日期保留在验收记录中，不写成当前未提交文档已通过 CI。
