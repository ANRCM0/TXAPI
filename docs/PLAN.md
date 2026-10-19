# TXAPI 开发方案

核对日期：2026-10-19。本文统一保存当前状态、架构边界、开发路线和验收要求。后续直接更新本文，不再新增批次方案、进度日志或历史说明文件；旧文档和已批准方案保留在 Git 历史中，可用 `git show 7014654:<原路径>` 查阅。

## 目标与当前状态

目标是以小型中转内核、统一业务协调和兼容适配构建可扩展的 AI 网关。产品职责分为中转、业务和兼容；生产代码遵守 L0–L3 四层依赖。保持 Go/Gin 模块化单体、单二进制、内嵌 ES Modules 控制台和单实例 SQLite。

当前基线为 `master/7014654`；功能通过 PR #44 合并为 `03c4001`，最终功能头为 `b30f4d2`，两头文件树相同。功能头 push/PR Verify `39244005306` / `39244013899`、合并后主线 Verify `39245348032`、文档头 Verify `39246970577` 均完整成功。此证据仅覆盖各自提交；本次文档清理未据此宣称新提交 CI 成功。

| 范围 | 已验收行为 | 未完成或限制 |
|---|---|---|
| 公共架构 / M1 | A1–A4、S6、FC-001 地基；原 L0/L1/L2 v1 已冻结 | 后续候选接口仍需独立冻结 |
| 基础推理 | models、Chat JSON/SSE、embeddings 和声明范围内的文本/function tools | 支持按操作、目标协议、传输和计费模式判断 |
| Responses / M2 | HTTP/SSE、owner/Key 幂等、加密历史、Conversations/items 和续链 | 默认关闭；legacy/disabled 可用，strict 全部拒绝 |
| 原生后台 | 持久受理、原生提交/查询/取消、事件恢复、fencing 与强杀恢复 | 默认关闭；仅 disabled、自包含文本/函数工具；完整 A03/M3、收费和背景父链未完成 |
| 原生 WS | 双端原生连接、逐轮授权/准入、串行文本/函数轮次、连接内最新父 ID | 默认关闭且仅 disabled；收费、持久重连和任意多路复用未完成 |
| R00–R02 | 路由观测/shadow、执行事实、原生资源代次发布/排空和失败清理 | shadow 不控制路由；不代表外部 Runtime |
| R04a 账号 | operator-only 授权/重新授权、加密元数据、单次刷新和停用 | 受控验收；真实账号、推理路由、自动刷新和完整账号池未验收 |
| R05a 用量 | 原生 Token 明细、不可变售价报价、持久前台 HTTP 用量 | 报价、用量、用户收费、Provider 成本与 hold 分开；后台/WS 明细未覆盖 |
| S5 / R05b | 定点价格、双层 hold、幂等结算、对账、未知保护和崩溃恢复 | S5-03 完整成本证明与 Responses/后台/WS 收费未完成 |
| R06 管理 | 任务控制、路由诊断、配置发布、个人财务读取及 R06c 账本展示 | 账号管理依赖 R04；更广泛页面仍暂缓 |
| HTTP / 数据库 | [OpenAPI](openapi.json) `0.11.0` / 111 操作；schema v17 | 接口数和迁移版本不表示生产收费就绪 |
| 真实兼容 / R07 | 已有十项受控官方 Python SDK 场景；扩展采用编译期注册 | 真实 Provider/OAuth/SDK WS/Codex 完整组合及外部 SDK/Runtime 未验收 |

标准 API Key 路线不依赖 OAuth 账号池。完整 S5、A03–A06/M3/M4 和 R04–R07 整体目标继续开放；不以子批通过、测试数量或阶段数换算完成百分比。本轮不自动新增支付/套餐、媒体、多实例、PostgreSQL/Redis、外部 Runtime 或更广泛账号池。

## 架构与契约

| 层级 | 责任与主要目录 | 依赖边界 |
|---|---|---|
| L0 | `internal/core` 及登记的机械执行：一次发送、读写、取消、背压和提交事实 | 不放用户、路由、账本、账号或会话策略；限制由架构测试执行 |
| L1 | access、gateway、billing、generation、scheduler、store、secrets 等基础能力 | 持有唯一鉴权、准入、attempt、预留/结算、迁移和资源生命周期 |
| L2 | protocol、compat、server、transport、web | 负责入站/上游编解码、错误/事件、HTTP/WS 和展示；通过公开入口调用能力 |
| L3 | responses、responsesws、管理/查询用例及其他登记功能 | 仅消费登记的 L1/L2 公开契约；不直接依赖其他功能实现或 L0/私有框架包 |
| 组装根 | `cmd/gateway` | 注入实现、注册和管理启动/关闭，不放功能规则 |

包的精确层级/owner 以 [layers.json](../internal/architecture/layers.json) 为准，完整公开类型、字段、标签和签名以 [public-v1.json](../internal/architecture/public-v1.json) 为准；[契约源码](../internal/foundation/contracts) 定义实际接口。新包先登记，不放宽依赖或可见性检查，不用测试例外掩盖生产依赖。

