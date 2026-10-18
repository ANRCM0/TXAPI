# TXAPI 参考项目对比与吸收方案

版本：方案 v1，2026-10-18。执行目标已获用户确认；R01 具体接口变更另获批准。逐批状态见 [实施记录](REFERENCE-ADOPTION-PROGRESS.md)，接口批准范围见 [R01 变更请求](REFERENCE-R01-CHANGE-REQUEST.md)。

## 1. 结论与本次交付边界

建议保持 TXAPI 的四层结构和单一执行/账务权威，分别吸收两个项目的优势：

- **sub2api：** 账号运营闭环、可解释调度、执行作用域隔离、多维计量与管理业务。
- **codex-proxy-rs：** 稳定执行事实与原始协议分离、续接作用域、配置代次保活、受宿主管理的扩展生命周期。
- **TXAPI 保留：** 小型单次执行内核、现有公开契约、用户/Key 双预算与未知执行保护、加密历史、fencing 和持久后台事件恢复。

不是把 TXAPI 改写成另一个项目，也不同时引入新数据库、微服务和进程插件。先让现有能力可解释、契约可承载下一项真实需求，再做业务扩展。

最初设计批次仅克隆参考源码、核对证据并编写方案。没有新增业务代码、修改公开契约、执行数据库迁移、提交、推送或合并。当前工作区已有调度观测等改动和文档删除，均原样保留；本文件是单独新增方案，不恢复原整套文档。

### 1.1 参考基线与复现

| 对象 | 本地位置 | 精确基线 | 本次证据性质 |
|---|---|---|---|
| sub2api | `D:\my-project\TXAPI-references\sub2api` | `b0a02a3304a0a78cdd003ddda1dd23ba02c03084` | `Wei-Shaw/sub2api`，main，depth=1，源码读取，未启动/测试 |
| codex-proxy-rs | `D:\my-project\TXAPI-references\codex-proxy-rs` | `591f95cc18ce95eb6fc87cf46a3526c5b3ecba36` | `ANRCM0/codex-proxy-rs`，main，depth=1，源码读取，未启动/测试 |
| TXAPI | 当前 worktree | HEAD `536bf2b8389eb4fd3ba22cbc83457f871b4a93c2` 加现有未提交改动 | 对比工作区实际源码，不将其等同于 HEAD 或远端主线 |

参考快照和 TXAPI 文件清单保存在 `D:\my-project\TXAPI-references\reference-snapshot-2026-10-18.json`。
TXAPI 写本文前的 424 个路径（含删除标记）摘要为 `e47017c01486b7cf6913b6fab07c48c63c6f9650a98693ac50a4363bd7667d11`。
该摘要只标识分析对象，不是测试通过证明。两个参考仓库克隆后工作区干净；未安装其依赖、执行安装钩子或复制其源码进入 TXAPI。

两个参考项目的提交时间分别为 2026-10-16 04:42:02 UTC 和 2026-10-18 20:34:20 +08:00。这是固定快照比较，不保证未来远端不再变化。后续更新应先记录当前 SHA 和本地状态，再显式 fetch/选择新 SHA；不自动 pull/reset 参考目录。

根 LICENSE 文件分别标示 LGPL v3 和 Apache 2.0。这里仅记录源码文件内容，不作法律合规结论；若以后复制代码或分发衍生制品，需另核对具体文件、第三方组件、许可证与 NOTICE。当前方案借鉴机制，由 TXAPI 独立实现。

### 1.2 本批六项工作协议

| 项目 | 本批边界 |
|---|---|
| 层级/目录 | 项目根目录本方案；仓库外 `TXAPI-references` 参考目录与快照 |
| 依赖接口 | 只读现有 execution/identity/configuration/lifecycle/background/responses 等公开契约 |
| 地基触碰 | 无；后文新接口/目录均是候选，不登记为 frozen |
| 预期行为 | 形成可追溯比较、具体设计、批次依赖和验收门槛，不改变运行行为 |
| 验证 | 文档差异、链接、来源路径、快照、已有文件不变性；不重复跑 Go/race/CI |
| 并行影响 | 无共享入口写入；未来契约、schema、注册、main 和 CI 由集成批次统一协调 |

## 2. 能力与架构对照

以下“有”表示所查快照存在对应源码/契约，不表示本次运行验收或生产可靠性评定。参考索引见第 11 节。

