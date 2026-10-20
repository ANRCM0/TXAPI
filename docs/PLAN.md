# TXAPI 后端开发方案

更新日期：2026-10-20。本文是当前唯一开发方案，记录当前能力、剩余工作、架构约束和验收门槛。批次过程、精确提交与运行证据保留在对应 PR/提交记录，本文只保留必要索引，不另建平行方案或进度文档。第三方来源与许可证保留在文末。

## 目标与范围

建设可扩展的 AI 网关，保持 **Go/Gin 模块化单体、单二进制、单实例 SQLite**，通过已登记的 L0–L3 契约扩展功能。以核心转发正确、协议兼容、故障可定位、容量有边界为主线，优先从固定参考源码原生化实现，适配既有 owner、契约和生命周期。

标准 API Key 路线独立可用，不依赖账号池。保留现有内嵌 ES Modules 控制台与 API 回归，新前端、支付/套餐、媒体平台、多实例、PostgreSQL/Redis 和外部插件 Runtime 后置。基础权限、用量、对账、诊断与备份恢复随功能交付，不随商业页面后置。

真实 Provider/OAuth 按用户当前没有真实 Key 的指示跳过，不请求或发起付费调用；真实组合继续列为未验收。此限制不阻止后台父链等可独立实现的工作，也不构成收费许可。

## 当前基线与能力边界

已核对主线为 `master/b5519a5f7a2e802e9f18034a8d2f8a01861761ef`（PR #91）。最终功能头 `0d5f03f3e2148ce8a1efa1162c92ed349628a804` 的 push Verify `39401522023`、PR Verify `39401528001` 全六项成功；实际合并主线的独立 Verify `39404357439` 也已完整成功，包含 check、delivery、windows-resource-query。该功能头与合并主线的完整文件树相同。证据只覆盖对应代码与环境，后续修改另行验收；CI、合并、制品发布与生产运行分别记账。

[OpenAPI](openapi.json) 为 `0.13.14` / 117 操作，数据库 schema v21。接口数、测试数和已完成子批数不换算为项目完成百分比。

| 能力 | 已验收范围 | 当前限制 |
|---|---|---|
| 公共地基 | A1–A4、S6、FC-001；FC-002 原生后台子集 | 仅按明确批准范围冻结，后续候选入口不推断全量冻结 |
| 基础推理 | models、Chat JSON/SSE、embeddings、声明范围的文本/function tools；完整事件和原生用量 | 支持按操作、源/目标协议、传输和计费模式判断，不推断跨协议全量支持 |
| Responses HTTP | 独立模型、JSON/SSE、owner/Key 幂等、加密历史、Conversations/items、续链；声明范围的工具与不透明 reasoning | 默认关闭，legacy/disabled 可用，strict 全部拒绝 |
| 原生后台 | 持久受理、原生提交/查询/取消、事件序号恢复、fencing、强杀恢复、任务用量元数据 | 默认关闭，仅 disabled、自包含文本/function/custom 及单层 namespace 工具；后台父链和收费未实现 |
| 原生 WS | 双端原生连接、逐轮授权/准入、串行文本与声明工具；显式持久续链、HTTP/WS 跨模式、prewarm/reasoning、固定 SDK/Codex 与受控镜像回归 | 默认关闭且仅 disabled；默认只支持连接内最新父 ID，持久历史须显式开启；后台跨模式、收费、任意多路复用未完成 |
| 执行与路由 | 唯一执行事实、资源代次/排空、资格/优先级/权重/亲和/健康、诊断与 shadow；阶段超时、有限响应头和重放审计 | shadow 默认关闭且不控制路由；无准入等待队列，不代表外部 Runtime |
| 托管账号 | R04a 授权/加密/CAS；P1 单 SIWC 绑定文本流、P2 有界刷新、P3 资格反馈/人工恢复的受控闭环 | 无完整账号池；reset 未知，不自动推断恢复；真实 OAuth、过期/轮换和限流未验收 |
| 用量与计费 | Responses/Chat/embeddings 原生 Token 明细、不可变售价、定点价格、双 hold、幂等结算、对账/未知保护；WS 每轮明细和后台任务用量 | 缺失与部分用量保持未知；通用用量历史仅 disabled；后台/WS 收费未实现，S5-03 未完成 |
| 管理与交付 | 任务控制、路由诊断、配置发布、个人财务/账本读取、占用指标；固定客户端、实际镜像安装/升级/备份恢复回归 | 受控环境通过；真实 Provider 部署、生产告警阈值、备份保管和运维演练未验收 |

### 当前剩余工作

| 工作 | 已具备的基础 | 未完成及下一步 |
|---|---|---|
| P6 后台父链与跨模式 | HTTP/WS 共用加密历史、持久续链及重启恢复 | 后台仍拒绝 `previous_response_id`/conversation；先实现 HTTP/WS 历史进入后台，再验证后台结果供后续生成续链，按各方向声明支持范围 |
| P7 后台/WS 收费 | 每轮 WS 明细、后台任务用量与状态元数据 | 逐生成预留、结算、取消/未知恢复和成本准入未接通；依赖各操作 P8 证明，继续仅 disabled |
| P8 完整成本证明 | 已登记 strict Chat/embeddings 档案、双预算与恢复账本 | 缺逐凭据最高适用费率、失败收费、税/附加项、未来改价假设和完整计量证明；S5-03 不能关闭 |
| P0 完整性能与承载 | HTTP/1.1、TLS/H2、受控反代、慢读/取消/过载恢复、一次 30 分钟 Windows 持续流 | 完整长时 soak、最大稳定容量、目标部署网络/SLO 和完整性能验收仍开放 |
| P4 真实兼容 | 固定 HTTP/WS SDK、Codex、工具和受控 Provider 回归 | 至少一个声明范围内的真实 Provider/客户端完整组合未验收；按当前指示跳过真实调用 |
| P1/P2/P3 真实账号生命周期 | 单 SIWC 文本流、刷新、资格反馈及人工恢复受控验收 | 真实 OAuth、过期、Token 轮换、账号限流和恢复缺外部证据；不扩展为完整账号池 |
| 生产可操作性 | 受控镜像安装/升级、停写备份恢复、任务/账本查询和占用指标 | 告警阈值、备份保管、生产网络及运维演练需目标部署环境验收 |

