# TXAPI 后端开发方案

更新日期：2026-10-20。本文是当前唯一开发方案，统一记录实际能力、后端开发顺序、架构约束和验收门槛。批次验证证据写入 PR/提交记录，不再新增平行方案或进度文档；第三方适配声明与许可证保留在文末。

## 目标与范围

建设可扩展的 AI 网关，保持 **Go/Gin 模块化单体、单二进制、单实例 SQLite**，通过已登记的 L0–L3 契约扩展功能。当前第一目标是核心转发可靠、协议兼容、故障可定位和容量有边界；在这条主线上，以参考项目的具体实现为起点原生化交付，减少重复设计和泛化框架建设。

前端新功能暂缓，保留现有内嵌 ES Modules 控制台和 API 回归；后端接口变化仍同步 OpenAPI 与已有消费者。暂不新增支付/套餐、媒体平台、多实例、PostgreSQL/Redis 或外部插件 Runtime。标准 API Key 路线继续独立可用，不依赖账号池完成。

## 当前开发重点

| 顺位 | 注重什么 | 交付判据与对应批次 |
|---|---|---|
| 第一优先 | 转发正确性、真实兼容、稳定性与故障隔离 | P0/P4 同批推进：JSON/SSE/工具与终态正确，超时/背压/取消/重试可解释；先用现有标准 API Key 路线完成一个声明范围内的真实客户端/Provider 闭环 |
| 第一优先内的性能验收 | 网关增量延迟、流事件及时性、持续承载及过载恢复 | P0 建立首有效输出/事件延迟、完成率、CPU/RSS/连接/任务占用基线；优化实际瓶颈，不以单点 QPS 或减少必需校验换成绩 |
| 每批同步的底线 | 权限、用量/成本、诊断与可操作性 | 复用已有身份/账本和管理入口，落实请求→渠道/凭据/attempt/终态关联、拒绝原因、密钥撤销、配置回退、备份恢复和适用告警；P7/P8 只按已证明组合扩展，不为新页面另建运营平台 |
| 第二阶段 | 最小账号推理与必要生命周期 | P1 形成一个托管账号闭环，再按实际过期/限流/故障数据交付 P2/P3；账号池不是基础转发验收的前置条件 |
| 有需求和数据再扩展 | 真实多因素路由、持久跨模式续链、WS 池及新模式收费 | P5/P6/P7 分别立可验收子批；保留现有 disabled 限制，证明收益/语义/成本后开放，不为对齐功能数量提前建设 |
| 后置 | 支付、套餐、充值、营销、外部 Runtime、多实例及新前端 | 明确用户/收费/规模需求后另行排期；基础用量、对账与权限隔离不随商业页面后置 |

阶段完成以用户请求的完整结果和可恢复行为判断，HTTP 200、框架层数或账号数量不替代成熟度。先完成受控真实闭环和稳定运行证据，再扩大 Provider/协议/模式；不要求等全部账号、后台、WS 和商业能力完成才验证基础使用场景。真实调用仍须有明确账号、预算与数据授权，付费开放须满足对应成本和交付门槛。

## 当前基线与能力边界

最新已合并且完整通过CI的功能头为 `master/2d7f5ffce32b2a7f3a384ec483a7746571cacbc5`：双端TLS/H2基线PR #81最终头 `b0286bd70e2605a87eb707349d0a996ecf0cd4df` 已通过 push Verify `39374041346` 与 PR Verify `39374044882` 全部六项，PR测试合并文件树与该头相同；合并后独立 Verify `39375474361` 同样全部成功。strict强杀恢复PR #80合并后的 Verify `39373920654`、PR #79的 Verify `39370670996` 也已成功。更早证据保留对应PR；本批Chat原生用量另按最终精确头验收，不继承旧头绿灯。证据仅覆盖相应提交。下表记录已验收子集，未列入的组合不自动继承支持或计费许可。

| 能力 | 当前已验收范围 | 仍未完成或必须保留的限制 |
|---|---|---|
| 公共地基 | A1–A4、S6、FC-001；FC-002 原生后台子集 | 基础 v1 和 FC-002 已按批准范围冻结，后续候选入口不得推断全量冻结 |
| 基础推理 | models、Chat JSON/SSE、embeddings、声明范围的文本/function tools | 能力按操作、源/目标协议、传输、计费模式判断 |
| Responses HTTP | 独立模型、JSON/SSE、owner/Key 幂等、加密历史、Conversations/items、续链 | 默认关闭；legacy/disabled 可用，strict 全部拒绝 |
| 原生后台 | 持久受理、原生提交/查询/取消、事件序号恢复、fencing、强杀恢复 | 默认关闭；仅 disabled、自包含文本/function/custom及单层namespace工具；收费和后台父链未完成 |
| 原生 WS | 双端原生连接、逐轮授权/准入、串行文本/function/custom与单层namespace轮次、连接内最新父 ID | 默认关闭且仅 disabled；显式持久续链/跨模式、受控镜像TLS/反代/重启及固定WS SDK已通过精确头CI并合并；真实Provider部署、收费和任意多路复用未验收 |
| 执行与路由 | 唯一执行事实、原生资源代次/排空、资格/优先级/权重/亲和/健康、诊断与 shadow | shadow 默认关闭且不控制路由；无准入等待队列；不代表外部 Runtime |
| 托管账号 | R04a 授权/加密/CAS；P1 单 SIWC 绑定文本流、P2 有界刷新已完成受控验收；P3 资格反馈/人工恢复已合并 | 无完整账号池，reset 未知不自动恢复；真实账号未验收 |
| 用量与计费 | Responses 原生 Token 明细（Chat legacy 持久明细仍 unknown）、不可变售价报价、前台 HTTP 用量、定点价格、双 hold、幂等结算、对账/未知保护 | 用量历史仅 disabled；WS 每轮明细已通过精确头CI并合并；后台任务用量元数据已通过精确头CI并合并；后台/WS 收费未覆盖，S5-03 完整成本证明未完成 |
| 管理与交付 | 任务控制、路由诊断、配置发布、个人财务/账本读取，受控 SDK 与容器交付回归 | 真实 Provider/OAuth/Codex 完整组合未验收；固定 WS SDK 受控验收按本批记录，账号管理待后端能力 |

本批源码的 [OpenAPI](openapi.json) 为 `0.13.12` / 117 操作；数据库保持 schema v21，固定 Codex WS 与实际镜像组合及准入/原生 WS 占用指标已通过精确头 CI 并合并；Chat原生用量观察增量单列于下。接口数量、测试数量或子批通过不换算为项目完成百分比。S5、完整后台/WS 与真实组合验收继续开放，已完成的公共框架不重新立项。

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
- execution/v2/v3、responsesws、routes、taskcontrol、ownerledger、quotation、usagehistory、upstreamauth 等后续入口只按既有批准范围消费；登记、导出、版本常量和 CI 均不等于全量冻结。
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

`usage_history` 默认关闭且仅允许 disabled，覆盖前台 Chat/embeddings/Responses 与原生 WS 每轮；不覆盖原生后台或 durable claimed 执行。本轮 WS 增量已通过精确头验收，查询用 transport 区分 HTTP/WS，用 protocol_status 区分协议快照与宿主终态。实例父记录上限 1..65536、owner 上限不大于实例、保留期 1..2592000 秒；零配置采用有限默认值，每调用预留有限 retry 子槽。预留/写入失败沿既有准入拒绝，不退化成可丢日志；记录数限制不是磁盘/WAL 字节保证。

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

近期执行主线为 **P0 + P4 原生化交付 → 现有 API Key 路线的真实闭环/运行验收 → P1 最小账号闭环 → 按实际需要交付 P2/P3**。基础诊断、权限、用量/成本和容量验证随每批完成，P8 成本研究可独立推进。P5/P6/P7 按需求与前置证据排期；影响转发热路径的批次复验适用 P0 场景，不把后台工作强行等待 WS 池。共享契约、组装、迁移和配置统一协调。

| 批次 | 交付目标 | 前置条件与对应旧目标 |
|---|---|---|
| P0 | 核心转发保真、连接/流资源、阶段超时、重试和性能基线 | 固定版本源码对照已完成；运行/性能验收未完成，不先重写内核 |
| P1 | 最小托管账号推理消费者 | 选定 Provider/账号来源/操作；承接 R04b |
| P2 | 有界自动刷新 | 复用账号状态机；承接 R04c |
| P3 | 账号资格、能力与细分限流恢复 | 消费 P1/P2 状态；承接 R04d |
| P4 | 真实客户端协议回归、必要的转换诊断 | 可独立准备；已有兼容框架的增量 |
| P5 | 账号信号进入 shadow，评估真实调度 | 依赖可信 P3 事实；承接 R00 增量 |
| P6 | 续链 scope、持久父链/重连，按需 WS 复用 | 先确认状态与账号绑定；承接 S7-A03/A04 剩余语义 |
| P7 | 后台/WS 用量与逐生成收费 | 分操作接线，依赖对应执行事实和成本许可；承接 R05/S7 剩余收费 |
| P8 | 已支持操作的完整成本证明 | 独立启动，不等待前七批完成；承接 S5-C/S5-03 |

下列批次的功能/性能验收均未完成；源码分析不等于运行验收。计划更新不构成地基 CR 批准、真实账号调用授权或收费许可。每批确认范围后推进实现，满足退出条件才更新状态，候选能力不得按已有导出符号直接接入。

### P0：核心转发保真、稳定性与性能

- **层级/目录：**L0 `internal/core`、`core/websocket` 仅核对既有机械行为；L1 `gateway`/`scheduler` 持有路由与重试，L2 `protocol`/`compat`/`transport` 持有协议与输出；行为/基准场景位于现有测试目录。
- **依赖入口：**复用已批准 execution v1/v2/v3、lifecycle、httpoutput 及当前协议/操作端口；采用范围按各自批准记录，不新增第二条转发链。
- **地基影响：**先做观测、缺陷复现和基线；阶段超时、响应头、事件/提交或重试语义若需改变公开契约，先提具体 CR。L0 改动说明上层扩展为何不足，不以“性能优化”跳过批准。
- **行为/契约：**分别声明同协议原始字节、同协议语义保持和跨协议转换；模型映射、ID 改写、错误/终态、响应头白名单及安全排除项可解释。区分连接/TLS、响应头、首个有效输出、流空闲、下游写入和整请求 deadline；心跳不算有效输出，实际 HTTP 提交仍不可撤销。重试同时受操作重放许可、发送/提交事实、上下文绑定和全部尝试成本约束，底层传输自动重试也纳入审计。
- **验收：**从下方五类场景选择本切片适用范围，定向复现后原生实现，集中验证分片/终态/背压/取消/重试，不把全参考项目跑分作为缺陷修复前置。性能型改动先取相关直连/网关基线，再比较合并 Flush、连接参数或窄快速路径收益，分别报告首字节/首有效输出、事件延迟、完成率及资源；量化预算在首轮基线后、优化前写入验收记录，不事后挑阈值或仅统计成功请求。完整热路径测试包括真实 socket、TLS/HTTP 与反代，不能用内存 writer 代替网络表现。
- **协作影响：**P0 只管核心中转，不绑定账号池或收费功能完成；与 P1/P4/P6/P8 协调公开入口和热路径修改。未知执行不能因观察失败清零，允许重试也不代表上游恰好执行一次；性能不通过删校验、旁路鉴权/账本或放宽资源上限获得。

P0/P4 按功能切片交付：选定参考实现和行为差异 → 在现有 owner 原生接线/补缺 → 集中验证与修复。当前源码机制盘点完成，未运行参考程序、测试或同机性能比较。已明确的正确性缺陷可先定向复现并实现，不等待所有参考项目安装、完整跑分或大阶段分析结束；性能优化只要求先建立相关场景基线，完整验收矩阵逐批补齐。

| 验收组 | 必测场景与证据 |
|---|---|
| 保真与兼容 | 普通 JSON、完整 SSE 事件、CRLF/多行 data/注释、跨块 UTF-8、未知可保留字段、工具交错、错误/incomplete/截断、gzip、超长行/事件；成功码和响应头按操作声明，敏感/逐跳头不透传 |
| 及时输出 | 延迟响应头、只发心跳无业务、慢首输出、持续长流、间歇停顿、终态后不关连接；首字节、首有效输出、上游到下游事件延迟分别测量，不能以提前心跳缩短 TTFT |
| 背压与释放 | 真 socket 慢读/不读、客户端断开/取消、取消风暴、请求总超时、关闭/代次退休；检查缓冲上限、goroutine/任务、FD、RSS 和恢复后的占用，前台取消与后台继续各按契约 |
| 故障与重试 | DNS/TLS/建连失败、失效复用连接、429/5xx、写请求后断线、首输出前/后失败、半截流、父链绑定；同时记录客户端结果、实际上游 attempt 次数、发送/提交及未知费用事实 |
| 对照与容量 | 同一固定模拟上游、载荷/事件节奏、机器/资源限制、客户端并发和 HTTP/TLS 条件，分别比较直连与各项目共同支持路径；冷/热连接、JSON/SSE、单/多上游、持续负载、慢客户端，记录版本/配置、完成率、P50/P95/P99、CPU/RSS/分配/FD/连接复用 |

性能分别测机械 executor 与完整网关，完整路径固定鉴权、计费模式、日志/持久化、亲和和重试设置；必需能力不可为跑分关闭，不等价配置单列。用重复轮次和配对样本差值估计网关增量，不直接相减两个独立 P99。不同项目缺少共同操作或不能连同一受控上游时记录不可比，不伪造对等能力。现有 `BenchmarkExecutor` 只覆盖五种合成场景、5ms 上游延迟、内存 writer、并发 1/4 和整调用耗时，不能证明真实 TTFT、HTTP/2、网络背压或生产容量。

#### P0 真实网络与资源收尾子批

1. **层级与目录：**仅新增 `cmd/gateway/p0_network_lifecycle_test.go`，组装真实 store、owner/调用 Key、准入、production engine、gateway 与唯一 executor；不以测试内存 writer 代替网络。
2. **公开依赖：**消费现有 identity/admission/execution 和 server 组装入口；无生产端口、公开字段或冻结范围变化。
3. **地基影响：**无生产/L0/config/schema/收费修改；只验证既有总请求预算和取消行为，不能声称新增阶段超时已交付。
4. **行为与回退：**TLS client→真实 httputil reverse proxy→TLS production engine→TLS模拟上游逐跳验证 HTTP/2，中文SSE和唯一终态在上游保持连接时仍完成。另以原始 TLS HTTP/1.1 下游socket、1024字节接收窗口、不读或每20ms读1字节制造真实背压；反代到网关及上游仍为HTTP/2，2.5秒总预算小于12秒attempt预算。有限4流取消风暴检查满额请求在发送前429、所有已发送场景仅一次上游attempt、上游active及request/attempt/user/Key/channel租借归零，并用后续请求证明准入恢复。
5. **验证：**Windows Go1.26.3、GOARCH=amd64，三组定向测试通过（9.603秒）；测试生成内容有限，无无限生产队列。包级 vet 和 cmd/gateway 全部测试也已通过；Linux race/精确头CI另行记录。本批不是CPU/RSS/FD容量或性能排名，也不覆盖容器/NGINX、WS Upgrade及真实Provider；同机TLS反代测试不能代替实际部署证据。
6. **协作：**保留P0先前性能预算和完整矩阵；本切片不扩账号池、后台或收费，不关闭已明确后置门槛。无需数据回退。

首个完整网关基线入口为 `python tests/smoke/stream_baseline.py --self-check --samples 100 --output <外部报告路径>`。生产 main 使用独立临时 SQLite、测试 API Key 和 disabled，保留鉴权、模型权限、准入、路由、用量和日志；与同一模拟上游交错配对，HTTP/1.1 loopback、并发 1、每事件 5ms 配置节奏，heartbeat/role/8 段文本/finish/usage/DONE。分别记录响应头、首正文 byte、首有效文本、完整事件观察延迟及写入/flush 区间；事件延迟包含客户端调度/读取等待。绝对 deadline watcher 和上游 drain 失败均禁止成功计数，所有启动进入完成率分母。CI 仅执行有界 self-check + 20 对样本及报告日志，不以性能数字设门槛；完整原始报告在指定路径，CI 临时报告不作为永久原始制品。

2026-10-19 首轮 Windows 实测：生产源码基于 `705c72aec354f8e62a704e9bb40f1a870242eab4`，工作树含未提交 harness（报告 `dirty=true`，不是该提交 CI 证据）；Windows 11、16 逻辑 CPU、Python 3.14.3、Go 1.26.3 工具链，构建 GOARCH=amd64。直连/网关各 100/100 完成、各 100 上游 attempt、各 1300 完整事件，客户端各复用一个连接；测量期上游新增连接 100，结束活动请求 0，drain 失败 0。配对网关增量 P50/P95/P99：首正文 1.427/3.684/7.626ms，首有效文本 0.134/1.189/15.191ms；网关完整事件观察延迟上界 P95 0.633ms。100 请求的尾分位数仅为首轮描述，不能证明稳定尾延迟或生产承载；Windows CPU/RSS/FD/线程采样为 unsupported，Linux `/proc` 资源实现等待精确头 CI。配置 5ms 不代表 Windows 实际事件调度恰为 5ms，上游连接未全复用也单列保留，未优化或隐去。

后续同条件性能改动的前瞻预算先固定为：正常样本完成率 100%、每请求一个上游 attempt、drain 失败 0；配对首正文与首有效文本增量 P95 各不超过 5ms，网关完整事件观察延迟上界 P95 不超过 2ms。此预算用于下一轮同机重复对照，尚非本批通过声明或 CI 硬门槛；资源预算须先取得 Linux 可靠基线，P99 须扩大独立请求和重复轮次。TLS/HTTP2/反代、慢读/不读、持续并发/过载恢复、取消风暴及真实 Provider/客户端验收继续开放，P0 完整性能矩阵未完成。

本轮 P0 并发承载/过载恢复基线切片（源码及本地受控检查完成，精确头 CI 待验证）：

1. **层级/目录：**仅 `tests/smoke/capacity_baseline.py`、既有 `stream_baseline.py` 的有界 hold 场景、Verify 与本方案；运行生产 `cmd/gateway`，不修改转发热路径。
2. **公开依赖：**通过既有 HTTP API、测试用启动配置和进程生命周期消费现有鉴权、容量、路由、执行及 SQLite；复用既有完整 SSE 观察/绝对 deadline/上游 drain 测量，不增加公开端口或冻结声明。
3. **地基影响：**无 L0、地基契约、迁移和产品配置变更；无需新增 CR。单实例/用户/Key/channel 有限准入保持原实现，不旁路鉴权或改健康状态。
4. **行为/回退：**同机 loopback HTTP/1.1 明文，固定有限 worker/请求/轮次、每 worker 持久连接；直连与完整网关分阶段交替先后。先同时占满 Key 名额，额外请求必须429且上游 attempt=0；释放后每个 holder 完整终态且一次发送，再以保留全部尝试的有限 no-send429探测验证恢复。恢复成功也须只有一次发送，并再同时填满全部 Key 名额，检出只恢复一个空位的部分租借泄漏。每阶段请求完成后上游活动归零；正常请求失败保留样本并停止该 worker，计划/实际启动数同时报告，不选择性隐去失败。
5. **验证/验收：**并发1、4和16的有界回归、既有串行 self-check，Linux CI再采样进程 CPU/RSS/FD/线程；各阶段资源分别记录，峰值为50ms采样下界。记录首正文、首有效文本、完整事件延迟、完成率、吞吐/连接及恢复时间；并发分布差只做非配对描述，不套用已有并发1的配对性能预算。CI检查完整终态、一次发送、满额拒绝、drain和恢复，性能数值只报告。本切片不改优化参数，取得重复资源基线后再制定后续优化/容量门槛。
6. **协作：**报告写仓库外，记录源码SHA/dirty、两个harness摘要、环境和有限负载；CI不把临时报告当永久制品。Windows资源采样仍unsupported；有限closed-loop不宣称最大容量、长时soak、TLS/H2/反代承载或真实Provider验收。P0完整矩阵仍开放。

本轮 P0 Windows 资源采样补齐切片（精确头CI通过并合并，合并后CI另记）：

1. **层级/目录：**仅测试侧 `process_resources.py`、串行/并发基线、采样回归与 Verify；运行现有生产进程，不改 Go 热路径。
2. **公开依赖：**Linux 继续读取 `/proc`；Windows 通过 Python 标准库 ctypes 调用 Kernel32 的只读进程查询，申请有限查询权限；不引入 psutil、WMI、PowerShell 子进程或额外产品端口。
3. **地基影响：**无公开契约、数据库、依赖或产品配置变化；独立测试模块由两个既有 harness 消费，回退删除测试增量即可。
4. **行为/边界：**分开采集 gateway 与 harness/fixture PID。Windows 记录累计 user+kernel CPU、working set、进程生命周期 peak working set、private commit 和 executive handle 数；句柄不等同 Linux FD，private commit 不等同物理内存，Windows 线程/FD与Go goroutine仍未观测。Linux保持CPU/RSS/线程/FD含义。每次打开句柄均在finally关闭，harness自身句柄样本包含本次临时查询句柄。按进程创建标识隔离PID重用；失败/不支持保留状态、原因和次数，不能补零或静默丢失。50ms峰值只含已成功采样的已声明指标，含前后端点；生命周期峰值单列，不当作阶段峰值。
5. **验证/验收：**Windows受控子进程CPU、触页内存、文件句柄增减、退出/非法PID，重复采样无句柄增长；模拟PID更换和采样失败保留缺口。复验生产串行self-check与并发满额拒绝/恢复，Linux CI运行同一采样回归和原有基线，独立Windows CI只验证采样及句柄释放，不代表完整网关Windows验收。报告schema升至2并记录指标定义；不增加数值性能门槛，不因指标存在宣称资源稳定或无泄漏。
6. **协作：**报告仍在仓库外，保留源码/dirty/摘要与环境；已有Windows unsupported记录为历史事实。先取得本机可解释资源基线，再单列重复/持续负载、TLS/H2/反代及优化预算。此切片不扩大P0完整验收或真实Provider/收费许可。

2026-10-20 本地受控结果：Windows 7项采样回归通过，包含独立进程CPU/32MiB触页内存/64个文件对象增减、100次正常查询及各API失败路径无句柄增长、PID更换和缺口保留。生产串行 self-check 与5对测量通过；最终并发4、每worker5请求、两轮，直连40/40与网关40/40均完成且每请求一次attempt，两个429均未发送且全部4名额恢复，6阶段活动上游归零。生产源码基于 `d02933a`，harness尚未提交时运行（dirty=true），报告记录三个源码摘要；网关103次资源观察全部可用，样本峰值working set 30666752字节、private commit 63668224字节、句柄213。这里只证明查询及有界场景可解释；短时峰值不证明无泄漏、资源稳定、容量上限或性能预算通过。 Windows CI首轮发现测试把“查询无增长”误写成“总句柄数完全相等”：子进程工作后比初始多1个句柄，测试进程前后减少4个；总数可能受延迟初始化和运行时清理影响。已改为直接核对64个工作文件对象全部关闭，以及每种失败路径100次查询后的句柄不增长；保留CPU/内存/非法PID和缺口断言，采样实现未变。Go生产源码未变，本批不重复已有完整Go回归，远端完整Verify仍须按本批精确头检查。

本轮 P0 TLS/HTTP2 与反代组合回归切片（精确头CI通过并合并，合并后CI也已通过）：

1. **层级/目录：**仅 `internal/gateway` 的既有真实 socket 测试夹具及新增反代回归、本方案；不修改 Go 生产实现。
2. **公开依赖：**消费已批准的 gateway Client/HTTPStageTimeouts/响应头开关、Chat HTTP handler 与 Responses coordinator；使用标准库 TLS test server、HTTP/2 client 和 ReverseProxy。TLS仅信任测试server证书，不关闭证书验证，不新增sender或解析器。
3. **地基影响：**无契约、迁移、依赖、产品配置或收费语义变化；固定可信身份的网关测试不等同生产鉴权/持久历史/外部反代部署验收。
4. **行为/边界：**下游TLS HTTP/1.1或HTTP/2→标准库反代→HTTP/1.1网关→TLS HTTP/1.1或HTTP/2上游，显式校验两端实际协商协议；Chat/Responses分别覆盖正常终态后上游不EOF、截断、客户端取消和已提交后的stream idle。终态被测试门闩扣留时必须先读到完整UTF-8文本事件，证明反代未等待整流结束才输出。保留工具参数、各协议终态/ID及白名单request ID；失败不补成功终态，不在提交后换目标重试。
5. **验证/验收：**32个真实网络组合检查单次上游、协议完整/失败/取消/超时事实、两级准入释放与上游context结束；运行受影响Go网关包及完整精确头CI。读写有总deadline，测试失败也释放全部门闩，不能由HTTP2自动回落到HTTP1得到伪通过。
6. **协作：**不测不读客户端背压、不承诺任意反代缓冲配置/WS Upgrade；不把Go测试进程内资源归零当生产CPU/RSS或容量证据。性能、持续负载、代理部署和真实Provider验收仍另列，原有生产主进程/SDK/容量证据按各自精确源码范围复用。

本地GOARCH=amd64定向32组合通过，受影响gateway包vet与完整回归通过；整合主线 `94f3ca6` 的请求重放修复后，所有TestP0网关真实socket回归再次通过；精确头Verify另记PR。所有组合均校验真实协议，没有HTTP2回落或skip。仅标准库反代与网关测试夹具，不声称真实部署/生产鉴权/持久Responses/收费/性能验收。

