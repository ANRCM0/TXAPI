# FC-001：四层地基契约与渐进迁移提案

状态：**用户已批准；FND-01 至 FND-05 已依次实施并验收，v1 已冻结**。日期：2026-10-18。
最终代码基线 master/889ecbd（PR #18，功能头 38a8cce）；精确 CI、逐层 DoD 与冻结范围见 [实施记录](../history/implementation-fc001.md#fnd-05-最终-ci合并与冻结验收) 和 [v1 接入清单](../reference/foundation-integration-v1.md)。以下保留批准时边界和原始差距，不把历史状态当作当前源码。
核对基线：`master/4312f774e48ad8f5066f1e238589666549b3a533`，开工 fetch 后未漂移。
批准记录：2026-10-18 用户明确指示“好批准实施提案。请你按照顺序完成提案”。
批准覆盖本文件的四层边界、中立契约、分批迁移与验收约束；范围内实现继续推进，
范围外破坏性变更仍另提请求，不因批准实施就标记接口 frozen。

活动排期仍只维护在 [PLAN](../PLAN.md#地基符合性收口fc-001)。
原 A1–A4、S6/M1、A01/A02/M2 的实现与验收事实保留；它们不等于本提案的四层 DoD 已通过。
本文只拟定仓库内模块公开契约，不要求拆仓、发布 SDK、换前端或引入动态插件加载。

## 1. 本批任务与六项边界

| 必填项 | 本批范围 |
|---|---|
| 所属层级 | FND-00 是 L1/L2 公开边界的提案与治理准备，同时明确 L0/L3 归属；尚不修改运行代码 |
| 依赖的公开接口 | 以现有 `Gateway.ExecuteTo`、`core.Output`、Adaptor/Capability、身份解析、账务端口、HistoryStore 和配置用例为迁移来源；它们仍是旧契约，不宣称已经冻结为 v1 |
| 是否触碰地基 | 本批只新增提案与流程说明；后续 FND-01 至 FND-05 会改变地基内部依赖和模块接口，必须在本提案确认后分批实施 |
| 新增/修改契约 | 拟新增按能力分组的 L1 v1 契约、L2 操作/协议注册契约、模块清单和地基变更流程；外部 HTTP/OpenAPI 与持久格式保持既有语义 |
| 测试与验收 | 本批只检查 Markdown 差异、链接/锚点和状态一致性；后续每批按实际风险集中验证，最终依赖/契约、完整 Linux race、冒烟与容器交付通过后合并 |
| 对并行模块的影响 | FND 收口前暂缓新 A03/A04 功能接线；现有功能缺陷可独立修复，接口变更由集成单点协调。FND-05 冻结并合并后，L3 才按已验收契约并行开发 |

## 2. 目标依赖与职责

依赖指**生产代码的编译期依赖**，不把 HTTP 请求流向等同于包依赖。
功能层可以使用 L1/L2 的公开契约；禁止跳到其实现包或 L0。
L2 通过 L1 定义的接口/注册回调调用已组装的功能服务，不导入 L3 实现。
L1 通过自身定义的端口消费 L2 提供的协议实现，不反向导入这些实现。
这些回调的运行时调用不改变编译期的向下依赖方向。

```text
L3 功能实现 -> L2 公开兼容契约 / L1 公开基础契约
L2 兼容实现 -> L1 公开基础契约
L1 框架实现 -> L1 契约 / L0 公开执行机制
L0 内核     -> 标准库
```

| 层 | 拟定责任 | 当前来源及迁移边界 |
|---|---|---|
| L0 内核 | 单次网络执行、取消/截止时间、流式背压、提交事实和原始执行结果 | 保持现有 `internal/core` 的小内核。用户/Key、渠道选择、计费、会话和任务策略不移入 L0；现有调度/worker 属于 L1 通用能力 |
| L1 基础框架 | 公共类型与端口、生成协调、身份与准入、调度、账务、持久化、密钥、配置发布、观测和生命周期 | 从 gateway/access/scheduler/billing/generation/store/secrets/config 等现有实现提供受限门面。公开契约不引用 core/protocol/store/model/GORM 的实现类型，不以移动目录替代隔离 |
| L2 兼容层 | 入站 HTTP/API 表示、上游协议编解码、能力/错误/事件映射、纯传输适配及前端调用契约 | 从 server、protocol、transport、gateway HTTP handler 与 Responses HTTP 表示中分离；网络派发仍由 L1 计费/attempt 边界控制 |
| L3 功能层 | Responses 幂等/会话/历史、配置管理业务、用户/Key 管理用例及后续后台产品等 | 现有 responses/management/server API 用例逐项分类；通用 worker/AEAD/账本不因被某功能消费就变成 L3。功能间只通过登记的接口/事件协作 |

L1 的持久实现可按 L1 端口调用数据库，L2 的协议/传输实现可隔离外部系统差异。
禁止 L3 自建数据库连接、直接 HTTP/WS 调上游、导入 ORM 或复制鉴权/账本。
迁移时必须登记每个生产包的层级，不能把混合职责整包标成 L1 来消除违规。

`cmd/gateway` 是唯一组装根，可导入各层构造器并注入实现；不放功能逻辑，
不能以它作为功能调用底层的转发后门。集成测试/示例可以组装多层，
但业务模块自己的源码仍必须满足依赖限制。独立 Go module/物理目录重排不是验收前提。

## 3. 源码差距与拟定处置

以下是 `4312f77` 的已知差距，不是本批已修复项。

| 编号 | 当前证据 | 处置与验证批次 |
|---|---|---|
| D01 | [Responses 应用](../../internal/responses/service.go) 的 Executor 签名暴露 core/gateway 类型，并调用 `core.NewHTTPOutput` | FND-01 提供 L1 执行契约与 L2 HTTP 输出适配，功能只接受注入的输出；保留 JSON/SSE、提交与短写回归 |
| D02 | [公共 Execution](../../internal/gateway/coordinator.go) 暴露 `protocol.Operation`、`ResponseTarget`；[RequestResult](../../internal/gateway/instance.go) 嵌入 `core.Result` | FND-01 建立中立公开值类型与显式桥接；禁止用公开 type alias 把实现类型泄漏到 v1 |
| D03 | [gateway relay](../../internal/gateway/relay.go) 导入 Responses Observation，按操作解释终态/重试；[protocol.Request](../../internal/protocol/request.go) 用固定 switch 选择操作 | FND-02 用真实注册操作与协议会话承接差异；L1 统一执行安全规则，未知操作拒绝，不能默认回落 Chat |
| D04 | [gateway handler（4312f77 原始差距）](https://github.com/ANRCM0/TXAPI/blob/4312f774e48ad8f5066f1e238589666549b3a533/internal/gateway/handler.go) 同包保留 Gin 入口；Responses 同包同时有 HTTP、业务与存储编排 | FND-02/FND-04 拆清兼容入口和功能用例，让框架实现不导入 Gin 或 L2 包；HTTP 成功码/错误/头保持 |
| D05 | [ConfigurationStore](../../internal/management/configuration.go) 的参数使用 `store.ChannelInput` / `CredentialInput`；若干 server API 直接调用 store/billing 实现 | FND-03/FND-04 迁移公开 DTO 与受限服务端口，保留当前事务/发布排序、角色与 owner 检查 |
| D06 | [HistoryStore](../../internal/responses/history.go) 消费带持久模型的快照和限额；加密正文记录保存目标字段 | FND-03 使用无 ORM/实现依赖的状态 DTO/小端口，桥接现有事务；既有密文、关联数据和 JSON 字段仍可读取 |
| D07 | [依赖检查](../../internal/architecture/dependencies_test.go) 明确允许 responses->core、gateway->protocol；只覆盖旧模块边界 | FND-01 开始禁止新依赖，FND-05 收紧最终四层规则；既有债务逐项列出路径/目标/退出批次，不新增大范围豁免 |
| D08 | [内部端口说明](../reference/foundation-contracts.md) 是已验收内部约定；尚无四层接口冻结和独立 L3 接入样例 | FND-05 完成版本清单、变更流程与独立模块实例；旧完整 CI 不当作新 DoD 的通过证据 |

## 4. 拟定公开契约

以下保留提案获准时的路径和边界；最终已冻结的真实 API 路径、版本、消费者与签名见 v1 接入清单。
第一批只实现有真实消费者的 execution/identity 接口，后续小组按对应消费者扩展。
不建立包含所有厂商字段的公共大对象，也不增加任意代码执行、插件市场或通用分布式平台。

| 能力组 / 拟定路径 | 公开内容 | 必须隐藏的内容 |
|---|---|---|
| `internal/foundation/contracts/execution/v1` | Operation、Invocation、Output/Metadata/OutputState、Outcome/AttemptOutcome、受限目标连续性约束、Executor；元数据与 byte slices 按所有权复制 | core.Executor/Input/Result、gateway 内部 scope、协议 DTO、凭据原文和财务内部对象 |
| `internal/foundation/contracts/identity/v1` | 当前主体解析/能力校验与不可由请求 body 伪造的认证上下文；绑定 ID 只用于重新解析当前权限 | token/用户 ORM 对象、任意设置可信 principal 的公开构造器、静态权限快照作为永久授权 |
| `internal/foundation/contracts/state/v1` | 实际消费者需要的快照、CAS/守卫、限额、TTL、领取/fencing、原子意图/预算等小端口，按文件/接口分别定义 | ORM/SQL/事务回调、任意数据库句柄、第二套余额或由功能自行拼接跨财务事务 |
| `internal/foundation/contracts/configuration/v1` | 无 store 类型依赖的配置输入、保存/发布结果与重新 Apply 契约 | store 实现 DTO、运行缓存指针或功能自行发布快照 |
| `internal/foundation/contracts/lifecycle/v1` | 真实使用的启动/停止/观察取消约定、有限关闭和资源归属 | 任意模块启动未登记 goroutine、功能直接关闭共享 core/client/worker |
| L1 的操作/协议端口，随 execution v1 定义 | 操作校验/能力描述、准备后请求的中立表示、每次 attempt 独立的编解码/观察会话 | 某厂商 Response 或 Chat DTO、Gin/HTTP writer、回调中直接发起网络或记账 |
| L2 公开接入契约 | 入站 handler/输出工厂、上游 codec 注册与声明清单，内部 HTTP 表示与能力适配 | 下层导入具体厂商包、功能直接构造底层输出或识别厂商名称 |

新契约应做到：公开签名递归可达的字段/方法/泛型参数均不泄漏禁止类型；
只检查 import 或把 core 类型改名导出不算完成封装。
现有使用已导出 core 接口的消费者是迁移来源，不一概认定为复制或篡改内核；
目标是让新功能只面对 L1/L2 公开契约。

### 4.1 执行入口与输出的行为草图

下列是本次提案的接口形状与行为边界；Go 声明和实际消费者在 FND-01 同批评审，
类型文件拆分、私有桥接等已确认范围内的例行细节不重复索要许可。若必须改变下列
所有权、安全/财务语义或新增范围外能力，先补具体变更请求；不能用 `any` 或任意 map 绕过评审。

```text
Executor.Execute(ctx, Output, Invocation) -> Outcome
Output.Commit(ctx, Metadata) -> error
Output.Write(ctx, bytes) -> (written, error)
Output.Flush(ctx) -> error
Output.State() -> OutputState
```

Invocation 只提供操作、原始载荷、受限调用选项和目标连续性要求。
认证由 L1 提供并在每次生成/派发检查，不能通过 Invocation 中的 user_id、Key ID、
操作声明或上次结果获得执行许可；缺可信身份、撤销或模型无权时在发送前拒绝。
目标约束只能缩小已授权候选集；不能指定新账号、放大权限或为重试重新授权。

Outcome 必须保留 phase、实际/已公开 HTTP 状态、单调 committed、协议完成、
终止原因、usage 的已知/缺失、尝试事实、耗时/字节数、目标/价格/财务关联；
协议终态与账务终态分开。不得因改类型丢掉未知执行或误报已完成。
协议特有 item、原生 response ID/状态通过经登记、类型明确的操作结果返回，
不让 L1 通过断言某厂商 Observation 类型解释它。

Output 按一次执行持有，调用者控制寿命，attempt 不关闭共享资源；
Commit/Write 失败后也要报告已经发生的提交，短写必须成为错误，取消和写入均有界。
L2 HTTP 输出构造器负责标准 writer 和可选写 deadline，L3 只接收 Output；
不得在业务处理中重新创建 core.HTTPOutput。适配桥需要显式转换这些值，不能先吞错再造成功。

### 4.2 操作与协议注册

拟采用启动时、复制后只读的注册；重复 ID、typed nil、缺少版本/必要编解码或不兼容版本拒绝启动。
操作 ID 表示语义，HTTP 路径、目标厂商、传输和计费许可分别声明，不能混成一个开关。
不自动根据操作注册生成公开路由；新增外部路由仍要同步 OpenAPI、鉴权和消费者。

L2 实现操作校验、能力映射和独立 attempt 编解码会话；L1 负责身份、准入、选目标、
准备后安全/成本检查、持久 attempt、派发、收尾。准备/解码回调不能直接访问外部网络、
关闭资源或写财务表。编解码会话每次 attempt 新建，不能跨请求保留解析/usage 状态。
注册回调是受信任的编译期代码，不是可运行不可信插件的安全沙箱。

协议适配可以声明不支持、不可重放或更严格的限制，不能单凭声明放开 strict、
证明上游幂等或允许 unknown 后重发。重试仍受 L1 已提交/已派发事实、财务包络与策略约束。
Responses 当前 strict 全拒绝，Chat/embeddings 的既有许可/拒绝范围不因注册改造扩大。

### 4.3 版本与变更治理

每组公开契约记录：接口版本、所有者、导出符号、DTO/错误/资源规则、支持范围、
消费者与契约测试位置、弃用/迁移方式及验证提交。版本不替代 schema/OpenAPI/密文格式版本。
`v1` 只有在实现、消费者、文档和匹配提交 CI 都通过后才标记 frozen；不能由本草案提前冻结。

新增可选能力也要核对零值/缺省行为；给现有 Go interface 增加方法可能破坏实现者，
不能仅因“新增方法”就记为兼容。优先独立可选小接口，破坏变更提出 v2 或有期限的显式桥接。

地基变更请求必须包含六项任务边界、问题/替代方案、精确签名与语义差异、所有消费者、
存储/配置/外部协议影响、迁移与回退、验证和并行影响。
流程为 draft -> 用户明确确认 -> 分批实施 -> 验收/合并 -> frozen；
确认必须针对具体提案，不能由代理自己把状态改为 approved。
范围外的破坏性变更回到提案流程；已确认范围内的例行实现不重复索要许可。

## 5. 分批迁移与不变约束

以下为批准时的分批范围；现已全部按顺序完成验收，精确实现/CI/合并记录见上述实施记录。
每批开工都从最新已验收 master 核对消费者，不并行改共享类型、注册点、迁移或 CI。

| 顺序 | 负责目录与依赖 | 交付与退出条件 |
|---|---|---|
| FND-00 | docs/proposals、PLAN/ARCHITECTURE/COLLABORATION/DEVELOPMENT、AGENTS、README | 本提案、差距清单和前置变更流程；确认前不改 Go/OpenAPI/schema/运行配置，不表示四层地基已完成 |
| FND-01 | 新 L1 execution/identity 契约、L1-core 桥、L2 HTTP 输出、现有 gateway/responses/generation 消费者；集成协调 main | 最小公开执行门面真实运行，移除 L3 的 core 类型/构造依赖；现有 JSON/SSE/worker/归档和身份回归保持。旧入口仅供清单内迁移消费者，不接受新依赖 |
| FND-02 | gateway、protocol、server/gateway HTTP 接入、main 注册 | 操作和上游协议通过 L1 中立端口注册，L1 无 Gin/具体 protocol/Responses Observation 依赖；Chat/embeddings/Responses 都经同一执行协调，未知操作与缺能力拒绝 |
| FND-03 | state/configuration/lifecycle 契约与 store/billing/generation/management 桥 | 移除公开 store/ORM/财务实现类型；原事务、CAS、fencing、hold、价格、密钥与关闭顺序保持。任何暂不能移除的引用明确记债，不提前宣布整体完成 |
| FND-04 | responses、management、server API、web 契约与 main | 所有现有功能用例与外部表示明确分开；清除直接实现依赖和跨功能强耦合，既有 91 个 HTTP 操作与管理员/用户/Key 行为保持 |
| FND-05 | architecture/契约测试、独立样例、文档和完整 CI | 所有层级有包级归属，禁止边界零违规/零未清例外；独立 L3 样例只用公开契约运行；L0/L1/L2 分别通过 DoD，版本冻结并合并后才开放新功能并行 |

迁移允许短期桥接，必须指定仅存消费者与退出批次；不能用 type alias 或无限期豁免固化旧依赖。
每批保留完整行为回归，现有消费者在同一批更新，不给其他模块留下无法编译的公共基线。
不得同时维护两个真正执行/结算的协调器；桥只做表示转换，调用原执行链一次。

外部 API 路径/状态码/错误/头、默认开关、OpenAPI 0.4.0/91 操作、schema v12、
既有密文/关联数据、快照/账本/幂等/已发布迁移保持兼容。旧读取 DTO 可以桥接，
不能重写密文格式或把 ResponseTarget 重命名导致历史无法续链；若无法保持，必须另提迁移请求。
不操作真实库或真实上游，不新增支付/账号池，不由本轮宣称 A03/A04 已实现。

回退以每个可验收提交为边界：没有持久格式变化时可切回前一匹配版本；
若发现需要 schema/密钥/持久格式变化，停止依赖迁移，单独评估停写备份与恢复。
不得自动重发 unknown、退款、释放原 hold 或删除旧迁移来制造回退成功。

## 6. 地基 DoD 与证明

| DoD | L0 核验 | L1 核验 | L2 核验 |
|---|---|---|---|
| 公开接口与版本 | 标记 L1 可消费的执行/输出/解码边界；其余实现对上层不可达 | 能力契约 v1 清单、消费者/弃用策略、无实现类型泄漏 | 入站/上游/传输/前端契约与支持矩阵，版本和 L1 接口一致 |
| 自动化关键路径 | 超时/取消/短写/背压/提交单调性 | 授权、配额、attempt/结算、CAS/fencing、未知执行与资源释放 | JSON/SSE/usage/错误/工具保真，未知与不可表示语义拒绝 |
| 最小可运行示例 | 隔离 loopback 的单次执行，L1 负责组装 | 通过公开 Executor/Output 完成一次真实生成协调 | 注册一个测试 codec、用真实 HTTP 消费者验证公开接入 |
| 扩展点说明 | 输入/输出与 decoder 的责任和寿命 | 能力注册、状态端口、配置/生命周期的接入说明 | 新操作/上游/HTTP 表示的最小目录与接入步骤 |
| 错误/日志/配置 | 原始提交/终止事实不丢失 | 缺能力、存储不可用、unknown、幂等与账务错误规则；日志脱敏，默认保守 | 原生失败和 HTTP 200 的区别、兼容错误映射、配置拒绝规则 |
| 限制与未实现 | HTTP 内核范围与独立 WS 执行边界 | 单实例、容量/TTL、strict、未交付产品边界 | 支持的操作/协议/传输/客户端及不支持项 |
| 上层样例 | L3 样例无法导入 L0 | L3 样例只引用已列公开契约，禁止实现包与外部 I/O | 样例以注册与明确契约接入，不修改框架源码 |

样例不能只 mock 整个框架或只证明接口能够编译：需要真实 L1 协调、身份/准入、
受限假上游和输出检查；既有财务行为复用并运行适用回归，不能另建样例账本。
样例自身生产文件与完整组装/测试 harness 分开，禁止测试例外掩盖生产跨层依赖。
至少一个新增操作 ID 经注册可执行而无需编辑框架的操作 switch，缺注册时明确拒绝。
该测试操作不自动成为生产公开 API，不扩大收费或支持矩阵。

架构检查至少覆盖所有生产包的层级与公开入口、L0/L1/L2 向上依赖、L3 直达 L0/实现包、
跨 L3 模块依赖、L1 的具体厂商依赖、L3 的 HTTP/WS/ORM 外部 I/O、公开签名泄漏。
通过 AST/type 信息和真实消费者行为证明，不以函数名/目录名或 type alias 冒充隔离。
所有负面规则要有会被拒绝的受控样例，避免只对当前源码返回绿灯。

新增规则可在迁移中有精确已知债务清单，但现有检查和 CI 门槛不降低；
FND-05 全部清零后才能宣称这套四层地基可冻结。全量、race 与冒烟按集中验证节奏执行，
不因每次文档更新重复手动跑 CI，也不把旧 S6/A02 的绿灯当作迁移后通过。

## 7. 提案交付与批准记录

FND-00 提交时仅提供可评审文档与 Agent/协作流程，当时尚未运行 Go 行为测试、修改接口、
创建注册实现或标记地基完成。文档验证的精确命令与实际结果见
[PLAN 的 FND-00 记录](../PLAN.md#地基符合性收口fc-001)。

用户在批准时确认按第 2 节职责/依赖、第 4 节中立公开契约与第 5 节分批迁移推进，
先实施 FND-01；保留小 L0，不将用户/账务/会话策略整体搬入内核。
具体批次按六项边界开工，新增范围外破坏变更另提请求。