- FC-001 在 PR #18 / `master/889ecbd` 验收冻结：admission、configuration、control、execution、identity、json、ledger、lifecycle、observability、responses、state、text 的 v1 契约，以及 `foundation/httpoutput/v1`、`compat/responses/v1`。httpoutput 是 L2 输出门面，L3 接收输出端口，不持有 HTTP writer。
- FC-002 在 PR #22 / `master/a44d4d1` 追加冻结 `background/v1` 与 `responses/v2`，仅限已验收的 disabled、自包含原生后台范围；不扩大计费许可。
- 后续 execution/v2/v3、responsesws、routes、taskcontrol、ownerledger、quotation、usagehistory、upstreamauth 等新增入口按各批已批准范围消费。登记、导出和完整 CI 不等于所有候选契约已经冻结；需要新增或改变接口时先核对批准记录与实际消费者。
- 每次生成重新检查可信身份、当前 Key/模型权限与准入；用户提交的 owner、余额或绑定值不构成认证。会话登录和调用 Key 分开，跨 owner/Key 查询不可枚举。
- 保持单一授权、执行、迁移和账本权威。业务不自行发送 HTTP/WS、启动另一套迁移/刷新/结算流程；观察结果不控制资金或恢复事实。
- 同协议保真，跨协议仅声明共同子集；无法表示的字段在发送前拒绝。Chat、embeddings、Responses 保留各自模型，不经 ChatRequest 有损中转。
- 协议终态、操作终态、财务终态和输出提交事实分开。提交后不改目标重试，不伪造成功终态；未知执行不清零、不盲目重发。连接、生成、会话、后台任务与 Provider 状态分别管理寿命。
- 配置先准备后发布，失败保留旧代次；资源退休后排空，worker/连接取消并 join 后才能关闭共享 gateway/store。正文、消息、事件、队列、连接、保留期与退休代次都必须有界。

## 后续开发路线

原吸收方案的 R00 → R01 → R02 → R03a/b 已按选定子批验收；后续优先补完整用户链路，不重复抽取公共框架。R05 的成本研究可独立推进，共享契约、账本、迁移和组装修改仍统一协调。

| 批次 | 下一步 | 退出条件 |
|---|---|---|
| R04 账号接入 | 在 R04a 受控工具上划定真实账号验收、最小推理凭据接入和后续生命周期批次 | 明确 Provider/账号来源；当前权限、发送边界、刷新/CAS/unknown 和静态 Key 回退成立；真实证据独立记录 |
| S5-C / R05b | 选定模型/操作/账户，补输入、最高适用价格、失败收费和全部尝试成本边界 | 满足下方完整清单后才开放对应 strict 范围 |
| S7-A03/A04 | 在 disabled 后台/WS 子集上补收费、父链、持久重连和跨模式语义 | 逐批通过完整清单，不能以 MVP 关闭 M3/M4 |
| S7-A05/A06 | 故障矩阵、可操作性和指定真实客户端/Provider 组合 | 至少一个完整真实组合通过；原 API 无回归，最终头完整 CI 成功 |
| R05a / R06 | 按具体操作补用量维度与稳定管理页面 | 先有真实消费者和后端契约；更广泛前端另行确认 |
| R07 | 外部 SDK/Runtime 与独立分发 | 至少两个真实、可独立安装升级的消费者，且编译期注册不足，再确认进程/权限/资源/升级契约 |

## 严格计费边界

strict 目前只允许已登记的官方 OpenAI 文本 Chat/embeddings 档案；Responses 全部拒绝，原生后台/WS 启动必须使用 disabled。Anthropic/Gemini、兼容代理、未知模型/维度不自动继承许可。legacy 为兼容模式，不具备 strict 的持久预算保证；disabled 只关闭计费，不关闭身份和准入。

`StartPreparedAttempt` 检查最终真实 URL、正文、输出限制和鉴权头，是唯一可执行成本准入入口。无最终正文的旧 `StartCostedAttempt` 不能开启新尝试。输入缺证明返回 `provider_input_unproven`，费率缺证明返回 `provider_tariff_unproven`，凭据绑定不匹配返回 `provider_credential_unproven`，均不发送、不罚健康、不换凭据绕过。