本轮 P0 生产主进程同时取消与恢复切片（精确头及合并后 CI 均通过）：

1. **层级/目录：**仅 `tests/smoke/stream_baseline.py`、`capacity_baseline.py`、Verify与本方案；复用Windows/Linux资源采样，Go生产实现不变。
2. **公开依赖：**通过现有生产HTTP API Key鉴权、Chat SSE、容量/路由/executor/SQLite及进程生命周期；受控上游与测试客户端共用harness PID，不添加产品观察端口或执行者。
3. **地基影响：**无公开契约、数据库、依赖、配置或收费变化；只是既有容量验收增补有界故障场景，无需新CR。真实上游/账号仍不调用。
4. **行为/边界：**每轮先启动与Key名额相同的有限请求，所有客户端观察到完整非空文本事件后，在有限barrier共同发起socket shutdown。上游在首文本后只发有界comment，不发送finish/usage/DONE；必须观察到每个上游handler因连接关闭结束，取消请求一次发送，不把已读前缀记作成功完成；客户端只证明取消前所见事件，不据此推断断开后的服务端终态。取消样本单列保留，不混入正常完成率/延迟分布；超时/拒绝/barrier破坏/未结束均保留为失败，不能当预期取消通过。
5. **验证/验收：**并发1、4、16的受控生产主进程、既有串行self-check；每轮取消后立即沿既有满额hold→额外no-send429→释放→完整再填满的恢复检查，验证全部名额。每阶段记录独立CPU/内存/对象采样及失败后的资源结果，CI集中检查正常请求、取消、排空和恢复；量化性能仅报告，不引入新数值门槛。
6. **协作：**每请求/phase/barrier/上游等待均有界，失败释放所有测试门闩并join；报告在仓库外并记录各harness摘要。disabled只关闭收费，不旁路权限、准入、日志或持久化；不能把少量取消当无限取消风暴、无goroutine泄漏、持续承载、TLS/H2反代或真实Provider验收。资源采样PR #57已合并，GitHub已将本批base转为master；合并仍按最终整合的精确头CI。

本地最终harness：并发1/4/16分别预期取消1/8/16个，均先见有效文本、一次发送且上游结束；各轮全部Key名额重新占满、额外请求429且未发送、释放后全额再填满。并发4两轮直连40/40和网关40/40正常完成，8阶段活动上游归零且资源观察均可用；取消样本另列保留。串行self-check与5对测量通过；缺一个barrier参与者时明确保留cancel_barrier失败，并排空上游，未误算预期取消。报告基于 `ba54d83` 加本批未提交harness（dirty=true），最终4/1/16报告的三个摘要均与本批源码一致。Go生产源码未变，完整Verify仍按最终整合的精确头确认。

<details>
<summary>FC-P0-HTTP-001：前台 HTTP 阶段超时具体 CR（已批准，源码与本地验证完成）</summary>

本 CR 的目标是让同一个既有 attempt 在响应头、首有效输出、流事件空闲或下游写阻塞时有界退出并可定位原因，不新增 sender、重试权威或读队列。实现按“header/write 机械部分 → L2 进展与 first/idle → 集中验证”连续交付，全部配置默认0关闭。

1. **层级与现有扩展不足。** L0 `internal/core` 当前只有 `Input.Timeout`，阻塞的 `Client.Do`/`Scanner.Scan`/同步 write 无法从 L2 取消并报告独立阶段；`len(Frame.Data)>0` 无法区分文本/工具与 role/usage/heartbeat。共享 Transport 也不能提供请求级语义进展。新增最小机械字段，保持 L0 无项目依赖。L1 gateway 控制启用、输入 capability 检查、重试/health/unknown 与现有诊断；L2 protocol 只报告完整已校验事件事实；config/组装、OpenAPI和架构登记同批协调。
2. **精确端口与字段。** 保持冻结 `execution/v1` 与既有 v2/v3 结构不变。新增候选 `execution/v4`：`Progress{EffectiveOutput bool; CompleteEvents uint64}`、可选 `StreamProgress interface { Progress() Progress }`，由本 stream decoder 报告单调快照，禁止 I/O、策略或资金含义。L1 的现有 decoder/facts/usage bridge 取一次既有解码结果及可选快照，不重复解码；零数据进展帧只传机械事实，不额外写客户端。L0 新增 `HTTPStageTimeouts{Headers, FirstEffectiveOutput, StreamIdle, DownstreamWrite time.Duration}`、`Input.HTTPStageTimeouts`、`Frame.EffectiveOutput bool`、`Frame.EventComplete bool`、`Result.TimeoutStage TimeoutStage`；新增 `TimeoutStage` 固定值 `headers`、`first_effective_output`、`stream_idle`、`downstream_write`（未发生新增阶段超时时为空）。这些是经本 CR 明确请求批准的 L0 v1 追加字段/语义，不宣称现有冻结已自动允许。gateway.Options 追加同名阶段配置；新增 startup `server.http_stage_timeouts.{headers_seconds,first_effective_output_seconds,stream_idle_seconds,downstream_write_seconds}`，各0关闭、非零1..3600秒；负数/溢出拒绝。无数据库迁移。
3. **阶段时间与有效输出。** Headers 从 core 调用既有 Client.Do 前开始至返回最终响应头，包含连接、代理、TLS及上传等待，不能冒充纯 Provider 计算耗时。成功 headers 后，stream 首有效输出及 idle 开始；first 在完整已校验的非空文本或实际 function/tool 名称/参数增量后停止，合法完整终态也停止，不要求空成功输出产生文本。role、空 delta、ID、usage、heartbeat、单独 finish reason 不停止 first。Idle 每个完整合法 SSE 事件重置；合法 comment/heartbeat 可以证明连接活动但不取消 first，半行/半事件/非法事件不重置。已声明 Chat/转换路径、原生 Responses 和 SIWC wrapper 在各自既有解析中报告这些事实，未知字段不推断业务内容。JSON 仅启用 headers/write/原有总超时，first/idle 不适用。
4. **背压与终态。** 首输出和 idle 的上游等待预算在同步 commit/write/flush 区间暂停，下游独立 write budget 继续约束；这不是墙钟 TTFT 承诺，性能测量仍记录真实 headers→首有效输出墙钟时间。每次 commit/write/flush 用 `min(now+writeBudget, attemptDeadline, generationDeadline)` 设置已有真实 write deadline，结束恢复原总截止时间；显式启用 write 而 Output 不支持时发送前拒绝，不能用遗留写 goroutine 伪造中断。显式启用 first/idle 的 stream decoder 不支持 v4 时，同样发送前拒绝，不降级按字节判断。一次 attempt 至多一个有界 timer/watchdog，由同一状态机仲裁触发、暂停、终态和关闭并 join；无另行读 worker、额外缓冲队列或执行权威。先确认协议终态不被晚到 timer 追改，但终态已收到后下游交付仍可失败；只见 finish/EOF 不伪造终态。
5. **结果、权限与兼容。** 仅接现有前台 HTTP 执行源；后台受理/查询/取消、durable claimed 和 WS 保持现有预算。已有 attempt/total deadline 永远优先约束，新增阶段不延长它们。L0 保持原 Termination 枚举，上游等待阶段为 Deadline，write 为 ClientWriteError，并用 TimeoutStage 精确归因；未提交时等待超时可返回504，已提交状态单调且保留原 HTTP status。L1 通过现有 ErrorDetail.Code 承载固定 `{"timeout_stage":"..."}`（不改 v1 类型），已有 routing Trace.Reason 追加相应固定原因并更新 OpenAPI。金额/unknown 仍由执行 owner 处理，不以错误文案为资金权威。已 dispatch 的新增阶段超时不新增自动重试；write 不重试、不处罚凭据。静态/托管凭据权限不扩大，prelude 已提交时不恢复重试资格；没有 usage 不补零。0配置保持原行为，自定义 decoder/client 的旧路线继续可用；关闭新增配置并回退原二进制即可，无数据迁移。
6. **验证与协作。** 定向覆盖延迟 headers、持续 role/heartbeat、慢首文本/工具、usage-only、合法空终态、半事件滴流、首输出后 idle、错误/incomplete/截断；真实 socket/TLS/HTTP2/反代慢读和不读、write/flush 阻塞、取消/终态/timer 竞态与 close/join。检查一次发送、commit单调、取消不占租借、无伪完成、未知成本不清零、managed反馈不误罚，以及0配置静态/Responses/后台/WS回归。先集中本地验证、Linux race/交付及精确头 CI再合并；不把阶段超时当性能优化排名，不跳过现有鉴权/账本，不新增前端或收费许可。新 v4 仅登记候选，不宣称全量冻结。

本 CR 已按批准范围完成源码：启动配置/组装 → gateway 前台 source 与 capability 预检 → 同一个 L2 stream 的 v4 进展 → 既有 L0 executor 的单 watchdog/同步输出 deadline → ErrorDetail.Code 与固定 route reason。后台/claimed/WS不接新增阶段。新增阶段超时不重试或处罚凭据；strict仍由既有EndAttempt/Finish处理未知用量和双hold。候选v4已登记并更新公开快照，未宣称冻结。

定向验证已覆盖实际socket的role/usage/finish-only、半事件滴流、文本后idle、Chat/Responses工具、空/incomplete终态；三类等待超时各仅一个attempt且strict保留pending、Provider hold及user/Key双hold。L0定向覆盖write暂停、合法终态与late-timer仲裁、取消/join、TLS/HTTP2不读下游。零配置下的既有End确认与资金事实回归保持原行为。Windows GOARCH=amd64 build ./...、vet ./...、全量 go test ./... -count=1、生产主进程 local.py 冒烟与定向协议/网关/配置/架构检查均已通过。包含当前授权、静态Key/Responses/后台/WS、强杀恢复、usage与shadow回归；小数秒/越界/溢出/拼错阶段字段拒绝启动。PR #53精确头Linux race/交付Verify及合并后主线 `07fb7ec` Verify均已成功；证据仅覆盖该阶段超时切片。

实际限制单列：当前Go HTTP/1 TLS写入失败后的标准库close_notify关闭路径可能额外等待约5秒；阶段触发与最终返回不是同一时刻，不能声称所有TLS阻塞都在配置秒数内完成释放。真实TLS/H2测试验证写阶段归因并以8秒封顶检查退出。Windows明文HTTP/1测试可能被内核完整缓冲，未实际阻塞的子场景标记skip，不作为TCP写阻塞证据。同步自定义Output必须遵守已声明deadline语义，无遗留写goroutine兜底；真实Provider及外部部署仍未验收。

用户已明确批准直接实施并集中验证修复；默认关闭、无真实账号调用授权，已有批次CI在后台运行。

</details>

<details>
<summary>FC-P0-HEADERS-001：前台 HTTP 上游诊断与重试头具体 CR（已批准，源码与本地验证完成）</summary>

此切片补当前 `ExecuteTo` 丢失全部上游 Header 的具体缺口：客户端看不到最终上游的 request ID，也收不到429/503的 Retry-After。只追加有限元数据，不改变现有 JSON/SSE 正文、状态码、ID/错误改写、HTTP 提交屏障或重试许可。本 CR 已获用户明确批准实施与集中验证；随后用户又授权本任务后续同类开发契约调整记录方案后直接推进。

1. **层级/目录及现有扩展不足：**L0 `internal/core` 持有唯一实际 HTTP response，既有 Decoder 只看正文，L1无法在首次输出/错误收尾前读取 Header；`ExecuteRaw` 有独立 Header，但改走 Raw 会旁路当前流/执行事实。L0只增加同步只读机械观察入口，L1 `gateway` 决定白名单、校验、attempt隔离及最终提交；L2现有HTTP Output继续只渲染已有 `ResponseMetadata.Values`；config、组装、OpenAPI、架构快照与测试同批更新。不另建 HTTP client、Transport、正文/错误解析器或执行链。
2. **精确公开变化：**L0 `core.Input` 追加可选 `ObserveResponseHeaders func(status int, headers http.Header)`。每个成功返回非nilresponse的 `ExecuteTo` attempt 在标记ResponseReceived后、任何正文读取/输出前同步调用一次；无response/未发送则不调用。Header为仅回调期间有效的只读借用，回调禁止保留/修改map、I/O、阻塞或panic；L1实现只复制经过有限校验的两个字符串。nil维持原行为，Raw/WS不接该入口。L1 `gateway.Options.ForwardUpstreamMetadata bool` 和startup `server.forward_upstream_metadata` 默认false；仅foreground HTTP使用。保持冻结execution/v1/v2/v3/v4的类型/签名不变，不新增地基版本或数据库迁移。L0不携带项目白名单、重试/健康/账本策略，也不将完整原始Header加入Result/日志/持久状态。
3. **白名单和校验：**启用后，已登记前台Chat、embeddings与Responses（同协议或现有已批准转换）统一只考虑 `X-Request-Id` / `Request-Id` 和 `Retry-After`。恰有一个request-ID来源且只有一个非空值时，将最多128字节的可见ASCII单值输出为 `X-TXAPI-Upstream-Request-ID`，不覆盖本地 `X-Request-ID`；重复、歧义、控制字符、逗号或超长则忽略。Retry-After仅用于最终upstream429/503且最终HTTP错误状态仍相同；单值最多64字节，接受非负十进制0..2147483647或 `http.ParseTime` 可解析HTTP-date，保留去除首尾OWS后的原值，不猜reset、不睡眠或自动重试。`Connection` 不分大小写点名的任何候选头均剔除；Connection输入至多16值、合计4096字节/64选项，超界则丢弃本次全部上游元数据。未列入的鉴权/Cookie/Set-Cookie/WWW-Authenticate/Location、逐跳/长度/压缩、Provider账号/限额/组织等头均不转发；不承诺全Header透明。
4. **提交/attempt/兼容：**上游元数据保存在请求Output的独立私有attempt槽，选定下一attempt并检查准入前清空，不覆盖本地billing/usage/retry等元数据。只有最终实际输出合并该槽；先前429/503/ID不能进入后来成功、无response或本地拒绝的回复。只读headers观察不提前Commit，不增加prelude；已Commit后不再改Header。成功、协议失败、上游错误、取消/阶段超时都复用现有终态、资金与安全错误规则；HTTP status不一致时不残留Retry-After。关闭开关并回退旧二进制即可，无数据回退；默认false及后台/claimed/WS维持原行为。自定义Executor调用者的非nil回调须遵守同步只读约定。
5. **验证/验收：**L0测试一次回调/时序、零配置、无response/发送前失败不调用、原正文/终态/关闭不变；L1/L2实socket覆盖Chat JSON/SSE、embeddings、Responses与现有转换、429/503数值和HTTP-date、重复/超长/非法头、Connection动态排除、本地ID与元数据优先级。多attempt验证首个错误的Header不串入后续成功/本地准入拒绝/无response；发送前拒绝与已提交失败均不重试越权，strict未知与双hold不变。config默认/布尔类型拒绝、OpenAPI与公开快照同步；集中本地回归和完整精确头CI后合并，真实Provider不在授权范围。
6. **协作/边界：**这是P0/P4的诊断与客户端退避元数据切片，不扩大账号推理范围、定价、收费、额度恢复或后台/WS；新开关只重启生效。RFC9110第7.6.1与10.2.3节用于Connection逐跳排除及Retry-After语法核对，不复制参考项目实现。参考：`https://www.rfc-editor.org/rfc/rfc9110.html`。无新前端、依赖、迁移或性能提升声明。

源码已按该范围接入；OpenAPI `0.13.2` / 117操作与L0公开快照同步。定向验证覆盖只读观察时序、配置布尔类型、实socket的Chat JSON/SSE、embeddings、Responses及Anthropic/Gemini现有转换、原生HTTP自动gzip解压、429/503数值/HTTP-date、重复/超长/非法值、Connection动态排除和未入选敏感头不转发。重试回归分别证明旧响应头不进入后续成功、无response及本地准入拒绝，已提交截断只发送一次且不伪造完成；非foreground不继承开关。strict阶段超时回归保留pending、Provider hold及user/Key双hold，并保留独立billing ID。本批Windows GOARCH=amd64的build ./...、vet ./...和全量go test ./... -count=1均已通过；生产主进程local.py受控冒烟也已通过；固定Python SDK回归与合入主线后的并发容量回归也已通过：4worker、每worker5请求、2round，直连/网关各40次全部完成且每次仅1上游attempt，两次过载均在发送前429，所有4槽再次并发占满后释放并恢复active=0。合入的主线只改变容量Python脚本/CI与方案，没有Go运行源码差异，因此复用本批已有完整Go验证；精确头远端CI仍单列，不继承其他批次通过。

</details>

<details>
<summary>FC-P0-REPLAY-001：模型正文隐式重发与连接故障回归（源码与定向验证完成）</summary>

已定向复现：同一已复用HTTP/1连接上，受控Provider完整读取带 `Idempotency-Key` / `X-Idempotency-Key` 的POST后断开且不返回headers；当前L0用bytes.Reader创建的Request自动带GetBody，标准Transport在一个ExecuteTo内再次发送正文。即使Input.Replayable=false，夹具收到2次、GotConn/WroteRequest均2次，Result却只有一次成功。这不是当前内置适配器默认会附加幂等头的声明，也不能依赖Provider真实去重来证明收费次数。

1. **层级/目录及必要性：**L0 `internal/core` 持有实际Request及GetBody，L1无法控制Client.Do内部的正文重放；按唯一attempt权威去除模型Request的GetBody回放hook，保留当前共享Transport和L1有限重试。L0/gateway测试、PLAN及适用OpenAPI声明同批更新，不新建HTTP client、Transport或账本。
2. **精确契约/公开依赖：**不改core.Input/Result及冻结execution类型、签名或版本。Replayable只作为既有L1重试许可返回，不授权Transport在该L1 attempt内重发已读正文；ExecuteTo构造Request后清空GetBody，保留原Body、ContentLength、Header和已验证成本正文。ExecuteRaw已有无hook行为保持。此修复不宣称任意自定义RoundTripper、空正文GET、HTTP2拒绝流等所有机械恢复都等于一次物理连接取得；重点是非空模型POST在可能已受理后的正文重放不能被隐藏。
3. **consumer/兼容与回退：**Chat/embeddings仍由gateway按Replayable、状态、提交和全部attempt成本决定重试；Responses仍NonReplayable。自定义client/测试probe不能依赖模型Request.GetBody复制正文，必须在自己的有界观察中读取并原位恢复Body；现有两处strict/shadow测试probe同步迁移；既有managed/static自定义client回归同步声明307/308不重放正文，同时保留302到GET、Cookie jar和自定义callback行为。默认不跟随redirect保持；自行配置跟随307/308并依赖GetBody重放正文的client将不再得到自动正文回放。失效复用连接零写入的恢复也可能交回L1并形成保守unknown，而非在L0免费重开一次。回退旧二进制会恢复旧隐式回放行为，无数据迁移。
4. **行为/故障范围：**实际socket记录连接取得/复用、完整服务端请求及正文、L1 attempt。HTTP/1明文/TLS覆盖幂等头有/无、Replayable有/无、已受理无headers；另覆盖零写入的失效复用、建连/TLS失败、JSON头后截断、SSE提交前/后截断和Responses不重试。保持请求总deadline、取消、提交屏障、关闭/join及原安全错误；Dispatching依旧不证明未执行。
5. **验证/资金：**先保存上述失败复现，再集中定向/包级及精确头Linux race/完整CI。strict真实socket验证先前unknown与后续成功各有单独durable attempt/Provider预算，先前Provider包络与CostKnown=false不清零。没有完整最终结果时保持pending及user/Key双hold；若另一个已准入attempt产出已知完整最终答案，则复用现有最终答案结算，用户双hold释放/记收费，同时先前Provider预算历史继续占用。既有strict费用档案原本已拒绝额外幂等头，此拒绝继续在发送前回归，不把此L0扩展场景称为已开放strict漏洞。非空Model POST与Raw消费者均核对正文和Header不变；不以夹具成功证明Provider去重、单次真实收费或完整S5-03。
6. **协作/边界：**本任务用户已授权同类开发契约调整记录方案后直接实施、集中验证修复，不重复请示；不开放真实账号、收费、新Provider/协议、WS池或前端。源码机制依据仍为既有固定参考项目的发送watermark/提交屏障对照；Go net/http文档及官方 `https://raw.githubusercontent.com/golang/go/go1.23.12/src/net/http/transport.go`、request.go仅核对HTTP/1恢复条件，夹具和修复独立编写，无第三方代码复制。响应头PR #55精确头Verify `39296951675` 全部成功并合并为 `d02933a6d24adb97b76d7ed0887beb469455d8ce`；本批已经集成其源码，按最终范围复验。

Go1.26.3/Windows GOARCH=amd64的受控复现先失败后修复：HTTP/1明文/TLS、两类幂等marker、Replayable有/无共12种场景修复后均只受理一次；零写入故障、受控DNS/TLS失败与注册adapter三种操作的attempt计数也通过。gateway故障矩阵覆盖Chat/embeddings/Responses、JSON截断/SSE首事件前后、429/503，检查Credential变化和所有租借归零；strict单次unknown及unknown后成功的独立attempt/预算/双余额状态、额外marker发送前拒绝、strict/shadow探针迁移均通过。集成完整build/vet和全部包测试已执行：仅旧managed redirect测试仍期待307自动重发而失败，已按本CR迁移并定向通过；此改动仅测试，随后仅重跑受影响gateway全包，不把首次全量记为全绿。生产local.py冒烟与固定Python SDK均已通过；迁移后gateway全包 `go test ./internal/gateway -count=1`（47.862秒）及包级vet通过；其他包在首轮完整测试均通过且源码未再改变，不额外重复全量。集成容量4worker、每worker5请求、2round，直连/网关各40次全部完成且仅1上游attempt，两个无发送429与全部4槽恢复/active=0通过；测量时仅managed测试/方案有未提交修改，未改生产源码。远端精确头Linux race/交付CI仍须独立通过。

</details>

P0 扩展有界负载证据（2026-10-20，非长期 soak 验收）：在干净源码 `26d6d36b944d53480d5c890c73cdac1de14addb0` 上复用既有生产主进程 harness，并发8、每worker100请求、四轮交替直连/网关、13事件且间隔25ms、disabled、真实API Key/SQLite/请求日志保留。两条路径各3200/3200完成，每请求一次attempt；32次有意取消单列，四轮429均零发送、全部8名额复填，16阶段活动上游归零。八个测量波次合计304.615秒，网关阶段累计CPU22.953125秒；5069次网关进程采样均可用，50ms样本峰值working set38084608字节、private commit70152192字节、句柄246，后3轮网关排空后句柄均229。网关首有效输出P50/P95/P99为74.6094/77.5115/88.6355ms，41600个完整事件延迟上界样本P95为2.1751ms。原始报告与三个harness摘要保存在仓库外；Windows工作集/commit/句柄定义及采样下界沿既有声明。初始化后的保留、峰值和排空端点不等于无泄漏证明，不把两条独立分位数之差当配对开销；单机明文HTTP/1.1夹具不覆盖TLS/H2/反代性能、最大容量、长时间运行或真实Provider，未设优化门槛也不声明性能通过。该证据仅覆盖本次未整合新合并提交前的精确源码，后续CI状态另列。

#### P0 原生 Responses SSE 配对基线子批（源码与本地测量完成，精确头 CI 待验）

1. **层级/目录：**仅 tests/smoke/stream_baseline.py、既有 Verify 和本方案；沿现有测试侧完整事件、绝对 deadline、write/flush 观察和只读资源采样增加原生 Responses text SSE 档案，真实运行生产 main。capacity_baseline.py 的 Chat 默认调用保持原形，不新增生产 sender/解析器。
2. **公开依赖：**消费现有 API Key Responses HTTP、disabled/store=false、配置和进程生命周期；复用当前独立模型权限和 response runtime。仅临时合成内容密钥/SQLite/配置，无公开 Go 端口、冻结、L0、数据库迁移或收费许可变化，无需新 CR。
3. **行为与限额：**命令追加 --operation chat|responses，默认 chat。Responses 上游提供 heartbeat、created、8段固定Unicode文本、text done及完整 completed/原生usage；按相同固定节奏交错直连与网关，每请求独立时间戳、一次上游attempt、完整事件边界/内容/终态计数。首有效文本单列，不用heartbeat/created缩短TTFT；接受网关合法ID重写，不声称字节透明。
4. **验证与回退：**现有 self-check 在两种档案分别覆盖错误Key401、截断、合法chunk-extension慢滴的绝对deadline、排空与测量新库；保留所有失败、完成率分母和配对成功数，不把两个独立P99相减。保持1..1000有限样本和并发1、源码/harness摘要、直连/网关同条件、临时进程Kill/Wait及资源查询缺口。已有Chat self-check/容量消费者同批定向回归；最终精确头完整CI，不替代真实Provider、TLS/H2/慢读/长期soak或WS/后台基线。
5. **验收与预算：**先得到Responses本机重复基线再制定该操作的优化预算，不套用Chat既有预算或根据本轮结果倒设通过阈值。CI只做受控正确性self-check和有限样本，报告配对首正文/首有效文本/完成/事件延迟、CPU/RSS/对象、连接/attempt和恢复，不以减少鉴权、持久化或校验换性能。
6. **协作：**P0/P4当前纯测量缺口，不扩默认Codex工具、账号池、strict/legacy收费或产品开关；不运行真实Provider/外部网络、不修改用户配置，报告写仓库外。发布/合并/生产验收分别记账，未通过CI前仅称源码及本地证据。

