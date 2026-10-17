# 当前开发计划：极简内核与统一兼容框架

核对日期：2026-10-17。本文是唯一活动开发计划；产品边界和最终分层见 [ARCHITECTURE.md](ARCHITECTURE.md)，操作命令见 [DEVELOPMENT.md](DEVELOPMENT.md)，团队接线规则见 [COLLABORATION.md](COLLABORATION.md)。历史 P 系列方案不再用于排期，S0–S5 的原始提交/CI/验收证据已移至 [历史验收记录](history/acceptance-s0-s5.md)。

## 当前状态

本次依据本地代码、路由契约与已有验收记录整理，不把规划、接口存在或旧 CI 通过当成新功能完成。本架构批次已于 2026-10-17 重新 fetch，`origin/master` 仍为 `90df76b0340e4cdfa6c332c781f7aa772a04ed07` 且为当前分支祖先；合并前重新确认。远端 CI 按最终提交记录，不复用旧提交绿灯。

| 核对项 | 当前结论 |
|---|---|
| 当前代码 | `feat/s5-s6-mainline`，本批基于已推送 `66afe6b` 推进架构优先的输出边界；既有输入/费率门槛、S6 与独立 Responses 切片仍在分支，尚未合并 master |
| 本地集成基线 | `master/90df76b`，S0–S4 与 F0 已验收合并；开工/合并前重新确认最新基线 |
| 产品形态 | Go/Gin 模块化单体、单二进制、单实例 SQLite；原生 ES Modules 控制台内嵌 |
| 现有推理入口 | `/v1/models`、`/v1/chat/completions`（JSON/SSE）、`/v1/embeddings` |
| HTTP 契约 | [OpenAPI](openapi.json) `0.2.3`，79 个操作；不是 79 种推理能力 |
| S0–S4 / F0 | 正确性、极简 core、身份隔离、调度准入、控制台与配置交付已有阶段验收记录 |
| S5 | 6 项中 5 项通过；当前费率/凭据批次已通过本地构建/vet/全量测试与进程冒烟，远端 CI 随提交记录。已将标准费率与 USD/凭据适用范围纳入准入，但逐账户最高适用价、失败收费及未来价格变更假设仍在，S5-03 与完整阶段未验收/合并 |
| 统一兼容框架 | Chat/embeddings 已迁入非 Gin 协调函数；本批通过 `core.Output` 抽离输出，HTTP 由适配器消费，操作/能力进入路由。正文仍是 JSON/SSE 字节；尚非完整 WS/worker 框架，完整 S6 未验收 |
| S6 / S7-A | S6 本轮切片已提交并通过完整 CI，尚未阶段验收合并；S7-A 仅独立 Responses 原生请求/JSON/SSE 协议与 core 样本，没有生产 Responses、持久会话、后台任务或 WS 入口 |
| 安全与部署限制 | 上游 API Key 当前明文落库，响应掩码不是静态加密；客户端调用 Key 已摘要存储。没有实际生产升级或真实全客户端兼容验收承诺 |

不按代码行数或阶段数换算整体百分比。S5 的 5/6 只表示该阶段清单状态，不表示项目完成 83%，也不表示已经满足可靠对外收费的全部门槛。

## 开发设想与偏差纠正

目标是 **极简执行内核 → 统一兼容与协调框架 → 按需接入的功能模块**。不是以 Responses 为中心，也不是先复制三个参考项目的功能总和。core 边界目前仍成立；偏差在于文档将已有接线基线与完整框架混用，以及把共用执行入口推迟到 Responses 批次。

最初整理纠正文档和开发顺序；本轮已推进协调/能力/工具及独立协议切片，具体结果见页末：S6 先补齐真实共用入口、操作模型与能力边界；S7-A 以这些契约接入状态型功能。动态插件系统、独立服务、通用 Agent 运行时和第二套鉴权/账本均不在目标中。当前代码缺口不会因本次整理自动消失。

## 架构优先执行顺序（2026-10-17）

用户已明确调整为：**先完成底层架构并验收为共同基线，再补全功能。** 本节覆盖本文旧批次记录中的“先完整 S5、再 S6”排期；保留旧记录的事实与验收结果，不把它们继续当作当前顺序。S5-03 仍未完成，但厂商逐账户价格/失败收费等外部证明不再阻塞与其无关的架构建设；strict 的既有检查和拒绝范围保留，新能力未经计量验收不开放 strict。

底层不等于 core，责任范围如下。表内“已有”只指实际实现，不表示全部架构已验收。

| 底层组成 | 当前基础 | 收口目标 |
|---|---|---|
| 模块边界与组装 | 依赖检查、实例注入、单体组装已有 | 新模块遵守单向依赖，资源启动/关闭责任清晰 |
| 极简 core | 标准库单次 HTTP 执行已有 | 执行/提交事实稳定，不接用户、数据库、会话与计费业务 |
| 统一执行协调 | 现有 HTTP 共享非 Gin 业务函数 | 统一 generation/attempt/收尾；输出与 HTTP writer 分离 |
| 操作与结果模型 | Chat/embeddings、独立 Responses 样本已有 | 通用信封和各操作语义分开，错误、usage、提交与终态明确 |
| 协议和能力矩阵 | 转换、工具、候选过滤已有切片 | 保真/拒绝和操作/协议/传输支持范围进入真实执行 |
| 传输和生命周期 | HTTP/SSE 已有 | 连接、生成、后台任务分别建模；最小真实消费者验证 WS/worker 边界 |
| 身份与授权 | 当前 HTTP 用户/Key/模型隔离已有 | 每次生成/派发重新验证可信身份，撤销和 owner 策略一致 |
| 调度与资源控制 | HTTP 准入、健康、限额和租约已有 | 连接、排队、generation、网络 attempt 分别有界，所有路径释放 |
| 可选模块端口 | billing/观测已有接线 | 按真实消费收口小接口，仍保留 strict 的安全门槛 |
| 持久化和恢复 | SQLite/统一迁移、财务恢复已有 | 状态幂等、事务/版本、任务领取、事件游标和未知执行恢复有实证 |
| 运行与安全 | 配置快照、日志队列、HTTP 关闭已有 | 密钥保护、正文安全及 WS/worker 关闭和容量边界按批交付 |
| 验收与协作基线 | CI/契约/依赖回归已有 | 最终架构在最新集成基线上通过完整 CI 并合并，再开放功能扩展 |

| 顺序 | 可独立评审的架构批次 | 验收结果 |
|---|---|---|
| A1（当前） | 输出端口与现有 HTTP 消费者：core/gateway/HTTP 适配及风险回归 | JSON/SSE 和拒绝行为保持；独立输出不伪装 HTTP writer；提交/取消/短写/租约事实正确 |
| A2 | 操作模型、能力矩阵、可选模块消费侧小接口 | Chat/embeddings 真正复用；不同操作不经 Chat 有损中转；strict 许可独立判断 |
| A3 | 传输、状态和运行基础的可验收切片 | 以最小 WS/worker/持久消费者验证寿命、授权、容量、恢复和安全，不预建通用任务平台 |
| A4 | 故障矩阵、最终 CI 和共同架构基线集成 | 回归及依赖门槛通过，记录稳定端口、支持范围和未交付功能，再合并共同基线 |

A3 按真实需求逐步增加执行/持久端口；最小消费者是架构验收证据，不等于完成 S7 的会话管理、后台 API、WS 多轮或客户端产品闭环。不得只建空包/接口就勾选架构完成。新增迁移、WS 依赖和 API 时另划批次，同步契约与消费者。本批仅 A1，不修改数据库模型、迁移、HTTP DTO、OpenAPI 操作、依赖或 CI。

## S5 剩余验收