当前可独立推进的功能为 P6 后台父链，P0/P4 受影响回归随批次完成；P8 可独立补证据。P7 收费须等待对应操作的完整成本证明。P5 多账号评分、WS 池/多路复用和准入等待队列按需求与数据后置，不为对齐功能数量强行建设。

## 架构与契约

| 层级 | 责任与主要目录 | 依赖规则 |
|---|---|---|
| L0 | `internal/core` 及登记的机械执行 | 一次发送、读写、取消、背压、提交事实；不放用户/账号/路由/会话/账本策略 |
| L1 | access、gateway、billing、generation、scheduler、store、secrets、upstreamidentity | 持有唯一授权/准入、attempt、迁移、预留/结算、凭据和资源生命周期 |
| L2 | protocol、compat、server、transport、web | 协议编解码、错误/事件、HTTP/WS、展示；通过登记入口调用能力 |
| L3 | responses、responsesws、管理/查询用例 | 只消费登记的 L1/L2 公开端口，不依赖 L0、地基私有包或其他功能实现 |
| 组装根 | `cmd/gateway` | 构造、注入、注册、启动和关闭；不承载功能规则 |

精确层级和 owner 以 [layers.json](../internal/architecture/layers.json) 为准，公开类型/字段/标签/签名以 [public-v1.json](../internal/architecture/public-v1.json) 和[契约源码](../internal/foundation/contracts)为准。新增包先登记，不新增私有依赖或临时豁免；跨层测试不能掩盖生产依赖违规。

- FC-001 已冻结 admission、configuration、control、execution、identity、json、ledger、lifecycle、observability、responses、state、text 的 v1，以及 L2 `foundation/httpoutput/v1`、`compat/responses/v1`。L3 接收输出端口，不持有 HTTP writer。
- FC-002 追加冻结 `background/v1` 与 `responses/v2`，仅覆盖已验收的 disabled、自包含原生后台子集，不扩大收费许可。
- execution/v2/v3/v4、responsehistory/v1、background/v2、responsesws、routes、taskcontrol、ownerledger、quotation、usagehistory、upstreamauth 等后续入口只按既有批准范围消费；登记、导出、版本常量和 CI 均不等于全量冻结。
- 地基公开接口新增或签名、语义、权限、生命周期变化，先记录具体变更请求，列明原因、影响、消费者、迁移、兼容/回退与验证。2026-10-20 用户在当前完成本方案任务中已授权后续同类开发契约调整按最优方案直接实施、集中验证修复，不再逐次请求批准；其他任务仍按其明确授权。真实账号、预算与数据、收费许可和范围后置要求继续按各自约束判断。
- 新冻结必须具备文档/版本、契约测试、最小运行样例、扩展说明、基础约定、限制清单及独立上层消费者。L0 变化额外说明现有扩展点为何不足和上层迁移；本路线优先保持 L0 稳定。

## 运行不变量

1. **当前授权。** 每次生成，包括后台执行及每个 WS 轮次，重新核对可信身份、当前 Key/模型权限与准入；用户提交的 owner、余额或绑定值不是认证。会话登录与调用 Key 分离，跨 owner/Key 读取不可枚举。
2. **唯一执行权威。** 功能不自行发送 HTTP/WS，不另建迁移、刷新或账本流程。协议终态、生成终态、财务终态、发送事实和下游提交分别记录；提交后不改目标重试，未知执行不补零、不盲目重发。
3. **协议保真。** 同协议保持语义，跨协议仅开放已声明共同子集；Chat、embeddings、Responses 使用独立模型。无法表示的字段在发送前拒绝，不提高输出限制、不静默改变服务等级、不伪造完成。工具由客户端执行，不在网关建立任意工具执行器。
4. **独立寿命。** 连接、轮次、生成、会话、后台任务和 Provider 状态分别管理。后台断开只停止观察；WS 前台断开按当前轮次的取消/未知事实收尾。网络重连不能证明旧生成可重放。
5. **有界资源。** 正文、消息、事件、队列、连接、保留期、退休代次均设上限。配置先准备后原子发布，失败保留旧代次；退休资源排空，worker/连接取消并 join 后才能关闭共享 gateway/store。
6. **独立资金事实。** Token 允许未知、零和部分完整，不默认补零或推导总量。售价、估算 Provider 成本、实际账单、当前 hold、历史预留与最终收费分开；观察/日志不成为财务或恢复权威。

### 路由与用量

先过滤授权、模型、能力、绑定和凭据资格，再在有效优先级池选择。故障回退不能突破账号范围，资格变化在发送前复核。`route_diagnostics` 与 `shadow_route_scoring` 默认关闭；shadow 仅为有界前台 HTTP 对比，不改变真实优先级、权重、亲和、重试，样本不代表 Provider 总负载。

`usage_history` 默认关闭且仅允许 disabled，覆盖前台 Chat/embeddings/Responses 与原生 WS 每轮；不覆盖原生后台或 durable claimed 执行。查询用 transport 区分 HTTP/WS，用 protocol_status 区分协议快照与宿主终态；后台用量保存在任务元数据中，不重复写入通用用量历史。实例父记录上限 1..65536、owner 上限不大于实例、保留期 1..2592000 秒；零配置采用有限默认值，每调用预留有限 retry 子槽。预留/写入失败沿既有准入拒绝，不退化成可丢日志；记录数限制不是磁盘/WAL 字节保证。

### 账号与持久状态

R04a 命令为 `-upstream-account authorize|reauthorize|list|refresh|disable`，仅输出脱敏元数据，凭据复用既有 AEAD。loopback 授权使用 state/nonce/S256 PKCE 和签名身份/签发 client 校验。刷新遵循 claim → started → 单次网络交换 → revision/fence CAS；只有已证明未发出的尝试可退避重试，可能轮换但未存回的结果保持 uncertain 并要求重授权。现有托管账号不自动成为推理凭据。

历史绑定当前 owner、创建 Key 和模型权限，内容加密、CAS/TTL/容量受限。`previous_response_id` 与 conversation 明确上下文来源，原生父 ID 和本地全上下文不能重复叠加；只有完整可重建、重新准入和计价的上下文才允许显式重放。`store=false` 不变永久历史，原生后台仍按声明临时保存恢复内容。后台受理先持久化任务和适用准入/预留事实再确认；内部任务状态、公开协议状态和财务终态分别映射。事件序号先持久提交再公开，按 `sequence_number` / `starting_after` 恢复；事件过期、丢失或不可重建时显式报错。