2026-10-20 本地受控基线：Windows GOARCH=amd64、生产主进程、disabled/store=false、loopback HTTP/1.1、每事件配置5ms（实际调度间隔由逐事件时间戳记录）、单并发/每目标持久连接，先后独立运行两轮各100对请求。两轮均为直连100/100、网关100/100完整成功，每请求一次实际上游attempt、每流12个完整事件，测量后上游活动为零；正常/截断/合法chunk-extension慢滴的绝对100ms截止和错误Key401 self-check均通过，失败注入样本独立保留。配对首有效文本增量P50为0.2057/-0.0497ms，P95为1.9369/1.7241ms；首正文增量P95为4.7973/4.9560ms，完成增量P95为5.3950/5.3822ms。网关上游write至下游完整事件观察延迟上界P95为0.7317/0.7687ms，包含测试客户端调度，不解释为纯网关服务时间。未把两个独立P99相减，也未据负配对样本声称网关加速；尚未设Responses优化预算或做性能改动。

两轮网关各553次资源查询全部可用，CPU增量0.90625/0.984375s，50ms采样working set峰值33763328/33402880字节，private commit峰值65159168/64638976字节，handle峰值207/199；排空后的句柄206/198包含保留连接和运行时初始化，不据此判定泄漏或资源稳定。报告基于源码 `fd127abe377399716595b1dcc5084d73859a6f65` 加未提交harness（dirty=true），stream_baseline.py SHA-256 `210e125279b2039e2b207152c3e7c9d8db4c4886bad23c0cb21fa8c5c78c1a67`，资源采样摘要 `40748d8de431e4ce99a221dfb9960bca1ba989d4bea402b7bf4ca61f357961a0`；与PR #69合并后的生产树完全相同。初次夹具初始化因遗漏凭据密钥文件及其digest_key而失败，补齐独立合成配置后才开始测量，不把零样本算通过。CI追加独立Responses self-check/20对有限测量，数值仅报告；最终同一harness的Chat self-check/5对测量通过；原容量消费者并发4、每worker5请求、两轮，直连40/40与网关40/40完成、8个协调取消排空、两次未发送429及全部4名额恢复均通过；Python AST、测量摘要一致性、CI命令保留及diff检查通过。Go生产树不变，不重复已有全量Go本地回归，最终新头完整CI独立验收。此批不扩大TLS/H2、长期soak、真实Provider或收费验收。

补充既有Chat并发持续基线：在干净 `fd127abe` 上，16worker、每worker50请求、4轮、事件配置5ms，独立重复两遍；每遍直连3200/3200、网关3200/3200均完成、每请求一次发送，每目标41600个完整事件。每遍64个协调取消均观察上游结束，4次过载429均未发出，释放后全部16名额恢复；16个阶段结束后活动上游均为零。网关完整事件观察延迟上界P95为2.4742/2.4562ms；working set采样峰值44306432/44433408字节、private commit峰值76632064/76472320字节、handle峰值均270，所有资源查询可用；两遍运行中预热后句柄分别229→237→237→238及229→229→237→237。报告保留完整样本/阶段资源与源码及三个harness摘要，既有capacity源码摘要 `5ea07723b8c5a05a94c03239889cb400475fee4415fa19664bc28cdf034252c8`。这是有限closed-loop和恢复证据，不代表最大容量、长期资源平稳、并发1配对预算通过或真实Provider运行。

#### FC-P0-OCCUPANCY-001：当前准入与原生 WS 占用指标（2026-10-20）

1. **层级/目录：**L1 access 复用现有 Capacity.Snapshot；L1 gateway 在 NativeWebSocketService 读取现有 sessions/active 状态；叶子 metrics 只输出固定聚合值，cmd/gateway 注入只读提供者。修改上述既有目录、生产入口回归和本方案/OpenAPI，不新增包或后台采集 worker。
2. **公开依赖与冻结：**新增 metrics.RuntimeStats / SetRuntimeStatsProvider 和 gateway.NativeWebSocketService.Occupancy 组装能力；不改变冻结 observability/v1、control/v1、responsesws/v1 或 L3 功能契约。当前观测不能成为准入、恢复、路由或资金事实。
3. **地基影响与来源：**L0 不变。主参考 sub2api 固定 f2e5b55f3e8069df1c02a9a27af537b5fdefa64e 的 backend/internal/service/openai_ws_pool.go::accountPoolLoadLocked / AccountPoolLoad（1802–1830）从 owner 当前受锁状态读取连接/在途快照，以及 ops_metrics_collector.go::dbPoolStats（872–878）从资源 owner 读取实际占用。TXAPI 按已有 session/round 与 request/attempt 槽位改写，不引入参考账号池、排队、Redis、持久指标或周期数据库扫描，不复制第三方代码。
4. **接口、兼容与回退：**既有 /metrics 增加固定无标签 gauges：gateway_admission_requests_active、gateway_admission_attempts_active、gateway_native_ws_enabled、gateway_native_ws_connections_active、gateway_native_ws_rounds_active。连接表示已授权且未完成 Close 的原生 session（不是 TCP FD 或上游池连接）；轮次包含已开始 Execute 的授权/准入/发送/终态收尾，预热同样计入；request/attempt 仍是原两层准入槽位，不能相加当生成总数。跨 owner 快照不保证同一时刻；关闭后仍在释放的资源不能提前报零。没有注入提供者时省略该组；WS 关闭时 enabled=0 且两项 WS 占用为零。无身份、Key、模型、父 ID 或正文标签；保留既有 /metrics 访问边界。删除注入即可回退，无配置/数据库迁移。
5. **消费者与验收：**真实消费者为生产 main 的 /metrics。定向验证 provider 动态快照/固定输出、未启用行为、空闲连接与活跃轮次分离、busy 拒绝、取消/完成/关闭后的释放及并发抓取；真实 socket 组装回归与实际 main 冒烟证明注入可见，完整 build/vet/Go/精确头 Linux race 与交付集中执行。只声明本切片占用可见，不将其当 CPU/RSS/FD、后台恢复/事件保留指标或长期资源稳定证据。
6. **其他影响：**无新产品开关、依赖、schema、收费或权限变化；OpenAPI 同步解释指标语义，不增加操作。其余运行验收和真实 Provider 门槛保持待验。

本地受控验证：provider 未注入时省略、动态快照与固定无标签 gauge 输出、WS 关闭时 enabled=0、真实 request/attempt 槽位变化均通过；原生 session 的 busy 拒绝、活跃轮次取消与 Close join、并发读取通过。真实 socket 完成与断开两种路径分别观察空闲 1连接/0轮次、执行 1/1、完成保留连接及关闭后全部释放，且只有1次实际上游发送、零HTTP回退。Windows GOARCH=amd64 build、vet、全量 Go 回归通过；生产 main 的完整 local.py 冒烟通过，包含本批关闭 WS 的 /metrics 注入断言与既有 strict 拒绝、结算、密钥轮换、后台/用量强杀恢复及 shadow。OpenAPI0.13.10/117、Python语法、差异/局部格式及方案链接检查通过。未执行本地 Docker/race；最终精确头 Linux 完整 CI/交付仍待验收。

#### FC-P0-BACKGROUND-OCCUPANCY-001：后台控制、恢复与事件占用（2026-10-20）

前一批 FC-P0-OCCUPANCY-001 的精确头 `080a8d97c08a5445d7931772ae17de501f8abc63` 已通过 push Verify `39360990700` 与 PR Verify `39360997575` 的全部六项检查，PR #74 已合并为 `abb10d91d32181d3267ee92831ca8e43150e1833`；合并后主线 Verify 独立记录，不外推为本批证据。

1. **层级/目录：**L1 store 增加后台元数据聚合读取，generation.BackgroundRunner 记录本进程已 claim 控制任务的执行占用，cmd/gateway 复用 Responses 原有保留期维护循环采样，metrics 输出缓存值；修改上述既有目录、受控生产入口冒烟及本方案/OpenAPI，不新增包、采集 worker、管理页面或恢复流程。
2. **依赖与冻结：**新增 Store.BackgroundOccupancy、BackgroundRunner.ActiveControls 及 metrics.BackgroundStats/SetBackgroundStatsProvider 组装能力；不修改 background/v1/v2、observability/v1、control/v1 等冻结/已批准功能契约或私有 backgroundstate.Repository。只读聚合不授予调用者任务、事件正文或资金权限。
3. **来源与地基影响：**主参考 sub2api 固定 f2e5b55f3e8069df1c02a9a27af537b5fdefa64e 的 backend/internal/service/ops_metrics_collector.go::collectAndPersist（254 起）把 DB 查询和系统采样放在后台采集阶段、分别保留可用性；沿用本项目 store 单一数据权威和现有维护循环，改为有时限的内存快照而非新增持久指标、Redis/leader/外部采集器。控制占用沿 accountPoolLoadLocked 的 owner 当前实际执行/释放状态观察原则改写，不复制第三方源码。L0、SQLite schema 与既有发送/恢复顺序不变。
4. **行为、兼容与回退：**固定聚合 gauge 包括 background enabled、active controls、snapshot success/最后成功 Unix 秒、jobs 按 queued/dispatching/remote_active/terminal/uncertain/other 六种固定 stage、replay pending、retained events/event bytes/reserved bytes。store 单条只读 SQL 投影元数据，最多读4097行；超过4096合法上限或负计数视为不可用，不读取/解密正文、凭据、snapshot 或事件 blob，不按 owner/Key/ID/model 标签展开。沿原保留期循环启动即采样、每次后台清理后以独立500ms deadline 采样；/metrics 抓取只读内存。未有成功采样时省略持久计数；失败保留上次计数与时间且 success=0，不伪装为零；时间戳供消费者判断新鲜度。后台功能关闭仍观察保留元数据和正常隐私清理。控制占用从 claim 成功到 release 尝试结束，含取消/失败收尾，不能当远端生成数或有效 lease 数。无新配置/迁移；移除注入即可回退，快照不成为恢复/路由/账本权威。
5. **验证与真实消费者：**生产 main 的 /metrics 消费缓存；定向验证空库、五类阶段和未知阶段、持久事件/预留聚合、独立重开、负值/超限/取消/DB失败、初始未知和失败保留旧值、后台关闭仍可见、active control 完成/取消释放。真实生产 main 的后台SSE/强杀重启、事件恢复、删除和关闭功能后的TTL路径同时核对聚合及实际发送次数；集中 build/vet/全量Go与适用精确头完整Linux CI/交付。不以采样计数证明长期资源稳定、数据库字节上限、财务恢复或真实Provider。
6. **协作影响：**维护循环只有一次有界只读查询，失败不阻断原清理或新请求；Close 原有 cancel/join 后再关闭共享 store。OpenAPI 同步固定指标、采样失败和新鲜度语义，操作数量不变；收费、授权、配置默认值和前端保持原范围。

实现时发现既有 CleanupBackground 在同一事务删除过期事件、清空字节占用时遗漏清零 event_count，导致保留任务元数据仍记着已删除事件。本批在原 store 清理事务补齐 event_count=0；不新增清理路径、迁移或执行权限。定向回归通过真实清理路径同时核对事件表为空、保留任务仍存在且事件/字节/预留/恢复计数归零；控制任务完成/取消均验证 release 收尾结束前保持占用、结束后归零。

2026-10-20 本地受控验证：Windows GOARCH=amd64 build、vet 与全量 Go 回归通过（cmd/gateway95.537s、architecture46.098s、gateway62.335s、generation83.306s、store92.018s）；聚合只读投影/重开/4096行合法与4097行拒绝/非法元数据/取消/DB失效及未知、已知零、失败保留旧值均通过。生产 main 的完整 local.py 通过，含后台SSE/强杀/重启恢复与实际计数核对、取消后3条终态、关闭功能后的TTL保留任务而清零事件/字节/预留、strict已知结算与未知保护及密钥轮换拒绝等既有回归。OpenAPI0.13.11/117、Python语法、差异与格式检查通过。控制任务测试初轮因测试服务未读请求体导致取消后handler未退出，补齐请求读取和释放门闩后通过；不把失败轮计为成功。最终精确头 Linux race、完整兼容和实际镜像交付仍待本批 CI 验收；未执行真实Provider/OAuth，未开放收费。

后台占用已验证树的补充有限负载：干净 `15144d4ea72c0bd3db4bfbe5f1df48baa5782816` 上运行既有 HTTP/1.1 Chat 生产主进程基线，16worker×每worker50请求×4轮、配置事件5ms。直连和网关各3200/3200完成、各41600个完整事件、每请求一次发送；64条协调取消排空，4次429零发送且每轮恢复全部16名额，16个阶段上游活动均归零。1396次网关进程资源查询均可用；四个网关负载阶段采样峰值working set44728320字节、private commit77189120字节、句柄278，阶段排空后句柄均245，四阶段合计CPU26.21875s。网关完整事件观察延迟上界P95为5.3935ms；未做配对前后对照，不宣称采样无开销、性能改善或串行预算通过。使用既有capacity/stream/resources三个harness摘要分别为5ea07723b8c5a05a94c03239889cb400475fee4415fa19664bc28cdf034252c8、210e125279b2039e2b207152c3e7c9d8db4c4886bad23c0cb21fa8c5c78c1a67、40748d8de431e4ce99a221dfb9960bca1ba989d4bea402b7bf4ca61f357961a0；这是单轮有限负载和恢复观察，长期soak/最大容量/TLS-H2性能/真实Provider继续待验。

后台占用最终精确头验收：`15144d4ea72c0bd3db4bfbe5f1df48baa5782816` 的 push Verify `39363223724` 与 PR Verify `39363228191` 六项全部成功，涵盖 Linux 全量 race、生产冒烟、固定 HTTP/WS SDK 与 Codex、既有流/承载基线，以及实际非root镜像 TLS/反代/重启/强杀和固定 Codex WS 交付。PR #75 已合并为 `b405416973097911f1e070eddf7fb0f2122199b2`，合并后 Verify `39364578453` 独立跟踪，不作为本批 HTTP/2 新测试的证据。

#### P0 HTTP/2 并发流取消与名额恢复子批（2026-10-20）

1. **层级/目录：**只追加 internal/gateway 的真实 TLS/HTTP2/反代回归与本方案；复用现有 Chat handler、Responses coordinator 和 access.Capacity，不改生产实现或新增包。
2. **依赖/冻结：**消费已批准 HTTP 执行、Output 和两层准入；不新增/冻结公开端口、配置、迁移或依赖。测试可信身份沿既有网关传输夹具注入，不宣称覆盖生产登录/Key中间件、Responses持久历史或计费。
3. **来源/地基：**主参考 sub2api 固定 f2e5b55f3e8069df1c02a9a27af537b5fdefa64e 的 backend/internal/repository/http_upstream.go::doUpstreamRequest/cancelOnCloseBody（299–345）将每次尝试取消归属到自己的请求，关闭时结束该次读而保持其他请求独立。按 TXAPI 已有 core 取消链改写隔离回归，不复制第三方实现，不引入新 HTTP client 缓存、PING/fallback 或取消机制；L0 不变。
4. **行为/边界：**Chat/Responses 分别经同一客户端 TLS/HTTP2 连接、真实 HTTP/1 反代到 gateway、同一上游 TLS/HTTP2 连接。至少四条同时在途，精确取消一条或一半；未取消流保持活动且不提前终态，随后正常完成。以实际两层准入记录验证满载拒绝不发送、取消释放、恢复后全部名额重新同时占满和完整释放；每个已受理请求最多一次上游发送，拒绝不触发重试。连接数以实际握手/ConnState 计数核对，不能凭 Client 配置推断协商或多路复用。
5. **验证/验收：**为每个请求保留独立结果和每条上游终止观察；终态后上游保持打开直到网关关闭该流，取消不伪造成功。所有等待和客户端寿命有界，测试失败也取消/join并释放门闩。集中执行相关包和适用完整精确头CI，保留既有回归；只报告功能隔离和有限名额恢复，不作为延迟、最大容量或长期资源稳定验收。
6. **协作影响：**生产树不变，收费/身份/历史/后台/WS许可不变；不扩大真实Provider、长时soak、慢读/不读网络背压或整套P0性能通过声明。

本地受控结果：Chat/Responses × 取消1/2条四种场景全部通过，完整 gateway 包回归通过（58.532s），该包 vet 与局部格式/差异检查通过。每场景均在满载四条流后取消部分流、保留其余活动流并补满名额、全部完成后再次同时填满四个名额；总计38次实际上游发送、6条取消流、32条正常终态、8次零发送满载429。每条受理流仅一次发送，正常流的文本/工具/本地Responses ID/单次终态保持，取消终态499且不伪造完成；每场景两端分别只建立1条真实TLS连接并协商HTTP/2，结束后所有请求/attempt/User/Key/channel名额及上游活动均归零。生产源码与后台占用已验证树相同，不重复其全量Go/实际main冒烟本地检查；最终新头完整Linux race/兼容/交付仍待CI，长期资源与性能验收继续开放。

HTTP/2并发隔离最终精确头 `9d828995ed8f2dda1fab98a39926644337467b5e` 的 push Verify `39364726313` 与 PR Verify `39364761367` 六项全部成功；PR #76 已合并为 `548cb6ed507210bad3e3fbcd081ec2b8f971a662`，合并后 Verify `39366021776` 另行跟踪。后台占用合并后 `b405416973097911f1e070eddf7fb0f2122199b2` / Verify `39364578453` 已完整成功。上述证据不覆盖下列实际镜像默认传输增量。

#### P0 实际镜像默认传输 HTTP/2 SSE 子批（2026-10-20）

1. **层级/目录：**仅扩展 tests/upstream/nativews 的隔离 Provider/客户端、tests/smoke/native_ws_delivery.py 和本方案；使用既有实际镜像、生产 main、真实 Key、反代及加密 SQLite。无生产代码、配置接口、依赖或迁移变更。
2. **依赖/冻结：**沿当前 Chat HTTP/SSE 与用量历史许可；夹具不注入 gateway Client、不改变其 dialer 或宿主 DNS/信任。隔离 Docker 网络中的官方域名别名、临时 CA 与 SSL_CERT_FILE 复用既有交付装配，不扩公开地基。
3. **来源/地基：**对照 sub2api 固定 f2e5b55f3e8069df1c02a9a27af537b5fdefa64e 的 backend/internal/repository/http_upstream.go::buildUpstreamTransport（1379–1409）的真实 H2 协商及每次取消边界，验证 TXAPI 现有默认 Transport.Clone 路线；不复制 LGPLv3 实现，不引入画像、PING、HTTP回退或新连接池。现有注入 Client 的测试不能单独证明实际镜像默认路径，因此在原交付消费者补证据。
4. **行为/边界：**正常 Chat SSE、首完整文本事件后取消、随后新调用三场景；Provider 在终态后保持流打开直到客户端取消。逐次核对每场景恰好1次发送、实际 HTTP/2 与相同远端连接、文本/终态、上游活动归零及生产 request/attempt 槽位释放。正常终态和取消均不能依赖上游 EOF，取消不伪造 DONE。三条用量均持久封存且不新增永久 Responses 历史或资金记录。
5. **验证：**本地夹具覆盖真实 H2 协商/连接复用/关闭流取消、客户端完整事件/主动取消/截断拒绝；既有 CA/WS/反代夹具回归和 vet 集中执行。Linux 完整 CI 同批运行实际非root镜像、固定SDK/Codex与新增默认传输链；本地无Docker，夹具测试不能替代镜像通过。
6. **协作/限制：**保留既有 HTTP/WS 续链、强杀、未知保护及用量计数断言，汇总额外3条Chat记录；disabled与真实Provider跳过范围不变。只补此受控交付组合，不声明多流并发/最大容量、TLS延迟预算、长期soak或S5-03完成。

本地受控验证：Windows GOARCH=amd64 原交付夹具及新增真实H2协商/连接复用/流关闭测试、Chat客户端完整/取消/截断拒绝均通过，夹具包vet、Python UTF-8语法和差异检查通过。生产Go树未变；未执行本地Docker或重复全量Go。最终精确头完整Linux CI与实际镜像新增链仍待验收，不能从本地夹具成功推定镜像默认传输成功。

实际镜像默认传输最终精确头验收：`778891be1a0966dd63e005d66f661bd71ed7a830` 的 push Verify `39366096453` 与 PR Verify `39366132260` 六项全部成功，PR #77 已合并为 `24ec655fdd278e38fec34efbd4fa80ec0e2d0bbe`；交付日志确认正常/取消/恢复三次SSE共用一条实际HTTP/2 TLS连接且全部流排空，既有非root镜像、SDK/Codex、强杀及持久用量断言同时通过。合并后Verify `39367073953` 单列；PR #76合并后 `39366021776` 已取消，不作通过。该证据不覆盖下列TLS测量档案。

#### P0 TLS 上游配对测量子批（2026-10-20）

1. **层级/目录：**tests/smoke/stream_baseline.py 复用现有生产main/真实Key/临时SQLite的串行配对测量，追加TLS上游档案、独立夹具检查和既有Verify；tests/upstream/nativews 的临时CA生成器补loopback SAN与标准Authority Key Identifier。本批不改Go生产实现、新增包或依赖。
2. **依赖/契约：**消费当前Chat/Responses前台HTTP、disabled、store=false和既有默认Client，不注入测试Transport。直接客户端信任临时CA并校验IP，Go进程仅通过Linux子进程SSL_CERT_FILE加载该CA，不改宿主信任或生产配置接口。当前生产TLS基线限Linux；Windows仅执行夹具和明文消费者回归，不称TLS主进程已验收。
3. **来源/地基：**主参考sub2api固定f2e5b55f3e8069df1c02a9a27af537b5fdefa64e的backend/internal/repository/http_upstream.go::buildUpstreamTransport的TLS握手/协议边界，改写为TXAPI既有测量消费者的实际协商记录，不复制LGPLv3实现或引入HTTP2 PING/回退策略。测量沿既有独立时钟/配对差值实现；TLS服务器只声明HTTP/1.1，记录实际TLS版本、ALPN与HTTP协议，不能冒充H2性能证据。L0/公开地基不变。
4. **行为/范围：**直连为验证证书的TLS HTTP/1.1；网关边缘为loopback明文HTTP/1.1、上游为相同TLS fixture。两目标采用相同认证/载荷/事件节奏，分别保持连接，逐对交替次序；报告首正文、首有效文本、完成和事件观察区间、完整分母/失败样本、实际attempt、连接/资源与排空。只度量该拓扑，不声称双端TLS或实际反代性能等价；所有源码/资源/harness摘要单列。
5. **验证与预算：**夹具分别覆盖Chat/Responses的合法CA/IP、未知CA和错主机拒绝、完整/截断/合法chunk-extension慢滴的绝对接收deadline与排空；已有明文档案和capacity消费者回归。Linux CI新增每操作20对的TLS生产主进程self-check/有限测量，保留生产Key401、测量前新库、全部失败和配对数；不把独立P99相减，不用20对声称P99通过，不新增或事后挑选延迟阈值。
6. **协作/限制：**本批只补TLS档案与测量证据，不是性能优化；证书/密钥/配置/SQLite及报告只在临时目录/Runner temp，不提交。真实Provider按用户要求跳过，H2配对测量、多并发TLS/慢读、长期soak与完整P0性能验收仍开放；strict/S5-03、账号池和收费许可不变。

本地受控验证：Windows GOARCH=amd64 的TLS夹具在Chat/Responses均通过完整事件、未知CA/错主机拒绝、截断拒绝、100ms慢滴绝对接收截止与排空；原有Go交付夹具和vet通过。首轮因叶证书缺AKI被Python严格校验拒绝，改为使用解析后的CA（含SKI）签发并生成标准AKI后复验通过，未放宽验证。既有明文生产main的Chat/Responses self-check及每目标3条测量全部完成、每请求1次发送；capacity并发4×每worker2请求×1轮，直连/网关各8/8、4条协调取消、未发送429与全部4名额恢复通过。Python语法/差异检查通过，性能数据不作优化结论；Linux TLS生产main与最终精确头完整CI/实际镜像仍待验收。

TLS测量最终头的完整CI已成功并合并。Linux PR运行中Chat/Responses分别直连20/20、网关20/20，逐请求1次发送、20个成功配对，结束上游活动为0且无drain失败；该默认TLS档案的配对首正文增量P95为41.831/43.544ms，首有效文本31.771/33.519ms，事件观察上界P95为40.071/40.253ms。尚无TLS延迟预算，不将正确性通过解释为性能通过；数据保留在PR #78及运行日志。当前干净头的Windows明文档案独立重复两轮各100对也全部完成，配对首正文/首有效文本P95为2.7273/1.1400ms与3.1072/1.5926ms、事件上界P95为0.4806/0.5334ms，满足此前固定的明文串行预算，完整源码摘要、连接和资源事实已记PR #78，不扩大到TLS/H2或长期稳定性。

#### P0 TLS 连接条件对照子批（2026-10-20）