| 维度 | sub2api | codex-proxy-rs | TXAPI 现状及吸收判断 |
|---|---|---|---|
| 产品重心 | 订阅账号聚合、多平台接入和运营管理 [S01/S05/S06] | 面向 Codex 的多 Provider 网关 [C01/C02] | 通用中转与受控状态型执行；不必转成某个客户端专用后端 |
| 协议范围 | gateway routes 按平台注册多种原生/兼容入口 [S01] | Responses HTTP/WS、图片、搜索、模型和 Live；明确没有 Chat Completions [C01/C02] | Chat/embeddings/Responses/Conversations；尚无生产原生 WS 路由 [T01/T02] |
| 上游身份 | 账号、OAuth 刷新、平台限制与缓存联动 [S04] | 编译期 OpenAI/xAI Provider 与扩展接入 [C01/C07] | 渠道+API Key 凭据池；需新增账号生命周期，不能只给凭据多加 token 字段 |
| 调度 | 续链/亲和、资格复查、多因素分数、决策事实 [S02] | 冻结路由计划、账号范围和续接绑定 [C04/C05] | 优先级+权重+凭据选择；当前工作区已有独立观测，先验收再加评分 [T03] |
| 执行事实 | 多个 Provider/WS 服务处理各自上下文 [S03/S04] | canonical facts 与 wire 同封套，避免重复交付 [C03] | 已有 Phase/Committed/Usage/Native/Outcome，增量补齐而非新建平行事件总线 [T04/T05] |
| 状态寿命 | WS 按 Key、thread/session、request kind 分道 [S03] | persisted/connection-local 续接，不透明 Provider 状态 [C04] | 本地历史、会话、持久任务已经分开；新增 WS 不覆盖已有恢复语义 [T06/T07] |
| 配置发布 | 调度缓存与状态更新协同 [S04] | RuntimeSnapshot、冻结扩展集合及旧代次保活 [C01/C05] | 已有保存/应用 revision 与失败保留旧快照；缺的是未来长连接/动态实例的资源租约 [T08] |
| 计量 | 缓存、多模态、服务等级等细分价格/用量 [S05] | 标准用量、计算费用与 Provider 报告费用分开 [C03/C06] | 三项 token 用量较窄；扩维要同时改价格、历史、账本和消费者，不只加 UI 字段 |
| 资金语义 | 通用扣费事务去重；另有批量图片预留/捕获/释放 [S07] | Key 日/周配额，准入不建 hold，结算累计已知费用 [C06] | strict 双预算、预留、未知执行保留；不能以其他项目的配额合同替代 [T09] |
| 业务管理 | 账号、分组、套餐、支付和用量页面 [S06] | 管理域、审计及插件 UI/宿主能力 [C01/C07] | 已有用户、Key、渠道、配置、账本控制台；优先补现有后台能力可见性 |
| 扩展 | 主要沿 Provider/service 组织源码扩展 | 独立插件进程、SDK/RPC、能力发布；SDK 明确实验阶段 [C07] | 编译期操作/协议注册已存在；尚不需要直接引入外部 Runtime [T05] |
| 部署/存储 | 后端含 PostgreSQL/Redis 适配，业务面更广 [S07/S08] | PostgreSQL/Redis，明确单进程单副本 [C01] | SQLite 单实例基线；依赖更多不等于更适合当前规模或自然多副本 |
| 架构约束 | 服务/仓储/路由按领域分工 | workspace 模块依赖由架构测试维护 [C08] | 四层登记、公开签名和可见性测试已有；继续使用，不另造第二套规则 [T10] |

### 2.1 “Core” 必须按职责对应

codex-proxy-rs 的 gateway-core 更接近 TXAPI 的 L1 gateway、准入、路由、执行事实和部分状态协调，并不等于 TXAPI 的 L0 core。

| 参考职责 | TXAPI 拟保留归属 | 禁止迁移方式 |
|---|---|---|
| 单次发送、读取、取消、背压和发送水位 | L0 单次执行机制；未来 WS 的机械 I/O 也需明确唯一 owner | 把重试、用户鉴权、账本、Provider 业务塞入 L0 |
| 路由、attempt 编排、资源租约、计量收尾 | L1 gateway/access/billing/generation | 协议插件自行执行网络并自称未发送 |
| 厂商载荷、原生事件、兼容转换 | L2 protocol/compat | 全部操作强制压成 Chat，或由 L1 读取厂商 JSON 字段 |
| 会话/账号管理/后台管理业务 | L3 用例，经公开契约消费基础能力 | HTTP handler 直接复制事务、刷新、鉴权和结算流程 |
| 实例创建、注册、worker 启停顺序 | cmd/gateway 组装根和公开 lifecycle | 功能包 init 隐式启动服务或自行运行迁移 |

## 3. 保持不变的工程约束

1. **资格先于评分。** 权限、模型、协议能力、目标绑定、凭据状态、已试集合先过滤，评分不能让不合格目标重新进入候选。
2. **事实来源不能混淆。** 选中不等于发送；准备好不等于已发送；本地终止不等于上游未执行；协议终态不等于费用完整；写出成功不等于客户端应用已消费。
3. **一条权威链。** 身份来自可信绑定，资金来自原账本，副作用水位来自宿主 I/O，持久事件来自已有 store/fencing；插件和 DTO 不成为新的权威。
4. **不扩大 strict 许可。** 新协议、新账号类型、新计量字段存在，不自动获得成本证明。收费后台和 WS 各自有独立 gate。
5. **现有冻结接口不就地破坏。** 新契约先提交具体变更提案；新增小端口或 v2 与 v1 并存，有真实消费者迁移与签名检查后再冻结。
6. **保留有界资源。** 不照搬禁用全部请求大小限制的做法；正文、消息、事件队列、解码缓冲、连接、worker 与退休代次必须有界。
7. **未知执行不自动重发。** OAuth 401、WS 断线、插件崩溃、超时、缺终态都不能仅凭错误名称判成无副作用。

## 4. 目标结构：增量扩展，而非大重写

```text
L3: Responses/WS 用例 | 上游账号管理 | 后台/计量管理
                     |
          版本化小端口与可信身份绑定
                     |
L1: gateway -> eligibility -> route decision -> admission
        |          execution facts / attempt owner
        +---- billing / resource leases / generation
                     |
L0: 单次 HTTP / 拟议 WS 机械执行与发送水位

L2: HTTP/WS 表示、厂商 codec、OAuth 协议解析
    通过注册和组合根接线，不自行接管账本/授权/重试

store: 统一迁移、事务、密文、revision/CAS/fencing
cmd/gateway: 实例、契约版本、启停与资源归属
```