迁移只追加版本，不修改已发布步骤。schema v21 升级前停写并一致备份；旧 schema20 二进制拒绝打开 v21，回退需恢复匹配的停写备份，不能删账号或改写账本/迁移修复。凭据/内容密钥、备份/WAL、内存和公网 TLS 的边界独立维护，管理掩码不构成加密。

## 严格计费边界

strict 当前仅放行已登记的官方 OpenAI 文本 Chat/embeddings 档案；Responses 全部拒绝，原生后台/WS 必须使用 disabled。Anthropic/Gemini、兼容代理、未知模型/计价维度不继承许可。legacy 不提供 strict 持久预算保证；disabled 只关闭计费，保留身份和准入。

`StartPreparedAttempt` 校验最终真实 URL、正文、输出限制和鉴权头，是唯一可执行成本准入入口。无最终正文的 `StartCostedAttempt` 不能开启新尝试。输入、费率或凭据证明缺失分别返回 `provider_input_unproven`、`provider_tariff_unproven`、`provider_credential_unproven`，不发送、不处罚健康、不换凭据绕过。

| 现有输入档案 | 实现约束，非远端最新保证 |
|---|---|
| `openai_embeddings_v1` | 官方根 URL、openai/embeddings；三个精确模型 `text-embedding-3-small`、`text-embedding-3-large`、`text-embedding-ada-002`；整请求输入包络至少 300000 Token，文本数组最多 2048 条，不接受 token ID 数组/混合输入 |
| `openai_chat_v1` | 官方根 URL、openai/chat、`gpt-4o-mini-2024-07-18`；输入包络至少 128000，`max_completion_tokens` 至多 16384；正文 `service_tier: "default"`，严格文本、`n=1`；工具/媒体及未登记缓存、推理、等级扩展拒绝 |

档案数字来自 2026-10-17 登记的实现约束，详见[输入档案](../internal/billing/input_profile.go)和[费用档案](../internal/billing/tariff_profile.go)。原核对来源为 OpenAI Create embeddings、GPT-4o Mini、Chat Completions、Priority processing、Pricing Standard 文档；修改适用范围时重新核对，不把旧快照或模拟上游当真实价格认证。

先证明不可变售价/目标倍率覆盖，再核对每次尝试和整请求累计 Provider 包络；缓存折扣不降低最坏预留，未知尝试继续占用，定点溢出或覆盖不足在发送前拒绝。异常实际用量保留 pending 与审计。当前仍缺逐凭据最高适用费率、失败收费、税/附加项、未来改价假设和完整计量证明，**S5-03 保持未完成**；事后用户收费封顶不等于 Provider 成本有界。

### 严格计费配置

以 [gateway.example.yaml](../configs/gateway.example.yaml) 为准。费率档案 `openai_standard_usd_2026_10_17` 使用 USD、显式 `credential_bindings` 和完整 Key 的小写 SHA-256，发送前核对实际 Bearer Header；更换 Key/ID 须重新核实并加载。内置价格下限不能证明账号最高适用价格。Provider 金额精度为 `1e-9`，用户售价采用独立账户单位，不自动换汇或相互比较；真实密钥、摘要清单和个人配置不提交。

## 后端开发路线

P0–P8 按可独立验收的切片推进，已验收子集保留，不重复立项。实现前记录具体契约变化，真实消费者、权限、协议、取消/释放、成本事实和交付回归同批处理；源码完成、本地检查、精确头 CI、合并和运行验收分别记录。

### P0：核心转发保真、稳定性与性能

L0 `internal/core` 只持有机械发送/读写事实；L1 gateway/scheduler 决定路由与重试，L2 protocol/compat/transport 决定协议与输出。已完成完整 SSE、阶段超时、有限响应头、传输自动重放修正、真实网络释放、TLS/H2/反代、并发/过载恢复与有限持续流子批。继续复用唯一 executor、资源代次和已批准 execution 端口，不先重写内核。

当前语义必须保留：

- 同协议按操作声明原始字节或语义保真；模型映射和原生 Responses ID 改写不宣称全字节透明。Chat/Responses 拒绝缺少事件分隔空行的尾终态，Chat 仍要求 finish 与完整 DONE。CRLF 规范为 LF，不支持裸 CR 分隔；完整事件、wire/data 缓冲有界，未知用量不补零。
- Anthropic/Gemini 的现有 HTTP Chat 转换先组装完整事件，支持首次 BOM、多行 data、CRLF 和 metadata error。Anthropic 要求 finish 与 message_stop；Gemini 保留 finish 后的 usage-only/重复 finish，以 EOF 或完整 DONE 收尾；缺分隔、畸形 delta 或终态后新内容不伪造成功。不扩大原工具/共同子集和 strict 范围。
- `server.http_stage_timeouts` 的 headers、first_effective_output、stream_idle、downstream_write 各自计时，配置键带 `_seconds`；0 关闭，非零为 1..3600 的整数。响应头阶段包含连接/TLS/上传；心跳、role、usage 和 finish-only 不算首有效输出，完整合法完成终态按协议规则处理。流空闲按完整事件推进，半截字节不续命；同步下游写入暂停首输出/空闲等待预算，写入有独立 deadline。不支持写截止的消费者在发送前拒绝，不遗弃读写 goroutine。阶段等待超时不重试、不处罚健康，未知成本占用保留。见[阶段配置](../internal/config/http_stages.go)与[网关实现](../internal/gateway/http_stage_timeouts.go)。
- `server.forward_upstream_metadata` 默认关闭。只允许有界、单值、可打印 ASCII 的请求 ID，以及 429/503 的 Retry-After；逐跳头、Connection 提名头和敏感头排除，本地元数据优先，attempt 间清除旧值。L0 只借用观察头，L1 选择最终 attempt。见[配置](../internal/config/upstream_metadata.go)与[输出政策](../internal/gateway/upstream_metadata.go)。
- 非空模型 POST 清除 `GetBody`，避免一次准入内被默认 Transport 隐式重发。Chat/embeddings 的 L1 Replayable 政策与 Responses NonReplayable 分开；提交后不换目标，`Dispatching` 不证明未执行，也不承诺 Provider 恰好执行一次。自定义 Transport 须单独审计。

后续验收沿五组矩阵开展，不以模拟 writer 代替真实 socket：