1. **层级/目录：**仅现有stream_baseline.py及其夹具回归、Verify、本方案；对实际生产main追加有界测量条件，不改Go生产实现或新增包、依赖、协议能力。
2. **依赖/契约：**沿既有disabled、Chat/Responses、生产Key、临时SQLite和Linux进程级CA接线；新增的connection-policy与fixture-tcp-nodelay只是测试CLI参数，不是产品配置或公开地基，无schema/OpenAPI变化。
3. **来源/地基：**仍对照sub2api固定f2e5b55f3e8069df1c02a9a27af537b5fdefa64e的http_upstream.go::buildUpstreamTransport中连接/协议画像的分离；实际Python测试服务器的TCP条件没有可直接移植的产品实现，因此在现有测量owner独立设置、记录真实socket值，不复制LGPLv3实现或改TXAPI的Client/默认连接池。
4. **范围/事实：**固定比较persistent/fresh客户端策略与TLS夹具接受端TCP_NODELAY=0/1四种条件，两操作分别记录；fresh在每次调用前关闭该客户端连接，连接建立计入请求延迟、前次关闭不计入。上游逐attempt记录peer、实际TLS/ALPN/HTTP、TCP_NODELAY和TLS会话复用事实，汇总观察到的peer数量与处于恢复会话上的请求数，不能把peer数当严格连接ID或把客户端新连接直接当上游/会话未复用。
5. **验证/预算：**四种TLS夹具操作/选项组合验证真实socket值、完整/截断/慢滴deadline、未知CA/错主机；已有明文persistent及新增fresh生产消费者、capacity恢复保持。Linux CI保留原默认档案，追加其余三组条件、每操作20对和self-check，保留全部失败、资源与摘要；每组使用独立新库，不修改健康以跑分。既有明文预算不套TLS，不倒设阈值，不因TCP_NODELAY数据变好宣称生产优化。
6. **协作/限制：**约40ms增量目前只是待解释观察，本机有限新TLS连接实验未隔离根因，不将Nagle假说当已证明结论。此批只准备同环境受控对照，不承诺解决延迟；网关边缘仍明文、上游TLS H1，H2配对/双端TLS/反代/长期soak/S5-03与真实Provider跳过边界保持原状。

本地受控验证：Windows GOARCH=amd64 的Chat/Responses × TCP_NODELAY=0/1四种TLS夹具组合通过证书、完整事件、截断与慢滴绝对截止检查，逐请求核对真实socket选项、TLS会话复用标记和peer。新增fresh客户端策略在两个生产main明文档案均完成直连3/3与网关3/3、每目标观察到3个客户端端口、每请求一次发送、上游活动归零且drain失败为0；这只是消费者回归，不是Linux TLS条件对照或性能预算通过。共享harness的既有capacity消费者也通过并发4×每worker2请求：直连/网关各8/8、4条协调取消、满额429未发送、全部4名额恢复。Python语法/差异检查通过。运行基于bcce1fc且harness未提交（dirty=true），stream摘要fe062f0518953776d31e1e498b26c4125a26957c6bfc7067209f69921932aba3；最终精确头完整CI与Linux TLS四条件结果另记。

最终精确头 `0451712ca656b3a1e6234a7ff5ad22c66272892a` 的 push Verify `39369288522` 与 PR Verify `39369334805` 全部六项成功，PR #79已合并为 `189eaef53ccf058acd5cae2e7795a99983e69a1d`。两套Linux运行各含两操作×四条件，每条件直连20/20、网关20/20、20个成功配对且每请求一次发送，上游活动归零、drain失败为0。PR运行的配对首正文P95（Chat/Responses）为：persistent/NODELAY=0的42.3643/44.3820ms，fresh/0的1.4326/2.7738ms，persistent/1的2.5007/4.4698ms，fresh/1的1.5501/3.2844ms。两套运行中NODELAY=0的新连接直连也出现约40ms事件观察延迟；NODELAY=1的网关事件观察上界P95为0.1519..0.2455ms。每条件网关观察20个上游peer，直连persistent为1、fresh为20，TLS恢复会话请求数均0；这证明本夹具条件和连接画像影响比较，不提供纯网关开销或底层Nagle/ACK包级归因。push运行persistent/0的Chat首正文P95仍有135.0432ms长尾，fresh/1的Responses首正文/首有效文本P95为10.8820/10.7992ms，未隐去或宣布TLS性能整体通过。报告保留dirty=true、各自产物SHA和Linux原始字节摘要；完整数值及限制在PR记录，合并后CI独立跟踪，生产传输参数未改。

#### P0 实际镜像双端 TLS/H2 配对基线子批（2026-10-20）

1. **层级/目录：**仅 tests/upstream/nativews、tests/smoke/native_ws_delivery.py 和本方案；复用原实际非root镜像、隔离Docker网络、真实Key鉴权、SQLite及用量历史，不改生产Go、配置契约、依赖或迁移。
2. **公开依赖：**当前Chat SSE disabled消费者与默认Transport；新增夹具TLS反代入口，形成客户端H2/TLS→反代→H1生产main→H2/TLS受控Provider，与H2/TLS直连配对。临时CA只在夹具进程和原网关子进程信任，不改宿主信任或dialer。
3. **来源/地基：**编码前核对sub2api固定f2e5b55f3e8069df1c02a9a27af537b5fdefa64e的backend/internal/repository/http_upstream.go::buildUpstreamTransport，采用分别记录连接与实际协商协议的场景；其产品连接池/PING代码不适用于测试计时，因此在既有交付夹具独立实现有界测量，不复制LGPLv3代码，不更改L0。
4. **行为/限制：**单次夹具运行最多20对，逐对交替目标顺序，两目标独立复用Client；固定8段Unicode文本、终态及DONE，Provider终态后等待取消。Provider写入前/Flush后与客户端完整事件时间均来自同一进程单调时钟，报告事件延迟区间、首正文/有效文本/完成与配对差值、失败分母、实际协议/连接、单次发送和排空。直连不包括网关鉴权/SQLite成本；网关含原用量历史，不能宣称两者功能成本相同。
5. **验证/预算：**夹具集中覆盖真实H2/TLS及反代、完整事件、截断/错误终态拒绝、绝对超时、重复标签/容量/并发测量拒绝、取消排空；实际镜像最终CI运行20对并核对新增20条sealed Chat用量及零资金/历史副作用，既有WS/SDK/Codex/强杀断言保留。只报告基线，不倒设H2预算或以20对声明P99通过；不重复生产Go本地全量检查。
6. **协作/回退：**原fixture用量容量由32显式增至64容纳原32及新增20条，仍保持有限预留与持久断言；报告携带源头/夹具摘要且不含密钥。此批不声称慢读/并发H2性能、CPU/RSS、持续满载、真实Provider或strict成本证明；回退仅删除测量夹具，无数据/接口回退。

本地集中验证：Windows GOARCH=amd64 的完整nativews夹具包最终2.267s通过，含3对实际H2/TLS直连/反代测量（本地反代为夹具映射，并非生产main）、完整事件/usage与时间区间、截断/缺终态/错误usage拒绝、绝对截止与排空，以及原CA/HTTP/WS消费者。包vet、Python语法和差异检查通过。测量不以纳秒数非零判断有效输出，允许Windows时钟分辨率范围的零间隔。生产Go树保持现状；本地无Docker，实际非root镜像20对及原32条用量与20条新增回执、固定SDK/Codex和强杀链必须由最终精确头完整CI独立验收。

首次Linux交付测量完成40/40、20个成功配对，逐请求1次发送与排空均通过，但随后夹具仍用默认50条分页等候52条记录，持续轮询后触发120次/分钟查询限流，故交付整体失败。修复仅显式请求允许的limit=100，并要求has_more=false，不扩大生产查询/留存容量、取消限流或丢弃旧记录断言；最终修复头完整CI重新验收，首轮数据仅为有限观察，不能把测量成功当交付通过。

随后核对到用量验收假设超出现有Chat能力：gateway仅从实现execution/v3.Decoder的消费者取得原生明细，现有Chat legacy解码器会将持久明细保留unknown，日志中的v1计数不转换为v3事实。本批保持这一边界，客户端仍严格校验原始SSE的2/8/10，但持久验收改用实际X-TXAPI-Usage-ID关联全部20个新增sealed回执，逐条核对单次dispatch以及六个原生数量均为null、source/completeness为unknown；不根据线端数据伪造持久事实，不为测量新增生产解码能力或收费许可。首次20条原生持久计数的错误预期作废，相关失败保留在CI日志；Chat原生明细若另行补齐，需独立功能切片及契约验证。

#### P4-CHAT-USAGE-001：Chat 原生用量接入既有观察契约（2026-10-20）

1. **层级/目录：**L2 `internal/protocol` 在 OpenAI Chat 解码入口实现现有 execution/v3 观察，复用同一完整 SSE 事件状态机；L1 gateway 和生产 main 的现有 usage recorder 为真实消费者。修改协议实现、适用网关/生产入口回归、既有镜像消费者和本方案/OpenAPI说明，不新增发送链、包或观察数据库。
2. **公开依赖/冻结：**只消费已批准 execution/v1/v2/v3/v4、usagehistory/v1 与当前准入/账本端口，不新增公开签名或扩大冻结范围。OpenAI兼容 Chat JSON/SSE 的标准 `prompt_tokens`、`completion_tokens`、`total_tokens`、`prompt_tokens_details.cached_tokens` 与 `completion_tokens_details.reasoning_tokens` 按真实存在性映射；cache-write 未声明，保持未知。Anthropic/Gemini转换、embeddings、Responses、后台/WS另按原能力。
3. **来源/地基：**主参考 new-api 固定 `25372e30b1f94255b1f818aeaaf006f042572580` 的 `relay/channel/openai/usage.go::extractCachedTokensFromBody`（53–82），采用标准字段指针区分零与缺失的做法；不采用以零触发跨厂商回退、估算或自动补总量。按本项目现有 v3 观察和累计校验独立改写，不复制第三方实现。L0、HTTP客户端、解析线程、配置和schema不变。
4. **行为/兼容/回退：**保留JSON原始字节与SSE完整事件的未知字段、顺序和工具内容；每流独立观察，使用现有JSON UTF-8、32层嵌套和重复键边界。原生数量仅接受非负精确int64，保留零/部分/未知，显式总量和子项受既有一致性/溢出规则约束，非法数量拒绝，不根据文本或其他数量推导。SSE在完整usage事件观察快照，在合法finish加DONE后只交付一次真实终态；截断、取消和deadline不伪造完成。只有完整终态数量投影至原聚合计费，strict许可/费率/未知hold不扩大；去掉私有v3包装可退回既有观察能力，无迁移。
5. **验证/验收：**JSON/SSE原生完整、零、部分、缺失/null、细分单独存在、非法类型/负值/不一致/溢出/重复键；多usage累计、终态后无新数据、跨块UTF-8和多行事件、工具/未知字段保真、截断/取消/首输出/空闲/进度；真实socket和生产main按实际usage ID查询落库并重启读取，逐次发送与disabled资金不变。strict既有JSON/SSE结算、未知保护及强杀矩阵完整回归；集中build/vet/全量Go、SDK/镜像与最终精确头完整CI后合并。
6. **协作影响：**只在OpenAI Chat私有包装中接入v3，避免公共基础decoder连带改变跨协议转换。镜像消费者新增已知数量预期必须绑定实际wire/usage ID，保留既有记录数、单attempt、强杀与资金断言；更新能力说明并保留旧批次unknown历史事实。真实Provider/OAuth继续按用户要求跳过，收费、长期容量和S5-03不随本批完成。

本地受控结果：JSON/SSE协议定向回归通过完整、零、部分、未知、细分单独存在及非法数量/重复键拒绝、原始wire、独立流与进度；双端真实socket的10个正常组合和累计下降/截断/取消/null四个场景通过。12个strict JSON/SSE场景各只有一次发送：完整11/7/18按原价只收5850000单位，显式零正常结算为零；部分、仅细分、未知与非法快照均保持pending，user/key各保留20000000000单位hold，历史Provider成本准入不丢失。完整终态前的已知观察不被当作聚合财务回执。

Windows GOARCH=amd64 build与全量vet通过；全量Go首轮仅旧TestFactsDoNotInventProvenanceForLegacyDecoder把内置OpenAI解码器隐含视为v1-only的假设失败，其余包通过（cmd/gateway92.198s、architecture38.304s、billing41.680s、protocol2.732s、generation83.890s、store82.004s）。改为显式v1-only包装并保留未知来源原断言后，完整gateway包66.113s及其vet通过，包含既有11场景真实Process.Kill恢复矩阵。生产主进程local.py通过，真实usage ID对应JSON/SSE4/1/5、cached=2、reasoning=0、cache-write=null，重启保持原样；旧首块强杀/不重放、关闭采集、权限隔离和资金不变断言保留。Python AST、OpenAPI0.13.12/117与差异/格式检查通过。最终精确头完整CI、Linux race与实际镜像新增已知回执组合另验，不能继承PR #81的绿灯或回写其历史unknown记录。

固定官方Python SDK 2.11.0/httpx0.28.1的Chat/Responses JSON/SSE、工具/UTF-8、终态、截断、关闭和准入释放受控回归同样通过；客户端wire不因增加原生观察而改写。SDK证据仍限loopback夹具，不证明真实Provider。

本批首轮精确头231fd947的两套CI均在原慢读/不读回归失败：旧夹具要求先填满中间HTTP/2窗口，再在2.5秒总预算前观察连续100ms的上游写入停顿，race运行未观察到该条件。新增完整JSON校验可能影响填满窗口的速度，但没有同条件吞吐证据，不据此断言生产性能根因。仅修正该测试的施压与观察位置：反代accepted TCP发送缓冲显式设1024字节，直接计数TLS下实际socket Write的开始/结束；同一写入连续100ms未返回且上游仍活动才证明真实背压。保留1024个有限32KiB事件、真实TLS/HTTP2中间跳、2.5秒总预算/12秒attempt预算、慢读/不读两种条件、单attempt、全部准入/上游排空和后续请求恢复；不删校验、不扩大生产deadline或缓冲，不把JSON处理速度当背压证据。新版完整精确头CI另验。

该测试修复后的完整cmd/gateway本地回归另暴露旧WS预热测试的竞态：native Execute的准入release defer先于usage finish defer执行，因此名额归零不代表用量已封口。仅在原测试增加最多3秒等待预期数量的sealed父记录，再执行原有单attempt、零/未知与权限断言；原已知零/未知组合连续5轮通过。生产生命周期与持久写入顺序未改，不以延时读取掩盖实际失败状态。

两处测试修正后的Windows GOARCH=amd64集中复验：全部P0真实网络子组10.999s、WS预热权限/用量子组5轮12.082s通过，最终完整cmd/gateway包67.045s及包vet通过。首轮CI其他包与两套实际镜像交付均通过；因测试修正产生新头，最终完整CI仍重新验收。两轮清洁231fd947头各100对完整main HTTP/1.1明文串行测量均100%完成、一次attempt、排空无失败；首正文配对P95为3.5151/3.4699ms、首有效文本1.9550/1.7954ms、网关事件上界P95为1.1023/1.2727ms，满足原5/5/2ms预算。原始报告/摘要与资源数写入PR，测试修正不改变所测生产与harness源码；不外推TLS/H2、容量或长期稳定性。

#### P4-EMBEDDINGS-USAGE-001：embeddings 费用回执存在性保护（2026-10-20）

1. **层级/目录：**仅L2 internal/protocol 的既有 embeddings JSON 解码、协议/网关回归、本方案与OpenAPI说明。实际gateway与原strict/legacy账本是消费者，不新增发送链或观察记录。
2. **公开依赖/冻结：**沿用execution/v1 Decoder/Usage，不新增签名、冻结范围或v3观察实现。embeddings的输入专用聚合计费仍使用prompt_tokens/total_tokens与零输出的既有操作语义；原生明细仍unknown，不把财务投影冒充Provider显式输出Token。
3. **来源/地基：**主参考new-api固定25372e30b1f94255b1f818aeaaf006f042572580的relay/channel/openai/relay-openai.go::OpenaiHandler，采用解析错误和Provider error先拒绝再处理用量的顺序；其零值估算/补总量不满足本项目未知保护，因此独立改写存在性校验。既有decodeEmbeddings忽略Unmarshal错误且缺失字段归零，可能将usage:{}结算为已知零费用。L0、账本、配置、schema与成本许可不变。
4. **行为/兼容/回退：**保留向量/base64/未知字段原始JSON字节；缺失/null/空usage或缺少prompt_tokens/total_tokens时返回未知聚合用量，strict保留pending及双hold，legacy沿原未知估算路径。只有两个字段显式存在、非负精确整数、相等且适合既有int范围时形成已知输入专用回执；可选completion_tokens只能缺失/null/显式零。非法JSON、重复键、非对象error响应、非法类型/溢出/负值/矛盾数量拒绝解码；已发送事实不清零，保持原重试/提交策略。回退旧二进制会恢复该缺陷，无数据迁移。
5. **验证/验收：**先复现空usage/部分/null被当成零或完整回执，再覆盖有效非零与显式零、未知/大小写别名、重复/非法/矛盾及原始wire。实际loopback上游与strict账本验证一次发送、正常只按输入计费、未知/非法均保留双hold与成本attempt；保留原legacy缺失用量估算、Chat/Responses和拒绝stream消费者。集中协议/gateway及适用完整Go/main/SDK回归，最终精确头完整CI后合并。
6. **协作影响：**与Chat原生观察分批，不改其解码器或历史记录，不新增embeddings原生明细承诺。OpenAPI仅声明费用回执的已知/未知条件；S5-03、真实Provider及后台/WS收费继续开放。

### P1：最小托管账号推理

- **层级/目录：**L1 `internal/upstreamidentity`、`gateway`、`scheduler`；L2 Provider 编解码；`cmd/gateway` 注入；现有 L3 生成功能消费。
- **依赖入口：**既有 `upstreamauth/v1`、identity/admission/execution 已批准范围；upstreamauth 不推断全量冻结。
- **地基影响：**保持 L0 稳定；先确认 L1/L2 凭据解析/资格/发送边界是否缺公开契约，必要时提出具体 CR。
- **行为/契约：**先明确一个 Provider、账号来源和操作，受控模式为 disabled；绑定账号/credential revision，发送前重查状态与权限。静态 Key 回退只按显式政策，不静默扩大账号范围。
- **验收：**停用/重授权/刷新与发送竞争、旧 revision、过期、uncertain 禁发、取消、无密钥泄漏和静态 Key 回归；授权真实账号下分别记录成功/拒绝/断开证据。
- **协作影响：**共享配置、注册、组装由本批协调；不扩大 strict，不顺带建设完整账号池或管理页面。

<details>
<summary>FC-P1-001：SIWC 单账号 Responses 推理接线具体 CR（已批准）</summary>

#### FC-P1-001：SIWC 单账号 Responses 推理接线（已批准）

日期：2026-10-19。用户已明确批准以下 CR 的源码实施与受控验证；不代表真实账号调用授权，也不表示实现或验收完成。

##### 1. 层级、落点与完整切片

交付“持久托管凭据绑定 → 重启加载 → 唯一 registry/scheduler → 当前 owner/Key/model 校验 → 发送前账号凭据复核 → 现有 Responses HTTP/SSE 执行与输出”。L1：store、controlplane、upstreamidentity、scheduler、gateway；L2：protocol/openaiauth、现有 Responses、server；组装根：cmd/gateway。测试及 OpenAPI 同批更新；不新增前端。

仅一个启用的绑定：一个既有 R04a OpenAI SIWC 账号、一个 owner、一个调用 Key、一个实际模型 slug、一个真实渠道与真实 credential 行。仅 `billing.mode=disabled`、文本、自包含 `input` 数组、`store=false`、`stream=true` 的 Responses HTTP/SSE。操作、字段和权限不合规时在网络前拒绝；拒绝本地/远端父链、conversation、background、WS、工具和媒体。专用入口不改变独立标准 API Key 路线。

##### 2. 依赖与新增公开端口

复用已批准的 execution v1/v2/v3、identity/admission、Responses 及配置 publication。保持现有 upstreamauth/v1 与 configuration/v1 类型/签名。新增候选 upstreamauth/v2 的窄凭据接口（版本登记不代表冻结）：

```go
type AccessRequest struct {
    CredentialID uint
    ExpectedBindingRevision int64
    AccountID string
    ExpectedRevision int64
    ExpectedCredentialRevision int64
    OwnerID uint
    TokenID uint
    Model string
}
type AccessCredential struct {
    AccountID string
    Revision int64
    CredentialRevision int64
    AccessToken upstreamauthv1.Secret
    ExpiresAt time.Time
}
type InferenceCredentials interface {
    AcquireAccess(context.Context, AccessRequest) (AccessCredential, error)
}
```

L1 gateway 是唯一推理凭据消费者；不返回 refresh/id token，不缓存 access token 到渠道快照、任务、管理 API 或日志。不开新 sender、刷新流程、重试权威或一次性授权流水表。

configuration/v2 追加托管配置端口：`ManagedBindingInput{AccountID string; OwnerID,TokenID uint; Model string}`；持久视图追加真实 `ChannelID/CredentialID`、绑定 `Revision` 和启用状态。提供创建托管渠道及其初始绑定、读取、按预期 revision 更新/停用的管理操作；由现有配置 owner 事务保存并发布。创建输入复用 v1 的渠道基本字段，但不接收静态 API Key；渠道模型必须恰为绑定 slug，暂不允许模型映射。现有 v1 静态写接口遇到托管行返回明确冲突，不能覆盖来源或 grant；托管读取通过 v2 明确展示来源和脱敏绑定。

管理员 API：`POST /api/admin/managed-channels` 接收 `{channel: <v1渠道字段，禁止api_key>, binding: <ManagedBindingInput>}`，原子创建真实渠道/credential/绑定并返回201；`GET /api/admin/managed-credentials/{id}` 读取；`PUT` 同路径接收 `{expected_revision: <十进制字符串>, binding: <ManagedBindingInput>}` 做CAS更新；`POST /api/admin/managed-credentials/{id}/disable` 接收 `{expected_revision: <十进制字符串>}` 停用。字段缺失/非法400，当前管理员权限不足403，缺失404，revision/来源/唯一绑定冲突409；保存成功但发布失败沿既有SavedError/Change语义，不能谎称未保存。上述操作复用现有认证与配置owner，不增加第二管理平台；JSON与错误写入OpenAPI。

##### 3. 地基影响、迁移与回退

schema18 追加 credential 来源与持久绑定（账号、owner、调用 Key、模型、绑定 revision；必要时使用与真实 credential 一对一的表），已有 credential 默认 static_api_key。托管绑定使用真实数据库 ID；不伪造 Key/ID，不把 OAuth token 复制入静态凭据字段。已有账号 token 继续由既有 AEAD envelope 持有；账号解密器、静态密钥迁移器及健康持久化按来源分支，不能将空静态 Key 当托管损坏或反向复制密钥。

保持 L0 不变。execution/v1 的 CredentialID 在此子集允许引用真实托管 credential 绑定行，需要明确批准该语义；不追加 Invocation/Outcome/TargetConstraint 版本。托管目标 fingerprint 绑定来源、绑定 revision、账号/credential revision、官方目标和模型，不包含 token。本批拒绝托管续链与持久重放，不将此语义外推其他模式。

升级按 PLAN 停写、一致备份并应用 schema18；旧 schema17 二进制拒绝打开18。回退恢复匹配的停写备份；不删除账号、改写旧迁移或账本来降级。迁移失败不得部分发布绑定。

##### 4. 授权、目标与生命周期语义

`AcquireAccess` 复用账号仓储已有跨进程 SQLite 短写事务锁：事务内先校验真实credential绑定启用、binding revision与可信调用身份/Key/model匹配，再校验账号provider、Active、无 refresh claim、两个账号revision、凭据有效期、已获批且已知scopes与受控SIWC来源，解密后只在提交成功时返回access token。请求中的owner/TokenID由L1从已认证principal填入，不能直接接受外部请求自报身份。配置绑定修改/停用同样参与该事务排序；提交失败或结果不确定不返回token、不发送、不自动补试。

**线性化点是成功提交的凭据发放。** Disable/Refresh/重授权先提交则后续 Acquire 拒绝；Acquire 先提交则当前 attempt 已获授权，之后停用/刷新阻止新发放，但不保证撤回已授权的实际网络发送。此边界明确适用于另一个 CLI 进程，不以进程内 mutex 宣称全局撤销。每次发送前重新取凭据，不跨 attempt 复用；gateway 私有调用栈只消费一次，仍沿既有 executor 发送并记录实际/未知事实，不声称上游 exactly-once。

当前可信身份、Key/模型权限、grant 与绑定状态在 registry/scheduler 筛选及实际 attempt 准备前核对。托管选中后失效/错误即结束，不换其他账号、渠道、静态 Key，不自动刷新或恢复旧 attempt。原静态路线保留现有行为。

只允许受控 R04a SIWC flow 为 `resource=https://api.openai.com/v1` 签发、具备 `resource.invoke` 与 `chatgpt.tokens.use.direct` 的 access token；ID token 的 client audience 不充当资源授权证明。Issuer/client 身份沿现有校验，来源缺失、scopes 未知或不匹配则拒绝。未知/任意导入 OAuth token 不继承许可。

推理 URL 固定 `https://api.openai.com/v1/responses`，模型目录 URL 固定 `/v1/models`；禁任意 base URL、跨 origin redirect、额外用户上游 Header 和 backend-api 目的地。部署 transport 保持正常 TLS 验证，代理不能改变已验证 origin。模型目录采用专用有界 decoder，识别 `models[]/slug/visibility`；按账号/credential revision 缓存最多60秒，只接受配置的一个可见 slug，目录缺失/过期或代次变化失败关闭。目录调用复用 core 支撑的现有机械 HTTP owner；需要将现有 OAuth Caller 的目标白名单明确扩展为上述固定 GET，并只为该 GET允许有界 Bearer Header，不把 OAuth token endpoint白名单改成通用请求器。

##### 5. 配置、消费者与验收