- `openai_embeddings_v1`：仅官方根 URL、openai/embeddings 和三个精确模型 `text-embedding-3-small`、`text-embedding-3-large`、`text-embedding-ada-002`。整请求输入包络至少 300000 Token，文本数组最多 2048 条；不接受 token ID 数组/混合输入，不拿单项上限或字符均值代替总输入边界。
- `openai_chat_v1`：仅官方 URL、openai/chat 和 `gpt-4o-mini-2024-07-18`。配置输入包络至少 128000，最大输出至多 16384，字段为 `max_completion_tokens`，正文显式 `service_tier: "default"`，严格文本且 `n=1`；工具、媒体和未登记缓存/推理/等级扩展拒绝。
- 上述接口数字为 2026-10-17 核对后登记的实现约束，不代表未来远端保证。具体检查和来源入口见 [输入档案源码](../internal/billing/input_profile.go)、[费用档案源码](../internal/billing/tariff_profile.go)。来源为 OpenAI 的 Create embeddings、GPT-4o Mini、Chat Completions、Priority processing 和 Pricing Standard 文档；变更适用范围时重新核对，不把模拟上游或旧快照当真实价格认证。
- provider 成本、用户售价和实际账单独立。先核对不可变售价/目标倍率覆盖，再核对每次尝试和累计 provider 包络；缓存折扣不降低最坏预留，未知尝试继续占用。定点溢出或覆盖不足在发送前拒绝；异常实际用量保留 pending 和审计。
- 失败/拒绝请求计价、逐凭据最高适用价格、税/附加项和未来改价假设尚未完整证明；目前没有已验收的完整生产成本契约或可靠通用 tokenizer。用户收费封顶不等于 Provider 成本绝对有界，S5-03 继续未完成。

### 严格计费配置

配置字段以 [gateway.example.yaml](../configs/gateway.example.yaml) 为准。现有费率档案 `openai_standard_usd_2026_10_17` 使用 USD 和显式 `credential_bindings`；其内置下限只约束已登记目标，不能证明账户最高适用价格。provider 金额精度为 `1e-9`，用户售价仍使用独立账户单位，不自动换汇或相互比较。

凭据 ID 必须与完整上游 Key 的小写 SHA-256 摘要对应，并核对最终发送的 Bearer Header；换 Key/新增 ID 需重新核实并加载。实际配置、摘要清单和密钥不提交。可在本地用隐藏输入生成摘要：

```bash
python -c "import getpass,hashlib; print(hashlib.sha256(getpass.getpass('Upstream key: ').encode()).hexdigest())"
```

## 路由与用量

- 路由先过滤授权、模型、能力、绑定和凭据资格，再在有效优先级池选择。扩展不能让不合格目标重新入选；选择、准入、发送与失败分别记录。
- `route_diagnostics` 与 `shadow_route_scoring` 均默认关闭；shadow 只作有界前台 HTTP 观测比较，不改变优先级、权重、亲和、重试或真实路由。采样不代表 Provider 总负载，重启不保留比较历史。
- `usage_history` 默认关闭且仅允许 disabled，记录前台 Chat/embeddings/Responses 的调用与 attempt 元数据；不包含 native WS、原生后台或 durable claimed 执行。容量、TTL、父子记录预留和失败拒绝由既有准入处理，不退化成可丢日志。
- 新采集上限为 1..65536 个实例父记录，owner 上限不超过实例上限，保留期为 1..2592000 秒；零配置选择有限默认值。每个调用预留有限 retry 子槽，这不是磁盘/WAL 字节配额保证。
- Token 明细允许未知、零和部分完整，不能默认补零或推导总量。用量、报价、最终收费、当前占用、历史预留和 provider 包络在读取与展示时保持分开；会话授权、精确金额和不可枚举边界继续适用。

## 账号与持久状态

R04a 通过 `-upstream-account authorize|reauthorize|list|refresh|disable` 提供本地命令；复用现有凭据 AEAD，输出仅元数据。真实 loopback 回调使用独立 state/nonce/S256 PKCE，验证签名身份与签发 client 绑定。刷新采用 claim → started → 单次网络交换 → revision/fence CAS；只有已证明未发出的尝试可退避重试，可能轮换但未存回的结果保持 uncertain，必须重新授权，不重用旧 refresh token。候选账号不自动成为推理凭据，没有自动刷新 worker。

Responses 历史绑定当前 owner/创建 Key/模型权限，内容加密、有 TTL/容量和 CAS。`store=false` 不升级为永久历史；原生后台仍按明确策略临时保存恢复内容。观察连接断开不取消已受理后台任务，WS 前台断开则按当前轮次取消/未知事实收尾。持久序号先提交再公开，不用进程内缓存替代跨进程历史。

迁移统一追加版本，现为 schema17；不修改已发布步骤。升级前停写并一致备份，旧 schema16 二进制拒绝打开 v17；回退须恢复匹配的停写备份，不删账号或改写账本/迁移“修复”。凭据和内容密钥、旧备份/WAL、内存与公网 TLS 的边界仍需独立维护，管理掩码不构成加密。

## S5 剩余验收

以下保留原阶段与完整验收清单，不新增完成勾选；已验收子集不等于整个阶段完成。