| 验收组 | 场景与判据 |
|---|---|
| 保真与兼容 | JSON、完整 SSE、CRLF/多行/注释、跨块 UTF-8、未知字段、交错工具、错误/incomplete/截断、gzip、超长事件；成功码和响应头按操作声明 |
| 及时输出 | 延迟响应头、只有心跳、慢首输出、间歇长流、终态后不关连接；分别测首字节、首有效输出、上游到下游完整事件延迟 |
| 背压与释放 | 真 socket 慢读/不读、断开/取消、取消风暴、总 deadline、关闭/退休；核对缓冲、goroutine/任务、FD、RSS 与排空后占用 |
| 故障与重试 | DNS/TLS/建连、失效复用连接、429/5xx、发送后断线、首输出前后失败、父链绑定；同时记录客户端结果、实际 attempt、发送/提交和未知费用 |
| 对照与容量 | 同一受控上游、载荷/节奏、资源限制、并发和 HTTP/TLS 条件；冷/热连接、JSON/SSE、单/多上游、持续负载、慢客户端；保留完成率、P50/P95/P99、CPU/RSS/分配/FD/连接复用 |

性能优化先建立相关基线，再在优化前规定量化预算；机械 executor 与完整网关分别测量。完整路径固定鉴权、计费、日志/持久化、亲和与重试，不关闭必需能力换成绩。使用重复轮次和配对样本估计增量，不直接相减两个独立 P99，不只统计成功请求；无法等价的项目与条件明确单列。

30 分钟子批的受控证据：干净提交 `13ee3bd1267a575a84ee858578a9e3920fc4155a`，16 worker、100ms 事件、disabled、Windows HTTP/1.1，持续 1801.1476 秒，完成 21975/21975 请求与 285675 个事件；每请求一次上游发送，排空 active=0，零协议/drain 失败。前后共 32 条协调取消、两次满额 429 零发送及 16 槽恢复通过。网关 CPU 平均约 0.0996 核，working set/private commit/handle 采样峰值为 47312896/80175104 字节/271，满足预先规定的各 256MiB、512 handles、0.5 核预算。前后 handles 为 221/238，保留 +17 事实，不据此声称无泄漏。实际运行脚本及生产代码与 PR #91 最终集成版本相同；细节见 PR #91。此结果只证明该受控场景，不关闭完整 soak、最大容量或生产网络验收。

### P1：最小托管账号推理

L1 upstreamidentity/gateway/scheduler 持有账号、资格与发送授权，L2 Provider 编解码，组装根只注入。已完成单 SIWC account/owner/call Key/实际 slug/channel/credential 绑定的受控 Responses HTTP/SSE 文本流，消费现有 upstreamauth 状态机，不另建 OAuth、存储或凭据缓存。

范围保持 disabled、自包含 input 数组、`store=false`、`stream=true`；不包含工具、父链、conversation、后台、WS 或媒体。仅官方固定 Responses/models 路径，校验资源/scopes、正常 TLS、禁止跨 origin 重定向，证明不足的旧账号须重授权。每次 attempt 发送前实时 AcquireAccess，绑定 revision；停用先提交则新 acquire 拒绝，已签发访问不推断可撤销。管理员会话与调用 Key 分离，不伪造 Key，不自动回落静态 Key 或重试绕过账号限制。

真实账号闭环仍未验收。后续扩展须分别声明 Provider、账号来源和操作，验证停用/重授权/刷新与发送竞争、过期/uncertain 禁发、旧 revision 和脱敏；不扩大 strict。

### P2：有界自动刷新

已交付 L1 upstreamidentity worker 与统一 lifecycle，CLI/worker 共用 claim → started → 单次交换 → CAS/uncertain。默认关闭；默认间隔 30 秒、到期前 600 秒、每周期最多 4 次，范围和约束见[账号配置](../internal/config/upstream_accounts.go)。扫描、分页、周期时间、Provider 并发/QPS、账号冷却和失败退避均有界，不新增等待队列；未提交的状态修复先于新工作，取消并 join 后关闭共享资源。

受控刷新/并发/CAS/关闭已验收；真实 Token 轮换、过期和 Provider 故障数据未验收。started/uncertain 不盲目重放，可能轮换但存回失败须重授权，不以刷新锁代替轮换安全。后续只按实际生命周期需求扩展，契约不足再记录具体变更。

### P3：账号资格与限流恢复

已交付 L2 Provider 分类、L1 持久资格反馈与管理员 CAS 人工恢复。区分凭据/权限/账号或模型限流/拥塞/网络故障；反馈绑定已签发 revision，旧反馈不处罚新凭据，成功响应不自动清除拒绝。reset 保持 unknown，不从本地估计伪造 Provider 窗口；人工恢复带版本，发送前 AcquireAccess 是当前权威资格。

待写反馈有界，持久失败按既有关闭政策处理；内存暂存与强杀窗口不宣称持久恰好一次。真实限流/过期/恢复未验收。后续自动恢复须有可信 reset、来源与新鲜度证据，模型限制不得扩大为整个账号限制，专属账号失败不得静默回落。

### P4：协议回归与转换诊断

L2 protocol/compat 负责独立流状态、编解码及有界/去重/脱敏诊断，L3 继续消费登记生成入口。固定 HTTP/WS SDK、Codex、同协议/转换 SSE、function/custom、单层 namespace、客户端 tool_search 加载及不透明 reasoning/prewarm 已按各子批声明验收；工具由客户端执行，不建立通用工具执行器。

真实 Provider 与实际客户端完整组合仍未验收。新增组合须列源/目标协议、操作、传输、字段/工具和固定客户端版本；验证工具交错、UTF-8 参数分片、delta/done 去重、ID/名称恢复、failed/incomplete、未知字段、部分 usage、背压/取消/断流。无法表示的字段发送前拒绝，不伪造完成；能力变化同步 OpenAPI 和已有消费者，不扩大财务/路由许可。

### P5：账号信号与调度评估

继续复用已有 scheduler/routes/shadow。当前唯一 active SIWC grant 的候选池为 1，shadow 比较要求 2..8 渠道；P3 没有可信窗口、剩余额度、等待或账号相对负载。多账号评分与真实调度后置，不为评分扩大账号池或猜测 reset。

现有 R00 有界采样和最高优先级池内静态比较保持；不足有效样本为 neutral，本地资格拒绝不伪装为 HTTP 结果，HTTP 成功/错误不推导账号窗口。满足前置数据后，再声明来源/新鲜度、饥饿/公平/分布、回退及效果标准，shadow 不直接控制生产路由。