新增 `upstream_accounts.inference.enabled: false`，只在 disabled、凭据加密已启用时允许开启；绑定由上述管理员配置端口持久创建，不用 YAML 静态渠道 seed 填假 Key。启用但无有效绑定时拒绝托管调用。最多一个绑定，删除/撤销 Key、用户或账号后失败关闭。当前配置重载、启动、退出仍沿唯一 lifecycle。

受控验收贯穿真实消费者并覆盖：创建/重启/发布回退、跨 owner/Key/model拒绝、源类型隔离、无静态假凭据、两独立 DB 连接的 Acquire↔Disable/Refresh排序、提交失败无 token、旧 revision/expired/unknown scopes/uncertain 拒发、禁字段发送前拒绝、目录状态/代次绑定、403/429/failed/incomplete/截断、取消/退出与不补 DONE、access/id/refresh 无日志泄漏、单次 executor及无fallback、静态 API Key 回归。全量现有 CI（Linux race、冒烟、交付）按精确提交验收。

真实闭环另需明确账号、模型、请求次数/订阅或费用预算与测试输入授权；批准本 CR 只批准源码实现及受控测试，不能自动调用真实服务或宣称账号已验收。

实现入口：渠道类型使用 `openai_siwc`，`base_url` 固定为 `https://api.openai.com/v1`。新增 `upstream_accounts.inference.enabled` 默认 false；启用同时要求 Responses、凭据加密和 disabled。管理操作仅开放当前管理员 session 的上述 `/api/admin` 路径；不以调用 Key 或管理静态 Key 代替当前 session。读视图只含真实 ID、来源、绑定和十进制字符串 revision，不回显 access/refresh/id token。托管写入、保存后的缓存失败与发布 pending 沿同一 configuration owner 处理；v1 静态写接口不能修改或删除托管行，返回409。

P1 使用 R04a 新签发的受控来源记录和资源标记，并将它们绑定到加密 envelope；schema17 旧账号来源保持未知，不自动推断其推理权限，需按已有重授权流程重新建立证明。CLI 停用/刷新/重授权后，旧 registry 的账号代次不能继续发放凭据；完成操作后通过现有配置 Apply 或重启加载新代次，目录缓存也必须匹配该代次。首批不自动刷新，不静默切到静态 Key。

本批已接通受控源码与完整消费者：管理员创建、持久重启加载、唯一 registry/scheduler、当前 API Key 鉴权、固定目录和 Responses SSE；跨进程停用后拒绝新发放。匹配托管 grant 时，停用渠道/凭据、后台和原生 WS 均在发送前失败关闭，不能回落到静态 Key；其他 Key 的独立静态路线保留。受控测试使用合成凭据与隔离上游；完整构建、契约、race、冒烟和交付的精确提交证据以本批 PR 为准。尚无真实账号运行验收，不据此把 P1 或整条路线记为完成。

##### 6. 其他模块影响与批准范围

共享契约、schema18、配置发布、OpenAPI和组装统一集成；独立协议/性能工作继续。暂不交付自动刷新 worker、账号池、窗口调度、后台/WS收费或付费开放。未批准的原工作区 `upstreamauth/v1/inference.go` 保持原样，不能视作接口授权来源。

已批准范围：上述 P1 子集、configuration/v2/upstreamauth/v2 追加端口、schema18真实来源/绑定、CredentialID指向托管绑定的语义、凭据发放与停用排序及不撤回已授权发送的边界、固定目录/推理目标和单grant无fallback。公开字段、生命周期或范围超出这些边界时另提CR。

官方依据（2026-10-19核对）：
- https://developers.openai.com/siwc/token-sharing-open-source/sign-in
- https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions
- https://developers.openai.com/siwc/token-sharing-open-source/token-reference
- https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference
- https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations

</details>

### P2：有界自动刷新

- **层级/目录：**L1 `internal/upstreamidentity` 持有 worker；`internal/config` 定义启动参数，`cmd/gateway` 只构造既有 Manager/Provider/caller、注入配置发布、启动、取消和 join。
- **依赖入口：**复用 `upstreamauth/v1.Manager.List/Refresh`、既有 claim/start/complete/failure/recover 和 `lifecycle/v1.Drainer`；通过既有管理配置 Apply 发布代次。worker 不读私有 store、不发送 HTTP。
- **地基影响：**既有 owner 内增量，无公开地基契约、迁移、管理 API 或 L0 变化。当前账号容量 1000 与每页 100 可满足完整扫描，未增加候选查询接口。
- **行为/契约：**默认关闭；单 OpenAI Provider 串行、无队列，完整分页扫描后按账号 ID 轮转。扫描失败/重复/超限时本周期不刷新；started/uncertain 不重放。刷新与手动 CLI 共用同一 claim/CAS 权威，发布失败只修复发布，不重做刷新。
- **验收：**分页公平、跨周期限速、预算、状态过滤、重复 worker/手动竞争、轮换后持久失败、取消 join、四个持久点的真实子进程强杀重启与 Provider 连续失败冷却；本批精确提交的完整检查记录于 PR，真实 OAuth/账号验收暂缓。
- **协作影响：**刷新独立于推理开关和收费模式，但要求既有 AEAD 密钥。账号代次变化通过既有配置 owner 更新推理快照；关闭先取消/join worker，再释放 caller 和共享 gateway/store。关闭刷新或旧版本回退保留全部账号持久状态，不新增第二套 OAuth 或分布式协调设施。

本切片参考 sub2api 固定 `f2e5b55f3e8069df1c02a9a27af537b5fdefa64e` 的 `backend/internal/service/token_refresh_service.go`（Start/Stop、`processRefreshContext` 的全量分页、Provider 门控），用 TXAPI 的 Go 类型和既有账号状态机独立实现；未复制参考项目的存储、锁、网络发送或结果重放流程，许可归属沿用文末记录。

`upstream_accounts.refresh` 为启动配置：`enabled: false`，`interval_seconds: 30`（5..3600）、`before_expiry_seconds: 600`（30..86400 且不小于扫描间隔）、`max_attempts_per_cycle: 4`（1..16）。启用时立即扫描，之后每周期结束再等间隔；只刷新 active/openai、有 refresh token、临近到期且已过 NextRefreshAt 的账号，不将 reauthorize/uncertain 自动恢复为可发送。

每周期最多 60 秒、扫描最多 5 秒/10 页/1000 条，单次 Refresh 最多 20 秒；既有 Manager 可另用最多 5 秒持久化取消结果。Provider 相邻尝试至少 1 秒，同账号至少 60 秒；连续两次 unavailable/uncertain/deadline（含跨周期）暂停该 Provider 60 秒。内存计时只作节流，持久 claim/fence 才是执行权威；进程重启不凭内存节流证明 exactly-once。

首次非空扫描、检测到账号集合/代次变化和每次 Refresh 后均通过 Apply 发布；发布失败保留待修复标记并阻止后续刷新，下次完整扫描先修复发布。Apply 沿用同步发布的既有生命周期，context 在入口和返回时核验，不能中断已开始的 Apply；join 超时保留依赖并继续等待，不后台遗留占用 store 的 worker。强杀测试在 claim、started、上游轮换返回但未保存、保存完成四点分别 Kill/Wait，再模拟 lease 到期验证恢复，不把缩短模拟时钟当真实等待或真实 Provider 证据。

### P3：账号资格与限流恢复

- **层级/目录：**L2 Provider 解析；L1 scheduler/upstreamauth 持有状态与决策；L3 后端查询消费。
- **依赖入口：**execution、routes、upstreamauth 已批准端口；账号窗口/能力事实不视作现成冻结能力。
- **地基影响：**涉及 L1/L2 契约；CR 明确状态 owner、来源、新鲜度和更新条件。
- **行为/契约：**区分权限不足、凭据失效、账号/模型/窗口限流、拥塞和网络故障；记录 reset/未知值及 revision，先资格后评分、发送前复核，提供脱敏拒绝/恢复理由。
- **验收：**本地估计不伪造真实窗口，旧响应不处罚新凭据，模型限流不扩大到账号，权限不足不盲目重试，专属不可用不静默回落，reset 缺失/时钟偏差可解释。
- **协作影响：**为推理、刷新和 shadow 提供唯一资格事实，不在本批启用真实多因素评分。

<details>
<summary>FC-P3-001：单绑定推理资格反馈与人工恢复 CR（已批准，源码及本地验证完成）</summary>

本 CR 仅覆盖 P1 已批准的唯一 active SIWC 绑定和当前 HTTP 文本目录/Responses 消费者。依据固定 sub2api `f2e5b55f3e8069df1c02a9a27af537b5fdefa64e` 的 `backend/internal/service/openai_account_runtime_block_fastpath.go` 分类与最小作用域思路独立实现；不采用其推测 reset、账号切换及同请求重试。官方核对来源为 [SIWC errors and recovery](https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery)：401/403 需核验授权上下文，429 不自动证明整个套餐耗尽或重置时间，503 不证明凭据失效。

1. **层级与写入。** L1 `upstreamidentity/store/gateway/management` 持有资格和 CAS；L2 `protocol/openaiauth`/`protocol/responses` 只分类可信固定上游的完整错误；`server/api` 提供管理员查询/恢复；组装、model、schema19、OpenAPI 和契约登记统一协调。保持现有 sender、执行、授权和刷新 owner。
2. **公开依赖与字段。** 经批准后给候选 `upstreamauth/v2.AccessCredential` 追加 `GateEpoch int64`；`AcquireAccess` 在原短写事务内检查资格，提交成功才发放 token 与 epoch。新增候选 `upstreamauth/v3` 的 `FailureRecorder`、可选 L2 `FailureObserver` 和 `EligibilityAdmin`，不修改冻结 v1、不宣称 v2/v3 全量冻结。`FailureSignal` 只含 reason/scope/provider-code 白名单、HTTPStatus 和 source，禁止正文、原始 message、token、URL 或任意标签；`FailureWrite` 包含原 `v2.AccessRequest`、issued GateEpoch 和 signal。`RecordFailure(ctx, write)` 返回 applied 与脱敏资格；`ReadEligibility(ctx, credentialID)` 和 `ResumeEligibility(ctx, request)` 返回同一资格 DTO。恢复请求必须带 credentialID、expected binding revision、expected gate revision、expected epoch，HTTP revision/epoch 均为正十进制字符串。
3. **持久化与权限。** schema19 在现有凭据下固定一条资格行，外键删除级联，不按请求或 token 轮换追加；最多 1000 行，超过容量时发放/创建资格失败关闭。字段为 credential/account/binding/model 标识、epoch、revision、blocked、固定 reason/scope/provider-code/status/source、observedAt、证据 account/credential revision、resetKnown 与 nullable resetAt。首批 reset 始终 unknown，无自建远端窗口估计。资格不改 account.State、token 或 refresh claim；静态 Key 不读写该状态。新增 SessionBearer 管理接口 `GET /api/admin/managed-credentials/{id}/eligibility` 和 `POST .../{id}/eligibility/resume`；调用 Key、普通用户和匿名请求不能操作。OpenAPI 预期升为 0.13.0/117 操作，既有四个绑定端点不改变 DTO。
4. **状态、排序与恢复。** Record 使用既有跨进程短写事务，核对完整 issued binding、account row revision、credential revision 和 gate epoch；停用/刷新/重授权先提交时，旧响应作为 stale no-op，不处罚新凭据。已有 gate 跨 token 刷新保留。成功响应永不清 gate，同 epoch 后续失败不能降级覆盖已阻断状态。人工恢复及每次重绑递增 epoch；同 account/model 重绑保留阻断，更换 account/model 才更新作用域，旧回包仍丢弃。恢复必须 CAS gate revision/epoch 和当前绑定，不等同于 Provider 已恢复；之后目录缓存键包含新 epoch，重新获取目录并发放凭据。新发放与 gate 写入按提交先后排序，已经授权的在途发送不承诺撤回。
5. **消费与兼容。** 现有 managed transport 仅按原读取节奏有界旁路观察非 200 错误（最多捕获 64KiB，截断/超限不解析 code），L2 分类不改变下游正文；SSE 通过可选 observer 从完整、已验证的原生终态保留固定 code，不从用户错误文案反推。目录/Responses 401/403 写 grant/authorization_review，保留 token；精确 usage-limit 429 暂停当前 grant，其他 429 至多暂停当前 model，reset_unknown 均需管理员恢复，不扩到整个账号/套餐。目录 200 但模型不可见沿现有拒绝，不写永久 gate。503 仅进程内 5 秒退避后允许下一条新请求沿既有 admission 尝试，不重放旧请求，不承诺单 probe 租约；取消、客户端写失败和普通参数错误不处罚凭据。反馈提交失败不能改写已发送输出，本进程阻止后续托管发送并先修复有界待写反馈；强杀前尚未提交的反馈不能保证恢复，不声称跨该窗口 exactly-once。
6. **验证、回退与影响。** 覆盖反馈→Acquire 拒绝、重启保留、跨进程刷新/重授权/停用排序、人工恢复后同 credential revision 的晚到旧响应、并发成功不清新拒绝、模型限流不扩账号、未知 reset 不自动放行、目录/SSE 分类、503 退避无重放、重复反馈/容量上限、提交失败及管理员权限隔离、静态路线回归和最终完整 CI。迁移只追加19，旧 schema18 二进制拒绝打开19；回退须停写并恢复匹配备份，不能关闭 gate 或删除行绕过拒绝。本 CR 不开放账号池、窗口调度、shadow 真实评分、收费、后台/WS 或真实账号调用。新前端仍后置。

用户已明确批准本 CR 的源码实施与集中验证修复；无真实账号调用授权。CI 后台执行，连续推进源码批次后集中修复，但未通过的检查不记为通过。

本子批已接通唯一存储、gateway 发放/反馈、L2 旁路分类、configuration 管理和真实 SessionBearer HTTP 查询/恢复链。定向测试已覆盖持久拒绝、独立重开读取、管理员恢复、恢复后重新获取目录、旧 epoch 回包 no-op，以及精确 int64 字符串、权限拒绝和错误脱敏。Windows GOARCH=amd64 完整 build、vet、全量 Go 测试及生产主进程冒烟均已通过；包含静态 Key、严格计费拒绝、SSE 强杀/恢复、后台、usage 和 shadow 回归。独立审查发现的取消等待问题已修复并定向复验。PR #51 精确头 `00adba13a4c6af2d69346e50d3ba2266b5ae25ae` 的 Linux race/交付 Verify 均成功并已合并；最新集成主线证据单列，不把旧通过扩展到新增阶段超时。

资源边界：待写反馈最多16个 issued tuple，同 tuple 合并固定分类，溢出永久阻止本进程新发放；未提交反馈不能靠重启恢复，不能把重启当解除拒绝的流程。串行反馈/发放的等待可按请求取消，普通内存锁不包数据库 I/O；发放含等待最多5秒，反馈独立5秒，SQLite COMMIT 失败另允许最多1秒清理确切连接，清理失败丢弃连接。反馈不会撤回已授权发送，不承诺强杀未提交窗口 exactly-once。


</details>

### P4：协议回归与转换诊断

- **层级/目录：**L2 `internal/protocol`、`compat`；`tests/smoke` 等适用回归；现有 L3 继续使用生成入口。
- **依赖入口：**已批准 text、responses、compat、httpoutput 和操作能力声明；不推断全部工具或转换方向已支持。
- **地基影响：**先修现有契约内缺陷；新增公开诊断、字段或共同子集语义前提 CR。
- **行为/契约：**按源/目标协议、操作、传输、字段和工具列范围；每流独立状态，诊断有界/去重/脱敏，无法表示则发送前拒绝。无真实消费者不新增通用转换平台。
- **验收：**多工具交错、参数分片/UTF-8、delta/done 去重、调用 ID/名称恢复、failed/incomplete、未知字段、usage 不完整、背压、取消和断流不伪造完成；补指定版本真实客户端组合。
- **协作影响：**不改资金/路由许可，能力变化同批更新 OpenAPI/已有消费者和支持声明；新前端暂缓。

#### P4 SDK 受控客户端子批

1. **层级与目录：**仅 `tests/smoke/sdk_conformance.py`、固定依赖、Makefile 和现有 Verify；使用生产二进制、临时加密 SQLite 和真实 owner/调用 Key 鉴权。
2. **公开依赖：**消费现有 Chat/Responses HTTP JSON/SSE 接口，无 Go 公开端口或冻结范围变更；固定官方 Python `openai==2.11.0`、`httpx==0.28.1`。
3. **地基影响：**无生产、L0、schema、配置或计费许可变化。每个故障场景使用独立渠道/凭据，避免健康冷却污染下一场景。
4. **行为与回退：**SDK 禁用自动重试、代理环境和环境凭据；loopback 上游使用逐字节 UTF-8 和多行事件。检查类型化 JSON、文本/函数参数、completed/failed/incomplete、终态后保持连接、截断、取消和单 Key 名额释放；13 个实际上游场景各一条请求，忙时请求不发送。SDK 该版本在干净 EOF 截断时不会自行报错，断言只得到部分内容且没有成功终态，不能把 EOF 当完成。
5. **验证：**Windows 独立虚拟环境定向运行已通过；Verify 增加相同固定依赖和回归；PR #50 精确头 `749c591d6284facd2cc00b571f900b8fc4976a40` 的 Linux Verify 成功并已合并。所有账号、Key 与上游均为一次性夹具，无真实 Provider 调用，不代表官方服务、其它 SDK 版本或 Codex/WS/后台已验收。
6. **协作：**保留已有冒烟、架构、race 和交付门槛；可通过 `make sdk-conformance PYTHON=<虚拟环境解释器>` 单独运行。新前端、收费和账号路线无变化。

#### P4 固定 Python WS SDK 子批（2026-10-20）

1. **层级/目录：**仅 cmd/gateway 的可选测试子进程、tests/smoke/ws_sdk_conformance.py、独立固定依赖、Makefile、既有 CI 与本方案；复用现有真实双端 socket/SQLite 的生产组件组装夹具。
2. **公开依赖：**消费当前 Responses WS、HTTP 历史检索接口；固定 openai==3.8.0、websockets==15.0.1，不替换原 HTTP SDK 2.11.0 环境，不新增地基端口。
3. **地基影响：**生产实现、L0、配置、schema、计费和权限不变；只有测试资源工厂使用 loopback，上下游发送次数与保存结果由 Go 夹具独立核对。
4. **行为/兼容：**官方 SDK 类型化文本/工具事件、同连接三轮、显式新连接续链、HTTP 检索、failed/incomplete、关闭客户端后的 unknown 拒绝及新根生成；SDK HTTP 重试/WS 重连均关闭，禁用代理和环境凭据。8 次明确新生成各仅1上游create；unknown父拒绝不发送、不补完成。
5. **验证：**独立 Python 虚拟环境、每客户端进程25秒总预算、唯一 reader、fixture进程 Kill/Wait 清理；核对8行历史/usage、7已知加1uncertain、完整上下文与工具call_id、真实Key鉴权及私有头隔离。集中受影响包/脚本检查与精确头完整CI，默认全量Go测试跳过需要Python依赖的helper。
6. **协作/边界：**这是固定SDK与生产组件组装的受控兼容证据，不等于实际main镜像、官方Provider、其它SDK/Codex、自动恢复、收费或长期容量验收；实际镜像由P6独立批验证。官方文档 WebSocket mode 用于核对connect/create方式，当前固定安装包源码另核对精确版本签名，无第三方实现复制。
2026-10-20 本地证据：固定 openai3.8.0/websockets15.0.1 的真实 socket 回归通过，SDK 解析8次新生成的类型化事件，同连接前三轮和新连接完整上下文/工具关联正确；HTTP 检索不触发上游。5次 completed、1次failed、1次incomplete与1次客户端取消分别保留事实；8条历史/usage和8条已发送attempt独立核对，取消历史uncertain且父链拒绝不发送，5条已知3 Token保留，另3条未知不补零。非成功终态及父链拒绝可能按既有策略关闭连接，仅对成功多轮声明同连接复用，后续使用明确新连接。Windows GOARCH=amd64 完整cmd/gateway回归通过（50.940s）及该包vet通过；新增独立持久事实断言随后在同一SDK harness内通过。Python AST、CI结构及diff检查通过。此处仍是生产组件组装和受控Provider证据，精确头完整CI另验，不表示官方服务或SDK自动重连通过。
P4 本批组合验证扩展：①在同一SDK脚本增加显式deployed入口，复用全部8个固定SDK用例；②仅扩展已受控验收的native_ws_delivery.py、fixture与既有delivery步骤，继续使用当前HTTP/WS接口；③实际gateway镜像和生产拨号/权限不变，独立非root Python SDK镜像只在构建阶段安装固定依赖，运行时加入唯一Docker internal网络；④在原6次部署生成之后执行8次SDK生成，核对全部14次create、2条unknown不可重放、13sealed/1interrupted用量、历史及恢复，不把组件与镜像的分离证据直接等同组合证据；⑤复用SDK客户端的25秒执行预算、容器命名/显式清理、网络与只读挂载检查，Linux精确头delivery验收；⑥本机无Docker，组合只能由CI证明，原有局部证据保留，不宣称真实Provider、自动恢复或收费许可。
#### P4 固定 Codex CLI 受控子批（2026-10-20）

PR #66 最终精确头 f9e0f5849d8b5d81732f537cff110b4e4796d4bb 的 Verify 39329673632 / 39329668835 全部六项成功并已合并；delivery 明确证明同一固定 SDK 客户端经过实际 main 镜像、TLS 夹具和反代的组合，14次 create、历史/用量、unknown 拒绝及强杀恢复均通过。初轮浅挂载导入宿主路径的脚本缺陷已修复，不把失败头记录为验收。

1. **层级/目录：**仅 tests/smoke、既有 CI/Makefile 与本方案；运行固定官方 CLI 0.163.0，经实际 main 的标准 API Key Responses HTTP/SSE 到隔离 loopback Provider。先观察真实请求，再按现有协议范围接入回归。
2. **公开依赖：**只消费现有 Responses 接口、当前鉴权/准入和 disabled 用量；CLI 使用独立命名 custom provider 与现有 Responses wire API。固定 release 资源和 SHA-256 校验，外部二进制不入库，不修改宿主安装。
3. **地基影响：**不先扩大工具种类或 hosted 能力，不改 L0、契约、迁移及默认配置；若发现新的能力缺口，明确当前拒绝与所需增量后再记录具体实现范围。
4. **行为/回退：**临时 CODEX_HOME 和工作目录、合成 Key、只读 CLI、关闭自动重试/WS/环境代理、无个人配置/账号读取；Provider 只发送固定文本或已声明函数事件，工具只由 CLI 客户端执行。先证明文本终态与错误/截断事实，再决定必要的函数工具子集；不把完整 Codex 产品支持作为本批结论。
5. **验证：**独立进程预算、失败 Kill/Wait、真实请求与实际上游次数、映射模型/字段/事件、CLI JSON 终态、用量和资金边界、无密钥日志、夹具释放；本地定向验证后精确头完整 CI，固定 Linux/Windows 客户端证据分别记录。
6. **协作/边界：**原 HTTP/WS SDK 和镜像证据按提交保留；不调用真实 Provider、不替换用户 Magpie/认证/配置，不扩大 strict/后台/WS/任意工具许可。官方 Advanced Configuration 核对 custom provider 与临时 CODEX_HOME 用法；具体功能以固定二进制实际请求为准。

本批本地证据：官方 Windows release `rust-v0.163.0` 的 `codex-x86_64-pc-windows-msvc.exe.zip` 经 SHA-256 `dabdb4d16aaf10fea40b74811c96efddcd9138f122163b69ed604fcb3eb704c3` 核对；实际 CLI 输出 `codex-cli 0.163.0`。六个场景分别验证文本、客户端函数结果往返、failed、incomplete、截断与 namespace 发送前拒绝。函数场景使用 CLI 已声明的 request_user_input，非交互模式返回 unavailable，再以对应 call_id 交回完整上下文；不执行命令、外部工具或询问真实用户。每个完整 SSE 事件内部按3字节切块并覆盖 UTF-8，模型映射到独立 native-* 目标。6条用量各绑定1条已发送 attempt，全部用量封口且不重试，3条已知3 Token与3条未知保留，namespace拒绝零发送且不新增用量/资金事实，disabled资金不变。首次脚本末尾误用了不存在的用量表名，改为现有 usage_attempt_receipts 后，最终完整本地harness通过；Python AST、CI结构和diff检查通过。生产源码与已通过精确头CI的PR66相同，复用原完整Go验证；新脚本及Linux固定客户端须由本批最终精确头完整CI单列证明。

支持边界：使用全新临时 CODEX_HOME/工作目录、API Key custom provider 与 Responses HTTP；关闭 apply_patch_freeform、multi_agent、goals、apps及WS/自动重试。默认 namespace 工具表当前会被明确拒绝，不能将本批标为完整默认 Codex、shell/apply_patch 执行、后台恢复或WS支持。夹具模型使用客户端fallback metadata，未证明官方模型目录和元数据。Windows CLI日志文件采用独立Job清理辅助进程，Linux用独立进程组和Kill/Wait；从未读取/改写用户Codex/Magpie配置。CI固定Linux release `codex-x86_64-unknown-linux-musl.tar.gz`，SHA-256 `beb339e8ecd9ba057a6bb2dc81ad8ca474812edfbf0fc5056ca818797648dde4`；release只作为独立客户端依赖，不复制上游实现。

官方配置依据：`https://developers.openai.com/codex/config-advanced`；固定发行来源：`https://github.com/openai/codex/releases/tag/rust-v0.163.0`。