- [x] S5-01 明确“不计费”和“严格预算”运行模式；严格模式缺少必需组件则拒绝新调用。
- [x] S5-02 价格采用整数/定点，保存价格版本；无价格默认拒绝严格计费请求。
- [ ] S5-03 用户与 key 双层预算原子预留；最大允许请求费用有上界。
- [x] S5-04 实际用量幂等结算、返还余量；usage 缺失进入估算或待对账状态，不默认免费。
- [x] S5-05 预留有租约/在途确认，崩溃恢复不重复扣费；客户端取消不等于无成本。
- [x] S5-06 每次尝试成本与最终用户收费分开；账本不依赖可丢弃日志队列。

S5-03 已实现双层事务与收取费用上限，但尚不据此宣称允许请求的厂商成本已严格有界。S5-05 的强杀矩阵、可操作对账、本地与最终 Linux race/完整 CI 均已通过，因此勾选该项；人工封顶或豁免不替代 S5-03，完整阶段仍未完成。

验收：并发请求不能突破定义的预算上界；重复结算不重复扣费；进程崩溃/取消/未知执行结果有可追踪恢复路径。预留与实际价格维度覆盖输入、输出及启用的其他计价项，不能只预留 max_tokens 的输出费用。

### S5-C：完成完整成本边界

负责范围：`internal/billing`、`internal/gateway`、相关 `internal/store`，必要的协议计量元数据；模型、迁移、入口、配置、OpenAPI 与页面变更由集成负责人协调。复用现有价格版本、双预算事务、attempt 事实、pending 和管理员审计。

已合并切片包括请求/转换限制、独立成本契约与累计预算、发送前售价覆盖、登记输入/费率/凭据门槛及诊断。当前行为见本方案的严格计费边界与实际源码。它们不替代逐账户最高适用价格、失败收费及未来价格假设的完整证明；以下完整任务保持未勾选。

- [ ] S5-C01 明确成本契约：分别定义用户收费上限、上游单次尝试成本上界及整次请求全部尝试的成本上界；上游价格与面向用户的售价不得混为同一份保证。写清支持的提供商、模型、操作、计价单位与保证假设。
- [ ] S5-C02 在转发前验证输入与输出边界：采用可验证的保守 Token 上界、受约束的模型上下文/输出限制或可靠的计数方式，覆盖 embeddings 的批量总输入。字符数平均估算不能充当严格证明；无法建立边界的组合在 strict 下拒绝，不自动降级。
- [ ] S5-C03 定义所有准许的收费维度：输入、输出、缓存、推理及服务等级按实际提供商语义处理。不得假定推理 Token 必然包含在某个输出限制内；缓存收益不能降低最坏情况预留。不能可靠界定的维度继续拒绝。
- [ ] S5-C04 为可选渠道、模型映射及有限重试计算整次成本边界，绑定不可变价格/能力快照；每次执行前确认剩余成本额度。执行不确定的 attempt 按保守上界占用，不因无回执就归零，也不能靠切换凭据无限扩大成本。
- [ ] S5-C05 将边界、计算依据和不可证明原因显示在必要的管理接口/页面；缺价、溢出、越界与组件故障在上游执行前拒绝。异常实际用量继续保留 pending 和审计，不把事后封顶当成事前成本控制。
- [ ] S5-C06 验收覆盖边界值、超长/批量输入、隐藏计价项、途中改价、映射/重试、跨 Key 并发及未知执行结果；复用仍适用的崩溃恢复证据，新行为补定向回归并通过最终完整 CI。

为 S7-A 保留真实扩展边界：后续每次生成都重新准入和计费，续链费用包含实际展开的历史/工具定义，不能只计算增量输入；后台任务运行和 WS 多轮不能共用一笔无限期预留。S5 本批先完成已支持操作的上界，S7-A 新操作使用同一引擎独立扩展并验收，不能把 S5 绿灯直接套给新操作。

完成标准：每个 strict 准许组合都能解释预留覆盖范围和上游约束，所有不满足假设的路径保守拒绝；用户双预算和上游多次尝试的边界分别成立。保证基于已验证的上游契约，不承诺远端违约时仍绝对无损。满足后才勾选 S5-03、完成阶段验收并合并；S5 单独完成也不代表支付平台已就绪。

### S6：统一兼容框架收口

S6-A/B/C 与 S6-00 至 S6-06 已独立验收合并为 M1。继续复用既有消费者与共享执行链，以下保留已验收清单。

#### S6-A：真实共用入口与操作能力