图表示职责而非新增包依赖许可。新增包仍必须登记在 `internal/architecture/layers.json`，遵守实际允许依赖与公开签名检查。候选包名只是本提案设计，当前不存在不代表缺失实现错误。

### 4.1 冻结什么，实时复核什么

| 事实 | 建议读取/冻结时点 | 变化处理 |
|---|---|---|
| owner/Key 当前权限 | 每次新生成轮次，持久资源操作按既有事务边界再核验 | 不因连接握手通过而永久授权；本批不声称撤销可中断已发送请求 |
| parser/codec 与扩展实例版本 | 一次生成开始，持有至该轮完全收尾 | 新轮用新代次；旧轮 decoder 不在中途更换 |
| 上游续接目标/实际模型 | 建立续接时保存；每轮校验绑定与当前可用性 | 失效明确拒绝，不静默迁移到其他账号 |
| 价格/成本合同 | 预留和 attempt 定价前按既有历史版本固定 | 恢复继续引用历史价，不读取最新价格重算旧账 |
| 健康/冷却/已试凭据 | 每次 attempt 选择前，保持当前行为 | 配置租约不使旧的可用性永久有效 |
| 凭据材料版本 | 发送前取得，只用于本次发送/连接 | 旧连接能否继续由 Provider 身份与撤销策略决定，不等同于自动关闭所有流 |
| 操作与 schema 版本 | 持久受理时记录必要版本 | 旧记录有明确读取/迁移规则；不把进程内租约写成可恢复对象 |

## 5. 吸收机制详细设计

### 5.1 可解释调度与有限评分

**来源：** S02 的决策对象、亲和/续链层和多因素评分。**现状：** T03 已将 routeDecision 与选择结果分离，并增加固定标签指标；这是现有未提交工作，不是本次新实现。

接入流程保持为：

```text
授权与操作解析 -> 候选/能力过滤 -> 续链目标约束
  -> 凭据冷却/已试过滤 -> 优先级池
  -> 选择器/加权或后续评分 -> 选择后准入 -> attempt
```

第一步仅收口现有观测：记录候选/可用/优先级池、凭据数量、回退、实际 picker、最终目标及耗时。`request_id` 用于日志关联，ID 不做 Prometheus 标签，不记录密钥/完整会话/正文。选择失败、准入拒绝、发送失败分开统计。

之后才考虑评分，建议先支持负载和错误率，不一次加入所有参考因素。评分数据必须注明采样时间、窗口、样本量与可信来源；缺样本使用中性值，不能默认奖励新账号。原生续链目标不参加可迁移负载均衡。

- 分数只在已经合格且处于有效优先级池的候选内排序；保留现有权重含义，不悄悄将高优先级改成一个可被低延迟抵消的加分项。
- 先做 shadow 计算，只记录“新算法会选谁”；对照错误率、切换率、首个业务事件延迟、负载离散度、选择开销与饥饿情况。
- 稳定后按配置小范围开启；禁用评分立即回到旧算法，且不改变当前流/账本。
- 自动评分不先依赖 Redis；当前单实例可用有界内存统计，丢失只影响排序，不影响授权或余额。

**验收：** 非法目标永不入选；配置扩展不伪造亲和命中；准入拒绝零 attempt；固定种子分布测试；并发下计数一次；统计过期/溢出降级；回退开关行为一致。

### 5.2 执行事实和原始协议数据分离

**来源：** C03 的一个上游事件对应多个 canonical facts 和至多一个 wire 表达。**现状：** T04/T05 已有 Outcome、Result、Frame、Observer；缺的是未来 WS/多模态需要的更丰富事实与明确完备性。

候选 `execution/v2` 只描述框架必须理解的事实，先由一个 HTTP/SSE 消费者落地，再供 WS 使用。不将参考项目完整事件枚举直接移植。

| 候选事实 | 权威来源 | 消费者与限制 |
|---|---|---|
| Dispatch/Send 水位 | 宿主单次执行器 | 重试、未知执行和账本；codec 不得自行报告“肯定没发送” |
| DeliveryCommit / 字节事实 | 输出适配器及执行器 | 区分响应头提交、业务数据提交、部分写和关闭 |
| UsageObservation | Provider codec 解析事实 | 携带来源、是否终态、是否完整、累积或增量语义 |
| ProtocolTerminal | 本次 decoder/session | 表示协议完整性；不是网络断开、不是资金已结清 |
| ContinuationUpdate | 已注册的 Provider codec/用例 | 交付作用域、账号与不透明状态；不从任意客户端 ID 建立可信绑定 |
| RawProtocolEnvelope | L2 持有的原始事件 | 同协议透传至多一次；标准事实不得再生成重复 wire 输出 |

候选封套最少包含 `attempt_id`、单调序号、受控事实集合、可选原始载荷和来源标识；正文/敏感上游状态默认不进日志或 metrics。不提供可任意覆写账本的 `map[string]any` 公共扩展口。

事件状态机由事实维度组合，不用一个 terminal bool 压平所有状态：准备、派发水位、上游协议终态、交付状态、账务状态分别记录。晚到用量若 Provider 合同允许，只能补充到规定终态窗口，不能重开输出或重复计费。

**迁移：** v1 HTTP 路径继续使用原 Result/Usage；v2-to-v1 桥只投影确定等价的字段，未知不转成零。先增加新 decoder 与真实用例，保留原测试；全部 consumers 迁移前不移除 v1。需要修改 L0 的发送/输出行为时另列具名 L0 变更，不能通过新增 DTO 绕过。