<details>
<summary>FC-P4-NAMESPACE-001：原生 Responses function namespace（精确头 CI 通过，已合并）</summary>

1. **层级/目录及缺口：**固定 Codex 0.163.0 的默认工具表包含 namespace，当前原生 Responses 在发送前拒绝。L2 internal/protocol/responses 校验声明及 function_call/function_call_output 的可选 namespace；internal/compat/responsesws 同步有界声明/选择及输出输入，现有历史 owner 保留原 JSON 字段。现有 L3/后台/WS 消费者、生产组装、用量和权限仍走唯一链路；测试、OpenAPI、固定 CLI harness 和本方案同批更新。不新增工具执行器。
2. **公开语义与冻结边界：**原生 HTTP JSON/SSE、disabled-only 原生后台和原生 WS 的既有 function 子集追加单层 namespace 分组，成员仅 named function。兼容 responses/v1 的类型/签名/版本不变，其 ValidateHTTP 投影新增该声明能力；冻结 execution/identity/state、L0 和资金权威不变，无新地基版本/迁移。按当前用户同类 CR 直接实施授权记录后推进，不将导出或旧CI当批准。managed SIWC 仍限文本，strict Responses/后台/WS继续拒绝；跨协议 Chat 转换不继承 namespace 能力。
3. **精确行为：**namespace 声明保留 type/name/description/tools 和原始成员 JSON；名称为1..128 UTF-8字节且无首尾空白/控制字符，单请求最多64个namespace，每组1..128个function，组内成员名唯一、名称同样有界，拒绝重复组、空/null/非数组成员、嵌套namespace、custom和hosted成员。原顶层function行为保留。tool_choice 的 function 可带可选namespace；function_call及function_call_output的namespace绑定原call_id保留，不拼接/剥离名字，不根据当前声明重写旧历史。HTTP缺失/null namespace视为未指定，显式字符串须有效；WS沿既有规则拒绝显式null。不执行、调度、搜索或加载工具，不把声明视为客户端执行授权。
4. **兼容/历史/回退：**输出、SSE事件、存储/检索、完整上下文编译和HTTP↔WS按原JSON保留namespace；既有响应/项目ID改写、error脱敏、有效输出、终态和bounded事件继续成立。输出function namespace的已知字段类型错误应显式协议失败；不能丢namespace后以同名顶层函数代替。旧密文/迁移/账本不重写；回退二进制将拒绝新namespace请求/历史续链，已存内容不删除，只有匹配停写备份才按原方案做数据库回退。
5. **验证/验收：**纯编解码覆盖同名跨组、选择、invalid/重复/深度/容量/UTF-8、未知可保留字段、JSON/SSE分片及错误字段；真实HTTP JSON/SSE、原生后台提交/恢复、WS和存储完整上下文核对上游字段/次数、当前权限、取消和用量未知保护。固定CLI扩展原namespace拒绝场景为真实声明与namespaced function结果往返（只调用不存在agent的wait类查询，禁止spawn/message/外部命令），另保留hosted/custom发送前拒绝。集中本地验证、精确头完整CI后合并；真实Provider另验。
6. **协作/限制：**这只补function命名空间，custom/freeform、tool_search、programmatic/async执行、媒体/hosted及任意多路复用不继承。原CLI子集证据保留其精确提交，新的默认namespace声明必须重新证明；不能宣称完整默认Codex、shell/apply_patch执行或WS Codex已经验收。OpenAPI补声明、namespace字段和支持范围，操作数不变；性能不作提升声明。

官方核对（2026-10-20）：OpenAI Responses Create API 的 Namespace tool、FunctionCall.namespace、FunctionCallOutput.namespace，以及固定CLI rust-v0.163.0的协议类型；不复制上游实现。依据：`https://developers.openai.com/api/reference/resources/responses/methods/create`、`https://developers.openai.com/api/docs/guides/function-calling`。

2026-10-20 本批本地受控证据：HTTP JSON/SSE 的 namespace 函数结果、存储读取和完整上下文函数结果续链通过；真实双端 socket 的 HTTP→WS namespaced function→HTTP result→运行组件重建/新 WS 连接保持 namespace/name/call_id、未知可保留字段和原模型映射，四个新生成各仅一次实际发送。连接本地非存储 WS 两轮与原生后台提交/已知终态/相同 SQLite 及密钥重启后的查询分别通过，不重发已知后台生成。跨 Key 查询仍 404，不支持的 custom 成员在发送前拒绝；存储仍加密。纯校验覆盖64组/128成员/128 UTF-8字节精确边界、重复/空/null/嵌套/hosted/custom/错误类型、output-item added/done 与终态字段，HTTP未知扩展保留、WS闭合/显式null规则及旧顶层function兼容。Windows GOARCH=amd64 的全量 Go 回归通过（cmd/gateway66.524s、architecture22.187s、generation70.193s、Responses13.440s、store72.966s），build/vet及生产主进程local.py通过；最终受影响cmd/gateway、compat/responsesws、protocol/responses完整包复验通过（58.787s/1.075s/0.973s）。固定官方Codex0.163.0现有六场景全部通过：namespace场景用真实multi_agent_v1.wait_agent声明的targets参数查询不存在ID并回传错误结果，再完成文本输出，未spawn/message或执行外部命令；本批共8次实际HTTP发送/独立sealed usage，5次已知3 Token、3次未知未补零，disabled资金不变。该CLI回传的function_call_output未附namespace时保留原样，以call_id关联；显式namespace结果由HTTP/WS用例另验。OpenAPI0.13.6/117操作，未新增迁移/依赖/导出端口或L0路径，最终精确头Linux race/SDK/容器CI另验。此证据仅证明受控Provider夹具，不证明真实官方Provider、完整默认Codex工具、shell/apply_patch或Codex WS组合，也不开放收费。


</details>

<details>
<summary>FC-P4-CUSTOM-001：原生 Responses custom/freeform 工具往返（精确头完整 CI 通过并合并，合并后 CI 另记）</summary>

1. **层级/目录与缺口：**固定 Codex 0.163.0 的 apply_patch_freeform 默认工具声明为 custom；namespace 子批仍明确拒绝该类型。L2 internal/protocol/responses 处理自定义工具声明、输入/输出项目、原生 JSON/SSE 与背景流，internal/protocol/responses_estimation.go 同步 legacy 输出文本估算；internal/compat/responsesws 增量接同类 WS/持久完整上下文。L3 现有历史、后台、WS 消费者不另建执行链；cmd/gateway 测试、固定 CLI/Python SDK、OpenAPI 和本方案同批更新。
2. **公开依赖与冻结：**沿已批准 execution、responses、compat/responses/v1、httpoutput、responsehistory/v1、background 等既有入口；公开类型/签名/版本与数据库 schema21 不变。compat/responses/v1.ValidateHTTP 对 self-contained custom 项的新增语义按本具体记录实施，不把已有导出/CI当批准。不新建地基端口、L0 路径或工具执行器。
3. **精确声明和边界：**原生 HTTP JSON/SSE、disabled 原生后台、disabled WS 的工具声明追加 named custom，支持顶层或单层 namespace 混合 function/custom，组内名称跨两种类型唯一；维持每请求至多64组、每组1..128成员。新 custom 名称1..128 UTF-8字节且无首尾空白/控制字符。format 省略或null使用原生默认；已指定对象仅声明 text 或 grammar，grammar syntax 仅 lark/regex、definition 为1..65536 UTF-8字节文本。网关仅校验形状和限额并原样传递，不编译 grammar、检查模型采样语法或代执行。HTTP扩展保留；WS沿现有闭合/显式null规则拒绝未知结构。
4. **调用、历史与回退：**custom_tool_call 必须有 call_id/name/string input，可有 namespace；custom_tool_call_output 必须有 call_id 和声明范围文本/文本parts output，可有 name/namespace。custom tool_choice 为 type/name/可选namespace；已知字段类型错误在发送前拒绝。原生 output added/done、custom_tool_call_input.delta/done、终态与历史保留原字符串、call_id、name、namespace、未知可保留字段；非空 custom input delta 或已命名custom item算有效输出，心跳/空delta/done不冒充首输出。完整上下文经同一 owner/Key/当前权限/目标绑定重编译，不把input转JSON函数参数或静默丢项目。旧数据/密钥/迁移/财务不重写；回退旧二进制会拒绝新custom请求/续链，留存原密文；数据库回退遵守匹配停写备份。
5. **验证与退出：**声明/format/语法枚举/UTF-8限额/重复跨类型名称/null/hosted拒绝、JSON/SSE跨块UTF-8和交错函数/custom、有效输出、错误字段/截断不伪造完成；真实HTTP/WS/后台和持久历史查询/跨模式/组件重建，保留权限与取消未知不重放/用量未知不补零。固定 CLI 通过显式本地 model_catalog_json 能力档案启用原生 custom apply_patch 声明请求；该夹具不启用 tool_search/Code Mode，不代表 bundled 默认模型配置，夹具只提供故意无效且不含路径的补丁字符串，检验客户端解析错误作为 custom_tool_call_output 的完整上下文往返；不写文件/运行命令或创建/消息 agent。固定 SDK 用类型化事件验证对应范围，精确提交完整CI/race/交付另验。
6. **协作与许可：**managed SIWC仍限文本；strict Responses/后台/WS收费仍拒绝；跨协议Chat不继承custom/namespace，hosted/media/tool_search/programmatic及任意多路复用继续拒绝。不把该显式档案等同完整默认Codex工具执行/WS/官方Provider验收。本批只补已观察到的原生客户端缺口，性能不作提升声明，操作数不变。

官方核对（2026-10-20）：OpenAI Function calling 的 Custom tools/CFG（text/grammar、lark/regex）与 Responses streaming 的 response.custom_tool_call_input.delta/done；固定 rust-v0.163.0 协议 CustomToolCall/CustomToolCallOutput 与 apply_patch 客户端处理。依据：`https://developers.openai.com/api/docs/guides/function-calling`、`https://developers.openai.com/api/reference/resources/responses/streaming-events`。不复制上游实现。

固定客户端边界：0.163.0 bundled `gpt-5.5` 还声明 tool_search，新模型还可提交 additional_tools/Code Mode；当前均在发送前拒绝，不能据本批局部成功宣称默认客户端全量支持。custom 夹具采用文档与固定版本 config.schema.json 登记的 model_catalog_json，测试数据自行构造 synthetic slug/freeform/text 模态档案，不复制上游模型提示或更改用户模型/代理配置。实际 0.163.0 执行器原生解析故意无效且无路径的补丁，错误结果按 custom_tool_call_output 回传；工作目录断言没有新增日志之外的文件。配置依据：`https://developers.openai.com/codex/config-reference/`，固定源码 `codex-rs/protocol/src/openai_models.rs`。

2026-10-20 本批本地受控证据：custom 声明/grammar64KiB/custom名称128 UTF-8字节/重复跨类型成员/已知字段错误/hosted拒绝、HTTP JSON/SSE 跨字节Unicode及函数/custom交错、有效输出、终态不重复估算、后台事件持久回放均通过。真实 socket 的 WS custom→HTTP result→组件重建/新WS完整上下文保持 input/name/namespace/call_id，custom delta 后断开保留 uncertain，查询和父链续接冲突且实际上游只发送一次。固定 Python SDK openai3.8.0/websockets15.0.1 的10个生成通过，含类型化custom delta、终态和HTTP读取；7个已知3Token、3个未知未补零，资金不变。固定 Codex0.163.0 的7场景通过，custom显式档案使用真实freeform声明和原生补丁解析错误往返；共10次实际HTTP发送/独立sealed usage，7次已知3Token、3次未知，工作目录无副作用文件，资金不变。全量 Go 回归通过（cmd/gateway76.492s、architecture91.854s、generation69.787s、Responses15.751s、store62.939s、TLS/Upgrade夹具2.743s）；build/vet和实际main local.py均通过。最终补齐custom输出估算后，protocol全树/gateway/cmd完整包复验通过（gateway33.129s、cmd57.769s），最终存储WS字段校验包复验1.017s通过。OpenAPI0.13.7/117操作和575个引用、Python语法、格式/差异检查通过。最终精确头 `fd127abe377399716595b1dcc5084d73859a6f65` 的 PR Verify `39349320682` 与 push Verify `39349276419` 全部六项成功，涵盖 Linux race/完整兼容检查、实际非root镜像TLS/反代/重启/强杀恢复和固定WS SDK组合，以及Windows采样；PR #69 已合并为 `329f1130fccb5e346bac387650155ab8ced5a483`，合并后 Verify `39350655654` 独立跟踪。仍未证明真实Provider、默认Codex全部工具或收费。

</details>

<details>
<summary>FC-P4-TOOL-SEARCH-001：客户端 tool_search 与显式工具加载（源码与本地验证完成，精确头CI待验）</summary>

1. **层级/目录与消费者：**固定 Codex0.163.0 bundled模型已观察到提交 client tool_search；当前原生校验在发送前拒绝。L2 internal/protocol/responses 校验声明、tool_search_call/output 与 additional_tools、JSON/SSE/后台投影；internal/compat/responsesws 接入同类事件与完整上下文；现有 HTTP/WS/后台/历史 owner 原生消费，不新增搜索服务、执行器或工具注册中心。cmd/gateway、固定 CLI/Python SDK、OpenAPI及本方案同步验证。
2. **公开入口与冻结：**消费现有 compat/responses/v1、execution、responsehistory、background 与输出端口；既有 ValidateHTTP 的附加客户端语义按本具体记录实施。无公开类型/签名、L0、迁移、依赖和产品配置变化，不推断候选端口冻结。关闭/回退旧二进制会拒绝这些新声明及历史，保留原加密内容和未知事实。
3. **声明与限额：**只接受显式 execution=client 的顶层 tool_search，至多一个；可选description为文本，parameters省略/null或至多64KiB JSON对象，保留原schema不编译。原有function/custom可选defer_loading须boolean，namespace对象不承接该字段；组内仍限function/custom。tool_search_call 必须client、1..64字节非空可打印call_id、至多64KiB JSON对象arguments，status省略/null或in_progress/completed/incomplete；tool_search_output 必须client、相同格式call_id、status=completed和非null tools数组（0..128）。additional_tools仅role=developer和同样有界tools数组。已加载工具只接受已有named function/custom和单层namespace，沿既有64组/128成员/128UTF-8字节名称/grammar64KiB限额，已知description/parameters/strict/defer_loading字段正确；禁止递归tool_search/hosted/MCP/CodeMode，防止经历史绕过发送前能力限制。调用关联原样保留，不以客户端字段作为授权或执行权威。
4. **流、历史与估算：**不新增专用delta协议；通过既有output_item.added/done、完整终态传递search call/output，已校验call算有效工具输出，空对象/心跳不伪造文本TTFT。完整JSON参数/加载定义保留，不转成function字符串或丢字段；旧文本/function/custom/未知可保留扩展行为保留。HTTP/后台复用同一原生解析，WS已知字段沿闭合/null规则；持久历史和HTTP↔WS/重连复用同一owner/Key/当前模型/目标fingerprint/配额/加密，上游仍发送完整上下文，不叠加native父ID。PromptText/legacy输出估算包括search参数和加载定义，done/终态不重复计数；strict Responses仍全部拒绝。
5. **验证与退出：**合法/缺失/client-vs-server/null/错误类型/精确限额/重复/嵌套/非法返回工具、JSON/SSE分片及search与function/custom交错、有效输出、截断/failed/incomplete；真实HTTP JSON/SSE结果→工具定义→已加载调用/结果、后台持久回放、WS/跨模式/组件重建和取消unknown不重放，使用现有真实消费者。固定官方CLI额外用例启用其真实search声明和客户端本地发现，仅搜索固定不存在工具（或本地无副作用已声明工具）并往返结果，保留既有七场景；无真实服务/账号/代理配置、无文件/命令/agent副作用。固定SDK类型化HTTP/WS组合，最终精确头完整CI/race/镜像交付；失败集中修复，不以纯schema通过宣称客户端兼容。
6. **协作与许可：**本批补已观察到的客户端能力，支持additional_tools中已有工具类型是同一加载链；不开放服务端搜索、CodeMode/programmatic/async执行、媒体或任意多路复用，managed SIWC仍限文本，WS/后台仅disabled，收费许可不变。官方Provider/完整默认Codex/真实长时容量仍独立验收，性能不作提升声明；原历史证据仅覆盖各自精确提交。OpenAPI下个小版本/操作数不变。

官方核对（2026-10-20）：OpenAI Tool search 的client execution/call_id、object arguments、返回tools及additional_tools developer输入，Function calling/Responses已声明工具；固定rust-v0.163.0协议ToolSearchCall/ToolSearchOutput保留JSON arguments/tools与可选opaque metadata。依据：`https://developers.openai.com/api/docs/guides/tools-tool-search`。不复制上游实现，不把官方支持范围解释为本项目已通过真实验收。

2026-10-20 本批受控验证：client声明/重复拒绝/defer_loading类型、64字节call_id与64KiB对象精确边界、0..128加载定义、hosted/递归/错误角色及类型拒绝通过。HTTP JSON/SSE搜索→加载定义→namespace函数→结果完整三轮保留JSON参数、call_id和未知扩展，后台重开后的序号回放不重发；真实WS→HTTP→组件重建/新WS保留加载定义与同一加密历史，搜索调用后取消保留uncertain且查询/父链均拒绝重放。固定Codex0.163.0八场景通过，搜索用bundled gpt-5.5元数据和客户端原生本地发现，固定不存在工具返回tools数组并完成后续文本；共12次实际上游发送/独立sealed用量，9次已知3Token、3次未知不补零，资金不变且无工具副作用文件。固定PythonSDK openai3.8.0/websockets15.0.1 的12个WS生成通过，类型化ResponseToolSearchCall及HTTP读取保留对象arguments，tool_search_output与additional_tools进入完整上下文；9次已知3Token、3次未知，历史/attempt各12条。全量Go回归、build/vet通过；最终交错completed/failed/incomplete/截断、未知usage与protocol全树/WS/server完整包复验通过，实际main local.py通过。OpenAPI0.13.8/117操作与592个引用、Python语法和差异检查通过。实际镜像TLS/Upgrade/重启/SIGKILL/SDK组合已扩展本批搜索场景，尚待最终精确头Linux CI执行；本地未运行Docker，不宣称真实Provider、完整默认Codex、收费或性能提升。

</details>

<details>
<summary>FC-P4-CODEX-WS-001：原生非生成预热与固定客户端 WS 字段（精确头CI通过并合并，合并后独立CI另记）</summary>

2026-10-20。按本任务后续同类契约变更的既有授权记录后实施。固定官方 Codex0.163.0 的隔离 loopback 观察确认：supports_websockets=true 时先发送 generate=false，再以该响应ID发送正式轮次；create还含stream=true、include、reasoning、prompt_cache_key、client_metadata。仅打开客户端WS配置不能证明网关走WS，预热失败可触发客户端HTTP回退。

1. **层级与消费者：**L2 internal/compat/responsesws 在原解析/映射/事件/完整上下文中支持这些具体原生字段与不透明reasoning项；cmd/gateway原生产组装、既有gateway唯一native session/exchange/准入/usage/历史直接消费。新增固定CLI WS回归和真实socket夹具，保留原HTTP CLI、SDK和实际镜像交付。L0、发送器、连接池、reader与资金owner不变。
2. **公开依赖/冻结：**消费既有execution v2/v3、responsesws/v1、responsehistory/v1和usagehistory/v1；仅扩现有L2 Parse/ParseStored/NewDecoder语义，不新增类型、签名、配置、schema、依赖或冻结端口。generate=false是发送到原生上游并等待其真实终态的独立请求轮次，不由网关合成预热成功。每轮仍当前Key/模型重查、RPM/TPM估算与原两层容量准入、同目标/凭据、单串行exchange和原有deadline；不得绕过限流或创建第二种执行。原usage按轮记录实际发送/已知或未知usage，包含预热；不是独立收费，WS仍disabled-only。
3. **具体字段和限制：**stream省略或true；generate省略/true/false，false仅store=false或省略（拒绝预热持久化/伪历史）。include省略或至多一个reasoning.encrypted_content。reasoning闭合对象仅effort（none/minimal/low/medium/high/xhigh）及summary（auto/concise/detailed），值为字符串；text闭合对象仅verbosity低/中/高及format.type=text，不开放JSON schema/媒体。prompt_cache_key非空UTF-8文本至多512字节；client_metadata为至多16个文本值的对象，键1..128字节、值至多2048字节，合计JSON至多16KiB；只当Provider元数据，不用于权限/owner/路由。WS原已知字段null拒绝保留。消息可携带可打印1..256字节id、status=in_progress/completed/incomplete与phase=commentary/final_answer，原样作为Provider内容字段，不当作本地授权引用。初次prewarm的input沿原文本/工具能力；不会放开background/conversation/媒体/hosted/多路复用。
4. **推理与寿命：**只携带自包含、不透明encrypted_content及可选文本summary/content，不解密或执行。输入reasoning需非空opaque文本至多128KiB，最多64个summary_text与reasoning_text/text组成项，各文本至多64KiB，完整消息/重建上下文仍256KiB/1024项；输入已知字段闭合，已保存输出的未来字段沿原raw历史保留。输出reasoning added/done与summary/text part/delta/done保持原生JSON，允许合法进行中encrypted_content=null，终态不强行编造密文。摘要/思考事件不算首有效回答，沿原完整事件计idle。历史只有可自包含的reasoning才可作为下一轮输入；缺密文不能凭Provider item ID重建。WS TPM估算包含opaque/summary原始JSON，不声称是真实计量或strict成本证明。预热父ID仍只接受当前连接最新ID；既有TPM>0且父usage未知时拒绝延续规则保留，不能补0绕过。persistent历史/HTTP↔WS采用同一owner/Key、target fingerprint、加密和容量，不另建缓存。
5. **验证/退出：**精确字段类型/值/字节/数量/null/未知字段、false预热+store拒绝、reasoning自包含/不完整/未知输出与完整上下文；真实socket预热→文本/函数/custom/search多轮，前后各自授权/准入/一次create、未知usage不补零、预热取消/Key撤销/模型改变、非成功终态及现有关闭/恢复；固定CLI实际WS握手与上游create计数独立于客户端turn.completed，明确零HTTP fallback，当前默认bundled模型与必要明确档案分列。客户端在失败后主动HTTP回退不作为网关重放许可或WS成功，取消/截断另用既有协议客户端验证。适用全量Go/build/vet/main smoke、固定SDK/CLI、OpenAPI和最终精确头Linux race/交付CI。
6. **兼容/协作：**既有store=false/true文本/function/custom/search路径保留；关闭WS仍沿已有拒绝，回退旧二进制会拒绝本批字段/历史但不删除数据/未知事实，受schema21停写备份规则约束。OpenAPI下一小版本/117操作同步。受控原生上游与真实固定客户端是本批证据；不冒充官方服务、任意默认工具/模型、客户端自动恢复、性能提升、完整账号池或收费，Provider授权与P8成本门槛不变。

主参考采用 codex-proxy-rs 固定 `60570883007febebe732597a9881397098d724f5` 的 `backend/crates/providers/openai/src/transport/protocol/websocket.rs`：`websocket_response_create_payload` / `ResponseCreateFrame` 保留请求对象与原生内容，映射和旁路观察分别处理；`responses.rs::transport_requirement` 将 `generate=false + store=false` 绑定原生 WS，`tests/transport/websocket_pool.rs` 的独立 downstream warmup/connection-local parent 场景用于补 TXAPI 回归。按 TXAPI 现有 RawMessage、Parse/MapModel/Decoder 和唯一 NativeSession 改写，保留本项目声明的字段、资源、权限和计费边界；未复制 Rust 代码、连接池或新增发送流程。许可 Apache-2.0，来源沿本文登记。

补充对照 sub2api 固定 `f2e5b55f3e8069df1c02a9a27af537b5fdefa64e` 的 `backend/internal/service/openai_ws_forwarder_support.go::performOpenAIWSGeneratePrewarm`、`openai_ws_forwarder_ingress.go::parseClientPayload` 与 `openai_ws_v2_passthrough_adapter.go::openAIWSPolicyEnforcingFrameConn`：预热读取真实终态、后续帧逐轮策略检查、只对必要字段做映射并保留其余原生内容。该项目的自动预热/HTTP 回退、恢复重试及账号池不移植；这里只参考行为与边界，不复制其 LGPLv3 实现。工具结果项的有界 `id` 与消息 ID 一同保留，均不用于权限或父链查询。

官方依据：固定rust-v0.163.0 core client的provider supports_websockets、generate=false prewarm及增量父ID逻辑，OpenAI WebSocket mode与Reasoning指南；仅对照线协议/配置，不复制实现。固定真实CLI仍独立验证线协议结果，参考程序本身未运行。

2026-10-20 本批本地受控验证：固定 Codex0.163.0 实际 WS 的文本/function/custom/client-search 四场景全部完成，独立上游观测共11个 response.create（4个真实预热及7个生成），每场景仅1条上游连接、零HTTP fallback；每轮一条sealed用量，预热已知0/生成已知3分别保留，store=false不生成永久历史或资金记录。已知零/未知预热父usage、跨连接父ID、Key撤销与模型权限改变、预热断开后的unknown和准入释放、reasoning经WS→HTTP→新WS的完整加密历史均通过定向真实socket回归。原固定CLI HTTP八场景、Python openai3.8.0/websockets15.0.1的12个WS生成、build/vet/全量Go与实际main冒烟通过；OpenAPI0.13.9/117操作/595引用及Python语法检查通过。现有Linux race、实际镜像TLS/Upgrade/重启/强杀与SDK交付保留在完整CI，当前尚未记为本批通过；实际镜像中的固定Codex WS组合、真实Provider、完整默认工具/模型、长期容量和收费仍未验收。本批同时将“优先参考具体源码并原生化”提升为AGENTS入口和本方案的后续开发约束。