- [x] 核对并收口已实现的非 Gin 类型化执行入口与 Chat/embeddings 共用协调链，保留 HTTP 适配的既有授权/准入边界、外部行为和同协议快速路径；只修补实际缺口，不重复提取已完成的入口。不通过伪造 Gin context 接 worker/WS，也不为未来消费者堆空接口。
- [x] 区分通用执行信封与操作载荷：可信身份/作用域、模型与能力、截止时间/取消、结果/提交事实属于共用契约；Chat、embeddings、Responses 各有能保持语义的模型，不把所有功能塞入 ChatRequest。
- [x] 固定授权、请求级准入、目标选择、attempt、预算、重试判断与统一收尾的唯一生命周期；明确资源所有权、失败/取消/panic 的释放责任，以及协议终态、usage 完整性和财务终态的区别。观察组件不成为财务或恢复真相源。
- [x] 为实际的操作/源协议/目标协议声明支持范围，首批覆盖现有文本、function tools、流事件和 usage；能力过滤进入真实路由并返回脱敏诊断，不能替代用户授权。声明 HTTP、后台、WS 等不同传输能力，不把“能建连”等同于能执行任意操作。
- [x] 明确同协议透传与跨协议转换策略：关键语义不能静默丢失，无法表示则在提交前拒绝。转换不得擅自提高输出限制或改变收费服务等级；必要变化重新检查能力和成本。
- [x] 通过已有消费者的契约/行为回归证明入口收敛：权限、双预算、重试与提交事实、每流隔离、取消/租约无回归；保留 core 标准库、protocol 无业务依赖、gateway 不依赖 server/store 的门槛。只有真实出现新的传输/持久需求时才增加最小消费侧端口。

#### S6-B：function tools 与支持矩阵

- [x] 实现工具定义、调用 ID、名称、增量 JSON 参数、多个调用及工具结果回传；保持顺序、关联和结束原因。工具由客户端执行，不让网关执行任意工具代码或建立通用 Agent 运行时。
- [x] 覆盖分片/UTF-8/交错事件、未知 usage、上游错误、客户端取消及流已提交后的失败；同一外部 Request ID 并发不共享状态，不自动补成功结束事件。
- [x] 分别登记 disabled/legacy/strict 的支持矩阵；strict 只允许已证明成本与计量的子集。托管搜索、图片、代码执行等额外收费工具默认不开放，不能用文本价格替代。

#### S6-C：统一对外兼容与扩展接入

- [x] 梳理现有公开推理/管理 API 与业务操作的映射，固定请求/响应、错误、分页、鉴权和版本兼容规则；复用 OpenAPI 与现有消费者，破坏性差异有显式迁移方案。
- [x] 统一已支持操作的能力声明及协议/传输映射责任。协议终态、业务终态、财务终态和错误保持可区分；现有字节输出或 sealed 游标不自动获得原生事件语义，未来操作按各自模型扩展。
- [x] 为实际存在的协议、业务与页面扩展登记消费端口、构造注册、配置校验、能力/版本和资源生命周期；以现有内建或注入消费者验证。新增最小端口须有真实调用，不要求建立统一大接口或动态加载系统。
- [x] 用现有控制台完成前端应用契约验收：DTO、错误、金额/分页、当前角色与能力表达、取消和状态刷新有真实 API 联调；页面通过业务入口操作，不承担账本、授权或任务恢复规则。维持现有 ES Modules 交付方式。
- [x] 用现有 Chat/embeddings、管理操作、页面及非 HTTP/扩展消费者证明三层闭环；新接入复用授权、准入、执行和收尾。记录支持矩阵、受影响消费者与明确拒绝，不将局部 OpenAPI 校验当成全部行为验收。
- [x] 核对并复用适用的原提交验证，补齐本批改变的契约/行为后在最新基线上通过完整现有 CI，合并为内核与兼容框架基线；同步团队接入说明后再开始同类项目能力吸收和新产品接线。

完成标准：S6-A/B/C 均有实际消费者和验收记录，现有中转、业务处理、公开 API、扩展与前端形成可复用闭环，并在最新集成基线上通过完整现有 CI 后合并。**内核与兼容框架验收合并后**，再让 S7-A 或同类项目的具体功能通过这些契约接入。A1–A4 的既有验收事实保留；本节不重做其仍适用的实现或将 S7 产品提前标记完成。

### S7-A：Responses 持久执行主线

A01/A02 已独立验收合并，M2 完成；A03 disabled 子批和 R03 原生 WS 子批也已验收，但完整 A03–A06 与 M3/M4 仍开放。标准 API Key 路线不依赖 OAuth 账号池完成。各模式保持本方案当前状态表中的限制。

#### S7-A01：HTTP/SSE 接入统一框架

已验收 PR #10 / `3195d09` → `master/a925140`；支持原生文本/函数工具 JSON/SSE、owner/Key 幂等与短期加密私有回放，strict 全部拒绝。

- [x] Responses 使用独立请求、item 和事件模型，覆盖文本、function tools、instructions、输出限制、usage、完成/失败/incomplete；不经 ChatRequest 有损中转。
- [x] 复用 S6 已验收的类型化生成入口接入 Responses HTTP；新增操作能力与 usage 映射，不旁路授权、准入、attempt、预留/结算或统一收尾。为后续持久/后台消费者评审最小原子端口，真实事务调用与故障回归在 S7-A03 交付。
- [x] 设计内部生成 ID、外部 response ID、上游 response ID 与用户幂等键的边界；幂等键以可信用户/Key 作用域和规范请求摘要唯一约束，同键异参冲突，重试不新建账单。
- [x] 完成 legacy/disabled HTTP JSON/SSE 和现有 Chat/embeddings 回归；Responses 的 strict 子集尚未完成，全部在计费前拒绝。