### P6：续链 scope 与后台跨模式

已交付 L1 generation 共用的加密历史、CAS/TTL/容量、HTTP/WS 双向续链、新连接/重启及权限回归。WS 的 `persistent_history` 默认 false，显式开启且 `store=true` 才走持久历史；默认仍仅连接内最新父 ID。function/custom、单层 namespace、客户端工具加载和不透明 reasoning 仅在已声明的可重建范围保持。

**后台父链尚未实现。** 当前后台仍只受理自包含输入，拒绝 `previous_response_id`/conversation。下一功能切片先接 HTTP/WS 历史进入后台，再按独立方向验收后台结果进入后续生成。沿 generation 历史/后台 owner 与已登记 responsehistory/background 端口接线，不另建历史或发送链；缺契约时记录具体变更、迁移及回退。

验收须覆盖 owner/创建 Key/模型权限、撤销、父链 TTL/缺失、凭据/revision 绑定、存储失败、unknown、受理/事件提交/强杀恢复与重复上下文。原生父 ID 与本地完整上下文只能选一个可信来源，显式重放须可完整重建并重新准入/计价；后台观察断开不取消任务。不能把 HTTP/WS 已通过的证据直接扩展到后台。

WS 池和任意多路复用继续按需求后置；租借隔离、上限、重连和失败后的轮次可恢复性须独立证明，连接重建不等于旧生成可重放。

### P7：后台/WS 用量与逐生成收费

已交付 WS 每轮原生明细（通用用量历史 transport=WS）与后台任务元数据（独立任务 owner）。原生 Token 允许未知、零和部分完整，来源/完整度、协议快照与宿主终态分开；持久失败不静默丢弃，事件重放不重复记账，后台不重复写通用前台用量历史。对应 schema v20/v21 迁移只追加。

**收费未实现，后台/WS 继续仅 disabled。** 每个操作先完成 P8 成本许可，再接不可变报价、逐生成预留/结算、全部 attempt 成本和取消/未知恢复；不能按连接或事件次数收费，不因有 usage 就认定最终账单成立。复用 ledger/quotation/execution 和既有事务账本，公开契约、配置及迁移统一协调。

退出条件包含零/缺失/部分用量、断流/取消、重试、回执提交和结算前后强杀、跨 Key 并发、幂等与重复恢复；未证明组合发送前拒绝，不从前台 strict 恢复证据推断后台/WS 财务安全。

### P8：完整成本证明

L1 billing/gateway/store 持有唯一报价、预算、attempt 和账本权威。已具备现有 strict Chat/embeddings 输入/费率档案、双 hold、不可变报价、pending/审计及受控强杀恢复；S5-03 仍未完成。

逐组合绑定操作、模型、账号/凭据、最高适用价格、输入/输出/完整历史和工具定义、推理/缓存/等级等维度、价格版本与有效假设；分别证明用户收费上限、单次 Provider 成本和全部尝试累计上界。失败请求收费、税/附加项、途中改价、批量输入、有限重试、溢出、跨 Key 并发及未知 attempt 都须覆盖。内置标准模型 Token 价格下限不能证明凭据最高费率或真实账单。

可以独立研究并补证据；证明不足继续发送前拒绝，异常实际用量保留 pending，不因账号、协议、usage 或模拟测试完成而扩大 strict。Responses/后台/WS 逐项申请支持，只有各准许组合的完整证据成立才关闭 S5-03；此门槛不代表支付平台就绪。

## 集成验收与后续门槛

| 门槛 | 已有受控证据入口 | 仍开放的范围 |
|---|---|---|
| 强杀与恢复 | PR #80 [strict 财务边界](../internal/gateway/strict_crash_test.go)、[后台 dispatch/事件窗口](../internal/generation/background_runtime_test.go)、PR #64 [持久 WS 窗口](../cmd/gateway/responses_ws_crash_test.go) | 不扩展到后台/WS 收费或真实 Provider 受理/计费认证 |
| 跨模式与权限 | PR #62/#63 [HTTP/WS 历史与权限](../cmd/gateway/responses_ws_history_test.go)及后续工具子批 | 后台父链尚缺；仅支持已声明的完整上下文和单串行轮次 |
| 后端操作 | [实际 main 冒烟](../tests/smoke/local.py)、[任务/事件/TTL](../tests/smoke/background.py)、[用量持久化/隔离](../tests/smoke/usage_history.py)，PR #74/#75 占用指标 | 正文读取须有明确授权；生产告警、备份保管及运维演练待部署现场验收 |
| 镜像与固定客户端 | [安装/升级/备份恢复](../tests/smoke/delivery.py)、[原生 WS 镜像/TLS/Upgrade/重启](../tests/smoke/native_ws_delivery.py)、固定 SDK/Codex 回归 | 只覆盖固定版本与声明操作；任意客户端、真实 Provider、models/compact 等不继承支持 |
| 性能与承载 | P0 对照、TLS/H2/反代、慢读/取消/过载恢复及 PR #91 的 30 分钟运行 | 完整 soak、最大稳定容量、生产网络/SLO 未验收；采样峰值不证明无泄漏 |
| 真实 Provider/账号 | 当前按用户指示跳过 | 真实客户端闭环、OAuth、轮换、限流和实际费用未验收 |

### 已完成批次证据索引

索引用于定位具体范围与最终验收，过程日志、旧失败/重试和中间“待 CI”状态不在方案中重复保存。各 PR 的精确头与实际合并后结果分别判断，后续新头必须重新满足适用 CI 门槛。

| 范围 | 对应 PR |
|---|---|
| 完整 SSE 与跨 Provider 转换、串行基线 | #45、#46、#47 |
| 单 SIWC 推理、刷新、HTTP SDK、资格反馈 | #48、#49、#50、#51 |
| 真实 socket/TLS/H2/反代、阶段超时、并发、响应头、重放与 Windows 资源 | #52–#59 |
| WS 每轮用量、后台任务用量 | #60、#61 |
| 共用历史、持久 WS 与跨 HTTP、强杀恢复 | #62、#63、#64 |
| 实际 WS 镜像、固定 WS SDK/CLI HTTP、namespace/custom/tool_search/reasoning 与 Codex WS 镜像 | #65–#73 |
| 准入/WS/后台占用、H2 取消、默认 H2 镜像与 TLS 条件 | #74–#79 |
| strict 强杀恢复、双端 TLS/H2 配对、Chat 原生用量、embeddings 费用回执 | #80–#83 |
| 固定 HTTP SDK 后台、固定 new-api Chat SSE/JSON 对照及验收说明 | #84–#87 |
| embeddings 原生部分用量、SDK 向量格式、JSON TCP 条件 | #88、#89、#90 |
| 同进程 30 分钟持续流、资源预算与前后恢复 | #91 |