</details>

#### P4 固定 Codex WS 实际镜像交付子批（精确头完整 CI 通过并合并）

1. **层级/目录：**仅测试侧 `tests/upstream/nativews`、`tests/smoke/native_ws_delivery.py`、固定客户端脚本和 Verify；使用交付 Dockerfile 构建的真实 gateway main，不修改产品热路径。
2. **公开依赖：**消费现有 API Key/模型授权、原生 WS、连接本地父 ID、disabled 用量及管理查询；无公开契约、配置默认值、schema、依赖或收费许可变化。`--codex` 是隔离交付测试的可选入口，不是产品配置。
3. **地基影响/回退：**无 L0 或生产 owner 改动。复用现有 internal Docker 网络、临时 CA、TLS 上游、Upgrade 反代及非 root 镜像；测试结束删除带本批标签的容器、卷和网络，宿主 DNS/信任不变。客户端配置、测试 Key 与日志均为一次性材料。
4. **行为：**固定 Codex0.163.0 的文本/function/custom/client-search 四场景经反代进入实际镜像；每场景独立 home/workspace，实际 generate=false 预热后沿同一连接继续，客户端工具只使用已有无副作用失败/元数据用例。观察端独立核对11次 create、4次预热、每场景一连接、零HTTP回退、父ID/模型映射及reasoning保留；每次实际发送都有独立用量，已知0与3分开，store=false不新增永久历史，资金不变。
5. **验证：**本地验证TLS夹具和固定客户端；实际main的镜像四场景由完整Linux CI执行，并保留现有SDK、历史/重启/SIGKILL及完整检查。客户端归零退出不替代上游观察，Docker未运行时不记镜像通过；超时、失败也收尾并保留脱敏诊断。
6. **来源/协作：**主参考仍为codex-proxy-rs `60570883007febebe732597a9881397098d724f5` 的 `backend/crates/providers/openai/src/transport/protocol/websocket.rs::websocket_response_create_payload` 及 `backend/crates/providers/openai/tests/transport/websocket_pool.rs` 中独立downstream预热/connection-local parent场景，采用真实预热和逐连接关联的回归机制，独立Go/Python夹具复用TXAPI现有交付链；不复制Rust代码或池化/自动回退。该组合不证明真实Provider、完整默认Codex工具、长期负载或收费完成。

本地受控验证：TLS夹具四模型均完成真实预热、工具回传和文本终态，另一物理连接携旧父ID被拒并留下失败观察；既有TLS/Upgrade/SDK夹具回归与该包vet通过。固定Codex0.163.0复用同一客户端函数的四个WS场景全部通过，独立Go组装观察仍要求11次实际发送、4个预热和零HTTP回退。Python语法通过。实际镜像新增观察同时比较前端反代和上游HTTP POST计数，拒绝在网关提前失败而被上游观察遗漏的回退；最终快照分别核对29条用量和18条历史，11次store=false客户端请求不新增历史。PR #73 精确头 `1969574eb34bb81652e7f3ec15460ae3ff4d190d` 的 push Verify `39358802030` / PR Verify `39358812647` 全部六项成功，交付日志明确确认四场景、11次发送/回执、4次预热/连接、零HTTP回退及镜像TLS/反代/重启/强杀保护通过；已合并为 `e995433ba81497819d4d7ce5504aef61833853a0`，合并后CI单独记录。不声称真实Provider、完整默认Codex或收费完成。

### P5：账号信号与调度评估

- **层级/目录：**L1 scheduler/gateway；L3 routes 查询。
- **依赖入口：**现有 routes/shadow 范围，及 P3 确认后的账号资格/配额端口。
- **地基影响：**内部观测优先；新增公开指标/语义按 CR 流程处理。
- **行为/契约：**先比较负载、等待、错误、TTFT、窗口和亲和信号，显式区分未知/过期与零；保持有界采样。真实评分是后续独立子批，须明确启用、回退和效果标准。
- **验收：**不合格目标不入选、最高优先级不变、样本不足不当零；候选策略验证拥塞、饥饿、分布、亲和逃离和关闭恢复。shadow 对比不能当线上效果证明。
- **协作影响：**复用已交付 R00，不重复建设观测框架，不在 shadow 批修改真实重试或 strict。

P5 当前门槛评估（2026-10-20）：唯一 active SIWC grant 的候选池恒为1，既有 shadow 比较要求2..8个渠道；P3仅提供带版本的拒绝与人工恢复，reset 始终 unknown，没有可信窗口、剩余额度、等待或账号间相对负载。因此账号评分/真实调度继续后置，不能为做评分扩大账号池或猜窗口。现有 R00 的8槽、每槽64结果、10分钟保留和最高优先级池内静态路线比较直接复用；不足4个有效结果保持 neutral。P3本地拒绝在 shadow Begin 前退出，不进入HTTP成功/错误样本；真实发出的完整HTTP错误可作为既有渠道结果，但不能推导账号窗口或账号质量。管理选择日志的 managed eligible 数仅表示配置快照候选，当前权威资格由发送前 AcquireAccess 决定。此评估不表示P5全量验收。

### P6：续链与原生长连接语义

- **层级/目录：**L1 generation/gateway/lifecycle 负责状态与租借，L2 transport/Provider 负责收发，L3 responses/responsesws 消费。
- **依赖入口：**现有 responses、responsesws、execution、lifecycle 的已批准子集；持久父链/跨模式不推断已冻结。
- **地基影响：**L1/L2 CR 先定义持久、连接本地、外部 opaque handle，以及 Key/账号/revision pin、失效与保留规则。
- **行为/契约：**先交付父链和重连边界；原生后台继续用持久上游 ID 查询，跨连接/模式保持权限与上下文来源。仅在真实复用需求成立后增加独占 lease、建连合并和有界连接池；多路复用另行定义，不与池化捆绑。
- **验收：**跨 Key/账号拒绝、父 ID 失效、双下游同会话不误复用、握手/凭据变化、唯一 reader/Ping、有界慢读、建连风暴、取消/关闭 drain；覆盖 Origin、帧/消息限制、握手/读写/空闲/总 deadline，拒绝 URL 明文 Key，WS body 的 background 不冒充 HTTP 后台；未知生成不重发，连接丢失不假续接，持久恢复不重复追加历史。
- **协作影响：**保持现有 disabled 限制；HTTP/WS/后台的上下文恢复分别验收，资源代次所有者不分叉，收费在 P7/P8 按操作扩展。

<details>
<summary>FC-P6-HISTORY-001：共用加密历史 owner 与现有 HTTP 消费迁移（精确头 CI 通过，已合并）</summary>

日期：2026-10-20。按本任务已获授权的同类契约变更记录后实施。本批先迁移已有运行消费者，持久 WS/跨模式接线另批验收。

1. **层级/目录：**L1 `generation` 持有历史内容解封、封装、保留期/容量配置和既有持久端口调用；L3 `responses` 保留 HTTP 命令、上下文重建和 prepare/finish 用例，通过共用公开端口访问内容；`cmd/gateway` 构造注入。L0、网关发送、WS reader/轮次和后台均不变。
2. **公开依赖：**新增候选 `responsehistory/v1`：固定 `Content`（沿用现有加密 JSON 字段）、`Repository` 的 `Read(ctx,binding,id)`、`Seal(row,content)`、`Fresh(id,kind,model,binding)`、`Apply(ctx,binding,changes,guards...)`；复用已冻结 state/identity/execution v1 值。Seal 仅编码加密，不写盘；Apply 仍由唯一 HistoryStore 执行当前权限、CAS、guards 和容量事务；端口仅供可信内部消费者，不是外部任意写入接口。登记不宣称冻结。
3. **地基/兼容与回退：**不改冻结 state/v1、持久历史行、schema、AES-GCM AAD `TXAPI/response-history/v1/` 或 JSON 标签。已有密文继续读写，未知执行保留原有封锁。移除 L3 对加密实现的持有；原 `responses.NewHistory` 改接候选 Repository，仅影响组装和现有历史/后台/服务器测试的四个调用点。无新开关/端点/用户可见字段，OpenAPI 无需升版；可回退旧二进制且仍须遵守已存在 schema20/21 边界。
4. **行为：**迁移既有 read/seal/fresh/apply，不改变完整上下文规则、目标 fingerprint、owner/创建 Key/当前模型权限、请求幂等、读限流、TTL、配额、删除和 unknown 不重放。原 JSON 字段及 omitempty 保留，明文临时缓冲继续 clear；不新增缓存、副本、历史扫描、worker 或密钥文件。
5. **验证：**独立旧格式密文夹具→新 owner 读取→新写回→旧格式解封的双向兼容；AAD 篡改、容量及未激活密钥配置拒绝；现有 HTTP JSON/SSE/工具/续链/会话、跨 owner/Key、CAS/并发、删除、未知执行和重启/轮换回归；架构注册与公开签名快照、适用全量 Go/生产 smoke 与精确头 CI。
6. **影响/退出：**现有 HTTP 历史必须真正消费新端口，不交空抽象；WS 持久父链仍未接入、不宣称跨连接/跨模式完成。后续消费者复用同一 owner/表/密钥与容量域，不将 HTTP L3 导入 L1，也不创建第二个历史权威。保持 disabled/strict、真实账号和费用授权边界。

2026-10-20 本地受控验证：Windows GOARCH=amd64 build、vet、全量 Go 回归全部通过（架构47.158s、generation79.843s、Responses18.436s、store80.604s）；生产主进程 local.py 通过。旧格式 literal JSON/AAD 密文由新 owner 读取，新写回通过旧格式解封，并覆盖内容/目标 pin 保留、密钥轮换、Key 身份篡改与超长内容拒绝；现有 HTTP JSON/SSE、会话 CAS/未知恢复与服务器双端回归仍通过。整合后台用量主干后，架构/generation/Responses/server 完整包级复验全部通过。公开快照仅新增候选 responsehistory/v1；PR #62 最终精确头六项 CI 全部通过并合并，合并后 CI 独立记录于基线。本批未接入 WS 持久续链，无真实 Provider 调用。

</details>

<details>
<summary>FC-P6-WS-HISTORY-001：显式持久 WS 历史与 HTTP/WS 完整上下文续链（精确头CI已通过并合并）</summary>