#### S7-A02：持久会话、检索与续链

已验收 PR #12 / `2084bcd` → `master/765501a`；本地加密历史、Conversations、完整上下文、CAS/TTL 与当前授权已交付。

- [x] 实现 stored Responses 的获取/删除/input items，以及 Conversations 与 items 的创建、读取、修改/删除和分页；方法、DTO 和状态码以实施时核对的官方 schema 为准，同批更新 OpenAPI/消费者。
- [x] `previous_response_id` 与 `conversation` 各自明确上下文来源与互斥校验；保存父子关系、实际输入/输出、模型/渠道/凭据绑定和价格版本，不把缓存命中当成历史持久化。
- [x] 第一版以本地持久历史为权威，并为原生上游建立明确映射；一次执行只能选择一种上下文来源。使用原生父 ID 时不重复追加完整本地历史；只有完整可重建、已重新计价的上下文才允许显式重放，不能静默丢弃加密推理或工具项。
- [x] 所有操作默认校验 user_id 与创建 Key 绑定，并重新检查当前 Key 有效性和模型范围；跨用户、无授权 Key 统一不可枚举。Key 更换/转交不能自动继承数据，若确需支持另行提供显式授权与审计。
- [x] 保存内容须加密，明确主密钥、保留期、用户/实例存储限额、删除/过期与清理规则；`store=false` 不创建可长期检索的历史，也不能悄悄升级为持久保存。仅保留必需的幂等/计费元数据，后台临时恢复内容另按声明的短期策略处理。
- [x] 验证重启后继续多轮/工具链、父子分支隔离、并发追加版本冲突、删除/过期、权限缩减和上游父 ID 失效；不能让普通请求重试切换到不认识父 ID 的账号。

#### S7-A03：持久后台任务与事件恢复

已验收 FC-002 A03-01/02/03（PR #20–#22，最终 `501507d` → `master/a44d4d1`）：原生 JSON/SSE、查询/取消、持久序号、fencing 与强杀恢复；仅 billing disabled、自包含文本/函数工具，store=false 仍按声明临时保留内容。任务控制台已由 R06a 交付，PR #39 修正 null 读取。

以下保留原完整 A03 范围；其中非收费行为已有上述子批证据，方框不代表这些行为没有代码。收费双 hold、账本终态与背景父链未全部收口，因此总清单与 M3 仍不勾选。

- [ ] 支持 HTTP `background=true` 创建、获取、取消；先接上游原生后台能力。持久队列、任务事实和用户/Key 预留成功后才确认接收，不能只返回 202 或启动 goroutine 就称支持。
- [ ] 任务内部状态含 queued、dispatching、in_progress、cancel_requested、终态及执行不确定标记；公开 Response 状态按协议映射，不把内部状态原样塞入协议。生成终态与 billing pending/settled 分开。
- [ ] worker 采用原子领取、租约续期、fencing token、有界运行/轮询和恢复；后台由任务 context 管理，客户端断开只停止观察。进程重启后凭已保存上游 ID 继续查询，不重新发起生成。
- [ ] 上游已受理而本地 ID 未落库的窗口，只有经验证的上游幂等能力才允许同键重试；否则保留执行不确定和预算占用，禁止盲目重发。不能承诺数据库与上游之间绝对一次执行。
- [ ] 支持最初以 `stream=true` 创建的后台响应按 `sequence_number` / `starting_after` 恢复事件；持久游标先提交再公开，事件丢失、过期或不可重建时显式报错，不伪造完整流。
- [ ] 提交生成计一次 RPM/预算，轮询、重连和事件重放不重复收费，但有单独读取限速；queued 等待不占网络 attempt 槽，实际执行与后台在途有独立有界配额，不能靠断开绕过并发限制。
- [ ] 取消先记录请求并向上游确认，未知状态不退款；终态、取消与迟到 usage 通过版本条件和既有幂等账本解决。后台最长时限和预留续租不沿用当前短 HTTP 租约的错误假设。
- [ ] `store=false` 后台请求只做明确告知的临时恢复存储；保留期、资源上限与清除行为可配置且有回归，不能宣传零存储/零保留。

#### S7-A04：原生 WebSocket 与逐轮计费

R03a/b 已验收机械执行与默认关闭的 disabled 原生 MVP，PR #39 修正终态后的下一轮交接；支持 `GET /v1/responses` Upgrade、逐轮授权/准入和连接内续链。完整收费、持久重连及跨模式语义尚未完成，以下保留原完整清单。