原始批次与完整验证证据见 [S5 历史记录](history/acceptance-s0-s5.md#s5-可选预算与结算)。以下阶段清单原样保留，未新增完成勾选：

- [x] S5-01 明确“不计费”和“严格预算”运行模式；严格模式缺少必需组件则拒绝新调用。
- [x] S5-02 价格采用整数/定点，保存价格版本；无价格默认拒绝严格计费请求。
- [ ] S5-03 用户与 key 双层预算原子预留；最大允许请求费用有上界。
- [x] S5-04 实际用量幂等结算、返还余量；usage 缺失进入估算或待对账状态，不默认免费。
- [x] S5-05 预留有租约/在途确认，崩溃恢复不重复扣费；客户端取消不等于无成本。
- [x] S5-06 每次尝试成本与最终用户收费分开；账本不依赖可丢弃日志队列。

S5-03 已实现双层事务与收取费用上限，但尚不据此宣称允许请求的厂商成本已严格有界。S5-05 的强杀矩阵、可操作对账、本地与最终 Linux race/完整 CI 均已通过，因此勾选该项；人工封顶或豁免不替代 S5-03，完整阶段仍未完成。

验收：并发请求不能突破定义的预算上界；重复结算不重复扣费；进程崩溃/取消/未知执行结果有可追踪恢复路径。预留与实际价格维度覆盖输入、输出及启用的其他计价项，不能只预留 max_tokens 的输出费用。

## 后续开发路线

制定日期：2026-10-16；2026-10-17 更新实际进度。当前功能分支 `feat/s5-s6-mainline`，本批基于 `66afe6b`，最近已核对的集成基线为 `origin/master/90df76b`；合并前重新确认最新集成状态。S0–S4 与 F0 已验收合并，S5 功能仍在分支，S5-03 未完成。下列清单为阶段验收目标；本轮实现与验证进展见页末，未勾选项不能据此视为已通过；执行顺序以“架构优先执行顺序”节为准。

本轮范围已由用户明确：**完成 S5、S6 和包含持久会话/后台任务/WebSocket 的 S7-A，不新增支付。** 三个参考项目已克隆到被 Git/Docker 忽略的 `.local/references/`，固定版本与代码阅读入口见[上游参考快照](reference/upstream-snapshots.md)。本方案同时纠正“先堆 Responses 功能、后补共用框架”的顺序：统一协调入口前移到 S6，S7-A 是框架的首组完整状态型功能，不是框架中心。不自动启动 S7-B、S8 或 S9。

### 目标与不变边界

- 默认产品路线是轻量、可自托管、多用户的通用 AI 网关；先把已有转发、预算和管理闭环做扎实，再扩充客户端与上游能力，不追求一次追平参考项目的全部功能。
- 保持 Go/Gin 模块化单体、原生 ES Modules 控制台、单二进制和默认单实例 SQLite。不因为参考项目使用其他前端、ORM、Redis 或外部数据库就整体替换框架。
- 保留 admin/user 两种身份，不引入组织、多租户和多级分销。订阅账号池与用户购买的订阅套餐是不同功能，不能共用一个含混的“订阅”状态机。
- 每项能力都区分协议可转发、客户端实测兼容、strict 可计价三个状态；支持上游协议不等于已提供原生入站，更不等于所有客户端功能都可用。
- S7-A 从标准 API Key 和一个经验证的原生 Responses 上游开始，必须真实支持状态、后台和 WS；不以“字段透传”“内存亲和”或自动改走同步 HTTP 代替这些能力。支付、订阅 OAuth 账号池、语音 Realtime、媒体生成及任意代码工具执行不在本轮范围。
- 先设计行为与数据边界，再以真实功能批次扩展现有接口；不预建空包，不复制鉴权、请求协调、迁移 runner、租约释放或第二套财务账本。

### 顺序与里程碑

| 优先级 | 批次 | 可独立交付的结果 | 依赖与排除项 |
|---|---|---|---|
| 后续计费收口 | S5-C 成本上界收口 | 保留未完成验收；不阻塞无关架构建设，strict 开放仍须有成本依据 | 不新增支付，不重做已验收对账；不扩大旧库报错/回滚专项，保留既有回归 |
| P0，架构优先 | S6-A 统一兼容与协调框架 | 类型化执行入口由现有 HTTP 真正消费，操作模型与能力进入路由，生命周期统一 | 保持 core 极简，不创建空接口或动态插件平台 |
| P1 | S6-B function tools 闭环 | 非流式、流式工具调用及工具结果可往返 | 不包含付费托管工具；strict 另过成本与计量验收 |
| P1，首个框架扩展 | S7-A01 Responses HTTP/SSE | 独立操作模型接入 S6 共用入口、JSON/SSE、幂等请求标识 | 不再为 Responses 重造一条协调链；保留 Chat/embeddings |
| P1，主线 | S7-A02 持久会话 | 持久 response/parent/conversation/items、owner 隔离及重启续链 | 不依赖内存亲和；上游状态与本地状态不重复追加 |
| P1，主线 | S7-A03 后台任务 | 原生后台提交、持久任务、查询/取消、事件游标及恢复 | 不能只起 goroutine；不在未知执行后盲目重发 |
| P1，主线 | S7-A04 WebSocket | 客户端及原生上游 WS、逐轮授权/预算、续链、控制和关闭 | 连接不是计费请求；WS 不伪装成后台执行 |
| P1，主线 | S7-A05 跨模式可靠性 | HTTP/WS 状态衔接、强杀恢复、取消与结算竞争 | 有界队列/存储，普通日志不承担恢复 |
| P1，主线 | S7-A06 客户端与交付验收 | 指定版本 SDK/Codex、控制台、代理与容器演练 | 不依赖 S7-B/S8/S9，不只验证接口名称 |
| 候选，暂不启动 | S7-B Messages / S7-C Gemini 入口 | 原生入口、对应工具与客户端 | 本轮仅记录后续方向，另行划定批次 |
| P2，可选 | S8-A / S8-B 账号池 | 一个提供商的完整账号生命周期，再扩第二个 | 需要确定提供商、合法授权方式与可维护接口，不是标准 API Key 路线的前置 |
| P2，可选 | S9-A / S9-B 用户运营 | 先个人账单与可选注册，再接一种支付渠道 | 支付必须在 S5 验收后；不依赖启用 S8 |
| 暂不排期 | 媒体与规模化候选 | 按真实需求分别立项 | 不阻塞前述单实例文本网关交付 |

本轮发布点按最新架构优先顺序执行：MA = A1–A4 底层架构验收合并；M0 = 保留 S5 成本验收，作为对应 strict 能力开放门槛；M1 = S6 统一框架、能力与工具验收并合并共同基线（框架部分纳入 MA）；M2 = S7-A01/A02 HTTP/SSE 与持久会话；M3 = S7-A03 后台任务；M4 = S7-A04/A05/A06 的 WS、跨模式可靠性与最终交付。M2/M3 可以分批验收，但只有 M4 完成才称本轮 S5–S7-A 主线完成；支付不在任何本轮里程碑中。按批次验收推进，不以固定日期或文档勾选代替功能验收。

S6 的独立协议实现与样本工作可以从最新已合并基线先行；架构共享接口按 A1–A4 批次推进；新功能生产接线依赖已验收架构基线，strict 能力开放另须完成其适用 S5 验收。本文描述可并行分工，不代表已经分配人员、创建任务或启动代理。

### S5-C：完成完整成本边界

负责范围：`internal/billing`、`internal/gateway`、相关 `internal/store`，必要的协议计量元数据；模型、迁移、入口、配置、OpenAPI 与页面变更由集成负责人协调。复用现有价格版本、双预算事务、attempt 事实、pending 和管理员审计。

首批切片：严格请求 JSON/文本输入校验，保留显式输出限制，并在真实协议转换后、上游执行前验证未遗失/改变该限制；拒绝不支持的跨协议 completion 限制与 embeddings→chat 路径。负责 billing/request、protocol/limits、gateway 调用方与对应回归，同步现有 OpenAPI 说明，不新增迁移/配置/依赖。此切片不证明输入 Token 或厂商总成本有界，不预先勾选下列完整任务。

第二切片：启动时编译独立上游价格/整次输入包络契约，精确匹配实际目标/操作/输出字段；每次执行前原子占用累计重试包络并写入不可变成本快照。负责 billing/provider、gateway 调用方、config/main、model/store 迁移 v7、现有管理员账本/OpenAPI 0.2.3/页面及对应回归。没有内置真实厂商契约或 tokenizer，不将运营填写的 evidence 当作验收证明；C01/C04/C05 的实现进展不等于 C02/C03/C06 或 S5-03 完成，全部完整任务暂保持未勾选。

第三切片：每次 attempt 前，以已固定售价和目标分组倍率计算声明输入/输出边界的收费上界，并与**持久 reservation** 的用户收费预留比较；超出/溢出不发送，重试逐目标检查。既有成本快照内部 format=2 保存计算依据，旧 format=1 继续可读；不新增 schema/HTTP 字段。具体输入与其他计价维度的真实上游证明仍未收口，不能据此勾选 S5-03。

- [ ] S5-C01 明确成本契约：分别定义用户收费上限、上游单次尝试成本上界及整次请求全部尝试的成本上界；上游价格与面向用户的售价不得混为同一份保证。写清支持的提供商、模型、操作、计价单位与保证假设。
- [ ] S5-C02 在转发前验证输入与输出边界：采用可验证的保守 Token 上界、受约束的模型上下文/输出限制或可靠的计数方式，覆盖 embeddings 的批量总输入。字符数平均估算不能充当严格证明；无法建立边界的组合在 strict 下拒绝，不自动降级。
- [ ] S5-C03 定义所有准许的收费维度：输入、输出、缓存、推理及服务等级按实际提供商语义处理。不得假定推理 Token 必然包含在某个输出限制内；缓存收益不能降低最坏情况预留。不能可靠界定的维度继续拒绝。
- [ ] S5-C04 为可选渠道、模型映射及有限重试计算整次成本边界，绑定不可变价格/能力快照；每次执行前确认剩余成本额度。执行不确定的 attempt 按保守上界占用，不因无回执就归零，也不能靠切换凭据无限扩大成本。
- [ ] S5-C05 将边界、计算依据和不可证明原因显示在必要的管理接口/页面；缺价、溢出、越界与组件故障在上游执行前拒绝。异常实际用量继续保留 pending 和审计，不把事后封顶当成事前成本控制。
- [ ] S5-C06 验收覆盖边界值、超长/批量输入、隐藏计价项、途中改价、映射/重试、跨 Key 并发及未知执行结果；复用仍适用的崩溃恢复证据，新行为补定向回归并通过最终完整 CI。

为 S7-A 保留真实扩展边界：后续每次生成都重新准入和计费，续链费用包含实际展开的历史/工具定义，不能只计算增量输入；后台任务运行和 WS 多轮不能共用一笔无限期预留。S5 本批先完成已支持操作的上界，S7-A 新操作使用同一引擎独立扩展并验收，不能把 S5 绿灯直接套给新操作。

完成标准：每个 strict 准许组合都能解释预留覆盖范围和上游约束，所有不满足假设的路径保守拒绝；用户双预算和上游多次尝试的边界分别成立。保证基于已验证的上游契约，不承诺远端违约时仍绝对无损。满足后才勾选 S5-03、完成阶段验收并合并；S5 单独完成也不代表支付平台已就绪。

### S6：统一兼容与协调框架收口

这是新增功能接入前的框架批次，不是重写 core。F0 解决了初始团队接线与依赖边界；本轮已抽离非 Gin 业务协调函数，gateway 包仍保留 Gin HTTP 适配，ProtocolAdaptor 仍围绕 ChatRequest，不能据此宣布统一框架已经完成。负责范围为 gateway/protocol/server 的受影响调用方与行为回归；main、公共端口、OpenAPI 和依赖边界由集成负责人协调。

#### S6-A：真实共用入口与操作能力

- [ ] 从现有 gateway HTTP 协调中提取不依赖 Gin 的类型化执行入口，先让现有 Chat/embeddings 生产入口真正使用；保留外部行为与同协议快速路径。不通过伪造 Gin context 接 worker/WS，也不为未来消费者堆空接口。
- [ ] 区分通用执行信封与操作载荷：可信身份/作用域、模型与能力、截止时间/取消、结果/提交事实属于共用契约；Chat、embeddings、Responses 各有能保持语义的模型，不把所有功能塞入 ChatRequest。
- [ ] 固定授权、请求级准入、目标选择、attempt、预算、重试判断与统一收尾的唯一生命周期；明确资源所有权、失败/取消/panic 的释放责任，以及协议终态、usage 完整性和财务终态的区别。观察组件不成为财务或恢复真相源。
- [ ] 为实际的操作/源协议/目标协议声明支持范围，首批覆盖现有文本、function tools、流事件和 usage；能力过滤进入真实路由并返回脱敏诊断，不能替代用户授权。声明 HTTP、后台、WS 等不同传输能力，不把“能建连”等同于能执行任意操作。
- [ ] 明确同协议透传与跨协议转换策略：关键语义不能静默丢失，无法表示则在提交前拒绝。转换不得擅自提高输出限制或改变收费服务等级；必要变化重新检查能力和成本。
- [ ] 通过已有消费者的契约/行为回归证明入口收敛：权限、双预算、重试与提交事实、每流隔离、取消/租约无回归；保留 core 标准库、protocol 无业务依赖、gateway 不依赖 server/store 的门槛。只有真实出现新的传输/持久需求时才增加最小消费侧端口。

#### S6-B：function tools 与支持矩阵

- [ ] 实现工具定义、调用 ID、名称、增量 JSON 参数、多个调用及工具结果回传；保持顺序、关联和结束原因。工具由客户端执行，不让网关执行任意工具代码或建立通用 Agent 运行时。
- [ ] 覆盖分片/UTF-8/交错事件、未知 usage、上游错误、客户端取消及流已提交后的失败；同一外部 Request ID 并发不共享状态，不自动补成功结束事件。
- [ ] 分别登记 disabled/legacy/strict 的支持矩阵；strict 只允许已证明成本与计量的子集。托管搜索、图片、代码执行等额外收费工具默认不开放，不能用文本价格替代。

完成标准：现有 HTTP 消费者已经迁入共用入口、工具往返与能力拒绝有实际回归，并在最新集成基线上通过完整现有 CI 后合并。**框架验收合并后**再让 S7-A 的 HTTP/worker/WS 依赖该基线；框架门槛不等于已经交付这些新消费者；此 S6 HTTP/协议范围只是底层架构的一部分，不自动关闭 A3 跨模式基础或 A4 最终架构验收。可先做不改共享入口的独立协议样本工作，不自动启动团队或代理。

### S7-A：Responses 持久执行主线

本阶段承诺交付持久会话、后台任务和 WebSocket，不再作为可选排除项；首个组合为标准 API Key + 原生 Responses 上游，提供商必须同时有可验证的相关接口。此组合验收后再考虑跨协议桥接，不能要求 OAuth 账号池先完成才能交付。

技术边界、建议数据模型及恢复语义见 [ARCHITECTURE.md](ARCHITECTURE.md#12-首组状态型扩展responses-持久执行)。下列是待开发的批次，不在本次预建包、迁移或接口。

| 批次 | 负责目录与接口依赖 | 独立验收结果 |
|---|---|---|
| S7-A01 | protocol 的 Responses 操作、server 独立 HTTP 入口，消费已验收的 gateway 执行入口；集成 main/OpenAPI | JSON/SSE、工具、usage 和错误正确，现有 HTTP 消费者复用同一协调链路 |
| S7-A02 | 实际需要时新增 `internal/responses` 应用服务；store/model、个人/管理 API；集成迁移版本 | response/会话/items 在 SQLite 持久化，查询/删除/续链隔离，重启后历史可用 |
| S7-A03 | responses 任务 worker/恢复、billing/store 原子边界、gateway 上游后台操作 | 断开后继续、轮询/取消、流式游标恢复，重启不重复生成或扣费 |
| S7-A04 | server WS 入口、独立 WS transport、protocol 事件及 gateway attempt 端口 | 双端 WS 可用，逐轮准入/结算、工具续链、多流隔离、心跳/背压/关闭成立 |
| S7-A05 | 上述模块的边界、tests 假上游/强杀矩阵、恢复可见性 | 多种故障窗口下状态、事件、预算和所有权保持一致 |
| S7-A06 | web、tests/browser、tests/smoke、部署说明与契约；CI 改动集成评审 | 控制台可操作，指定客户端真实兼容，完整现有 CI 与适用交付通过 |

#### S7-A01：HTTP/SSE 接入统一框架

- [ ] Responses 使用独立请求、item 和事件模型，覆盖文本、function tools、instructions、输出限制、usage、完成/失败/incomplete；不经 ChatRequest 有损中转。
- [ ] 复用 S6 已验收的类型化生成入口接入 Responses HTTP；新增操作能力与 usage 映射，不旁路授权、准入、attempt、预留/结算或统一收尾。为后续持久/后台消费者评审最小原子端口，真实事务调用与故障回归在 S7-A03 交付。
- [ ] 设计内部生成 ID、外部 response ID、上游 response ID 与用户幂等键的边界；幂等键以可信用户/Key 作用域和规范请求摘要唯一约束，同键异参冲突，重试不新建账单。
- [ ] 完成 HTTP JSON/SSE 和现有 Chat/embeddings 回归；strict 仅开放已完成计量与成本证明的 Responses 文本/函数工具子集，其余模式不自动回退为 strict。

#### S7-A02：持久会话、检索与续链

- [ ] 实现 stored Responses 的获取/删除/input items，以及 Conversations 与 items 的创建、读取、修改/删除和分页；方法、DTO 和状态码以实施时核对的官方 schema 为准，同批更新 OpenAPI/消费者。
- [ ] `previous_response_id` 与 `conversation` 各自明确上下文来源与互斥校验；保存父子关系、实际输入/输出、模型/渠道/凭据绑定和价格版本，不把缓存命中当成历史持久化。
- [ ] 第一版以本地持久历史为权威，并为原生上游建立明确映射；一次执行只能选择一种上下文来源。使用原生父 ID 时不重复追加完整本地历史；只有完整可重建、已重新计价的上下文才允许显式重放，不能静默丢弃加密推理或工具项。
- [ ] 所有操作默认校验 user_id 与创建 Key 绑定，并重新检查当前 Key 有效性和模型范围；跨用户、无授权 Key 统一不可枚举。Key 更换/转交不能自动继承数据，若确需支持另行提供显式授权与审计。
- [ ] 保存内容须加密，明确主密钥、保留期、用户/实例存储限额、删除/过期与清理规则；`store=false` 不创建可长期检索的历史，也不能悄悄升级为持久保存。仅保留必需的幂等/计费元数据，后台临时恢复内容另按声明的短期策略处理。
- [ ] 验证重启后继续多轮/工具链、父子分支隔离、并发追加版本冲突、删除/过期、权限缩减和上游父 ID 失效；不能让普通请求重试切换到不认识父 ID 的账号。

#### S7-A03：持久后台任务与事件恢复

- [ ] 支持 HTTP `background=true` 创建、获取、取消；先接上游原生后台能力。持久队列、任务事实和用户/Key 预留成功后才确认接收，不能只返回 202 或启动 goroutine 就称支持。
- [ ] 任务内部状态含 queued、dispatching、in_progress、cancel_requested、终态及执行不确定标记；公开 Response 状态按协议映射，不把内部状态原样塞入协议。生成终态与 billing pending/settled 分开。
- [ ] worker 采用原子领取、租约续期、fencing token、有界运行/轮询和恢复；后台由任务 context 管理，客户端断开只停止观察。进程重启后凭已保存上游 ID 继续查询，不重新发起生成。
- [ ] 上游已受理而本地 ID 未落库的窗口，只有经验证的上游幂等能力才允许同键重试；否则保留执行不确定和预算占用，禁止盲目重发。不能承诺数据库与上游之间绝对一次执行。
- [ ] 支持最初以 `stream=true` 创建的后台响应按 `sequence_number` / `starting_after` 恢复事件；持久游标先提交再公开，事件丢失、过期或不可重建时显式报错，不伪造完整流。
- [ ] 提交生成计一次 RPM/预算，轮询、重连和事件重放不重复收费，但有单独读取限速；queued 等待不占网络 attempt 槽，实际执行与后台在途有独立有界配额，不能靠断开绕过并发限制。
- [ ] 取消先记录请求并向上游确认，未知状态不退款；终态、取消与迟到 usage 通过版本条件和既有幂等账本解决。后台最长时限和预留续租不沿用当前短 HTTP 租约的错误假设。
- [ ] `store=false` 后台请求只做明确告知的临时恢复存储；保留期、资源上限与清除行为可配置且有回归，不能宣传零存储/零保留。

#### S7-A04：原生 WebSocket 与逐轮计费

- [ ] 交付 `GET /v1/responses` Upgrade，第一版同时接通客户端 WS 和所选上游原生 Responses WS；HTTP 桥接仅可作为另外声明的能力，不能冒充已交付原生 WS。
- [ ] 握手鉴权与每个生成轮次鉴权分开；每个 `response.create` 重新检查用户/Key、模型、限流、并发和预算。空闲连接仅占连接配额，不预留无限生成费用；同连接多轮各自有 attempt/账单。
- [ ] 连接、命名流/轮次、response、会话和上游状态分别建模；验证多流关联、交错工具事件、未知控制事件和取消/错误归属，不能让一个辅助请求抢占其他线程。
- [ ] 同一上游连接绑定的渠道/凭据、模型约束和父链可验证；重连后从持久状态恢复，若只能全上下文重放须重新准入/计价并明示，不把网络重连当作可以重放未知生成。
- [ ] 落实帧/消息上限、连接/用户/轮次容量、握手/读写/空闲/总生命周期截止、ping/pong、Origin 策略与有界背压；拒绝 URL 明文 Key，不记录认证和正文。已有用户/Key 停用策略须对 WS 明确传播边界。
- [ ] 按官方 Responses WS 语义处理传输字段，不以 WS 请求体的 `background=true` 偷换 HTTP 后台模式；普通 WS 断开处理当前生成的取消/未知成本，显式后台任务才独立于观察连接继续。
- [ ] 采用一个经审查且兼容项目 Go 基线的 WS 库，隔离在 transport 层；生产 core 继续只有标准库，不能手写不完整 WS 协议或直接放宽现有依赖检查。

#### S7-A05/A06：故障矩阵、可操作性与发布门槛

- [ ] 强杀覆盖任务已接收未发送、上游已接收未保存 ID、上游已完成未结算、事件已提交未送达、取消/完成竞争；重复恢复不重发未知生成、不重复追加会话、不重复扣费。
- [ ] 覆盖 HTTP→WS、WS→HTTP 续链、WS 重连、慢读背压、密钥撤销、TTL 清理、内容密钥缺失、数据库写失败及 worker 关停。终止连接后仍能区分已完成、已取消和未知执行。
- [ ] 控制台显示本人会话/任务与错误、后台状态/取消、存储期限和账本关联；管理员可查看必要诊断和处理 pending，敏感正文读取需独立明确授权，不默认展示全体用户内容。
- [ ] 指标覆盖连接/轮次、队列/在途、恢复/取消失败、事件存储大小、上下文命中/重建和 pending，不把健康轮询当生成。反向代理和容器交付增加 Upgrade、长连接、任务重启及内容存储恢复场景。
- [ ] 指定版本官方 SDK 与 Codex 验证 HTTP、工具往返、存储检索、后台恢复及 WS；客户端不使用某能力时以独立协议客户端补验。必要的 models/compact 等辅助接口在本批登记，不以 API 名称或伪造成功满足兼容。
- [ ] 真实上游回归只在授权的独立测试 Key、预算和数据范围内执行；尚无真实证据则记录未验收，不将假上游通过推断为官方服务兼容。最终最新集成基线通过完整现有 CI 和新增适用场景。

完成标准：S7-A01 至 A06 均有独立通过记录，持久会话重启可续、后台断开可查询/取消和恢复、WS 多轮及成本成立，且原有接口无回归。不要求所有提供商支持全部能力，但必须至少有一个真实验收的完整组合；不支持组合显式拒绝，不能以该拒绝代替主线功能完成。

### 非主线与暂缓项

以下仅保留方向，不作为本轮实施清单，不预建目录或预占迁移版本：

| 方向 | 重新立项条件 | 本轮边界 |
|---|---|---|
| S7-B / S7-C：Messages、Gemini 原生入站与专项客户端兼容 | 明确需求、独立协议/客户端验收 | 不阻塞 S7-A 的状态、后台与 WS |
| S8：订阅授权与账号池 | 提供商授权条件、凭据生命周期及许可审查明确 | 继续标准 API Key；不移植整套外部数据库架构 |
| S9：用户自助与商业运营 | 权威余额/账单可先单独规划；支付另行获得需求确认 | 本轮不新增支付、套餐、订单或分销体系 |
| 调度深化、媒体与更多上游 | 有真实负载或操作需求，定义计价/资源/安全边界 | 用户跨 Key TPM、等待队列、满渠道重选仍未交付 |
| 多实例、PostgreSQL / Redis | 测得容量/高可用需要，并解决全局预算/限流与状态一致性 | 当前保持单实例 SQLite，不承诺换库即分布式正确 |
| 现有上游凭据加密 | 独立安全加固批次，确认主密钥注入/轮换/备份与迁移 | 当前掩码展示不等于加密落盘；不借文档整理宣布修复 |

运维、指标、脱敏与资源边界随实际功能批次交付。持久内容加密是 S7-A02 的前置验收要求，不能因当前上游凭据尚未加密就沿用明文内容存储。

### 参考项目与借鉴方式

以下为参考方向；本轮已克隆并固定三个项目的提交与实际阅读入口，见[上游参考快照](reference/upstream-snapshots.md)。这不是安全审计或功能正确性背书。实际采用时进一步登记文件/代码范围并核对官方协议；README、历史调研和赞助宣传不能代替兼容性或计费正确性证据。

| TXAPI 功能 | 优先参考 | 阅读入口与借鉴内容 | 不直接照搬 |
|---|---|---|---|
| S6 / S7 协议与工具 | new-api | [RelayKit](https://github.com/QuantumNous/new-api/blob/main/relaykit/README.md)：操作分离、转换路径/损耗诊断、工具关联、逐流状态与 usage | 整体 DTO/转换库依赖、静默损耗策略、全局可变钩子 |
| S7-A WS 与续链 | new-api / sub2api | 快照中的 WS controller/relay、状态存储、线程作用域：逐轮鉴权/计费、连接与轮次分离、父链绑定和断开回归 | 将进程内粘性缓存当持久会话，将 WS 当后台任务；完整来源见快照文件 |
| S7-A 持久状态与后台 | 官方 Responses/Conversations 文档 + TXAPI 既有账本 | 本地持久模型、上游 ID 映射、原生后台查询/取消、事件恢复和预算生命周期 | 未确认参考项目已有完整可移植闭环，不做“直接复制即可完成”的承诺 |
| S8 账号调度 | sub2api | [项目功能](https://github.com/Wei-Shaw/sub2api/blob/main/README_CN.md)：OAuth/API Key 账号、粘性会话、账号并发；实施时继续定位对应 service/handler 与回归 | 整套 PostgreSQL/Redis、Ent、Vue 架构，以及未审查的可用性优先财务策略 |
| S8 生命周期与故障呈现 | claude-relay-service | [README](https://github.com/Wei-Shaw/claude-relay-service/blob/main/README.md)：OAuth 接入、账号冷却覆盖、不可路由原因和管理恢复 | 旧版认证、秘密输出方式及规避上游限制的做法 |
| S9 用户运营 | new-api / sub2api | [new-api 说明](https://github.com/QuantumNous/new-api/blob/main/README.zh_CN.md)、[sub2api 支付说明](https://github.com/Wei-Shaw/sub2api/blob/main/docs/PAYMENT_CN.md)：用户流程、套餐与支付配置；订单事务须进一步核对实现 | 广告中的成本/稳定性承诺、直接认定其收费正确、重新建立第二套账本 |

代码复用采用以下规则，具体批次记录随相应实现提交维护：

1. 开工先检查 TXAPI 已有实现，列出相同功能与缺口；先参考状态机、接口语义、失败案例和操作流程，再决定是否复用代码。纯协议行为以对应版本官方文档和独立样本为准。
2. 记录项目 URL、上游提交 SHA、具体文件/代码范围、参考目的、采用方式（设计参考/代码移植/新增依赖）、TXAPI 落点及测试。计划中的 main 链接仅为阅读入口，不能作为最终复用溯源。
3. 当前资料显示：new-api 及 RelayKit 使用 [AGPLv3](https://github.com/QuantumNous/new-api/blob/main/LICENSE)，另有 [NOTICE](https://github.com/QuantumNous/new-api/blob/main/NOTICE)；sub2api 的 [LICENSE](https://github.com/Wei-Shaw/sub2api/blob/main/LICENSE) 为 LGPLv3，README 另有使用/商业运营声明；CRS 的 [LICENSE](https://github.com/Wei-Shaw/claude-relay-service/blob/main/LICENSE) 为 MIT。实际采用前逐文件核对当时许可、第三方来源及适用要求；不得把复制/改写的受许可代码直接标成 TXAPI 的 MIT 原创。
4. 需要复制、改写或链接受许可代码时，先明确发布方式、署名/通知/源码等义务与项目许可策略。若会改变现有许可、Go 版本、依赖或部署承诺，先单独评审并取得维护者确认；本方案不授权自动引入 RelayKit 或整体移植任何项目。测试、样本和前端资产同样检查来源。
5. 直接复用范围核对上游安全修复，并在 TXAPI 中补与实际风险对应的回归。CRS 当前 README 对 v1.1.248 及以下有管理员认证绕过警告，不能选择这些旧版本的管理认证作为模板；这不等于确认新版无漏洞。
6. 只保留符合本项目边界的方案：严格预算/鉴权失败继续拒绝新请求，普通访问日志仍可有界降级；不得借鉴财务 fail-open、响应提交后重试、客户端取消后无限后台执行或重复资源释放。历史[调研笔记](reference/newapi-omniroute-research.md)仅作线索，发生冲突以当前架构、协作规则和验收行为为准。

### 开工与验收清单

每个批次先在任务或 PR 写清负责人、写入目录、依赖接口/基线、支持组合、排除项、参考来源和独立验收场景。协议、API/前端、账号和测试工作可按 [COLLABORATION.md](COLLABORATION.md) 分工；共享模型、迁移序号、OpenAPI、main/路由注册与 CI 由集成负责人协调。不允许多个团队并行修改同一注册点或自行预占已发布迁移版本。

默认执行 [开发与验证节奏](DEVELOPMENT.md#开发与验证节奏)：批量开发 → 集中验证 → 集中修复 → 合并。保留现有测试；权限、迁移、并发/资源释放等关键风险可以先定向验证。稳定后集中执行适用的 `make check`、契约/依赖与行为回归、`make smoke`；只格式化本次 Go 文件，最终最新集成基线通过现有完整 CI，包括 Linux 全量 race 和适用交付检查。不要求每次编辑或汇报重复全量测试，不因集中验证删检查或手动反复触发 CI。

交付记录写明实际命令/结果、验证提交/基线、OS/架构和外部测试范围；复用证据注明原提交或 CI 链接及适用理由。纯文档只检查差异、链接与规则一致性，不把历史 CI 写成本次已通过。更新 README 能力矩阵、OpenAPI/消费者和阶段勾选时，只标记实际达到验收门槛的部分。

## 每个阶段的交付约定

1. 先写清变更影响和可复现问题，再实现最小变更。
2. 按 [开发与验证节奏](DEVELOPMENT.md#开发与验证节奏) 分可独立验收的功能批次推进：批量开发、集中验证、集中修复、再合并，不等整个阶段结束才测试。新行为有与风险对应的回归；批次内按需编译和关键风险定向测试，批次稳定后集中检查，最终完整 CI 通过。同一代码已有有效证据时不无理由重复全量测试或细碎推送。
3. 不提交真实密钥、数据库、编译产物、日志、临时分析或个人配置。
4. 对性能敏感路径附基准对照；纯说明文档修改只检查差异、链接与一致性，不新增单元测试或手动重复全量测试/CI；脚本、契约和运行配置变更按实际影响验证。
5. 数据库变更附迁移和回滚限制；删除实现前确认调用方已迁移。
6. 完成后更新本文件勾选项和 README 能力状态，避免“文档完成等于功能完成”。

下一项工作：按架构优先 A1–A4 收口统一执行、操作/能力和跨模式基础并验收共同基线，再按 S7-A01 至 A06 交付 Responses、持久会话、后台任务和 WebSocket。本轮不新增支付，不自动启动 S7-B/S8/S9。旧无主 Key 仍须显式认领；代码交付不等于已迁移实际运行数据库。

## 前一批文档整理记录

以下保留历史批次的事实、当时排期和验证，不作为最新优先级；当前顺序统一以本文“架构优先执行顺序”节为准。

2026-10-16，Windows/PowerShell，代码基线 `746f673`，本地 master `90df76b`。本批只清理说明文档：入口概览、最终架构、活动计划、协作规则与历史归档；保留现有未提交方案和上游快照。未改生产代码、API 契约、数据库、配置、测试或 CI；未运行 Go 测试、上游程序、真实模型/支付请求或手动触发 CI。历史记录仅用于说明已验收范围，不作为本批新验证。未提交/推送。

实际检查：`git diff --check` 通过；一次性 `python -` 扫描 12 份 Markdown 的 78 个本地链接/锚点，通过；只读核对 OpenAPI `0.2.2` / 79 操作及未新增 Responses 路由，通过；归档与 HEAD 原记录对照，35 条阶段勾选与 13 处 CI 链接保留，S5 仍为 5/6、后续路线无新增完成勾选，通过。另一次 `python -` 核对三个本地参考库的固定 SHA/远端/干净工作树、Git/Docker 忽略范围与 19 个源码路径，通过。`git diff --name-only` 与未跟踪文件列表确认仅为 Markdown 变更；未将历史功能 CI 写成本次文档已通过 CI。

## S5-C 首批工作记录

2026-10-16，代码基线 `feat/s5-pricing/746f673`，本地 master `90df76b`，未提交工作树。环境 Windows/PowerShell，Go 1.26.3（工具为 windows/386，以下 Go 验证均显式 `GOARCH=amd64`）、Node 24.14.1、Python 3.14.3。保留前一批文档改动。

实现范围：billing 严格 JSON/文本输入校验及输出限制元数据，protocol 最终请求限制校验，gateway 在上游调用和持久 attempt 前拒绝不安全转换；新增对应行为回归，同步 README/指南/现有两个推理操作的 OpenAPI 描述。未改 core、数据库模型/迁移、运行配置、依赖或 CI；无第三方实现代码移植。不是完整 S5-C 验收，也未开始 S6。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/protocol ./internal/gateway -run 'TestStrictRequest\|TestOutputLimit\|TestStrictConvertedLimits\|TestStrictMalformedRequests\|TestStrictEmbeddingsCannot\|TestStrictRejectsUnpricedDimensions'` | billing/protocol 通过；gateway 初次因测试误将 Gemini 模型映射视为正文而非 URL 失败，已修正断言，无生产代码补丁掩盖 |
| `go test -count=1 ./internal/gateway -run '^TestStrictConvertedLimitsBeforeUpstreamAndDurableAttempt$'` | 修正后通过；真实 HTTP 假上游验证限制保留/拒绝、模型映射、attempt 事实、原结算和双预算释放 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 最终生产代码上全部通过，包含依赖/契约、现有强杀恢复及权限/协议/存储回归 |
| `node --check` 四个现有控制台/浏览器脚本，`node --test web/assets/api.test.mjs` | 全部通过，6 项前端/API 行为测试通过；未运行真实浏览器或真实上游 |
| `python tests/smoke/local.py --go go` | 通过；临时库/随机端口、原调用/控制台、strict 持久结算，以及真实 gateway SSE 强杀/重启 pending/审计豁免 |
| `git diff --check`、仅本批 8 个 Go 文件 gofmt 检查、`git diff --exit-code -- go.mod go.sum` | 全部通过，没有格式化无关 Go 文件或改变依赖 |
| 一次性 `python -` 文档/契约一致性检查 | 12 份 Markdown / 78 个本地链接和锚点通过；OpenAPI 保持 `0.2.2` / 79 操作，仅两个现有操作描述变化，路由/请求响应结构不变；S5 仍为 5/6，后续清单未新增完成勾选 |

本批未运行 Linux race、Docker/交付演练或远端 CI，未在线刷新集成基线，未提交/推送。合并前仍须在最新集成基线上通过完整现有 CI；不复用旧功能 CI 为本批背书。下一切片为模型/操作的可证明输入与输出计量边界、独立上游价格及不可变成本契约，再接重试总成本和管理展示；此处的输出限制保真不能替代这些工作。

## S5-C 第二批工作记录：独立成本契约与累计尝试包络

2026-10-17，代码基线 `feat/s5-pricing/746f673`、本地 master `90df76b`，保留前批未提交工作。环境 Windows/PowerShell、Go 1.26.3（宿主默认 windows/386，所有本批 Go 验证显式 `GOARCH=amd64`）、Node 24.14.1、Python 3.14.3；未在线刷新集成基线。

实现范围和依赖：billing 编译启动期不可变成本契约、独立精确价格/总输入包络与逐次成本上限；Reserve 固定策略，gateway 经现有协议最终限制检查后调用 `StartCostedAttempt`，同事务占用累计包络并记录 attempt 后才执行现有 core。模型/store 统一追加 v7 字段/版本，config/main 接线，现有管理员账本 DTO/OpenAPI 0.2.3/控制台展示。保持 79 个操作；不改 core、模块依赖规则、第三方依赖、CI、鉴权和唯一收尾流程，无第三方实现代码移植。

本批支持范围：现有受限文本 Chat 三协议/显式限制的已支持转换，以及 OpenAI 兼容文本 embeddings；仅在精确实际目标/操作有明确配置契约时允许执行。输入按整次厂商计费硬上限占用，embeddings 不按数组单项重复/遗漏计量；独立上游价格不替换售价。契约缺失、漂移或预算不足不发送、不降级、不用其他凭据绕过；失败/未知 attempt 不释放累计重试包络。回执用量超契约时保持不可自动结算。此处的配置声明仍需真实依据，非空 evidence 不等于已验证的厂商保证；没有内置生产契约，不宣称已有可靠 tokenizer 或任意厂商兼容。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/gateway ./internal/config -run 'TestProvider\|TestStrictProvider\|TestStrictConverted\|TestS5StrictGateway'` | 首轮定向通过；覆盖精确金额/舍入/溢出、契约缺失/精确匹配、累计预算、并发原子准入、重复/写入失败回滚、输入批次包络、异常用量 pending、不可变快照及售价不变；新增 embeddings HTTP 场景随随后全量验证执行 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 最终生产代码全部通过；包含新账本 DTO/79 路由契约、依赖边界、完整现有回归及强杀矩阵；新增真实 HTTP 假上游验证 embeddings、目标变化及仅允许预算内的网络尝试 |
| `node --check` 四个现有控制台/浏览器脚本，`node --test web/assets/api.test.mjs` | 全部通过，6 项行为测试通过；未运行真实浏览器 |
| `python tests/smoke/local.py --go go` | 通过；原调用/权限/控制台、模式隔离、strict 缺价拒绝和 JSON/SSE 结算正常；真实 gateway SSE 强杀后，重启保留 `5760000` 上游包络及契约快照、用户 hold 转 pending，再经管理员幂等豁免 |
| `git diff --check`、本批 14 个 Go 文件 `gofmt -l`、`git diff --exit-code -- go.mod go.sum` | 通过；未格式化无关 Go 文件，没有新增依赖 |
| 一次性 `python -` Python AST/文档/契约检查 | 冒烟脚本语法通过；10 份变更 Markdown / 75 个本地链接与锚点通过；OpenAPI 0.2.3 / 79 操作；S5 仍为 5/6，完整 S5-C 清单未新增勾选 |

以上为本次未提交工作树证据，不借用早期 CI 为本批背书。未运行 Linux race、Docker/交付演练、真实上游/客户端或远端 CI；未提交/推送，未操作实际运行数据库，也不新增旧库报错/回滚专项。合并仍需最新集成基线完整现有 CI。

下一切片：对准许模型/操作落实可复核的真实输入/总输出及缓存/推理计价依据，并用该边界检查售价侧是否已被用户 hold 事前覆盖；补齐拒绝依据的持久管理可见性和适用故障验收。当前成本契约配置与重试包络是 C01/C04/C05 的部分实现，不代替真实契约证明或 C02/C03/C06，S5-03 未完成，S6 尚未开始。

## S5-C 第三批工作记录：发送前用户收费覆盖

2026-10-17，继续基于 `feat/s5-pricing/746f673` 与本地 master `90df76b` 的未提交工作树；保留前批实现。Windows/PowerShell、Go 1.26.3（默认 windows/386，以下 Go 验证显式 `GOARCH=amd64`）、Node 24.14.1、Python 3.14.3；未在线刷新远端集成基线。

负责目录与依赖：billing 的固定售价计算/成本准入/持久 attempt，gateway 的拒绝映射与既有唯一收尾，相关回归与文档。每个实际目标按声明的完整输入包络、显式输出限制、请求固定售价/倍率核算收费上界；沿用“实际模型售价优先、否则公开模型”的既有结算规则，并使用同一宽整数公式。开始 attempt 的事务同时要求持久 hold 足以覆盖该上界；超出或溢出返回 400 `billing_fee_limit`，无新增网络调用、attempt、上游占用或健康处罚。精确相等允许，先前未知执行仍保留 pending。内部快照 format=2 持久保存售价版本/模型/分组/金额，旧 format=1 回执保持可读。无新 schema、HTTP 字段、路由、第三方依赖、core 或 CI 变更。

同时修正前批配套遗漏：现有 delivery 脚本仍将新增迁移固定为 `[5,6]`，本批同步为 `[5,6,7]`，未删除/缩减旧断言或改 CI 触发；只做版本一致性维护，不另开旧库专项。浏览器账本测试补齐 0.2.3 必填 provider 金额/快照样本及展开断言，避免真实 `ledgerMoney` 对旧样本 undefined 报错；本批未运行真实浏览器。原 provider 预算测试的 1000-token 输入按售价需要 0.1692 覆盖，隔离 fixture 的用户收费上限调整为 0.2，原 provider 预算/累计占用/实际收费断言保留，另加不足与精确边界回归。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/gateway -run 'TestFeeBound\|TestStrictFeeBound\|TestProvider\|TestStrictProvider'` | billing 通过；gateway 首轮仅新用例误断言成功健康凭据必然回写失败。既有健康逻辑对没有状态变化的成功不回写，修正测试断言，未改生产健康逻辑 |
| `go test -count=1 ./internal/gateway -run '^TestStrictFeeBoundBeforeNetworkAndEveryRetry$'` | 修正后通过：精确上限、少一单位、实际模型高价、倍率及重试换目标；本地收费拒绝不反馈健康，先前 503 仍正常反馈 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 最终代码全部通过；包含新精确舍入/零价/int64 宽输入/溢出、售价与倍率固定、历史 hold 不被新全局上限扩大、旧/新快照及未知格式处理，HTTP embeddings 全批输入拒绝、原依赖/契约/强杀与完整回归 |
| `node --check` 四个既有脚本，`node --test web/assets/api.test.mjs` | 全部通过，6 项行为测试通过；`node -e` 检查当前模块路径没有 Playwright，未安装依赖或冒充运行浏览器测试 |
| `python tests/smoke/local.py --go go` | 通过；原 JSON/SSE/模式隔离/控制台、strict 结算正常；真实 gateway SSE 强杀、重启后核对 format=2、`selling_bound_units="96000000"` 和原售价版本，未知执行 pending，幂等豁免后 provider 累计包络仍保留 |
| 一次性 `python -` 静态检查 | 两个 Python 冒烟脚本 AST 通过；delivery 期望新增版本与迁移注册 `[5,6,7]` 一致；10 份变更 Markdown / 75 个本地链接与锚点通过；OpenAPI 保持 0.2.3 / 79 操作，未新增 S5-C 完成勾选 |
| `git diff --check`、本批 8 个 Go 文件 `gofmt -l`、`git diff --exit-code -- go.mod go.sum` | 通过，未格式化无关文件或改变依赖 |

本批未运行 Linux race、Docker/delivery 实跑、真实浏览器、真实上游/客户端或远端 CI；未提交/推送，未操作实际运行库。不得将脚本/样本修正或静态检查写成容器/浏览器验收通过；最终合并仍须在最新集成基线上通过完整现有 CI。

此检查证明的是**声明边界之内**的售价收费覆盖，不证明厂商实际总输入/推理/缓存约束真实成立。下一批仍须收口 C02/C03 的计量与计价依据、持久拒绝原因展示及适用验收；原异常用量 pending/管理员审计保留。S5 保持 5/6，S5-03 未完成，S6/S7-A 尚未开始。

## S5-C 第四批工作记录：持久成本拒绝诊断

2026-10-17，工作区 `C:\Users\mn139\.codex\worktrees\5506\TXAPI`，detached HEAD `746f673b4b85e5054479792ef30510abe6c6a198`，保留前三批未提交工作。负责 billing 成本准入/唯一收尾与对应 gateway 回归，依赖既有 reservation reason 和管理员账本消费者。Windows/PowerShell，Go 验证显式 `GOARCH=amd64`；未刷新远端基线、提交或推送。

本批在成本拒绝返回前持久保存四种固定原因：缺契约、provider 总预算耗尽、契约输出越界、用户收费覆盖不足。无持久 attempt 时零收费释放双预算并保留原因；已有未知执行时仍 pending，原因记录为 `execution_or_usage_unknown:<拒绝码>`。收尾前中断后的既有恢复和重复收尾均保留诊断；不将拒绝计为实际执行，不释放已占用 provider 包络。诊断写入失败拒绝执行并返回组件不可用；客户端取消后只允许有两秒超时的诊断落库。只记录白名单原因，不持久化原始错误、URL、密钥或正文。

复用现有管理员列表/详情的 reason 展示，OpenAPI 0.2.3 仅补现有字段说明，浏览器 fixture/断言同步；无新 HTTP 字段、路由、模型迁移、依赖、core 接口或 CI 变更，无参考项目实现移植。此批是 C05 的部分实现，不代替真实厂商计量依据。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/gateway -run 'TestProviderRefusal\|TestStrictFeeBound'` | 首轮定向通过：四类原因在收尾前可查询，模拟丢失请求后的恢复/重复收尾，以及真实 HTTP 收费拒绝和重试 pending；后补的诊断故障/取消用例随最终全量检查通过 |
| 默认 `go build ./...`，随后 `go build -x ./cmd/gateway` | 失败；追踪显示 Go 在父目录 `C:\Users\mn139` 执行 `git status --porcelain`，VCS 元数据读取失败。工作区自身 git status/log 可读；未修改仓库或全局 Git 配置 |
| 本命令环境 `GOARCH=amd64 GOFLAGS=-buildvcs=false`：`go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 全部通过，包括新诊断写入故障/取消回归和既有契约、权限、并发、持久化/恢复测试。临时跳过本地产物 VCS stamping，不改变源码、测试或 CI 门槛 |
| `node --check` 四个既有脚本，`node --test web/assets/api.test.mjs` | 全部通过，6 项行为测试通过；新增浏览器原因展示断言仅语法检查，未运行真实浏览器 |
| 同一 Go 环境 `python tests/smoke/local.py --go go` | 通过；真实 gateway 隔离启动、鉴权/JSON/SSE/控制台/strict 结算，以及 SSE 强杀重启、pending 与管理员幂等豁免 |
| 本批 4 个 Go 文件 `gofmt -l`、`git diff --check`、`git diff --exit-code -- go.mod go.sum` | 通过，无无关格式化或依赖变化 |

未运行 Linux race、Docker、真实浏览器、真实厂商或远端 CI，未操作实际运行库；默认 VCS stamping 构建尚未在此工作树环境通过。S5 仍为 5/6，S5-03 与 S5-C 完整验收保持未完成。下一批继续 C02/C03 的可验证输入/总输出与缓存/推理计费依据，再集中完成剩余验收；S6/S7-A 尚未开始。

## S5-C 第五批工作记录：最终正文计价维度检查

2026-10-17，继续在受管工作树 detached HEAD `746f673` 的未提交实现上开发。负责 `internal/billing` 的发送正文计价白名单、`internal/gateway` 的发送前调用和本地拒绝分支，以及相应行为回归。依赖既有协议 Prepare、输出限制守卫、持久拒绝原因和唯一收尾；没有修改 core、协议适配器接口、数据库版本、依赖或 CI，没有参考项目代码移植。

本批将入口的文本计价约束落实到真实转换输出：OpenAI Chat/embeddings 复用严格 JSON/文本解析；Anthropic 与 Gemini 检查顶层生成字段和逐消息/内容块。拒绝重复 key、未知字段、工具/媒体内容、显式缓存/推理或 thinking 配置、服务等级及媒体输出配置；embeddings 仍只准许文本/文本批次。没有先丢弃未知维度再验证。所有检查在持久 attempt 和网络发送前执行；原售价/分组/累计预算检查继续保留。

正文越界、输出限制保真失败及 embeddings 跨协议拒绝共用 `unsupported_billing_dimension` 持久原因，沿用零执行释放双占用、未知执行继续 pending 的规则。本地拒绝不反馈上游健康；首轮测试发现既有该类 400 会走健康回调，已修正真实分支并保留断言。账本原因白名单和既有恢复覆盖新增代码，未另建收尾/迁移流程。OpenAPI 保持 0.2.3 / 79 操作，仅更新现有推理操作及 reason 的描述；README/指南同步支持范围。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/gateway -run 'TestPreparedBillingShape\|TestStrictPreparedDimensions\|TestStrictConvertedLimits\|TestProviderRefusal'` | billing 通过；gateway 新回归发现本地 400 会触发健康反馈，修正本地拒绝分支，没有删除断言 |
| `go test -count=1 ./internal/gateway -run 'TestStrictPreparedDimensions\|TestStrictConvertedLimits\|TestStrictEmbeddingsCannot'` | 修正后通过；使用替换适配器真实构造未计价正文，验证零网络/零 attempt/零 provider 新占用、原因可查询及 legacy 透传 |
| `GOARCH=amd64 GOFLAGS=-buildvcs=false` 下 `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 全部通过；含三协议真实文本转换、空串/中文/长文本、文本 embeddings 批次、22 类未计价/畸形样本、拒绝后的中断恢复/重复收尾，以及现有完整回归 |
| 同一 Go 环境 `python tests/smoke/local.py --go go` | 通过；真实 gateway JSON/SSE、模式隔离、strict 缺价/结算及 SSE 强杀后重启 pending、管理员幂等豁免 |
| 本批 7 个 Go 文件 gofmt、`git diff --check`、`git diff --exit-code -- go.mod go.sum` | 通过；未格式化无关文件或改变依赖 |

本地环境沿用上一批已定位的父目录 VCS stamping 限制，命令临时使用 `-buildvcs=false`，未修改 CI 构建门槛。前端源码和浏览器脚本本批未变，未重复运行 Node 或浏览器；未运行 Linux race、Docker、真实厂商/客户端及远端 CI，未刷新远端基线、提交/推送或操作实际运行库。

此批关闭转换层显式字段绕过的缺口，属于 C03/C05 的部分实现，不能证明默认推理、缓存计量、真实输入 Token 或配置 evidence 的真实性。S5 仍 5/6，S5-03 和完整 S5-C 不新增完成勾选；下一步收口真实计量与价格依据，再完成剩余验收，S6/S7-A 尚未开始。

## S5-C 第六批工作记录：有来源的 embeddings 输入边界档案

2026-10-17，继续在受管工作树 detached HEAD `746f673` 的未提交实现上开发。负责 billing 的输入边界档案/真实执行输入准入、gateway 发送调用方、config/main 启动接线及对应回归。依赖既有独立 provider 成本契约、售价覆盖、原子 attempt、持久拒绝和唯一收尾；无新 core 接口、模型迁移、第三方依赖、CI 或前端变更。

本批先核对 OpenAI 官方 Create embeddings 与 Embeddings guide，依据记录在[输入边界档案](reference/input-bound-profiles.md)。新增显式可选 `input_bound_profile: openai_embeddings_v1`，只登记官方根 URL 和三个明确模型，要求整请求输入包络至少 300000；不把单输入 8192 当作批次总上限。发送前核对最终 URL、正文模型、文本白名单与批次至多 2048 条。每次占用整个配置输入包络，不按字符估算或先低估执行。最高价格与币种仍由原独立成本契约提供，没有将官方输入约束误写成售价或错误请求免费证明。

gateway 改用 `StartPreparedAttempt` 将真实 URL/正文交给同一成本引擎，旧的无正文入口无法开启带档案的 attempt；未指定档案的旧契约保持运营声明语义。档案随现有 format=2 的 contract 快照保存、由现有账本展开显示，无新 HTTP 字段或 schema 版本。OpenAPI 保持 0.2.3 / 79 操作，只补现有 embeddings 说明。未登记模型、兼容代理、Chat 不自动继承该档案。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/config ./internal/gateway -run 'TestInputBoundProfile\|TestEmbeddingInputBoundProfile\|TestStrictEmbeddingInputProfile'` | 首轮通过：未知档案/模型、低报总输入包络、非官方地址拒绝；配置非种子；真实最终 URL/操作/模型变更和 2049 批次拒绝；2048 批次允许；无正文入口不可绕过 |
| `GOARCH=amd64 GOFLAGS=-buildvcs=false` 下 `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 全部通过；HTTP Transport 模拟验证短输入仍占用完整 300000 Token 包络，示例费率下 `6000000` provider units，实际回执只收 `100` 用户 units，快照持久保留档案；包括原契约/权限/并发/强杀全量回归 |
| 同一 Go 环境 `python tests/smoke/local.py --go go` | 通过；原 gateway 隔离启动、JSON/SSE/控制台/模式隔离/strict 结算、真实 SSE 强杀重启 pending 与管理员幂等豁免 |
| 本批 8 个 Go 文件 `gofmt -l`、`git diff --check`、`git diff --exit-code -- go.mod go.sum` | 通过，无无关格式化或依赖变化 |

来源读取：旧 platform 文档入口在本机请求遇到 Cloudflare 页面，改由 developers.openai.com 官方 API 参考与指南实际读取核对；另一次只读 `Invoke-WebRequest` 确认官方整请求上限原文。未将挑战页作为来源，没有真实厂商请求或密钥使用。档案测试使用注入 Transport，不是在线厂商验收。

本地沿用已记录的 VCS stamping 环境限制，临时 `-buildvcs=false`；未改 CI 门槛。未重复运行未改动前端的 Node/浏览器检查，未运行 Linux race、Docker、真实厂商/客户端或远端 CI，未刷新远端基线、提交/推送或操作实际运行库。

此批为 C02 落实一条有官方来源的输入约束路径，但不做本地精确 Token 计数、不认证运营最高费率或失败请求收费，Chat 默认推理/缓存及其他厂商仍待独立依据。S5 仍为 5/6，S5-03/S5-C 完整验收保持未完成，S6/S7-A 尚未开始。


## 2026-10-17 主线推进批次：协调、能力、工具与原生协议

范围：沿用既有 S5-C 工作树，新增 `billing/input_profile` 的 Chat 档案；将现有 gateway 业务协调从 Gin 入口中抽离，增加操作/能力路由和 function tools 行为回归；S7-A 只做不依赖生产接线的 Responses 协议实现与样本。不新增支付、生产路由、迁移版本或第三方依赖，不检查实际旧库或扩大回滚专项。

对应用户“1–5”主线的真实进度：

| 项目 | 本轮实际完成的切片 | 尚未完成的阶段门槛 |
|---|---|---|
| 1 S5 收口 | 固定 GPT-4o Mini 日期快照、官方目标、整上下文输入上界及总输出限制；显式标准服务等级，发送前复核 | 未认证自定义包络、运营费率/币种/错误收费假设；S5-03 和完整 S5 仍未验收合并 |
| 2 非 Gin 协调 | `Gateway.Execute(context.Context, http.ResponseWriter, Execution)`；原 Chat/embeddings HTTP 真正调用同一协调函数，每次生成隔离结果、RPM/TPM 预留和收尾；request lease 覆盖预留至最终收尾 | 该入口仍以 HTTP writer 输出，不宣称已具备 native WS executor 或持久任务恢复端口；未完成阶段验收合并 |
| 3 操作与能力 | Chat/embeddings/Responses 操作标识、源协议与传输要求；真实候选过滤；同协议保留原字段，跨协议不再只记日志后丢字段 | Responses/background/WS 未开放能力；完整操作矩阵与应用接线继续随真实消费者验收 |
| 4 function tools | Anthropic JSON 多调用/多结果往返、真实 content_block 起始事件、交错参数分片与孤立 delta 拒绝；原 OpenAI 原生路径保留 | strict 继续拒绝工具；Gemini 的签名/工具历史没有无损表示，因此真实路由拒绝其工具请求，而非假定所有模型兼容 |
| 5 S7-A | `protocol/responses` 独立请求模型与原生 JSON/SSE 解码，经 core 执行样本，不转换成 Chat；事件序号、帧上限、失败/截断识别 | 尚无生产 HTTP 路由、owner/幂等持久状态、加密正文、原生后台任务、native WS、跨模式恢复或指定客户端验收 |

新增 `core.Result.Phase` / `UpstreamStatus` / `ProtocolComplete` 记录本地提交与协议事实；Dispatching 后的错误不能证明上游未执行，ProtocolComplete 不等于已结算。独立 Responses 解码成功也不等于生成成功（例如查询到 failed/queued 对象）；S7 应用仍须处理操作状态，不能直接把 HTTP 200 当完成。原生 SSE 不补 Chat 的 `[DONE]`，失败事件或缺少终态不伪造成功。

兼容性变化：跨协议只准许已建模的共同子集；如 `max_completion_tokens`、`tool_choice`、`parallel_tool_calls`、`response_format`、seed 和不支持的消息/工具字段会在发送前拒绝，不能为了“兼容”悄悄忽略。OpenAI 同协议保留已有原生字段行为。公共 JSON 验证抽至 `protocol/jsonutil`，billing、跨协议解析与 Responses 共同使用，保留重复键/UTF-8/深度/单文档限制。

来源：本轮重新阅读固定快照中的 RelayKit README、sub2api execution scope 和 CRS codexToOpenAI 事件逻辑，仅借鉴职责/失败场景，没有移植第三方代码。协议依据重新核对 OpenAI Docs 的 Chat/模型、Background、WebSocket、Fast mode，以及 Anthropic 官方流式事件；来源见[上游快照](reference/upstream-snapshots.md)与[输入档案](reference/input-bound-profiles.md)。仓库阅读副本位于原工作目录 `D:/my-project/TXAPI/.local/references/`，本受管工作树未再克隆副本，不把目录缺失写成上游不存在。

验证环境：Windows amd64，基线 `746f673` 加本轮工作树；Go 命令使用 `GOARCH=amd64`、`GOFLAGS=-buildvcs=false`。

| 实际执行 | 结果与适用范围 |
|---|---|
| `go build ./...`、`go vet ./...` | 最终代码通过 |
| `go test -json -count=1 ./...` | 最终完整普通测试通过，含依赖/契约、身份/账本/恢复和新增协调/工具/原生 Responses 协议；中间一轮因 stream_options 兼容拒绝失败，修复并完整复验后通过 |
| `go test -count=1 ./internal/gateway -run TestTypedCoordinator` | 未发送请求退还 TPM、每轮独立 RPM/TPM、并发状态隔离、panic 释放和结算期间名额覆盖通过；随后完整测试包含最终能力路由回归 |
| 四个现有 `node --check`、`node --test web/assets/api.test.mjs` | 语法及 6 项前端 API 行为通过 |
| `python tests/smoke/local.py --go go` | 临时库/假上游启动、鉴权、JSON/SSE、控制台、模式隔离、strict 结算及实际进程强杀恢复/幂等对账通过；该次在最后的未发送 TPM 收尾修正之前，修改不涉及财务/启动/恢复，复用其适用范围，TPM 已定向及全量复验 |
| 仅变更 Go 文件 `gofmt -w`、`git diff --check` | 通过；另只读比较所有 Go 文件按 LF 规范化后的 gofmt 结果，无偏差，不格式化无关 CRLF 文件 |
| Python AST、Markdown 本地路径及 OpenAPI 结构检查 | 两个冒烟脚本语法通过，80 个本地文档路径无缺失；OpenAPI 0.2.3 / 79 个操作，未注册 Responses 路由 |

远端 CI 结果在提交后单独记录。本地未运行 Linux race、Docker、真实浏览器/真实厂商或指定客户端验收，没有操作实际运行数据库。阶段清单保持未新增勾选，不能把这批代码当作已验收共同基线。

## 2026-10-17 功能提交与 CI 记录

功能分支 `feat/s5-s6-mainline`，提交 `b7939543e55d827e5d9b9ebb4c4545b8e0fcb838` 已推送。该提交包含以上既有未提交工作和本轮协调/能力/工具/原生协议切片，没有覆盖无关改动。推送前 fetch 核对 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07`，且该集成提交是功能分支祖先；尚未合并 master。

[Verify run 39007867301](https://github.com/ANRCM0/TXAPI/actions/runs/39007867301) 对上述精确功能 SHA **通过**，由 push 自动触发，使用 `gh run watch 39007867301 --exit-status --interval 30` 等待完成，退出码 0：

- `check` 通过：模块整洁、core/protocol 与协作依赖、OpenAPI 契约、构建、前端语法/行为、vet、gofmt、Linux 全量 race、覆盖率报告、端到端冒烟及性能基线报告。
- `delivery` 通过：容器安装、升级、备份恢复与正常退出；这是 CI 隔离环境证据，不是实际生产库升级或物理部署验收。
- CI 输出含 Actions Node 20 运行时弃用与 `ubuntu-latest` 镜像迁移提示，未导致失败；本轮未为清除提示而改动无关 workflow。

本记录及 README 的后续说明文档更新复用该功能 SHA 的执行证据：没有再次修改可执行代码、API 契约、配置或测试；只检查文档差异、链接和状态一致性。自动 push/PR CI 触发规则保持原样，不手动重复 dispatch。

**剩余门槛不因 CI 绿灯消失：**未指定输入档案的成本契约仍依赖运营声明，最高费率/币种/失败请求收费没有统一可验证证明；不能将这些契约宣称为已认证，也不能勾选 S5-03。S6 的代码与工具切片已通过回归，但仍待成本边界收口及阶段集成评审；S7-A 的生产 HTTP、持久会话与加密、原生后台任务、native WS、跨模式恢复和指定客户端验收尚未完成。本轮不将“1–5”整体报为完成，也不绕过共同基线门槛提前接线。

## 2026-10-17 继续推进：输入档案成为执行门槛

本批基线 `feat/s5-s6-mainline/01716458af22b50b330b0e55559bd50c500c0c2c`，开工工作树干净；fetch 确认 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07` 仍为分支祖先。继续现有 draft PR #5，不新建平行实现或提前接线 S7。负责 billing 的成本准入与持久拒绝、gateway 错误映射、相关行为回归和进程冒烟；依赖既有档案校验、唯一结算/恢复与资源收尾。无新路由/字段、迁移、第三方依赖、core/CI 变更，也没有移植参考项目实现或操作实际数据库。

实现：

- strict 的每个新 attempt 必须匹配已登记输入档案并提供最终 URL/正文；未指定档案的旧契约可继续加载/读取历史，但仅有 `evidence` 不再获准执行，返回 503 `provider_input_unproven`。Anthropic/Gemini、兼容代理及未登记模型当前没有可执行档案；legacy/disabled 的原兼容路径不受此门槛影响。
- `StartPreparedAttempt` 重新解析最终正文，要求输出字段/数值与本次成本占用元数据完全一致，拒绝低报后发送更大上限；旧 metadata-only `StartCostedAttempt` 弃用且不能准入。继续使用真实原子 attempt、固定售价/分组覆盖和累计 provider 包络，不新增可配置豁免。
- 本地拒绝不发送、不换凭据继续重试、不反馈健康、不生成 durable attempt；未执行请求释放双 hold。已有未知执行则保留原 attempt/provider 占用和 pending；拒绝原因在原账本和崩溃恢复中保持，不用新限制抹除历史财务事实。
- 既有精确算术、重试、预算、快照和事务回归保留。billing 私有测试夹具只调用实际财务实现以构造声明包络/历史事实，不能作为生产输入证明；网关正向用例改为真实档案配合测试专用 loopback Transport，不给生产二进制添加测试开关。

验证环境：Windows/PowerShell，Go 命令均设置 `GOARCH=amd64 GOFLAGS=-buildvcs=false`，沿用已记录的本地 VCS stamping 限制。以下命令针对本批工作树，而非复用 `b793954` 的旧 CI：

| 实际执行 | 结果与适用范围 |
|---|---|
| `go test -json -count=1 ./internal/billing ./internal/gateway ./cmd/gateway` | 首轮定向通过；输入无依据拒绝、正文限制绑定、已发生未知执行后的保守恢复，以及 loopback-only 夹具边界通过。后补的 strict/legacy/disabled JSON/SSE 隔离用例由下列全量覆盖 |
| `go build ./...`、`go vet ./...`、`go test -json -count=1 ./...` | 最终生产代码与本批测试全部通过，18 个测试包通过；包含依赖/契约、并发/账本/恢复与原有协议回归 |
| `python tests/smoke/local.py --go go` | 通过；生产 `go build` 二进制验证启动/鉴权/原调用/控制台/模式隔离及无档案 strict 拒绝；`go test -c ./cmd/gateway` 测试二进制调用同一生产 main，在测试专用本地传输下验证带档案 JSON/SSE 结算、SSE 强杀/重启 pending 与幂等豁免。通过后只修改一条说明注释，复用此执行证据 |
| 本批 Go 文件 `gofmt -l`、`git diff --check`、`git diff --exit-code -- go.mod go.sum` | 通过；只格式化本批文件，没有依赖变化 |
| 一次性 `python -` Python AST / Markdown 路径 / OpenAPI 检查 | 通过；51 个本地路径/锚点和 12 个本批 Go 文件格式检查通过，OpenAPI 与 HEAD 排除 description 后完全一致，保持 0.2.3 / 79 操作，无 Responses 生产路由 |

冒烟的测试传输仅连接本次随机 loopback 假上游、禁用部署代理并拒绝其他目标；不验证真实 TLS、厂商行为或费率。生产构建不包含 `_test.go` 的入口/环境变量与拨号器；真实财务、路由、生命周期和恢复代码没有被 mock。未改动前端源代码，本批未重复运行 Node/真实浏览器；未在本机运行 Linux race、Docker 或真实厂商/客户端验收。

本批远端完整 Verify 由 push/PR 自动触发并按最终 SHA 单独记录于 PR；本地通过不替代该门槛，不手动重复 dispatch。当前仍为 draft，不合并、不新增 S5/S6/S7 阶段完成勾选。**输入无依据仍可执行的缺口已关闭，但完整 S5-C 尚未完成：**最高费率及币种、同渠道凭据的价格上界、失败请求收费假设仍未认证；后续先完成这些可计价边界，再验收共同基线，最后推进 Responses HTTP/持久会话/后台/WS 生产接线。


## 2026-10-17 继续推进：标准费率下限与凭据范围

本批基于 `feat/s5-s6-mainline/478dc7b6265a5d6729c31f19b7cf5ab61acd51ab`，开工工作树干净。重新 fetch 确认 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07` 仍为分支祖先；继续 draft PR #5，不提前合并或接线 S7。上批精确 SHA 的 [push Verify](https://github.com/ANRCM0/TXAPI/actions/runs/39010257470) 和 [PR Verify](https://github.com/ANRCM0/TXAPI/actions/runs/39010260970) 均成功；它们只证明上批，不替本批变更背书。

负责目录及接口：billing 的上游准入/成本快照，config 与 main 的启动接线，gateway 的实际 prepared Header 消费与拒绝映射，相关测试和 `tests/smoke/local.py`。复用既有 provider 包络、双预算、原子 attempt、持久拒绝和唯一收尾/恢复；没有新路由/HTTP 字段、迁移、第三方依赖、core 或 CI 修改，不新增支付，不操作真实库或扩展旧库/回滚专项。

实现结果：

- 新增有来源的 `openai_standard_usd_2026_10_17` 费率档案、`currency` 与 `credential_bindings`。实际读取 OpenAI Docs Pricing Standard 表，为现有登记 Chat/embeddings 目标校验独立 provider 单价下限；不能用缓存折扣、Batch/Flex、错币种或零价低报。低于已登记费率/未知档案/不完整绑定拒绝启动，允许显式更高包络价；新 provider 预算仅 USD，不换算用户售价单位。
- 每个可执行契约显式绑定凭据 ID 和 Key 的 SHA-256；启动编译深复制 map，之后修改原配置不能变更已固定策略。无费率档案的历史契约可加载读取，但新尝试返回 `provider_tariff_unproven`；新 ID、原 ID 换 Key 或实际鉴权不匹配返回 `provider_credential_unproven`。不把所有渠道凭据自动纳入同一成本许可。
- `StartPreparedAttempt` 增加真实 Header 参数；gateway 同批迁入，不只检查 Meta 中的 Key。多鉴权值、大小写重复鉴权、额外组织/项目选择头与未登记头拒绝，不允许适配器改变计费账户后复用旧许可。普通最终标准传输头由现有 executor 管理，可信自定义 transport 不得在校验后替换关键输入。
- 档案、币种与凭据摘要随既有 format=2 快照保存并纳入摘要，不保存真实 Key/鉴权头；旧 format=1/2 仍可读，不回填虚构 USD 证明。新拒绝原因沿用管理员列表/详情和恢复：无执行释放双 hold，先前未知执行继续 pending/保留 provider 占用，不重试、不反馈健康。
- 原算术/并发/重试/快照/恢复回归保留；网关 provider 预算用例改为满足标准费率的包络，继续断言首次/重试预算耗尽与实际网络尝试数。测试用凭据均为隔离假值，生产没有豁免配置。

来源、每千 Token 换算、摘要生成方法和保证边界见[费率档案](reference/provider-tariff-profiles.md)。这里只建立已核对标准费率下限与凭据绑定，**不是凭据实际最高价或完整发票证明**：运营仍须核实逐账户合同与附加费，错误/拒绝请求可计费 Token 的硬上界尚未由本批来源建立，带日期的价格不会在线自动刷新或保证未来不涨价。不能据此勾选 S5-03 或完整 S5-C。

验证环境：Windows/PowerShell，Go 使用 `GOARCH=amd64 GOFLAGS=-buildvcs=false`；VCS stamping 仍为已记录的本地环境限制。执行记录：

| 实际命令/检查 | 结果及适用范围 |
|---|---|
| `go test -json -count=1 ./internal/billing ./internal/gateway ./internal/config ./cmd/gateway` | 首轮通过；登记模型费率下限、错币种/档案/绑定、实际鉴权头、不可变 map、配置非种子、原账本与 gateway 重试、诊断恢复回归 |
| `go build ./...`、`go vet ./...`、`go test -json -count=1 ./...` | 最终代码通过，18 个测试包通过，含依赖/79 操作契约、权限/并发/协议/财务及恢复回归 |
| `python tests/smoke/local.py --go go` | 通过；生产二进制验证启动/原调用/控制台/模式隔离、无输入/无费率档案拒绝；同一生产 main 的测试专用 loopback 传输验证带档案 JSON/SSE 结算、实际管理 API 换 Key 后拒绝继承、SSE 强杀重启 pending 与幂等豁免 |
| 本批 Go 格式、差异、Python AST、文档路径/锚点与 OpenAPI 结构 | 通过；13 个变更 Go 文件、59 个本地文档路径/锚点及 Python AST 通过；OpenAPI 排除 description 后与 HEAD 完全一致，保持 0.2.3 / 79 操作；go.mod/go.sum、模型/迁移和 CI 无变化 |

未改前端源代码，本批不重复运行本地 Node/真实浏览器；未在本机运行 Linux race、Docker、真实上游/凭据账单或指定客户端验收。冒烟仍仅隔离假上游与临时 SQLite，不认证真实 TLS/价格。push/PR 自动完整 CI 按最终 SHA 另记于现有 PR，不改触发条件、不手动重复 dispatch。S5 保持 5/6，S6/S7 阶段未新增完成勾选；下一切片收口可保证的收费范围及失败收费假设，不把这些外部依据缺口隐藏为已完成。


## 2026-10-17 A1：架构优先与输出边界

用户调整开发顺序后，本批先交付真实执行输出端口，不新增厂商、支付、生产 Responses、会话、后台 API 或 WS 功能。代码基于 `66afe6bb11e43c2fcb594d60e14f88a99d59daa6`；已重新 fetch，`origin/master/90df76b0340e4cdfa6c332c781f7aa772a04ed07` 是祖先，开工时远端功能分支与 HEAD 同步。尚未作为共同架构基线合并。

负责范围：`internal/core`、`internal/gateway` 的输出与 HTTP 适配、现有消费者和风险回归；同步本计划、架构/协作/开发说明与 README。复用既有 core、protocol、access、Admission、billing 和依赖检查；不改模型/迁移、生产注册、HTTP DTO、OpenAPI、依赖或 CI。不是参考项目代码移植。

- 新增 `core.Output`：同步 Commit/Write/Flush/State，分别描述已编码响应的元数据、正文、背压和不可逆提交事实。输出由调用者持有，单次 generation 独占，不允许复用已提交出口；执行器不负责关闭它。
- `Executor.ExecuteTo` 消费输出端口，现有 `Execute` 只做 HTTP 适配；`HTTPOutput` 保留状态码、JSON/SSE 响应头、flush 和 write deadline。非 HTTP 输出不伪造 HTTPStatus，保持为 0。
- `Gateway.ExecuteTo`、协调 scope、准入与错误出口不再持有 HTTP writer。现有 Chat/embeddings 仍调用同一协调函数，经 `HTTPOutput` 输出，原 HTTP `Execute` 保留适配兼容；没有伪造 Gin context 或另起执行链。
- 原有有界正文捕获、重试/健康、授权、RPM/TPM、预算和收尾复用。输出短写、非法计数、提交/flush 失败显式失败，不对已坏输出追加错误或成功终态，不改目标重试、不惩罚凭据；已提交事实不因失败回退。
- 新回归用有界的非 HTTP observer 消费真实协调器和 HTTP executor，覆盖 Chat/embeddings、拒绝/Retry-After、未提交重试、提交失败/短写/flush、取消与资源释放。财务回归使用实际临时 SQLite 和原 strict 引擎，验证正常结算、无依据拒绝释放、输出失败仍保留双 hold/pending；没有降低生产准入。

边界：Output 仍承载 JSON/SSE 编码字节及状态/元数据，不是通用语义事件模型；非 HTTP observer 是测试消费者，不是持久 worker 或原生 WS 实现。core 仍是标准库单次 HTTP executor。操作模型、具体 billing 依赖、小接口与跨模式状态/安全基础仍由后续 A2/A3 收口；不因此勾选完整 S6、S7 或架构完成。

验证环境：Windows/PowerShell，`GOARCH=amd64 GOFLAGS=-buildvcs=false`。本机没有 Make，完整本地检查按 Makefile 的等价命令执行；VCS stamping 使用既有环境规避配置，不改变 CI。

| 实际命令/检查 | 结果和适用范围 |
|---|---|
| `go test -count=1 ./internal/core ./internal/gateway ./internal/architecture` | 初始输出切片通过；原 HTTP/协议/身份/租约行为和新增输出风险回归 |
| `go test -count=1 ./internal/gateway -run TestCoordinatorIndependentOutputPreservesStrictFinancialTruth` | 修正夹具后通过；原 strict 引擎/临时 SQLite/隔离 loopback，未更改计费准入 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 最终本地通过，18 个有测试包通过；含依赖、79 操作契约、权限/并发、协议、账本及恢复 |
| `node --check`（app/api/browser navigation/billing）与 `node --test web/assets/api.test.mjs` | 4 项语法检查、6 项现有行为测试通过；不是实际浏览器兼容验收 |
| `python tests/smoke/local.py --go go` | 通过；生产 HTTP JSON/SSE、控制台与 strict 无依据拒绝；同一生产 main 的隔离 loopback 传输验证结算、换 Key 拒绝、SSE 强杀后 pending 与幂等豁免 |
| 变更 Go 格式、`git diff --check`、文档路径/锚点与保护范围 | 通过；11 个 Go 文件已格式化，63 个本地文档路径/锚点有效；模型/迁移、OpenAPI、依赖和 CI 无变化 |

首轮新增财务用例及首次全量测试因测试夹具不符合既有 strict 规则而失败：未显式 standard service tier、预留上界不足、移除输入档案后仍带不匹配费率档案。已仅修正测试配置（显式 `default`、足额预留、无档案场景清除相应费率声明），定向复验和最终全量通过；没有关闭用例、放宽生产检查或用测试输出冒充免费执行。

本机未运行 Linux race、Docker、真实上游/客户端或浏览器验收。push/PR 自动完整 CI 按最终 SHA 单独记在现有 draft PR，不手动重复 dispatch；尚未合并，不勾选 S5-03、完整 S6/S7 或 A1–A4 整体完成。下一批 A2 收口操作模型与能力契约，不回到扩展厂商价格清单。