日期：2026-10-20。按本任务同类契约变更的既有授权实施。机制核对 [OpenAI WebSocket mode - reconnect and recover](https://developers.openai.com/api/docs/guides/websocket-mode)：连接缓存丢失时可重新发送完整上下文。本批只对现有文本/function 单串行轮次接入本地持久恢复，不继承远端额外并发或池化能力。

1. **层级/目录：**L1 generation 复用同一个 responsehistory/v1 Repository，持有 WS 历史轮次 prepare/终态持久化/未知收尾；gateway 仍唯一持有当前身份、RPM/TPM、容量、目标与凭据资格、一次 WS exchange 和 usage。L2 compat/responsesws 持有自包含文本/function 输入编排、模型/响应 ID/本地父 ID 改写与已校验事件投影；组装注入。L0、reader、传输/代次与历史表权威不分叉。
2. **端口与配置：**候选 responsehistory/v1 追加 StoredCreate、CompiledCreate、StoredEvent、PreparedRound、StoredCodec、StoredRound 和 StoredRounds 的窄类型/端口。StoredRounds.Prepare(ctx,binding,create) 读授权父历史并原子保留新行；StoredRound.Prepared() 返回本轮本地 ID、完整 wire create/PromptText/可选目标约束，Observe(ctx,alreadyValidatedJSON,target) 在原始事件完成事实已通过 decoder 后改写输出并持久化可观察终态，Finish(ctx,dispatched) 仅收尾未得终态的轮次。Codec 的 Compile/Project 只构造有界 JSON，不 I/O/发送/决定资格。gateway.NativeWSOptions 追加可选 StoredHistory 和 EncodeStored（仅持久完整上下文映射），NativeWSCreate 追加 Store bool；既有 responsesws/v1 与 execution v1/v2/v3 不改。本轮复用前批候选 v1，并更新签名快照；不宣称冻结。startup responses_ws.persistent_history 默认 false，为 true 要求 responses_ws.enabled、responses.enabled、disabled 及可用的同一个历史 Repository；缺依赖启动拒绝，不能退化丢历史。
3. **接口/兼容/回退：**仅新开关打开且本次 response.create 显式 store=true 才进入持久路径。返回随机 resp_ 本地 ID，沿现有 HTTP GET/DELETE/input_items 与父引用访问，绑定 owner、创建 Key、当前模型、TTL/配额和目标 fingerprint。下一轮持久续链也须 store=true；store=false/省略仍只接受当前连接内最新 native 父 ID，不读取持久历史或静默切换模式。无 conversation/后台、推理/媒体、WS 池或多路复用扩展。追加 OpenAPI 支持声明，schema/AAD/旧 JSON 格式不变；关闭开关保留 HTTP 读取/删除和既有 TTL 清理。回退遵守现有 schema21 边界，不删除未知历史。
4. **行为与不变量：**HTTP→WS、WS→HTTP、WS 重连/进程重启，只有已授权、Ready、无 Busy/Active、模型匹配且可完整重建的本地父历史才显式创建新生成；新轮次不是重放旧生成。发送 full context，去掉 native previous_response_id，强制上游 store=false；不同时叠加 native 缓存。父输入/输出保持工具 call_id/名称/参数及文本，拒绝引用式/非自包含/重复 item ID，1024项/256KiB 上限；重建后重新 TPM/容量准入。发送前约束当前 official static 目标/凭据 fingerprint，不能为不匹配父链换账号；仍拒绝托管账号 WS。使用原唯一 exchange，已校验 terminal 文档与目标必须先 CAS 持久成功才交付该 terminal；known failed/incomplete/cancelled 仍保留其真实状态/完整可重建内容，绝不改成 completed。已知终态随后下游写失败仍可读取真实结果；无终态且可能发送则保留加密 uncertain marker，不可作为父历史。已证明未发送只解除活跃 marker，不伪造结果。保存失败不得发布成功终态；取消后收尾至多2秒、独立于观察连接，权限仍在原事务重查。
5. **验证：**默认关闭/配置依赖和非法 boolean；真实双端 socket 的持久文本/工具多轮、同连接与新连接、HTTP↔WS、SQLite 关闭重开和密钥轮换；每新轮次只有一次新 upstream create、完整输入/映射/无 native 父/上游 store=false，历史/usage 不重复；跨 owner/Key、权限撤销、父删除/TTL、变更目标/Key、并发两条分支不共享连接、容量满拒绝。覆盖 failed/incomplete、截断/取消/deadline/下游写失败、历史保存失败/原子CAS、unknown及伪造 busy 清空仍禁续链；现有 stateless WS/HTTP/strict/background 与架构/公开快照/OpenAPI 集中回归，最终精确头 Linux race/交付CI后合并。
6. **协作与退出：**同一历史 owner/加密键/表/容量域，同时保留独立 usage 每轮记录和协议/机械终态；不新建 HTTP fallback、sender、后台恢复器、缓存或流水账。不扩大收费或真实账号授权；仍只受控夹具验证，真实 Provider/客户端部署和长时容量另行记录。本批必须交付生产组装的真实消费者与跨模式/重启证据，不能只新增接口。

2026-10-20 本地受控证据：真实双端 socket 的 HTTP首轮→WS函数调用→HTTP函数结果→新WS连接续链通过，四个新生成各仅一次上游发送，公开本地ID/模型和完整工具关联保持，终态交付后立即HTTP读取可见；查询/删除不触发生成。生产组装组件关闭重建后，同连接父链与独立两条新连接分支通过，各行/连接/usage独立；SQLite owner关闭重开、旧密文/密钥轮换与四种已知终态另行通过。跨owner/Key、父删除/过期、凭据轮换、Key撤销/模型权限、容量满在发送前拒绝；取消保持uncertain且不续链，注入终态保存失败不发布终态、事务无部分成功，清空明文busy仍由加密active阻止未知重放，已知终态后下游写失败仍可读取真实结果。显式false/省略store维持既有连接内行为、无新历史行；开关非法boolean、缺HTTP owner和nil owner启动拒绝。Windows GOARCH=amd64 build/vet、全量Go回归均通过（cmd/gateway64.156s、architecture54.744s、generation81.686s、Responses14.891s、store79.775s），生产入口local.py通过。公开快照只在既有候选responsehistory/v1追加窄端口，OpenAPI0.13.5/117操作、schema21不变；PR #63 最终精确头完整 CI 已通过并合并。WS 进程强杀和实际镜像受控部署分别另批补充；真实Provider/Codex组合及长时容量仍分别开放；未执行真实Provider/OAuth或开放收费。

</details>

P6 本批验证范围（2026-10-20）：在 `cmd/gateway` 增加独立子进程的持久 WS 强杀回归。六项边界：①只写测试与本方案，复用生产组装函数和真实 socket/SQLite；②消费既有 responsehistory/v1、usagehistory/v1、execution v2 与当前身份/历史查询端口，不新增或冻结端口；③生产实现、L0、迁移、配置和权限不变，故障门闩仅在测试依赖适配器；④分别在发送前、上游已收但响应 ID 未读、历史终态已提交但未交付、交付后用量未收尾强杀并 Wait，再以相同数据/密钥重新组装进程；⑤核对完整上下文新生成、不可重放 unknown、历史/usage 单行与重复恢复幂等、已知 Token 保留/未知不补零、资金事实不变，适用完整 CI 验收；⑥只证明受控生产组件组装进程的恢复，不冒充真实 Provider、未替换的实际 main 二进制、后台/strict 财务强杀矩阵或真实部署验收。复用现有测试资源工厂，不给生产增加端点重定向或故障开关。

2026-10-20 本批本地证据：四种边界均执行真实 Process.Kill + Wait，同一 SQLite/密钥启动恢复进程，再启动第三个进程复验重复恢复。发送前实际 create 为零，其余边界为一次；unknown 查询返回冲突且父链续接不触发发送，已知终态可读取，并只为明确的新轮次发送一次完整上下文。交付后用量未收尾场景由客户端先读取 completed 再强杀，重启后仍保留已观察的 completed/3 Token；其余未知 Token 不补零。旧密文/版本、新历史/usage 行数、资金快照及上游实际请求数保持预期。Windows GOARCH=amd64 的完整 cmd/gateway 回归通过（51.549s），该包 go vet 通过；生产源码与 PR #63 相同，既有全量 build/vet/Go/生产入口证据按原提交复用，PR #64 的最终精确头 d13e1ee 已通过六项 CI 并合并；合并后验收见基线。

P6 本批交付验证范围（2026-10-20）：①仅 `tests/upstream/nativews`、`tests/smoke/native_ws_delivery.py`、新夹具包的架构登记、既有 CI 和本方案，运行实际 Dockerfile 镜像的 main 与独立反代/受控 TLS 服务；②消费现有 HTTP/WS/本人用量公开接口，不新增生产端口或冻结契约；③生产拨号、TLS 校验、权限、迁移、L0 与配置默认值不改，Docker internal 网络以临时证书和网络内同名 alias 承接固定官方地址，禁止访问真实服务；④复用现有持久开关、同一数据卷和密钥，测试普通/工具轮次、HTTP↔WS、新连接与容器重启、在途强杀、关闭开关后历史可读与 WS 拒绝；⑤核对实际上游请求次数/完整上下文、Upgrade 反代、已知/未知终态、重复恢复无重放/重复历史/用量、不泄漏凭据和全部 fixture 清理，集中最终精确头 CI 验证；⑥这是实际镜像的受控部署证据，真实 Provider/OAuth/SDK WS/Codex、长期容量和收费仍另行验收。本机无 Docker，不能以本地夹具预检冒充容器验收；完整交付脚本在 Linux Docker CI 中运行。 PR #65 最终精确头 78d5b07 的完整 CI 与实际镜像交付均已通过并合并，HTTP/WS工具续链、在途SIGKILL unknown与持久用量的组合已受控验证；本批SDK镜像增量仍另验。CI 初轮暴露夹具错误假设：HTTP 持久请求保留显式 store=true，持久 WS 请求才强制上游 store=false；两条路径均重建完整数组上下文。已按现有生产契约修正夹具及逐请求断言，不修改生产存储语义，实际镜像验收须重新完成。

2026-10-20 本批本地预检：临时 CA 的官方域名 TLS 握手、夹具完整事件、真实 Upgrade 反代和客户端读取通过；全量 architecture 包、OpenAPI 定向检查、全仓 build、新夹具 go vet、Python 语法与 CLI 预检通过。这里只记录夹具和静态证据，尚未运行 Docker；实际镜像、数据卷重启、SIGKILL 与 TLS/反代全链路须由最终精确头交付 CI 证明。

### P7：后台/WS 用量与收费

- **层级/目录：**L1 `internal/billing`、`gateway`、`generation`、`store` 及对应 `foundation/contracts`；L2 `internal/protocol`/`compat` 提取 usage；L3 既有查询/管理消费者。
- **依赖入口：**定点金额、部分完整 usage、不可变报价、执行/ledger 权威；候选 quotation/usagehistory 只按批准范围消费。
- **地基影响：**按具体后台或 WS 操作提出终态、归因、持久幂等、预留/续租与资金生命周期 CR；迁移统一追加。
- **行为/契约：**先补准确用量与未知事实，再开放满足证明的收费子集；每次生成独立准入/RPM/账单，轮询/重连/重放有读取限流但不重复收费。queued 不占网络 attempt 槽，后台在途单独有界；取消未知不退款，迟到 usage 仍能对账。
- **验收：**重复/迟到终态、取消完成竞争、漏 usage、断线、强杀重启、报价中途变化、缓存维度和账号归因；未完成的财务状态可持久查询/恢复，未知占用不丢失、不重复结算。
- **协作影响：**不引入透支、补零或内存费用队列作为权威；HTTP 的成本许可不自动覆盖后台/WS，未通过 P8 对应操作证明时保持拒绝收费扩展。

<details>
<summary>FC-P7-WS-USAGE-001：disabled 原生 WS 每轮持久用量观察（精确头CI通过，已合并）</summary>

本批按当前任务的同类 CR 常设授权记录后直接实施。现有 WS 每轮已有发送、终态和基础用量事实，但没有持久查询消费者；先补观察，不开放收费。

1. **层级/目录：**L1 gateway 的 nativeSession 负责每轮 Begin/Prepare/MayDispatch/Observe/Finish、唯一准入与关闭 join；store/model 复用既有 usage 权威和查询。L2 compat/responsesws 复用 Responses v3 解析，组装根注入详细解码器；现有 SessionBearer 查询和消费者同步新增 transport。L0 与执行循环不另建。
2. **公开入口：**usagehistory/v1.Start 和 Summary 追加 Transport（http/websocket；Start 空值兼容 http），HostFacts 追加可选 ProtocolStatus（空/queued/in_progress/completed/failed/incomplete/cancelled，宿主终态与协议快照独立），responsesws/v1.Result 追加 UsageRecordID/RecordingState 供宿主关联；NativeWSOptions 追加可选 UsageDecoder 工厂，兼容原 Decoder。DecodeUsage 返回 v3.Envelope，原 Decode 继续返回其 v2 投影。只使用既有 v3 UsageObservation/校验，不修改冻结执行端口，不把候选端口推断为全量冻结。
3. **迁移/回退：**追加 schema20 transport 和嵌入 host_protocol_status 列，旧记录明确 http/空协议快照；空值/显式 http 的 Start 指纹仍采用旧 JSON 形状，保留重放幂等。仅接受 http 或原生 Responses stream、MaxAttempts=1 的 websocket。旧 schema19 二进制拒绝 v20；回退恢复停写匹配备份，禁止改迁移或资金事实。OpenAPI 小版本说明新增查询字段；无新开关、依赖或查询端点。
4. **行为/边界：**仅 usage_history 已启用且 disabled 时记录，默认关闭。每个已授权/准入轮次独立随机 usage ID，绑定可信当前 owner/Key 和连接请求 ID，不存正文/原生父 ID/密钥。Prepare 在 dial 前，MayDispatch 在唯一 round Write 前；持久失败不发送，不额外重试。成功/failed/incomplete/取消/截断保留宿主 phase/协议终态/写入下界，WS 的 HTTP 状态为0；机械 failed 映射既有 upstream_error，deadline 与取消分开；incomplete 可传输成功但协议状态仍明确 incomplete，不推断 completed。未知不补零，部分/缓存/推理细分沿 v3 有界校验。发送后 Observe/Finish 独立2秒、保留身份并脱离取消，失败标 unavailable/interrupted，不重写已交付的 Provider 结果；重启沿既有 InterruptUsageHistory 留未知。查询按已有权限脱敏，行数/TTL/单尝试/关闭 join 复用，不从记录推导资金或恢复授权。
5. **验证：**定向覆盖同连接多轮/续链、不同连接与 owner/Key、精确/零/部分/未知/细分、失败/incomplete/写失败/取消/deadline；Begin/Prepare/MayDispatch 失败及 panic 均不发送，Observe/Finish 失败不伪造成功用量。验证实时 SQLite 查询、容量、停用后只收尾、关闭 join、schema19→20 保留指纹/历史/账本并两次重开；架构/公开快照/OpenAPI与现有 HTTP/strict/原生WS回归同批集中验收，完整精确头CI后合并。
6. **协作与范围：**HTTP 默认行为和已有 Header不变，不向原生 WS 帧注入私有字段。Result 的usage ID只对宿主可见，终端用户通过现有列表读取；transport区分请求来源。原生后台、durable claimed、WS 收费/成本许可、持久续链、真实 Provider 与商业页面保持原边界；本子批不关闭 P7/P8 或S5-03。

2026-10-20 本地受控证据：双端真实 socket 同连接文本/函数两轮后，通过登录 SessionBearer 列表/详情读到各轮独立记录；调用 Key 不可作 Session、跨 owner 返回404、私有目标与密钥不泄漏。服务定向覆盖完整/零/部分/未知和细分、failed/incomplete独立快照、取消/deadline/写失败/panic、各持久步骤失败、Key停用后收尾、关闭join和满额禁发。schema19→20 两次重开保留旧Start/Observe/Finish指纹与重放，历史HTTP来源回填；既有升级回归继续核对账本/加密内容与已发布迁移时间。Windows GOARCH=amd64 build/vet通过；全量Go回归初轮仅旧Responses历史测试的最新版本写死19，补20后完整store包复验通过，其余包初轮已通过。生产主进程local.py与12项控制台行为通过；Linux race/容器交付按最终精确头CI验收。所有上游/账号仍为夹具，未执行真实Provider/OAuth，不声明收费或生产容量。 Linux 首轮同提交的 PR race 暴露 deadline 归因用例把30ms轮次时限用于SQLite准备后必发送的假设，push race 同头通过。已用发送/读入门闩后触发的受控 deadline 原因测试持久归因，保留独立真实超时计时器回归；本地定向复验通过，生产代码不变，新精确头完整CI重新验收。

</details>

<details>
<summary>FC-P7-BG-USAGE-001：disabled 原生后台任务的持久用量元数据（精确头 CI 已通过并合并）</summary>

后台的单次生成跨越提交、查询、恢复事件和取消 RPC；本切片在已有任务行及同一 fenced CAS 事务保留观察，不为轮询创建 usage invocation 或第二个资金/恢复权威。

1. **层级/目录：**L1 generation 在现有 backgroundProgress 合并观察，store/model 在原任务行保存无正文的协议状态及六个可空 Token 计数；L2 protocol/responses 的后台 JSON/SSE 解码复用现有 v3 usage 提取与校验，gateway 原唯一 backgroundOutput 传递该观察；L3 taskconsole/API 仅消费既有 taskcontrol 查询。
2. **公开入口：**新增登记的候选 background/v2.Observation（嵌入 v1 观察和可选 v3 详细用量）与 Observer.DrainUsage；冻结 background/v1 不变。已有 v1 codec/progress 继续兼容，私有 L1 Progress 可选详细观察能力。候选 taskcontrol/v1.Task 追加 protocol_status 和可空 usage（复用 usagehistory/v1 的精确十进制字符串视图）；无需新增 HTTP 路由、配置或依赖。现有 HTTP/WS 用量查询范围不扩大。
3. **迁移/回退：**只追加 schema21：后台行原生状态、六个可空整数、完整度/来源/档案；旧行状态为空、usage 为 null，不解密历史内容、不推断补零，不改加密 AAD、v1 snapshot 格式或已发布迁移。新 snapshot 可选详细用量与旧 v1 基础事实并存；重启从已持久行恢复，无需扫描正文。旧 schema20 二进制拒绝 v21，回退恢复匹配停写备份。同步 OpenAPI、公开快照、交付及迁移检查。
4. **行为/边界：**仅既有默认关闭、disabled-only 的原生后台。每任务始终一行，RPC/事件重放不累加用量；只在已验证的终态观察中合并用量，缺失为未知、零为已知零、部分不推导总量。重复同终态可补充此前未知的基础/缓存/推理维度，已知数冲突、终态种类冲突及非法细分在 CAS 前拒绝；历史前缀不回退终态或清空已知事实。元数据、加密 snapshot 与事件同事务落盘，失败不确认事件。取消请求不伪造取消终态；dispatching/uncertain/过期不能推断完成或免费。metadata 保留期沿任务既有上限，删除/清理正文不扩大内容读取权限，也不删除保留期内元数据。
5. **验证：**JSON/SSE 的完整/零/部分/未知/精确大整数/细分及非法数；真实 socket 提交→轮询/恢复→重复终态、迟到补充、取消与完成竞争、Key 撤销清理、失去 claim、事务失败/事件回放、重启及正文清理后查询；跨 owner 返回404、session/管理员范围不变，查询不能读取密文/正文/原生ID/凭据。schema20→21 两次重开、旧 snapshot/账本/迁移时间保留，集中 build/vet/Go/生产冒烟和最终精确头完整 CI。
6. **协作与范围：**后台用量记录由任务 owner 持有，并非可重放执行授权；排队不增加网络 attempt，轮询不新增账单，不改变 RPM/TPM 的现有基础完整用量收尾。strict/legacy 后台、WS/后台收费、真实 Provider/OAuth、跨模式父链和完整 P7/P8 验收继续后置。不会以缺密钥作为阻止本批受控实现的理由。

2026-10-20 本地受控证据：JSON/SSE 缺失、零、部分、精确大整数与缓存/推理维度解析通过；真实 socket 的迟到终态补充与冲突拒绝、关闭重开、失去 claim、事件/元数据同事务回滚通过。schema20→21 两次重开保留旧密文、事件、claim、revision 与迁移时间；SessionBearer 本人/管理员投影不读取 snapshot/ciphertext，调用 Key 和跨 owner 查询拒绝。Windows GOARCH=amd64 build/vet 与生产入口 local.py（含强杀恢复、删除/TTL后用量保留）通过，12项控制台行为通过。全量 Go 回归初轮仅新增 background/v2 未加入架构版本清单，补登记后完整 architecture 包复验通过，其余包初轮全部通过；Linux race与容器交付待最终精确头CI。未执行真实 Provider/OAuth，也未开放收费。

</details>
### P8：完整成本证明

- **层级/目录：**L1 billing/gateway/store，必要的 L2 计量元数据；共享配置/注册/迁移/OpenAPI 统一协调。
- **依赖入口：**既有 ledger、execution、价格版本、双预算事务、pending 与管理审计；不新建资金权威。
- **地基影响：**对新增支持组合的成本/计量契约先提 CR；本批先完成已支持操作，Responses/后台/WS 逐项扩展。
- **行为/契约：**分别证明用户收费上限、单次 Provider 成本和全部尝试累计上界；绑定模型/操作/账号、最高适用价格、输入输出限制、计价维度、价格版本和有效假设，无法证明则发送前拒绝。
- **验收：**覆盖批量输入、完整历史/工具定义、输出/推理/缓存/等级、失败请求收费、税/附加项、途中改价、映射/有限重试、跨 Key 并发、溢出及未知 attempt。界限/依据/拒绝原因可由后端查询，异常实际用量保留 pending；复用适用崩溃证据，新增行为补回归。
- **协作影响：**可与 P1/P4 独立研究；不因账号闭环或 usage 接线而扩大 strict。只有每个准许组合的完整证据成立才关闭 S5-03，S5 完成也不代表支付平台就绪。

## 集成验收与后续门槛

#### strict 前台网关强杀与恢复子批（2026-10-20）

1. **层级/目录：**仅internal/gateway的独立进程集成回归与本方案；真实SQLite、鉴权中间件、有限准入、完整Chat JSON/SSE handler、既有唯一executor及strict Engine组成被强杀子进程，不增加生产故障钩子。
2. **公开依赖：**复用gateway.StrictBilling现有Reserve/StartPreparedAttempt/EndAttempt/Finish端口和既有身份、执行、账本查询/恢复入口。测试包装器只在原调用前后暂停，不构造费用凭据或绕过最终URL/body/Header证明；无公开地基变更或新冻结。
3. **来源/地基：**对照new-api固定25372e30b1f94255b1f818aeaaf006f042572580的service/billing_session.go::Settle/Refund，采用其“资金步骤已提交后不得再全退”的故障场景；该实现的进程内标记、分步余额更新及异步退款不满足TXAPI持久双hold/未知执行规则，因此不移植实现，继续使用现有事务账本与恢复owner。L0、迁移、配置与strict许可不变。
4. **行为/回退：**在持久预留后未记attempt、已记成本attempt但未发送、上游实际受理未返回、完整输出已交付但receipt未提交、receipt已提交未结算、结算已提交未退出边界执行Process.Kill并Wait。同一库重新打开并等待真实lease到期，重复恢复及再次重开恢复；未知保留hold，已知只按旧报价结算一次，未记attempt才可无费用释放。测试transport只将经过原成本准入的官方目标改送显式loopback受控上游，禁止真实Provider访问。
5. **验证/验收：**核对独立上游发送次数、JSON/SSE客户端观察、不可变attempt/成本快照、user与Key两侧余额、历史累计Provider成本准入、旧价格版本、重复恢复无新请求/重复扣费；重启前更新当前价格，以区分历史报价与现价。局部矩阵稳定后执行完整gateway及billing回归/vet，最终精确头完整CI另记。
6. **协作/限制：**本批只补前台strict现有档案的受控恢复证据，不声称实际main镜像的故障注入、真实费用认证、后台/WS收费或S5-03完整成本证明；不以已交付的协议终态推断金融记录已经提交。回退仅删除测试，无数据/接口回退。

本地受控结果：Windows GOARCH=amd64 的11个Process.Kill+Wait场景全部通过，共7次实际上游发送，4个发送前边界为零发送；每个场景均等待真实lease到期、恢复两次、关闭/重开同一SQLite再恢复两次。两侧余额在无attempt时全部释放，prepared/受理后未知以及客户端完整SSE已交付但receipt未写入时均保留各20000000000单位hold；四个已知回执场景每次只收5850000单位。恢复前现价改为输入/输出各1每千Token，当前报价18000000单位，旧请求仍用原价；attempt与其成本快照不改，历史累计Provider成本准入19219200单位保留，不冒充当前余额或已付账单。完整gateway包60.669s、billing包22.602s及两包vet通过；最终补强无后台goroutine残留/失败路径有界与原始状态断言后，完整11场景复验29.718s通过。生产Go与schema/OpenAPI保持基线，最终精确头完整CI待验收。

本批CI修复范围：PR Verify 39370871889在既有tests/smoke/background.py的单次控制台取消断言失败，原断言缺少状态/正文诊断；源码task_control.go::Cancel明确要求revision CAS，worker可在GET与POST之间合法推进revision。仅在该真实main冒烟消费者补已知旧revision必拒绝409及最多5次重新读取后显式取消意图，其他响应立即失败并带诊断；保留3次生成发送、2次远端取消、用量/事件及TTL断言，不改生产CAS/客户端自动重试策略或新增接口。该回归复用现有任务owner与已固定来源的取消竞争场景，不引入第三方实现。Windows GOARCH=amd64 完整实际main冒烟集中复验已通过，包括新旧revision、资金/后台/用量/重启/TTL链；旧头push Verify 39370820574成功但不能抹去PR失败，最终修复头完整CI重新验收。

P0 与 P1–P8 逐批验收，不能把整个后端阶段积压到最后。产品主线完成还须覆盖以下跨批场景；已有子批证据按精确提交复用，仅为新增/变化范围补证据。

- [ ] 强杀矩阵：受理未发送、上游受理未保存 ID、完成未结算、事件提交未送达、取消/完成竞争；恢复不重发未知生成、不重复追加历史、不重复扣费。
- [ ] 跨模式与权限：HTTP→WS、WS→HTTP、WS 重连、父链过期、Key 撤销、内容密钥缺失、数据库失败和 worker 关停；终止后可区分完成、取消和未知。
- [ ] 后端可操作性：本人任务/历史/存储期限/账本关联，管理员脱敏诊断与 pending 处理，连接/轮次/在途/恢复/事件占用指标；正文读取需单独明确授权，前端新页面后续再排期。
- [ ] 交付与真实兼容：容器/反代 Upgrade、长连接、重启及内容恢复；指定版本 SDK/Codex 的 HTTP、工具、检索、后台恢复与 WS，客户端缺能力时补独立协议客户端。辅助接口如 models/compact 按实际需求登记，不伪造成功。
- [ ] 至少一个声明范围内的真实 Provider/客户端完整组合通过；真实测试具备明确账号、预算和数据授权。受控上游不能证明官方服务兼容，不支持组合显式拒绝也不能代替主线完成。
- [ ] 最新集成基线的最终功能头通过完整现有 CI，已有 API 无回归；分别记录本地验证、精确头 CI、合并、发布和运维状态，不互相替代。

准入等待队列只在容量/拒绝数据证明必要后单独设计 L1 admission CR，明确队列上限、最长等待/总 deadline、取消、公平性、槽位只释放一次和关闭 drain；HTTP/WS/后台不自动共用。外部 Runtime/R07 继续等待至少两个可独立安装升级的真实消费者，并证明编译期注册不足，再确认进程信任、回调权限/副作用、制品验证、代次和升级/回退契约。

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

固定源码作为原生化开发起点，允许在许可允许且层级适合的范围内具名移植/改写具体模块、函数、算法与有价值的回归场景；不把整仓库作为运行依赖/子模块。本轮只有源码分析，没有运行参考程序/测试或真实 Provider 验收。当前四份阅读副本均在 `.local/references/`，不会随 Git/worktree 分发；旧副本目录和旧运行状态不作为当前依赖或验收证据。

### 原生化实施方式

**后续开发约束（用户于 2026-10-20 明确要求）：优先参考已登记参考项目的具体源码并实现原生化。** 编码前先确认主参考的仓库、固定 SHA、文件/函数及可采用实现；按“**固定来源 → 选最小可用切片 → 适配既有 owner/类型/生命周期 → 接真实消费者 → 集中验证交付**”推进。同一问题优先选一份最适合的主实现，其他项目只补边界用例。交付须记录采用部分、TXAPI 改写落点和行为回归；没有适用实现，或因架构、权限、资金、生命周期、许可等具体约束需要偏离时，在本方案或 PR 中说明原因和替代实现。参考和改写必须落到实际调用链，不以源码分析报告替代功能交付。

| 切片 | 主参考及原生化内容 | TXAPI 落点与交付顺序 |
|---|---|---|
| 完整事件与流输出 | codex-proxy-rs 的事件 framing/原始帧与观测分离；sub2api 的完整事件 Flush、首输出/终态场景；CRS 补工具/客户端回归 | 第一批 P0/P4，L2 protocol/compat 处理事件，L1 保持唯一执行，L0 只承担必要机械能力；先接当前 HTTP/SSE 消费者 |
| 阶段超时、连接与背压 | new-api 的阶段超时/写 deadline/有界 worker 收尾；sub2api 的池参数/首有效输出计时 | 第二批 P0，复用 core HTTP client/资源代次与既有取消链；先交付有实际故障场景的能力，连接分片/预热/池扩展有数据才加 |
| 账号接线与生命周期 | codex-proxy-rs 的账号/revision 绑定，sub2api 的统一刷新/分页/Provider 隔离和资格原因 | P1→必要的 P2/P3；直接消费 TXAPI upstreamauth claim/started/CAS/uncertain，不复制参考项目的存储/锁/刷新体系 |
| 调度、续链和用量后续 | sub2api 资格/窗口与幂等冲突场景，codex-proxy-rs continuation scope/租借，CRS 缓存与账号归因 | P5/P6/P7；分别接现有 scheduler、generation、quotation/usagehistory/ledger，不合并成通用大 Runtime |

规则：能在现有批准端口内完成的实现直接推进，不为移植增加无消费者的抽象层、插件协议、重复兼容门面或再次进行全项目架构设计。Go 实现可适配复用，Rust/JS 机制按 TXAPI 的 Go 类型、错误、context 和资源所有权改写；保留有价值的缺陷用例，剔除宿主专用依赖与策略，但不能以换语言/改名消除来源及许可记录。

只在新公开接口、权限/语义/生命周期、L0 机械边界或迁移方案超出既有批准范围时先提具体 CR；本轮采用方向不构成这些变更的批量批准。每批最多围绕一个可独立验收功能：先把选定实现接入真实链路，再集中验证权限、协议、取消/释放和适用成本事实，不长期停留在比较报告或只交付空端口。

原生化完成须同时具备来源与许可记录、真实消费者、TXAPI 行为回归和适用验收；参考测试通过不能直接替代。语义改变同步更新 OpenAPI/既有消费者；性能型切片保留改动前后同条件结果，成熟机制的移植同样不能直接宣称性能或稳定性提升。

| 项目与固定 SHA | 采用重点及开发落点 | 不能直接继承的假设 |
|---|---|---|
| QuantumNous/new-api `25372e30b1f94255b1f818aeaaf006f042572580` | P4：RelayKit 路线/质量/步骤、有状态流与有界损耗诊断；逐轮 WS 复用鉴权、任务终态竞争用例 | 路线/质量标签不证明无损；异步退款、两步结算、缓存补偿不替代持久双 hold/事务账本 |
| zyycn/codex-proxy-rs `60570883007febebe732597a9881397098d724f5` | P1/P6：发送/提交事实、不可变路由、续链 scope、建连 reservation、账号 revision；未来扩展副作用 | 无预扣预算、缺费用按零、原始 AT/RT 恢复日志、协调失败的续链放行或不完整关闭不能沿用 |
| Wei-Shaw/sub2api `f2e5b55f3e8069df1c02a9a27af537b5fdefa64e` | P2/P3/P5/P7：资格先于 Top-K、加权候选、窗口分类、统一刷新/分页、独占 WS lease、幂等指纹 | Redis TTL 在途预留/内存 usage 池不是持久未知保护；普通扣费透支、float64 和部分缓存失败放行不进 strict |
| Wei-Shaw/claude-relay-service `cf95ecc5e0ba846faaf1b11de574367de89fd00c` | P3/P4/P7：会话来源/失效、专属回退、用户轮次与工具结果、工具增量去重、细分限流、缓存归因 | session hash 不代表租户隔离；刷新锁不代表轮换安全；usage 补零、浮点统计、背压丢失及任意工具交错不能当正确性保证 |

各项目只原生化真实增量，不重建 TXAPI 已有执行/shadow/代次框架，不照搬存储与部署架构。准入排队和连接预热/池化按实测需求立项；估算成本/缓存收益不成为成本上界证明。

### 核心转发源码对照

2026-10-19 对照上表固定 SHA 与 TXAPI `adab9b3` 的转发源码；不把工作区其他未提交功能当已验收能力。以下结论仅限所列路径，不推断同项目全部 Provider/协议都采用相同实现，也不按语言或机制数量评定性能高低。

| 项目 | 已确认的核心机制与优点 | 限制、取舍及 TXAPI 采用方向 |
|---|---|---|
| new-api | 可配置空闲池/响应头超时；HTTP/1 强制与可选 HTTP/2 多 Transport 分片；SSE 行上限、10 项 channel、有锁心跳/业务写入、逐次写 deadline、空闲检测、关闭 body 后 wait worker | 学习分阶段超时、受控连接策略和反代保活；分片默认 1，不代表必然更快。心跳可提前提交 HTTP，必须单独处理状态/重试；通用 scanner 主要按 data 行消费，不直接当完整 SSE 无损解析器。证据：`service/http_client.go:82`、`http_transport_sharded.go:15`、`http_transport_policy.go:18`；`relay/helper/stream_scanner.go:43`、`:76`、`:91`；`relay/helper/common.go:88` |
| sub2api | 按账号/代理/协议画像管理 client；响应头与连接池限制、HTTP/1 回退及 HTTP/2 PING；Responses 路径先暂存账号头与 prelude，首个语义输出前设限/计时，按完整事件/队列排空 Flush，必要时使用有界读队列 | 优先研究阶段超时与首输出/提交区分、完整事件合并 Flush。暂存最多 8MiB，部分平台会落临时文件，不照搬磁盘存正文；部分路径断开后继续 drain usage，与 TXAPI 前台取消政策不同。raw Chat 将 DONE/usage/finish_reason 任一个视为结束证据，比 TXAPI 部分路径宽松，属于兼容政策差异，不当通用成功证明。证据：`backend/internal/repository/http_upstream.go:1383`、`:1419`；`internal/service/openai_gateway_response_handling.go:60`、`:146`、`:287`、`:808`、`:967`；`openai_first_output_timeout.go:22`；`openai_raw_stream_truncation.go:19`（后三者同为 backend/internal/service 下文件） |
| codex-proxy-rs | Provider 按账号/代理等键缓存 client，响应块按消费节奏拉取并有流空闲超时；SSE 保留 raw frame 与旁路观测，规范事件与原始 wire 分离；唯一 coordinator 持有发送 watermark、重放屏障与实际 commit，WS 有界消息队列/租借 | 学习原始数据与观测分离、发送/实际提交证据和连接失效边界；不是零解析/零缓冲，也不意味着通用跨协议覆盖。Provider SSE client 与 host 通用 HTTP client 是不同路径，不能混用某个池参数证明整体转发。证据：`backend/crates/providers/openai/src/transport/client.rs:63`、`client_sse.rs:724`；`crates/gateway-protocol/src/openai/sse.rs:148`、`crates/gateway-core/src/engine/coordinator.rs:590`、`:691`（crates 均位于 backend 下） |
| claude-relay-service | Claude relay 使用共享 stream/nonstream keep-alive Agent，代理 Agent 缓存；解压后按块/行转发并解析 usage，连接 close 销毁上游，具备工具名/客户端协议修补与错误输出 | 学习真实客户端 Header/事件回归；选定 Claude 路径 write 未等待返回值/drain，unified 转换拦截始终返回 true，缺少完整背压传递证据。大 maxSockets 不是承载能力证明；逐块 toString、长行/事件缓冲需 UTF-8 和内存回归，不能直接复制。证据：`src/utils/performanceOptimizer.js:13`；`src/services/relay/claudeRelayService.js:2212`、`:2709`、`:3160`；`src/routes/unified.js:148`、`:202` |

TXAPI 当前保有共享 HTTP Transport（默认空闲连接总数 200、每 host 50）、整请求/attempt 超时、同步 Write/Flush、下游写截止与响应体关闭。JSON 先有界完整读取再输出，SSE 按行/事件解码；普通 OpenAI JSON 有正文直通分支，模型映射仍可能重序列化。普通 `ExecuteTo` 不自动转发全部上游响应头，`ExecuteRaw` 另保留 Header；原生 Responses 会按身份契约改写 ID，因此不声称全字节透明。

当前需验证的具体缺口：独立响应头/首有效输出/流空闲及下游写预算、HTTP/代理/TLS 画像与池复用、完整事件 Flush 和分配开销、必要响应头的白名单与错误保真。本批源码将普通 OpenAI Chat 从逐 data 行解码改为完整事件解码，保留注释、字段顺序和未知字段；仍要求 finish 与完整 DONE，不将 EOF 尾事件视作完成。原生 Responses 同样拒绝缺空行的尾终态。Chat 单事件 wire 与 data 观察缓冲合计上限 1MiB；Responses 原有单事件 wire 上限 1MiB 计入最终空行。现有 Scanner 将 CRLF 规范为 LF，不支持裸 CR 分隔，也不声称网络原字节透明；跨协议转换仍保留原范围。多行事件的输出估算消费者同步更新，未知原生 usage 不补零。此处为实现边界，验证状态以本批记录为准，兼容尾部缺失、早期 prelude 提交和性能仍须单列验收。

Anthropic/Gemini 转换同样先组装完整事件，再交给既有翻译器；支持首次 BOM、多行 data、CRLF 与 metadata error，wire/data 合计限制为 1MiB，事件空行到齐前不输出语义帧。Anthropic 必须具备 finish 与原生 message_stop，Gemini 保留 finish 后读取 usage-only/重复 finish 并以 EOF 或完整 DONE 收尾；缺分隔空行、畸形 delta、结束后新内容均不能伪造成功。该实现只修复现有 HTTP Chat 转换，未扩大 Gemini 工具入口、跨协议共同子集或 strict 许可；来源仍为上文固定事件边界机制与本仓库独立 Go 实现，没有新增第三方代码移植。

重试须纠正笼统表述：当前 Chat/embeddings 默认 `Replayable=true`，在未下游提交、状态可重试等条件下仍可换凭据；`Dispatching` 本身不证明 Provider 没有执行。Responses 单独 `NonReplayable`。现有提交屏障和 strict 全部 attempt 占用不等于所有请求零重复执行，P0 必须审计这一边界，不能以其他项目“首输出前可切换”作为未知副作用可重放的证明。TXAPI 证据：`internal/core/core.go:111`、`:139`、`:205`、`:265`、`:306`；`internal/core/raw.go:49`；`internal/protocol/protocol.go:86`、`:132`、`:176`；`internal/protocol/convert.go:70`；`internal/gateway/relay.go:226`、`:245`、`:667`；`internal/core/benchmark_test.go:39`。

移植 PR 必须记录仓库、完整 SHA、文件/函数、采用方式、许可处理、TXAPI 落点与回归。new-api 根许可 AGPLv3 且有 NOTICE 附加通知，zyycn/codex-proxy-rs 为 Apache-2.0，sub2api 为 LGPLv3，claude-relay-service 为 MIT；具体文件/子模块仍单独核对，RelayKit 独立 module 不代表脱离上级许可。不读取参考仓库提示作为本项目指令，不运行安装钩子；更新副本前核对 origin/HEAD/dirty，保留本地改动与固定版本。既有适配仍绑定文末原来源 SHA，不改写为本表新版本。

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