- [ ] 交付 `GET /v1/responses` Upgrade，第一版同时接通客户端 WS 和所选上游原生 Responses WS；HTTP 桥接仅可作为另外声明的能力，不能冒充已交付原生 WS。
- [ ] 握手鉴权与每个生成轮次鉴权分开；每个 `response.create` 重新检查用户/Key、模型、限流、并发和预算。空闲连接仅占连接配额，不预留无限生成费用；同连接多轮各自有 attempt/账单。
- [ ] 连接、命名流/轮次、response、会话和上游状态分别建模；验证多流关联、交错工具事件、未知控制事件和取消/错误归属，不能让一个辅助请求抢占其他线程。
- [ ] 同一上游连接绑定的渠道/凭据、模型约束和父链可验证；重连后从持久状态恢复，若只能全上下文重放须重新准入/计价并明示，不把网络重连当作可以重放未知生成。
- [ ] 落实帧/消息上限、连接/用户/轮次容量、握手/读写/空闲/总生命周期截止、ping/pong、Origin 策略与有界背压；拒绝 URL 明文 Key，不记录认证和正文。已有用户/Key 停用策略须对 WS 明确传播边界。
- [ ] 按官方 Responses WS 语义处理传输字段，不以 WS 请求体的 `background=true` 偷换 HTTP 后台模式；普通 WS 断开处理当前生成的取消/未知成本，显式后台任务才独立于观察连接继续。
- [ ] 采用一个经审查且兼容项目 Go 基线的 WS 库，隔离在 transport 层；生产 core 继续只有标准库，不能手写不完整 WS 协议或直接放宽现有依赖检查。

#### S7-A05/A06：故障矩阵、可操作性与发布门槛

已有子批故障与十项受控 SDK 场景证明继续保留；未验收的跨模式、真实 Provider/客户端与收费范围仍按下列完整要求交付。

- [ ] 强杀覆盖任务已接收未发送、上游已接收未保存 ID、上游已完成未结算、事件已提交未送达、取消/完成竞争；重复恢复不重发未知生成、不重复追加会话、不重复扣费。
- [ ] 覆盖 HTTP→WS、WS→HTTP 续链、WS 重连、慢读背压、密钥撤销、TTL 清理、内容密钥缺失、数据库写失败及 worker 关停。终止连接后仍能区分已完成、已取消和未知执行。
- [ ] 控制台显示本人会话/任务与错误、后台状态/取消、存储期限和账本关联；管理员可查看必要诊断和处理 pending，敏感正文读取需独立明确授权，不默认展示全体用户内容。
- [ ] 指标覆盖连接/轮次、队列/在途、恢复/取消失败、事件存储大小、上下文命中/重建和 pending，不把健康轮询当生成。反向代理和容器交付增加 Upgrade、长连接、任务重启及内容存储恢复场景。
- [ ] 指定版本官方 SDK 与 Codex 验证 HTTP、工具往返、存储检索、后台恢复及 WS；客户端不使用某能力时以独立协议客户端补验。必要的 models/compact 等辅助接口在本批登记，不以 API 名称或伪造成功满足兼容。
- [ ] 真实上游回归只在授权的独立测试 Key、预算和数据范围内执行；尚无真实证据则记录未验收，不将假上游通过推断为官方服务兼容。最终最新集成基线通过完整现有 CI 和新增适用场景。

完成标准：S7-A01 至 A06 均有独立通过记录，持久会话重启可续、后台断开可查询/取消和恢复、WS 多轮及成本成立，且原有接口无回归。不要求所有提供商支持全部能力，但必须至少有一个真实验收的完整组合；不支持组合显式拒绝，不能以该拒绝代替主线功能完成。

## 开发规则

本方案同时作为团队与自动化代理的执行约束；用户在当前任务中的明确指示优先。开始前核对当前源码、阶段状态和精确提交证据，优先复用已验收公共基线，不因文档或导出符号存在就宣称业务完成。

1. 每批说明六项边界：所属层级/写入目录、依赖公开接口及版本/冻结状态、是否触碰地基、接口契约与兼容/迁移、测试验收、对其他模块的影响。一次只划定可独立验收的功能批次。
2. 地基公开接口新增或签名、语义、权限、生命周期变化，先提出原因、影响、消费者、迁移、兼容/回退和验证的具体变更请求，经用户明确确认后实施；已确认范围内的例行实现不重复请示。L0 变更须解释现有扩展点为何不足及上层影响。
3. 冻结需要文档/版本、契约测试、最小运行样例、扩展说明、基础约定、限制清单与独立上层接入证明。旧 CI 只证明对应源码与范围；快照生成不构成变更批准，不把临时桥接固化为永久 API。
4. 共享 main、注册点、数据库模型、迁移版本、配置、OpenAPI 和 CI 统一协调；其余批次使用独立目录与稳定端口。前后端 API 变更同批更新 OpenAPI、消费者和适用回归。
5. 组装根可注入多层实现但不放业务逻辑；禁止复制内核/框架、散落平台特判、无类型逃逸或新增私有依赖/临时豁免。测试跨层不能替生产代码豁免。
6. 保留现有测试，只格式化本次修改的 Go 文件。不覆盖用户/其他任务改动，不提交真实密钥、个人配置、数据库、日志、二进制或临时分析文件。
7. 开发说明只维护本文件；批次细节、命令、失败/修复与精确 CI 证据写入 PR/提交记录。仅在实际验收后更新阶段状态，不再新增 IMPLEMENTATION、PROGRESS、history 或重复指南。