### 条件后置

准入等待队列只在容量/拒绝数据证明必要后单独设计 L1 admission 变更，明确队列上限、最长等待/总 deadline、取消、公平性、槽位只释放一次和关闭 drain；HTTP/WS/后台不自动共用。外部 Runtime/R07 须至少有两个可独立安装升级的真实消费者，并证明编译期注册不足，再确认进程信任、回调权限/副作用、制品验证、代次和升级/回退契约。完整账号池、更多 Provider、支付/套餐及新前端同样按明确需求排期。

## 开发与验证规则

每个任务限定可独立验收的批次，列明六项：层级/写入目录、公开依赖及版本/冻结状态、地基影响、接口契约/迁移/回退、测试与验收、对其他模块的影响。共享入口、注册、数据库模型、迁移版本、配置、OpenAPI 与 CI 统一协调。

执行顺序为 **批量开发 → 集中验证 → 集中修复 → 合并**：

- 批内先实现，按需编译；权限隔离、迁移、并发/释放和缺陷复现可提前定向测试。不因每次编辑、小提交或汇报重跑全量测试。
- 批次稳定后集中执行适用完整检查与行为回归；汇总修复，先复验失败项和影响范围。公开接口、依赖、环境或跨模块影响变化/不确定时重跑相关完整检查。
- 同代码/基线/环境已有证据可注明 SHA、CI、范围和理由后复用；历史绿灯不外推新代码，不反复细碎推送或手动 dispatch。
- 功能合并前通过现有完整 CI：依赖/公开契约、build、vet、格式、Linux 全量 race、冒烟和交付；保留测试、workflow 触发和合并门槛。
- 纯说明文档只查差异、链接和规则一致性；可执行脚本、配置、API 契约、CI 按实际影响验证。保留现有测试，只格式化本次修改的 Go 文件。
- 不覆盖用户或其他任务改动，不提交密钥/个人配置/数据库/日志/二进制/临时分析；真实调用须有明确授权。未运行、失败、待 CI 如实记录，仅达标后更新状态。

命令以 [Makefile](../Makefile) 和 [Verify workflow](../.github/workflows/ci.yml) 为准：`make check`、`make contracts`、`make smoke`、`make delivery`；适用协议组合另用 `make compatibility`。Windows 普通 Go 验证在当前 PowerShell 设置 `$env:GOARCH='amd64'`，race 由 Linux CI 验收。构建入口为 `go build -o bin/gateway ./cmd/gateway`；首次管理员使用 `TXAPI_ADMIN_USERNAME` / `TXAPI_ADMIN_PASSWORD` 与 `-init-admin`，正常启动使用 `-config`。`GATEWAY_ADMIN_KEY` 与会话/调用 Key 分离。可选 SDK 检查在独立虚拟环境安装依赖后运行 `tests/smoke/sdk.py`，只证明声明的场景。

## 参考项目与采用规则

后续开发优先参考已登记项目的具体源码并原生化，按 **固定来源 → 最小切片 → 适配既有 owner/类型/生命周期 → 真实消费者 → 集中验证** 推进。同一问题优先选择一份主实现，其他项目补边界用例；没有适用实现，或架构、权限、资金、生命周期和许可要求必须偏离时，记录具体原因和替代实现。不以分析报告、空端口或新增无消费者抽象替代交付。

固定 new-api 的共同 Chat SSE/JSON 路径已完成实际主进程 Windows 对照与独立 Linux CI，范围见 PR #85/#86/#90；其他三个项目仍只有源码分析，未运行其程序/测试。参考副本位于 `.local/references/`，不会随 Git/worktree 分发；不新增运行依赖、子模块或安装钩子。源码机制与参考项目测试不直接证明 TXAPI 性能、稳定性或真实 Provider 兼容。

| 项目与固定 SHA | 采用重点及开发落点 | 不能直接继承的假设 |
|---|---|---|
| QuantumNous/new-api `25372e30b1f94255b1f818aeaaf006f042572580` | P4：RelayKit 路线/质量/步骤、有状态流与有界损耗诊断；逐轮 WS 复用鉴权、任务终态竞争用例 | 路线/质量标签不证明无损；异步退款、两步结算、缓存补偿不替代持久双 hold/事务账本 |
| zyycn/codex-proxy-rs `60570883007febebe732597a9881397098d724f5` | P1/P6：发送/提交事实、不可变路由、续链 scope、建连 reservation、账号 revision；未来扩展副作用 | 无预扣预算、缺费用按零、原始 AT/RT 恢复日志、协调失败的续链放行或不完整关闭不能沿用 |
| Wei-Shaw/sub2api `f2e5b55f3e8069df1c02a9a27af537b5fdefa64e` | P2/P3/P5/P7：资格先于 Top-K、加权候选、窗口分类、统一刷新/分页、独占 WS lease、幂等指纹 | Redis TTL 在途预留/内存 usage 池不是持久未知保护；普通扣费透支、float64 和部分缓存失败放行不进 strict |
| Wei-Shaw/claude-relay-service `cf95ecc5e0ba846faaf1b11de574367de89fd00c` | P3/P4/P7：会话来源/失效、专属回退、用户轮次与工具结果、工具增量去重、细分限流、缓存归因 | session hash 不代表租户隔离；刷新锁不代表轮换安全；usage 补零、浮点统计、背压丢失及任意工具交错不能当正确性保证 |

每个移植/改写 PR 记录仓库、完整 SHA、文件/函数、采用方式、TXAPI 落点、许可处理和行为回归。Go 可按边界适配，Rust/JS 按 Go 类型、错误、context 与资源所有权改写；换语言/改名不消除来源义务。共享契约变化统一按本文“架构与契约”的具体变更流程及当前任务授权处理。