**验收：** 同一 wire 不重复交付；乱序、重复终态、截断 SSE、缺 usage、部分写、取消、观察回调失败均有行为测试；账本收尾不依赖尽力而为的观测队列。

### 5.3 配置代次与在途资源保活

**来源：** C01/C05 的不可变快照和扩展集合保活。**现状：** T08 已有 desired/applied revision、save/apply 分离、应用失败保留旧状态；T11 已处理旧健康观测不覆盖新凭据。不要重复做配置发布系统。

新增的仅是**运行资源租约**，候选 L1 `runtimegeneration` 与小型 versioned port，供长连接和未来可更新扩展消费：

```text
Prepare(revision) -> validate -> publish current
                      失败 -> 释放候选资源，旧 current 不变
旧 current -> retiring -> 无使用者且已收尾 -> close
```

- 新轮次 acquire 当前代次，release 幂等；资源关闭必须等网络读取、输出、结算与回调全部退出。
- 一条物理 WS 可持有传输资源代次，但每轮重新授权；需要升级 codec/账号连接时在轮次边界排空，而非中途换 decoder。
- 限制退休代次数、连接最长寿命与 drain deadline；超限先拒绝继续发布或新连接，不直接关闭仍承担账务收尾的 store。
- 发布状态只在候选准备完成后推进 applied revision；失败清理不能撤销已成功保存的管理变更。
- 不把现有每次重试读取当前健康/渠道的规则，悄悄改成全请求冻结一切。

**验收：** N 在流式输出时发布 N+1，旧流正确完成且新轮使用 N+1；发布失败旧流不受影响；并发 acquire/release 无重复 close；shutdown deadline 不导致提前关账本/store；重复发布不泄漏退休资源。

### 5.4 原生 WS：先隔离作用域，再提供协议兼容

**来源：** S03 的执行 lane 与 C04 的续接 scope。**现状：** T12 是有界服务端 WS I/O，尚无生产上游 WS executor/原生应用接线；不能仅包装已有 HTTP Executor 就宣称原生 WS 完成。

候选分工：L3 `responsesws` 持有轮次用例；L2 `compat/responsesws` 负责握手/消息和协议表达；L1 管理轮次执行、准入与资源；L0 的独立版本化 WS attempt 机制负责单连接机械读写/取消/发送水位。最后一项涉及地基变更，必须先批准具体依赖与关闭责任，不允许 L2 自己建立第二条发送/结算链。

作用域建议为 `(可信 owner, 可信 Key, 服务端 connection ID, 受限客户端 thread/session 提示, lane)`。客户端字段只能缩小同一可信身份内的会话命名空间，不能赋予身份、撤销权限或跨 Key 抢占能力。无可信关联信息时不使用正文相似度推导抢占身份。

- `turn/prewarm/compaction` 是否同 lane 必须按目标客户端协议回归确认；`memory`/子任务独立，未知 kind 有界隔离或明确拒绝，不能无限创建桶。
- 首个 MVP：每连接至多一个 active generation，有界 pending；新请求忙时显式拒绝/排队，不默认“新连接抢占旧连接”。抢占若以后需要，另定义授权和终止协议。
- 每轮重新解析可信身份、模型权限与准入；握手只验证建连资格和连接容量，不能预占所有未来轮次的预算。
- 前台断线取消本轮并 join 收尾；持久后台的观察者断开不取消任务。不能将两种规则混用。

续接至少分三类：

| 类型 | 可恢复范围 | 处理 |
|---|---|---|
| connection-local 原生状态 | 同一上游物理连接 | 断线后明确失效；不得换账号或伪装可持久恢复 |
| provider-persisted handle | 已证明支持的同一上游账号 | 当前授权+目标绑定校验后才能重连；缓存命中不构成权限证明 |
| TXAPI 本地历史/持久任务 | 已有加密 store/CAS/fencing 边界 | 不以新的亲和缓存替代；沿用 owner/Key/模型/TTL 与未知保护 |

绑定包含 Provider、渠道、账号/凭据身份、实际模型、作用域、过期时间及必要的连接/资源代次。账号身份稳定版本与认证材料刷新版本分开：同账号合法刷新不必废弃全部历史，但删除重建/换账号不得复用旧 handle。不得将未知外部 handle 默认等同于已授权本地历史。

建议 MVP 先以 disabled billing、单一已声明原生 WS Provider/操作上线；不要借此开放收费 WS。legacy 是否开放需验证用量和行为，strict 仍拒绝。支持模型、工具事件、帧上限、空闲/总时限、Origin/代理策略、错误码与关闭码必须写入本批契约，并用客户端回归固定，不能仅按参考项目 README 宣称兼容。

**验收：** 跨 Key 相同 thread 不干扰；同 Key 独立 lane 不互相取消；逐轮撤销生效；连接局部 handle 断线后拒绝；账号绑定不漂移；部分发送后不盲重试；慢读者/大帧/排队满/关闭均有界；真实 WS 客户端和可控上游故障回归通过。

### 5.5 上游账号与 OAuth 生命周期

**来源：** S04 的分页刷新、平台并发/QPS、错误分类及认证/调度缓存同步。**现状：** TXAPI credential 主要承载静态 API Key；不能由 scheduler 顺带刷新网络 token。

候选职责：L3 `upstreamaccounts` 处理导入、启停、重授权和管理流程；L1 `upstreamidentity` 处理凭据租约、刷新调度/去重、版本校验和失效；L2 Provider codec 处理授权协议表示。secrets/store 继续拥有加密和持久事务；main 统一启动和关闭刷新 worker。客户端 identity 与上游 identity 是两种身份，不能混为一个接口。