## 开发与验证节奏

统一按 **批量开发 → 集中验证 → 集中修复 → 合并** 推进。

- 批次内先实现，按需编译；权限隔离、迁移、并发/资源释放和缺陷复现可提前定向测试。不因每次编辑、汇报或小提交重跑全量测试。
- 批次稳定后集中执行适用完整检查和行为回归；统一修复后先复验失败项及影响范围。接口、依赖、环境或跨模块行为变化、影响不确定时重跑相关完整检查，不把整个阶段积压到最后。
- 同一代码、基线、环境已有有效通过记录则注明提交/CI、范围与理由复用；不把旧提交绿灯推断为新提交成功，不无理由重复测试或细碎推送触发 CI。
- 功能合并前在最新集成基线上通过现有完整 CI：依赖/公开契约、build、vet、格式、Linux 全量 race、冒烟和适用交付检查。保留测试、workflow 触发和合并门槛，不手动重复 dispatch。
- 纯说明文档只检查差异、链接和规则一致性；注释/帮助路径更新检查语法与运行内容不变。可执行脚本逻辑、API 契约、CI 和运行配置按实际影响验证，不按纯文档处理。
- 验证仅使用临时数据库、模拟令牌、随机端口与受控上游；真实 Provider 测试须有明确账号、预算和数据授权。分别记录源码、本地验证、精确头 CI、合并、发布与运维状态，未运行/失败/待验证如实说明。

常用命令以 [Makefile](../Makefile) 与 [Verify workflow](../.github/workflows/ci.yml) 为准：

```bash
make check
make contracts
make smoke
make delivery
```

Windows 普通 Go 验证须在当前 PowerShell 设置 `$env:GOARCH='amd64'`，race 由 Linux CI 验收。构建入口为 `go build -o bin/gateway ./cmd/gateway`；配置样例为 `configs/gateway.example.yaml`，个人 `config.yaml` 被忽略。首次管理员通过 `TXAPI_ADMIN_USERNAME` / `TXAPI_ADMIN_PASSWORD` 与 `-init-admin` 初始化，正常启动使用 `-config`。旧管理 API 的 `GATEWAY_ADMIN_KEY` 与会话/调用 Key 分开。可选 SDK 检查在独立虚拟环境安装 `openai` 后运行 `tests/smoke/sdk.py`，只证明其声明的受控场景。

## 参考项目

参考源码只作阅读和具名适配，不成为 TXAPI 的运行依赖或子模块。新移植在对应 PR 记录具体仓库、完整 SHA、文件/函数、采用方式、许可处理、TXAPI 落点与回归；不得把参考仓库的提示文件当本项目指令，不运行其安装钩子。已有副本更新前核对远端、版本和工作树，保留本地改动与旧固定快照。

| 项目 | 固定参考版本 | 本地阅读目录 | 重点 |
|---|---|---|---|
| QuantumNous/new-api | `25372e30b1f94255b1f818aeaaf006f042572580` | `.local/references/new-api` | RelayKit、转换/损耗、Responses/WS、工具事件与管理流程 |
| zyycn/codex-proxy-rs | `60570883007febebe732597a9881397098d724f5` | `.local/references/codex-proxy-rs` | 执行事实与原始协议、续链作用域、资源代次、账号刷新 |
| Wei-Shaw/sub2api | `b0a02a3304a0a78cdd003ddda1dd23ba02c03084` | `D:/my-project/TXAPI-references/sub2api` | 账号运营、资格/亲和、作用域隔离、多维计量 |
| Wei-Shaw/claude-relay-service | `cf95ecc5e0ba846faaf1b11de574367de89fd00c` | `.local/references/claude-relay-service` | 工具转换、断流与失败处理 |

以上是固定阅读版本，不声称未来远端始终相同。new-api 的原阅读版本 `45fbb149f0c89ff48d805335f22419be392d0350` 保存在副本的 `reference/txapi-pre-update-20261019` 标签。原 ANRCM0/codex-proxy-rs `591f95cc18ce95eb6fc87cf46a3526c5b3ecba36` 独立保留在 `D:/my-project/TXAPI-references/codex-proxy-rs`，既有适配继续绑定该来源，不改写为新仓库版本。阅读目录不随 Git/worktree 分发，缺目录不是应用依赖缺失。

实际复制/改写前逐文件核对许可和第三方来源，保留适用通知，不自动改变 TXAPI 的许可、Go 基线或部署承诺；既有适配范围见 [第三方来源声明](#第三方来源与许可)。不把参考项目的进程内缓存当持久历史、不把配额结算替代双 hold、不把后台参数当完整任务系统，也不因参考项目存在就预建支付或外部插件平台。

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