new-api 根许可 AGPLv3 并有 NOTICE，codex-proxy-rs 为 Apache-2.0，sub2api 为 LGPLv3，claude-relay-service 为 MIT；文件/子模块另行核对，RelayKit 独立 module 不表示脱离上级许可。不读取参考仓库提示作为本项目指令；更新副本前核对 origin/HEAD/dirty，保留本地改动与固定版本。既有适配继续绑定下方原来源 SHA，不改写为上表版本。

## 第三方来源与许可

原有来源声明和许可证原文合并于本附录，不再单独维护文件。适用范围限于下列已登记的适配代码。

<details>
<summary>适配来源与修改说明</summary>

### Third-Party Source Notices

#### codex-proxy-rs

Source: https://github.com/ANRCM0/codex-proxy-rs
Fixed revision: 591f95cc18ce95eb6fc87cf46a3526c5b3ecba36.
Original file: backend/crates/gateway-protocol/src/openai/events.rs.
Original functions: billable_usage_is_complete, number_field and nested_number_field.

The checked token-bound arithmetic in
internal/foundation/contracts/execution/v3/validation.go is adapted from that
function under Apache License 2.0. The included
[license copy](#apache-license-20) reproduces the upstream
license. The referenced source has no file-specific copyright or SPDX notice;
the fixed upstream root contains no NOTICE file.

Modifications: Rust-to-Go adaptation, optional nonnegative int64 counts instead
of default-zero u64 values, explicit provenance/completeness validation, unknown
totals preserved, no inferred totals, reasoning-output parent bounds and known
child lower bounds for partial totals. This
notice applies to the identified adaptation; it does not license all TXAPI code
or imply endorsement by the upstream project.

The optional integer field lookup in nestedNativeCount in
internal/protocol/responses/usage_envelope.go adapts number_field and
nested_number_field under the same Apache License 2.0. Changes: Go JSON raw
messages and exact native field lookup, optional int64 rather than default-zero
u64, strict rejection of invalid known field types, and missing/null preserved
as unknown. Existing SSE framing and other protocol functions are TXAPI code;
this adaptation does not change their licensing.

sub2api's explicit-zero field precedence informed a regression scenario only.
No sub2api implementation or additional dependency is copied by this batch.

#### P0/P4 Complete HTTP SSE Events

Primary mechanism reference: zyycn/codex-proxy-rs, fixed revision
60570883007febebe732597a9881397098d724f5,
`backend/crates/gateway-protocol/src/openai/sse.rs`, specifically
`SseFrame` and `SseEventDecoder::push_frames`: retain event wire separately
from parsed observations. This reference is Apache-2.0; the license copy below
is retained. The fixed root has no NOTICE file.

`internal/protocol/chat_sse.go` independently implements that separation with
TXAPI's existing line decoder and execution frames; no upstream function is
copied or translated. Unlike the reference's opaque oversize passthrough and
EOF tail delivery, TXAPI requires bounded complete events and its existing
finish/DONE validation. CRLF is normalized by the existing core Scanner.
`internal/protocol/responses/decoder.go` retains TXAPI's existing event decoder,
ID/error rewriting and native completion facts while fixing EOF and delimiter
limits. The private output-estimation consumers read complete events as well.

The sub2api complete-event flush and terminal-with-open-connection scenarios
in `backend/internal/service/openai_gateway_response_handling.go` at
f2e5b55f3e8069df1c02a9a27af537b5fdefa64e informed regression scenarios only;
no sub2api, new-api or claude-relay-service implementation is copied here.
Regression files are `internal/protocol/chat_sse_test.go`,
`internal/protocol/responses/sse_regression_test.go`,
`internal/protocol/sse_estimation_test.go` and
`internal/gateway/p0_sse_socket_test.go`. This notice does not expand protocol,
strict-billing or real-Provider acceptance.

#### codex-proxy-rs Account Authorization Mechanics

The R04a operator account tool also adapts selected mechanics from the same
fixed revision 591f95cc18ce95eb6fc87cf46a3526c5b3ecba36 (Apache License 2.0):

- `backend/crates/providers/openai/src/credential/oauth.rs`: random secrets,
  S256 challenge derivation, callback parameter uniqueness and constant-time
  state comparison, adapted in `internal/protocol/openaiauth/provider.go`.
- `backend/crates/providers/openai/src/credential/token_client.rs`: bounded
  form token exchange and fixed failure categories, adapted across the pure
  provider and the existing core-backed HTTP caller.
- `backend/crates/gateway-core/src/provider_ports/refresh.rs`: stable jitter
  and bounded backoff, adapted in `internal/store/upstream_accounts.go` only
  for proved not-dispatched refreshes.

Repository/refresh ownership in the reference informed the claim/CAS design;
TXAPI uses its existing SQLite runner and credential AEAD vault instead of
the reference's database stack. The implementation uses the current documented
dynamic registration and issued-client binding, verifies OIDC signatures and
identity, and never logs token material or retries ambiguous rotating-token
refreshes. This is an operator authorization tool, not an inference adapter.
The included license copy remains applicable; the fixed reference has no root
NOTICE. Other TXAPI code retains its existing licensing.

</details>

<details>
<summary>Apache License 2.0 原文</summary>

### Apache License 2.0

```text
                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

   1. Definitions.

      "License" shall mean the terms and conditions for use, reproduction,
      and distribution as defined by Sections 1 through 9 of this document.

      "Licensor" shall mean the copyright owner or entity authorized by
      the copyright owner that is granting the License.

      "Legal Entity" shall mean the union of the acting entity and all
      other entities that control, are controlled by, or are under common
      control with that entity. For the purposes of this definition,
      "control" means (i) the power, direct or indirect, to cause the
      direction or management of such entity, whether by contract or
      otherwise, or (ii) ownership of fifty percent (50%) or more of the
      outstanding shares, or (iii) beneficial ownership of such entity.

      "You" (or "Your") shall mean an individual or Legal Entity
      exercising permissions granted by this License.

      "Source" form shall mean the preferred form for making modifications,
      including but not limited to software source code, documentation
      source, and configuration files.

      "Object" form shall mean any form resulting from mechanical
      transformation or translation of a Source form, including but
      not limited to compiled object code, generated documentation,
      and conversions to other media types.

      "Work" shall mean the work of authorship, whether in Source or
      Object form, made available under the License, as indicated by a
      copyright notice that is included in or attached to the work
      (an example is provided in the Appendix below).

      "Derivative Works" shall mean any work, whether in Source or Object
      form, that is based on (or derived from) the Work and for which the
      editorial revisions, annotations, elaborations, or other modifications
      represent, as a whole, an original work of authorship. For the purposes
      of this License, Derivative Works shall not include works that remain
      separable from, or merely link (or bind by name) to the interfaces of,
      the Work and Derivative Works thereof.

      "Contribution" shall mean any work of authorship, including
      the original version of the Work and any modifications or additions
      to that Work or Derivative Works thereof, that is intentionally
      submitted to Licensor for inclusion in the Work by the copyright owner
      or by an individual or Legal Entity authorized to submit on behalf of
      the copyright owner. For the purposes of this definition, "submitted"
      means any form of electronic, verbal, or written communication sent
      to the Licensor or its representatives, including but not limited to
      communication on electronic mailing lists, source code control systems,
      and issue tracking systems that are managed by, or on behalf of, the
      Licensor for the purpose of discussing and improving the Work, but
      excluding communication that is conspicuously marked or otherwise
      designated in writing by the copyright owner as "Not a Contribution."

      "Contributor" shall mean Licensor and any individual or Legal Entity
      on behalf of whom a Contribution has been received by Licensor and
      subsequently incorporated within the Work.

   2. Grant of Copyright License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      copyright license to reproduce, prepare Derivative Works of,
      publicly display, publicly perform, sublicense, and distribute the
      Work and such Derivative Works in Source or Object form.

   3. Grant of Patent License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      (except as stated in this section) patent license to make, have made,
      use, offer to sell, sell, import, and otherwise transfer the Work,
      where such license applies only to those patent claims licensable
      by such Contributor that are necessarily infringed by their
      Contribution(s) alone or by combination of their Contribution(s)
      with the Work to which such Contribution(s) was submitted. If You
      institute patent litigation against any entity (including a
      cross-claim or counterclaim in a lawsuit) alleging that the Work
      or a Contribution incorporated within the Work constitutes direct
      or contributory patent infringement, then any patent licenses
      granted to You under this License for that Work shall terminate
      as of the date such litigation is filed.

   4. Redistribution. You may reproduce and distribute copies of the
      Work or Derivative Works thereof in any medium, with or without
      modifications, and in Source or Object form, provided that You
      meet the following conditions:

      (a) You must give any other recipients of the Work or
          Derivative Works a copy of this License; and

      (b) You must cause any modified files to carry prominent notices
          stating that You changed the files; and

      (c) You must retain, in the Source form of any Derivative Works
          that You distribute, all copyright, patent, trademark, and
          attribution notices from the Source form of the Work,
          excluding those notices that do not pertain to any part of
          the Derivative Works; and

      (d) If the Work includes a "NOTICE" text file as part of its
          distribution, then any Derivative Works that You distribute must
          include a readable copy of the attribution notices contained
          within such NOTICE file, excluding those notices that do not
          pertain to any part of the Derivative Works, in at least one
          of the following places: within a NOTICE text file distributed
          as part of the Derivative Works; within the Source form or
          documentation, if provided along with the Derivative Works; or,
          within a display generated by the Derivative Works, if and
          wherever such third-party notices normally appear. The contents
          of the NOTICE file are for informational purposes only and
          do not modify the License. You may add Your own attribution
          notices within Derivative Works that You distribute, alongside
          or as an addendum to the NOTICE text from the Work, provided
          that such additional attribution notices cannot be construed
          as modifying the License.

      You may add Your own copyright statement to Your modifications and
      may provide additional or different license terms and conditions
      for use, reproduction, or distribution of Your modifications, or
      for any such Derivative Works as a whole, provided Your use,
      reproduction, and distribution of the Work otherwise complies with
      the conditions stated in this License.

   5. Submission of Contributions. Unless You explicitly state otherwise,
      any Contribution intentionally submitted for inclusion in the Work
      by You to the Licensor shall be under the terms and conditions of
      this License, without any additional terms or conditions.
      Notwithstanding the above, nothing herein shall supersede or modify
      the terms of any separate license agreement you may have executed
      with Licensor regarding such Contributions.

   6. Trademarks. This License does not grant permission to use the trade
      names, trademarks, service marks, or product names of the Licensor,
      except as required for reasonable and customary use in describing the
      origin of the Work and reproducing the content of the NOTICE file.

   7. Disclaimer of Warranty. Unless required by applicable law or
      agreed to in writing, Licensor provides the Work (and each
      Contributor provides its Contributions) on an "AS IS" BASIS,
      WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
      implied, including, without limitation, any warranties or conditions
      of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A
      PARTICULAR PURPOSE. You are solely responsible for determining the
      appropriateness of using or redistributing the Work and assume any
      risks associated with Your exercise of permissions under this License.

   8. Limitation of Liability. In no event and under no legal theory,
      whether in tort (including negligence), contract, or otherwise,
      unless required by applicable law (such as deliberate and grossly
      negligent acts) or agreed to in writing, shall any Contributor be
      liable to You for damages, including any direct, indirect, special,
      incidental, or consequential damages of any character arising as a
      result of this License or out of the use or inability to use the
      Work (including but not limited to damages for loss of goodwill,
      work stoppage, computer failure or malfunction, or any and all
      other commercial damages or losses), even if such Contributor
      has been advised of the possibility of such damages.

   9. Accepting Warranty or Additional Liability. While redistributing
      the Work or Derivative Works thereof, You may choose to offer,
      and charge a fee for, acceptance of support, warranty, indemnity,
      or other liability obligations and/or rights consistent with this
      License. However, in accepting such obligations, You may act only
      on Your own behalf and on Your sole responsibility, not on behalf
      of any other Contributor, and only if You agree to indemnify,
      defend, and hold each Contributor harmless for any liability
      incurred by, or claims asserted against, such Contributor by reason
      of your accepting any such warranty or additional liability.

   END OF TERMS AND CONDITIONS

   APPENDIX: How to apply the Apache License to your work.

      To apply the Apache License to your work, attach the following
      boilerplate notice, with the fields enclosed by brackets "[]"
      replaced with your own identifying information. (Don't include
      the brackets!)  The text should be enclosed in the appropriate
      comment syntax for the file format. We also recommend that a
      file or class name and description of purpose be included on the
      same "printed page" as the copyright notice for easier
      identification within third-party archives.

   Copyright [yyyy] [name of copyright owner]

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
```

</details>