数据最小集合：稳定 account ID、Provider、认证类型、加密 secret reference、expiry、credential revision、状态、last error 分类、next refresh time、refresh fencing/version。API Key 沿用现路径，OAuth 为 opt-in；管理返回掩码，不返回 refresh token。

刷新流程：

```text
读到期候选 -> 按账号去重/版本领取 -> 平台限流与并发
  -> 有限时刷新 -> 分类结果 -> CAS 持久化新凭据
  -> 使认证缓存和调度资格收敛 -> 释放领取/记录下一次
```

- 不在数据库事务里等待网络。CAS 失败丢弃旧刷新结果，不能覆盖管理员更换后的凭据。
- 暂时网络错误/429 使用有界退避；invalid grant 等需重授权，不无限刷新。原请求已可能执行时不能以刷新成功为由重放。
- 若 Provider 会轮换 refresh token，网络成功后本地持久化失败属于特殊不确定状态：禁止盲用旧 token 循环重试，标记需恢复/重授权并告警。
- 缓存刷新失败不能把新密钥和旧调度状态混成“全部成功”；有可重试的收敛记录或从持久版本重建。初期可复用单实例失效机制，不先引入分布式 outbox 平台。
- 租约不跨账号复用；凭据移除/禁用影响新执行；旧结果按 account/revision 校验后才能更新健康。
- 首批只做一个明确 Provider 的授权流程，需可合法使用的测试账号及上游协议证据。未取得端到端证据时，仅标记本地模拟验证，不称真实认证可用。

**验收：** 并发到期只刷新一次、管理员更新赢过旧刷新、取消/关闭及时释放、429 不风暴、永久错误不无限重试、失效同步可恢复、日志/管理响应无 token、旧 API Key 路径零回归。

### 5.6 多维计量、成本证明与收费后台

**来源：** S05 的细分计量，S07 的事务幂等与专用 hold，C03/C06 的计算费/上游报告费分开。不能把任一参考项目概括为“没有预留”或“已证明完全不超扣”；它们的具体业务保证不同。

TXAPI 增量模型应区分：`UsageFacts`（数量）、`CustomerCharge`（向用户收费）、`ProviderSpend`（上游实际/估计成本）、`BudgetHold`（风险占用）。数量存在、费用算得出、成本上界有证据，是三个不同条件。

候选用量维度从 cached input、reasoning/output breakdown 开始；图片/音频/视频/工具各自按操作单独添加，单位不能都伪装成 token。每个维度记录 `known/unknown`、计量来源与版本、累积/增量性质；零是明确零，缺失不是零。

- cached/reasoning 常是 input/output 的子集，需由对应 profile 定义包含关系，不能总量再加子项导致重复收费。
- 新价格版本与计量版本在受理/attempt 前固定；结算和冲突指纹覆盖真正参与收费的维度。旧持久回执保留旧版本解释，不回填假零。
- 金额继续使用现有整数权威和明确舍入规则；展示浮点不得回写成为结算来源。
- 跨重试累计的是所有可能产生费用的 attempt，不是仅最后成功的一次；上游已报告费用和估算费用不可相加两遍。
- provider spend 上界需覆盖目标/认证材料绑定、操作、输入/输出界、服务等级、费率和失败收费条件。未知情形保留占用或拒绝，不沿用“缺费用=0”。

收费后台应在成本 gate 通过后复用已有原子意图+双预算机制和原生后台恢复，不新增单独余额表。受理、领取、派发水位、取消、终态和最终结算必须保持同一账务权威。无论进程在哪一步崩溃，恢复都不得重复预留、双扣或把可能已执行请求退款后重发。

日/周窗口和套餐可后续提供，但需要定义 UTC/业务时区、窗口边界、跨窗口 hold、迟到费用、退款与补单。先做只读统计不能等同于硬预算控制。

**验收：** 缓存包含关系、reasoning 重叠、未知值、溢出与舍入；旧回执读取；冲突重放；两方余额同事务；跨 attempt 成本累计；受理/派发/回执/结算各崩溃点；成本不明时不自动清零；现有 strict 限制保留。

### 5.7 管理产品与外部扩展

优先让现有能力能被运营，而非先做支付商城：

1. 后台任务查询/事件状态/取消与异常诊断；明确 queued、remote active、protocol terminal、unknown、content expired，不以页面加载失败判任务失败。
2. 路由诊断：按 request ID 关联候选与失败原因，敏感 ID 仅授权管理员可见；用户只看自己的调用，不暴露上游密钥。
3. 账号到期/刷新/重授权状态及配置 desired/applied 差异；管理动作带版本或幂等保护。
4. 用量与费用分列展示“用户费用/上游成本/保留预算/待对账”，不把全部混成 consumed。
5. 套餐、充值、订单等独立立项：支付回调验签、幂等、退款与对账使用账本端口，不能由前端或 webhook handler 直接改余额。

当前 `background.Service` 只有 Submit/Read/Cancel/Events，没有管理列表端口。新增后台管理页若要枚举资源，必须先追加 owner/admin 分离的只读查询端口和对应 API；不能让页面扫描内部表，也不能让管理员接口默认读取所有加密正文。使用 `/api/me` 与 `/api/admin`，不扩大旧管理密钥 API。

外部 SDK/Runtime 暂列最后。进入条件：至少两个真实扩展需要独立安装/升级，编译期注册已不足。届时再交付能力清单、版本协商、有限 RPC、取消/背压、宿主发送事实、故障隔离、发布回滚和 UI 桥接。独立进程不等于安全沙箱；若允许不可信插件，OS 权限/网络/文件隔离必须另立安全设计。观察型扩展失败不改响应或账单，受管上游扩展仍不能重写账本权威。

## 6. 分批实施与依赖

以下编号仅为本方案吸收批次，不替换既有 S5/A03/A04 阶段验收。规模为相对工作量，不是工期承诺。

| 批次 | 可独立交付结果 | 主要写入目录/接口 | 依赖与 gate | 回退方式 | 规模 |
|---|---|---|---|---|---|
| R00 | 收口现有调度观测，精确头 CI/合并 | gateway/metrics 及测试；不改公开签名 | 当前未提交差异需划清范围；现有本地证据只覆盖原被测内容 | 回退指标/日志代码，不改数据库 | 小 |
| R01 | 执行事实候选契约+一个 HTTP/SSE 真实消费者 | foundation/contracts/execution 新版本、gateway/core 桥、L2 decoder、architecture | 具名地基提案批准；v1 保持；不借机增加 WS 业务 | 保留 v1 执行路径；无自动迁移旧记录 | 中 |
| R02 | 运行资源代次与排空 | 候选 L1 runtimegeneration、lifecycle、management、main | 先有真实长寿资源消费者；准备/发布与关闭合同明确 | 禁用新发布；排空后回退；不强杀收尾 | 中 |
| R03a | WS 契约、作用域及机械执行能力 | L0 新版本/子包候选、L1 gateway/generation、L2 transport/codec | R01；L0 变更明确批准；资源所有权/发送水位完成 | 不注册生产 WS 路由；原 HTTP 不变 | 大 |
| R03b | 默认关闭的原生 WS MVP | L3 responsesws、L2 compat、main、API/客户端回归 | R03a+R02；disabled；一个 Provider 的原生协议通过 | 停止新握手并排空；前台明确取消；不破坏后台 | 大 |
| R04 | 一个 Provider 的账号授权/刷新闭环 | 候选 upstreamidentity/upstreamaccounts、L2 provider、store/secrets、管理 API | R01 相关发送边界；R02 租约；用户确定 Provider/账号来源 | 禁用 OAuth 入口，保留静态 API Key；旧密文不删 | 大 |
| R05a | 用量事实/价格历史/展示扩维 | contracts、protocol、billing、model/store、web | 具体 profile；旧计量/持久记录兼容；同步签名与 API | 关闭新操作/维度写入，按版本读取旧账 | 中/大 |
| R05b | S5 成本门槛与收费原生后台 | billing、generation/background、store、回归 | R05a 所需部分；独立完整成本证明；现有双 hold/意图权威 | 拒绝新收费任务；排空/对账旧任务，不能切 legacy 清库 | 大 |
| R06 | 后台、路由、账号、账务管理产品 | L3 管理用例、查询契约、server/api、OpenAPI、web | 每个子页依赖对应能力；后台列表需新增查询契约 | 隐藏新入口，不删除任务/账单 | 每子批小/中 |
| R07 | 外部 SDK/Runtime | 未来 sdk/runtime/host/UI 桥 | 有独立发布需求，R01/R02 稳定且至少两个消费者 | 禁用扩展、保留原生路径、等待资源退出 | 很大 |

建议顺序：**R00 -> R01 -> R02 -> R03a/b**；R05 的成本研究可提前做，但公共契约/账本写入由集成批次串行。R04 在真实账号来源明确后推进；R06 按已有稳定端口分子批。若优先目标是商业收费，R05a/b 应排在 R03b 前；未收费 WS MVP 不依赖收费后台完成。R07 不作为其他功能前置条件。

R02 不能只做无人消费的框架。至少用可控长寿 HTTP/SSE 资源或 R03a 的真实连接消费者证明发布/排空；只有接口和单元 mock 不算验收。

每一实施批开工时单独列出六项边界，并给出最终签名、错误、权限、取消、寿命、迁移与消费者列表。本文方向批准不自动批准所有后续地基签名；需对具体改动另行确认。

## 7. API、schema 与兼容方案

- 当前源码 OpenAPI 为 0.5.0，63 个路径模板/92 个 HTTP 操作；当前集成分支文件是 `docs/openapi.json`；原分析工作区另有未提交的 `api/openapi.json` 路径迁移，该迁移不混入吸收批次。新 WS/管理能力按实际协议新增描述和客户端回归，不修改已有路径含义。
- 当前统一 migration version 为 15。后续 schema 版本由集成时最新主线分配，不在本提案预占 16/17；只追加迁移，不重写已发行步骤。
- OAuth 新字段/表与计量扩展记录默认 opt-in；先完成读旧/写新兼容，再允许启用。表中不保存明文 token；日志错误采用分类与脱敏细节。
- v1/v2 并存期间只在显式桥转换，不能把新消费者静默落回不支持的旧协议。未声明能力在执行前拒绝。
- 升级前停写备份；数据库升级不可默认支持旧二进制打开。回滚区分“关闭功能”和“恢复旧备份”；不可为降级删除未知任务或未结算 hold。
- 前端 API helper、管理身份、公开模型调用 Key 继续分离。新增页面复用现有请求/错误处理，不在页面重新实现鉴权或多套重试。

## 8. 行为验收矩阵

| 风险 | 必须证明的行为 | 证据类型 |
|---|---|---|
| 权限绕过 | 同 thread/session/response ID 跨 owner/Key 不泄漏、不抢占；每轮撤销生效 | 真实身份+HTTP/WS 集成负例 |
| 路由误判 | 选择失败/准入拒绝无 upstream attempt；绑定目标不可迁移 | 计数上游与故障注入 |
| 事实重复 | 原始事件交付一次，累积用量不重复相加，重复终态不双结算 | codec+执行+账本组合回归 |
| 非确定发送 | 部分写/网络超时/崩溃保持 unknown，不自动重发退款 | 持久派发标记与进程强杀恢复 |
| 热更新泄漏 | 新旧代次并存、失败候选清理、最后使用者退出后关闭 | 并发/race 与资源计数 |
| 刷新冲突 | 一个账号单刷新、CAS 拒绝旧结果、认证/调度状态收敛 | 可控 Provider+真实持久层 |
| 收费正确性 | 用户/Key 同事务，所有 attempt 成本计入，未知不当零 | 崩溃/事务失败/重放/历史价回归 |
| 容量与背压 | 慢客户端/大消息/满队列不无限增长，关闭可界定 | 受限规模压力和资源上界断言 |
| API 回归 | 老 Chat/embeddings/Responses、后台 SSE、前端管理不退化 | 原有全量测试+主程序/浏览器回归 |
| 依赖越层 | L3 不导入实现，L2 无第二执行链，公开类型不泄漏 ORM | architecture 全包与签名检查 |

性能不以源码判断胜负。建立相同模型假上游、相同硬件、相同并发和流速的基线，分别测 p50/p95 选择开销、TTFT、吞吐、峰值内存、活跃 goroutine/连接和退休资源。业务首事件与 HTTP 首字节分开计时。相对阈值需先跑基线再在该批确定，不能捏造当前收益百分比。

## 9. 验证节奏与交付记录

沿用“批量开发 -> 集中验证 -> 集中修复 -> 合并”。权限、迁移、并发资源与缺陷复现可提前定向测；纯说明文档不运行全量 Go/CI。

实现批稳定后在 Windows 设置 `$env:GOARCH='amd64'`，集中执行适用的 build/vet/test、架构/契约、前端与真实主程序冒烟；只格式化本批 Go 文件。Linux 全量 race 和现有完整 CI 在最终代码、最新集成基线上验收。保留 push/PR 自动触发规则，不靠大量细碎推送或删用例绕门槛。

每批记录：范围与非目标、负责目录、依赖版本、最终源 SHA/工作区摘要、实际命令/环境/结果、失败修复、复用证据的原 SHA 与适用理由、未运行项。把本地通过、功能头 CI、合并、合并后 CI、镜像/发布分开记。

现有调度观测上一轮曾记录本地 1222 项测试/子用例、30 包通过（6 helper 跳过），以及 build/vet/前端/主程序恢复冒烟。该段保留最初设计批次的历史来源，不作为新实现证据；随后 R00 已在独立主线基线上重新完整验证并经 PR #24 合并，精确来源见实施记录。R00 提交前须关联原被测摘要/日志，并按最终基线完成剩余验收。

本次文档交付只核对来源、链接与一致性；不将参考项目测试文件存在称为测试已通过，也不将历史阶段文档重新勾选。原 docs 文件已删除，开工约束从当前 HEAD 只读查看，不恢复删除项。

## 10. 暂不吸收与需要后续确认的选择

| 暂不做 | 原因/重新考虑条件 |
|---|---|
| 整体换 PostgreSQL/Redis、微服务或多副本 | 没有本次容量/高可用目标；先保留统一事务和单实例正确性，独立测量后立项 |
| 照搬所有评分因素/缓存体系 | 当前观测数据不足；先有解释和基线，避免规则互相抵消 |
| 无限制 payload、未知字段全放行 | 与 TXAPI 有界资源/strict 证明不相容；只对明确原生透传范围保真 |
| 把所有操作转 Chat | 会丢失原生 Responses/WS/多模态语义；按操作 codec 扩展 |
| 以日/周已用费用代替双 hold | 不能等价保护并发与未知执行；窗口预算需单独设计 |
| 把进程插件当安全沙箱 | 生命周期隔离不等于文件/网络/凭据隔离 |
| 将支付、所有平台 OAuth 与 WS 一次交付 | 权限、资金和资源风险相互叠加，无法独立验收 |

后续实际开工只需逐批确认具体问题：优先原生 WS 还是收费后台；OAuth 第一个 Provider 与合法测试身份来源；WS 需兼容的客户端版本/事件范围；是否确有外部扩展独立发布需求。以上不会阻塞本次源码对比与方案交付。

## 11. 源码证据索引

以下链接对应第 1 节固定本地快照。引用源码表示机制存在，不表示已运行验证。TXAPI 链接指本工作区；参考仓库使用绝对路径，移动目录时应同步索引。先找列出的符号，不依赖未来漂移的行号。

| 编号 | 文件 | 核对入口 |
|---|---|---|
| S01 | [sub2api gateway routes](D:/my-project/TXAPI-references/sub2api/backend/internal/server/routes/gateway.go) | 实际 gateway 注册/平台分流 |
| S02 | [account scheduler](D:/my-project/TXAPI-references/sub2api/backend/internal/service/openai_account_scheduler.go) | OpenAIAccountScheduleRequest/Decision、Select、ScoreSnapshot |
| S03 | [WS execution scope](D:/my-project/TXAPI-references/sub2api/backend/internal/service/openai_ws_execution_scope.go) | resolveOpenAIWSExecutionScope、resolveOpenAIWSExecutionLane |
| S04 | [token refresh](D:/my-project/TXAPI-references/sub2api/backend/internal/service/token_refresh_service.go) | processRefreshContext、providerConcurrencyGate、refreshWithRetry、postRefreshStateSync |
| S05 | [billing service](D:/my-project/TXAPI-references/sub2api/backend/internal/service/billing_service.go) | ModelPricing、UsageTokens、CostBreakdown |
| S06 | [accounts UI](D:/my-project/TXAPI-references/sub2api/frontend/src/views/admin/AccountsView.vue) / [subscriptions](D:/my-project/TXAPI-references/sub2api/frontend/src/views/user/SubscriptionsView.vue) / [payment](D:/my-project/TXAPI-references/sub2api/frontend/src/views/user/PaymentView.vue) | 运营产品参考，非本次运行证明 |
| S07 | [usage billing repository](D:/my-project/TXAPI-references/sub2api/backend/internal/repository/usage_billing_repo.go) | Apply、claimUsageBillingRequest、Reserve/Capture/ReleaseBatchImageBalance |
| S08 | [scheduler cache](D:/my-project/TXAPI-references/sub2api/backend/internal/repository/scheduler_cache.go) | 调度缓存持久适配边界 |
| C01 | [architecture](D:/my-project/TXAPI-references/codex-proxy-rs/docs/architecture.md) | 单副本声明、模块/插件边界、发布代次、关闭职责 |
| C02 | [OpenAI router](D:/my-project/TXAPI-references/codex-proxy-rs/backend/crates/gateway-api/src/openai/router.rs) | responses GET/POST、图片/搜索/模型/Live 路由 |
| C03 | [event](D:/my-project/TXAPI-references/codex-proxy-rs/backend/crates/gateway-core/src/event.rs) | GatewayEvent、ProviderEvent、ProtocolWireEvent |
| C04 | [continuation](D:/my-project/TXAPI-references/codex-proxy-rs/backend/crates/gateway-core/src/engine/continuation.rs) | NativeContinuationScope、NativeContinuationPin |
| C05 | [snapshot](D:/my-project/TXAPI-references/codex-proxy-rs/backend/crates/gateway-core/src/routing/snapshot.rs) | RuntimeSnapshot、编译和 revision 稳定性校验 |
| C06 | [budget](D:/my-project/TXAPI-references/codex-proxy-rs/backend/crates/gateway-core/src/engine/budget.rs) | ClientBudgetPort、ClientBudgetCharge；无预扣/缺费用按零的契约边界 |
| C07 | [plugin SDK](D:/my-project/TXAPI-references/codex-proxy-rs/backend/crates/gateway-plugin/sdk/README.md) | 实验状态、宿主能力与内置 OpenAI/xAI 上游适配合同 |
| C08 | [architecture tests](D:/my-project/TXAPI-references/codex-proxy-rs/backend/apps/gateway/tests/architecture.rs) | workspace 依赖约束；本次未执行 |
| T01 | [server](internal/server/server.go) / [OpenAPI](docs/openapi.json) | 当前实际路由和 0.5.0 契约 |
| T02 | [protocol registration](internal/protocol/registration.go) | 操作/协议注册 |
| T03 | [selection](internal/gateway/selection.go) / [routing tests](internal/metrics/routing_test.go) | chooseTarget、recordRouteDecision、固定标签与并发测试 |
| T04 | [execution v1](internal/foundation/contracts/execution/v1/execution.go) | Phase、Result、Outcome、Output、Executor |
| T05 | [protocol contracts](internal/foundation/contracts/execution/v1/protocol.go) | Frame、Observation、Adapter、OperationRegistration |
| T06 | [background contract](internal/foundation/contracts/background/v1/background.go) | Service、Resources、阶段与查询/取消/事件边界 |
| T07 | [background runtime](internal/generation/background_runtime.go) | 持久原生后台 runtime；不等同于 WS 连接状态 |
| T08 | [configuration](internal/management/configuration.go) / [public contract](internal/foundation/contracts/configuration/v1/configuration.go) | publication、SavedError、Apply、desired/applied revision |
| T09 | [strict billing](internal/billing/strict.go) / [tariff profile](internal/billing/tariff_profile.go) / [settlement](internal/billing/settle.go) | 当前 strict 权威、费率绑定与收尾 |
| T10 | [layer registry](internal/architecture/layers.json) / [visibility](internal/architecture/visibility_test.go) | 四层与公开类型可见性 |
| T11 | [health](internal/gateway/health.go) | observeHealth 的 credential generation/材料/状态复核 |
| T12 | [WS transport](internal/transport/websocket/transport.go) / [core](internal/core/core.go) | 有界服务端 I/O 与当前单次 HTTP 执行边界 |
| T13 | [migrations](internal/store/migrations.go) | 当前 v15，统一追加迁移 |

## 12. 评审后的第一个具体动作

先将 R00 现有观测改动按独立批次收口，确认最终工作区范围与基线，完成匹配头的完整 CI/合并；不要把用户文档删除或其他未提交改动混入未经确认的提交。
然后产出 R01 的具体签名和消费者迁移清单：只以一个真实 HTTP/SSE 用例证明“执行事实 + 单次原始交付 + 未知用量”合同，不在同一批实现 OAuth、WS、支付或外部插件。它是后续演进的最小公共基础，而不是另开一次全仓重构。
