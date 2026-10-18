# 多团队开发与框架基线

2026-10-18，[FC-001 地基提案](proposals/FC-001-foundation-contracts-v1.md) 已按用户批准的 FND-01 至 FND-05 全部完成，代码基线 `master/889ecbd` 经完整 CI 验收，L0/L1/L2 v1 已冻结。冻结记录合并后，L3 按独立目录和已登记公开接口并行；共享入口、注册、迁移、CI 和地基接口仍单点协调，变更先提案确认。公开入口/样例/DoD 见 [v1 接入清单](reference/foundation-integration-v1.md)，活动排期以 [PLAN](PLAN.md#地基符合性收口fc-001) 为准；A03/A04 与 S5 成本门槛仍未完成。

本页定义当前团队边界、可依赖的接口与集成门槛，并保留初始协作框架 F0 的验收记录。核对日期：2026-10-18；S6/M1 已验收并经 PR #8 合并为 `master/ca009c2`，功能头 `9a6eaa2` 的 push/PR 与合并后 master 完整 CI 均通过，精确证据见 [S6 记录](history/implementation-s6.md) 和唯一活动计划 [PLAN.md](PLAN.md)。A1–A4 原验收及 S5 计费安全门槛保留。架构仍是模块化单体；“多个团队”指开发分工，不会给产品新增团队/组织租户模型。

2026-10-18，S7-A01 已完成独立验收：PR #10 将功能头 `3195d0923d957eb5f41dde5412912d10e72259f7`（基线 `bea97fa`）合并为 `master/a925140ab86fca780a71ef6956a45e0e6af38658`；push `39073160376`、PR `39073164111` 与合并后 master `39073532710` 均为 check + delivery 全部成功。精确修复、命令与范围见 [S7-A01 功能验收](history/implementation-s7-a01.md#2026-10-18-s7-a01-functional-acceptance)。默认关闭的前台 Responses 在 legacy/disabled 可用；尚无已证明的 strict 子集，全部 Responses 在计费前拒绝。该段保留 A01 原验收范围；A02 现已由 PR #12 独立验收合并为 `master/765501a`，M2 已完成。后台/原生 WS 与 S5 成本门槛继续独立验收。

F0 是已合并的初始接线基线，不是最终统一兼容框架。此后 S3/S4 已验收，S5 仍有成本边界缺口；已合并的实现已把 Chat/embeddings 业务协调迁出 Gin，Gin 仅保留接入适配；Chat/embeddings 已有独立载荷、共享操作信封及发送准备的能力检查；Chat codec 不承担其他操作，S6 验收时独立 Responses 模型未接入生产；默认关闭的 S7-A01 前台 HTTP/SSE 已完成独立 CI/合并验收。A3/A4 已收口为原共同基线；S6 进一步交付现有协议/工具、管理 API、扩展和前端的实际契约闭环。S7-A 的 Responses/任务/原生 WS 接线复用已验收的 M1 基线，按新操作单独验收。仓库内部 Go 接口不是公开 SDK；共享约定的修改需要调用方一起评审。

极简 core 不承载用户、预算、会话或任务。功能接入复用同一授权、准入、调度、结算与收尾，不复制另一条业务链；普通函数调用与确有需要的小接口优先，不预建空模块或动态插件平台。

2026-10-18 的职责澄清：core 处理中转；gateway 与身份、调度、计费、状态服务组成业务层；兼容层覆盖 server/protocol/transport 的对外适配、扩展端口及前端应用契约。`web` 经公开应用 API 使用业务能力，页面不能成为授权/账本/恢复的权威。该分工不要求合并成三个大包；现有 server/store 业务调用按真实消费者核对，必要时提取小入口，不能仅凭目录名称宣称已完成分层。

三层实际消费者、已支持组合、构造注册与资源责任见 [兼容契约清单](reference/compatibility-contracts.md)。新增协议沿用 `gateway.Options.Protocols/Operations` 与独立流 session，管理配置复用 `management.Configuration`，页面消费现有应用 API；共享端口变更连同调用方评审。S6/M1 已完成集成验收，S7-A01 Responses HTTP/SSE 已完成独立 CI/合并验收；同类项目能力按具体目标通过这些契约接入。

## 1. 可以并行推进的分工

S7-A02 已从 `master/04fbfa6` 经 PR #12 独立验收合并为 `master/765501a`；功能头 `2084bcd` 的 push/PR 与合并后 master 完整 CI 均通过。Responses 现通过 state/v1.HistoryStore/ReceiptStore 和注入的 ContentProtection 消费统一 v12/CAS/当前授权与既有 AEAD，不导入 store/model/secrets；gateway 只增加原目标约束，继续不依赖 responses/store。共享 main/config/路由/OpenAPI 已同步为已验收 0.4.0 / 91 操作。权限、存储/删除/CAS 与限制见 [历史契约](reference/responses-history.md)，精确验收见 [A02 记录](history/implementation-s7-a02.md#2026-10-18-s7-a02-functional-acceptance)。A02/M2 已完成；下一批 A03 仍须独立验收。

| 责任角色 | 主要写入目录 | 受控工作范围 | 需要与集成负责人协调的文件 |
|---|---|---|---|
| 平台/集成 | `cmd/gateway`、`internal/core`、`internal/gateway`、`internal/architecture` | 公共契约收口、持久执行边界、取消/关闭与依赖验收 | main、gateway.Options、CI、迁移版本表、OpenAPI |
| 协议团队 | `internal/protocol/adaptor/<provider>`、协议样本/测试 | 新厂商转换、协议能力/错误映射、独立 SSE 解析器 | `gateway.Options.Protocols/Operations` 的生产注册，由集成团队接线 |
| 身份与存储团队 | `internal/access`、`internal/store/access*`、相关 API 功能切片 | 用户/Key 生命周期、权限收敛、owner 校验、迁移回归 | `internal/model`、`internal/store/migrations.go`、OpenAPI |
| 密钥与运行安全（集成协调） | `internal/secrets`、`internal/store/credential_keys*`、generation 内容保护 | 标准库加密/挂载密钥解析，store 负责转换与生命周期；不新建鉴权/迁移 | 共享密文版本、model/store 迁移、main/config、保留与恢复规则 |
| 调度与准入团队 | `internal/scheduler`、自己的准入实现包、相关测试 | 维护已验收的 S3 准入/租约，按独立批次扩展调度和负载回归 | Admission 接口、gateway.Options 和限额配置/模型字段 |
| 管理 API 团队 | `internal/management`、`internal/server/api/<feature>.go` 与对应 store 方法 | 管理用例负责保存/发布，HTTP 表示与既有身份/事务边界分工；按资源补契约与隔离回归 | RegisterAccess/registerResources 注册点、OpenAPI、依赖声明 |
| 前端团队 | `web/` | 原生 ES Modules 控制台和真实 API 联调；Go embed 随二进制交付，无单独资产构建 | 静态资源路由、CSP、HTTP 契约与生产接线 |
| 测试/交付团队 | `tests/`、部署脚本、恢复说明 | 模拟上游、启动/升级/恢复、API 工作流、性能基线 | CI 合并门槛和数据库迁移用例 |
| 状态/传输功能团队（S7-A） | `internal/responses`、WS transport、相关测试 | A01 前台应用与持久幂等已验收合并；GET/续链/会话/后台/原生 WS 后续单独接线 | 持久端口、账本原子边界、生命周期与依赖表 |

表内是责任角色，不是已指定人员。当前只允许按已稳定边界分工，不表示已经开放全部状态型功能并行改共享入口。2026-10-17 核对仓库未见 CODEOWNERS；可读取的分支规则为空，经典分支保护因权限不足未确认，不能宣称强制评审/必需 CI 已生效。GitHub 成员权限、真实 CODEOWNERS 和分支保护仍由维护者落实，本次不修改远端治理设置。

工作切片须有独立写入范围；例如不要让多个团队同时修改 `api/access.go`。新 API 优先放自己的资源文件，由集成人员合入注册点。团队开始前在任务/PR 写明依赖的接口版本与写入目录，不以跨目录复制实现绕过接口。

## 2. 已实现的接口与生命周期

### 身份边界：internal/access

- L1 内部通过私有 access context 传递可信身份；L2 Auth 只消费 `identity/v1.Authenticator` 返回的已认证 context 和 TokenView。L3 通过 Authority.Current/Refresh 消费当前身份，不导入 access 或自行用 Principal 值构造身份。
- accessor 对模型切片做拷贝，不能通过修改返回值污染其他请求；前端 body/header 里的 user_id 不构成身份。
- Request ID 使用 `observability/v1.WithRequestID` / `RequestID`；旧 access 入口只转发相同规则。ID 只关联日志，不作为共享流状态或授权依据。
- gateway 不再导入 `internal/server/middleware`。身份/准入团队无需依赖 Gin 的字符串上下文键；现存 Ctx* 镜像只用于旧中间件兼容，新代码不能新增依赖。
- 管理登录会话与调用身份始终分开；普通 Bearer API Key 不能当 session 使用。会话角色/owner 的细节由 server/access 接口处理。
- `access.Binding` 仅保存来自认证边界或可信任务记录的 user/Key 绑定，不携带原始 Key、权限快照或管理员替身。A3-01 的 `Store.ResolvePrincipal(ctx, binding)` 与 HTTP `LookupToken` 共用同一事务内的当前 owner/Key/系统权限和限额投影；失效与存储不可用可区分，但都不授权执行。事务查询不延长到上游 I/O，也不表示后续在飞请求可被瞬时撤销。

### 请求执行：internal/core

`Executor.ExecuteTo(ctx, output, input)` 只执行一次 HTTP 尝试；`Execute(ctx, writer, input)` 是 HTTP 适配入口。Output 是执行独占、同步且有界的 Commit/Write/Flush/State 端口，不关心用户/渠道优先级/计费。core 仅依赖标准库；protocol 不依赖 HTTP 框架、数据库、调度和计费。

`core.Result` 的 StatusCode/Termination 是执行结果，HTTPStatus 是已写入的 HTTP 状态（非 HTTP 输出为 0），Committed 是不可逆输出提交事实，两者不能混为一谈。流已提交后无法把 HTTP 200 改为 502，也不能重试或追加正常结束标记来假装成功。

### 类型化协调入口（A1–A4 基础契约已验收）

`execution/v1.Executor.Execute(ctx, Output, Invocation)` 是功能消费者的冻结 v1 入口，返回中立 Outcome；gateway.ExecuteTo/RequestResult 仅是 L1 内部协调和组装迁移接口，不接受新 L3 依赖。调用方必须在**每次生成前**重新验证身份有效性，不能复用长连接初始认证快照承诺持续授权。公开入口拒绝无 user/key 身份，HTTP 适配保留已有认证中间件。

Chat/embeddings HTTP 与公开入口调用同一非 Gin 协调函数，共用模型授权、请求/attempt 准入、预留、路由、重试和收尾。普通公开执行与内部 ExecuteTo 每次屏蔽父 context 的旧 RPM/TPM 准入和 mutable reservation。组装根只给 HTTP 兼容入口注入 NewHTTPExecutor：以私有、绑定当前可信 Key 的原子一次性标记接管中间件已经完成的 RPM/TPM；复用父 context 或并发消费只能有一次接管，其余重新准入。Invocation 没有可伪造的跳过准入字段。结果与流解析状态不按外部 Request ID 共享。亲和文本由 operation Request 投影，调度器只计算原有 owner/Key 摘要，不解析协议消息。FND-02 协调链只消费已注册的中立 Parser/Request；内建 ParseRequest 留在 L2 工厂；路由/准入/attempt 与 `PrepareRequest` 使用同一操作事实，结果显式记录 Operation。每次准备只把副本交给适配器，不能修改原信封或下一次重试。`model.ModelRequest` 与整数 Mode 仅在旧 Relay/适配器边界保留；旧投影必须匹配正文，未知模式拒绝。

HTTP 入口通过 compat/httpoutput 返回公开 Output，L1 版本化门面内部桥接原 core.HTTPOutput；协调 scope 不持有 `http.ResponseWriter`。非 HTTP 输出的 HTTPStatus=0，Committed 仍约束重试；提交失败、短写或 flush 失败不惩罚上游、不改目标重试、不补造成功终态。端口仅输出已编码 JSON/SSE，尚不是独立的协议事件模型或 WS executor。元数据含 ContentType/StatusCode 和重试/账单等键值，HTTP 适配为响应头。调用者负责出口关闭与每次生成的独占实例，禁止复用已提交出口。`protocol/responses` 已提供独立原生请求/item/JSON/SSE 编解码，A01 通过 `internal/responses` 应用注册 `POST /v1/responses` 并消费同一 gateway；它不是持久任务、历史/续链或 WS 应用。`core.Phase` 仅说明本地观察，ProtocolComplete 只说明协议解析终态，不替代操作成功或财务终态。

### 最小 worker、输出记录与意图：internal/generation（A3-01 至 A3-05 基础消费者）

L1 generation 通过公开 execution/Parser 和 state/v1 消费执行与持久快照，不导入 core/gateway/protocol/model。access/secrets 仅为 L1 私有实现；功能通过 state.Archive/Foreground/Cursor 等注入端口消费，不能导入 generation 或密钥实现。组装者注入当前身份解析、注册 Parser 与公开 Executor；不能导入 store/GORM、Gin 或另建 billing 引擎。固定 worker 和有界队列实际消费现有 Chat/embeddings，正文副本总预算最高 64 MiB，排队起算取消期限。交接不会预占 generation/network 槽、预留预算或持久受理。每次派发重新解析可信绑定，旧 context 的身份/准入标记不能带入；排队后撤销禁止新执行，已派发轮次仍用本轮快照，尚无后台任务撤销传播服务。

观察者取消不取消已交接工作；显式 `Future.Cancel`、服务关闭和期限可取消生成，均继续走 gateway 既有收尾。输出属于本次生成，结果仅一次接收；commit 后 panic 保守标记执行可能未知，不重放、不伪造未派发或清零账本。`Close` 等待所有 worker 与收尾；超时后不得提前关闭 gateway/store，同进程不合作回调无法被隔离或强杀。

A3-02 通过消费侧 `ArchiveStore` 端口接入 store 的统一 v8 迁移与原子序号追加；`SubmitRecorded` 为实际输出保留加密片段，读取/删除重新校验当前绑定，读取还检查当前模型权限。逐条容量预留、owner/实例限额、TTL、CAS 和故障回归见开发指南。它不是任务意图、领取/fencing 租约或原子任务+预算端口；输出片段不是可公开恢复的协议事件。没有公开路由、任务 ID 或重启重派，不在 main 隐式启动，不能替代 S7-A 的后台受理与持久 worker。架构回归用真实临时 SQLite/授权/准入/执行/财务链验证它，不把独立内存消费者当作整个 A3 已完成。HTTP 前台生命周期不变，A3-03 的 WS 消费回归见下节；产品持久任务仍另划批次。A3-04 新增独立 IntentAcceptor，不将持久意图送入 volatile Runner；其原子受理边界见下节。

### 意图与账本原子受理：A3-04（历史切片范围）

`generation.IntentAdmission` 只提供类型化 Admit/Read/Cancel；实际 Engine 实现在同一 SQLite 事务保存加密意图、幂等摘要配置标记和原用户/Key 双 hold。generation 不导入 billing/store/GORM；共享 reserve/finish 的事务内实现保留账本权威，没有任意事务回调或第二套余额。原普通 attempt 明确拒绝意图关联 reservation，后续派发不能伪造普通 PreDeduct 或先再预留一次。

`IntentAcceptor` 复用当前身份解析，对 Admit/Read/Cancel 校验可信 owner/创建 Key/当前模型权限；身份是事务前本轮快照，不保证撤销瞬间中断。输入加密和独立稳定 HMAC 密钥由组装者注入，schema v9 统一追加 `generation_intents`，不由应用另迁移。容量、幂等保留期、加密/密钥变更拒绝、取消和恢复详见[开发指南](reference/generation-runtime.md#a3-04-加密意图与双预算原子受理)。这是不可派发的受理消费者，不是公开后台 API/持久队列；没有领取/续租/fencing/重启重派，main 不启动它。

以上段落保留 A3-04 的历史范围，不是当前领取能力限制。A3-05 已在 `master/ccbfe29`（PR #6）交付 `IntentLeases` / `ExecuteClaimedTo`、v10 epoch/取消/输出关联、原预留与历史价接管、sealed 输出字节游标、默认关闭 main runtime，以及 secrets/store 凭据加密。最终功能头与合并后 master 完整 CI 均通过；上述内部端口可作为现有操作的共同开发基线，不是永久冻结的公开 SDK/S7 产品事件契约。共享入口/模型/迁移/main/config 仍由集成协调；支持/寿命/责任及故障证明见 [基础层契约审计](reference/foundation-contracts.md)，不另建鉴权、账本、迁移或 S7 路由。

### WS I/O 与物理连接：A3-03（基础 I/O 已验收）

`internal/transport/websocket` 是纯服务端 I/O，生产只依赖标准库与固定版本 coder/websocket，不导入 access/gateway/generation/store；库不得进入 core 或业务包。`access.ConnectionCapacity` 独立限制物理连接，可信绑定由现有鉴权提供，不复制鉴权。manager 的服务寿命、HTTP 握手和每次生成独立，active/pending/消息/截止时间有界，关闭不代表生成收尾或退款。

当前实际消费者是 generation 的测试专用 socket 应用：每轮经原 Runner 重新授权并调用同一 Gateway/账本；前台断开显式取消并 join，记录型观察断开只取消 Wait。它验证了已有输出端口足以承载编码结果，尚未创建生产 WS 应用或上游 WS executor。原生 WS 应用协议/事件、续链、持久任务及相应 main 注册必须后续独立验收；A01 的原生 HTTP JSON/SSE 与 main 接线已独立验收，不从测试帧复制为产品契约。关闭顺序、Origin/代理、容量和错误边界见[开发指南](reference/generation-runtime.md#a3-03-有界-ws-传输与连接生命周期)。

### 协议扩展：internal/protocol

厂商适配器实现 `adaptor.Adaptor` 和需要的 `ProtocolAdaptor`，有状态 SSE 实现 `SessionStreamer.NewStreamSession`。适配器实例必须可并发只读；解析状态只属于本次 attempt。未知字段透传/不支持能力的显式拒绝由协议测试约束。`ProtocolAdaptor` 仅是 ChatRequest/ChatResponse/Chat SSE 的 codec，不是通用操作 DTO。内建适配器通过 `adaptor.Capability.Supports(Requirements)` 声明操作能力，`protocol.Supports` 先限制已实现的 OpenAI 入站 HTTP 范围；相同需求用于候选过滤和 `PrepareRequest`，旧声明接口兼容保留。embeddings 显式选择原生转换和独立响应 decoder，保留 input/vector/base64/扩展字段，不能经过 Chat 或输出 SSE DONE；自定义 embeddings opt-in 必须提供 OpenAI-compatible 原生载荷/usage，并不支持任意厂商格式。malformed tools/history 不按纯文本放行 Gemini。A01 已声明并验收原生 OpenAI-compatible Responses 前台 HTTP/SSE 能力，不经 Chat 转换；background/原生 WS 仍未实现。Gemini 工具与 strict 工具均保守拒绝，Responses 尚无已证明的 strict 子集，全部在计费前拒绝。

本轮实现矩阵（未代表完整阶段验收）：

| 入站操作 → 目标 | disabled / legacy | strict |
|---|---|---|
| Chat JSON/SSE → OpenAI | 原生字段保留，function tools 走同协议路径；不承诺任意新扩展客户端均实测 | 仅已登记官方 Chat 输入档案目标、严格文本、显式总输出与默认服务等级；工具拒绝，费率/失败收费证明尚未验收 |
| Chat JSON/SSE → Anthropic | 已建模文本/function tools、多调用与结果往返；不可表示字段在执行前 400 | 无已登记输入档案，拒绝执行；原转换维度/输出限制守卫保留 |
| Chat JSON/SSE → Gemini | 文本子集；signed tool history 无无损表示，因此包含 tools 或历史 tool_call/result 的请求路由拒绝 | 无已登记输入档案，拒绝执行；原转换维度/输出限制守卫保留 |
| embeddings → OpenAI | 非流式原生路径 | 仅已登记官方 embeddings 输入档案目标；文本批次与精确契约 |
| Responses 前台 HTTP/SSE（A01） | 原生 OpenAI-compatible 文本/函数工具 JSON/SSE，默认关闭，legacy/disabled 已验收；GET/续链不在本批 | 尚无已证明子集，全部计费前拒绝，不继承 Chat 的预算证明 |
| background / native WS | 尚未实现公开产品路由/能力，后续独立验收 | 不开放，不继承 Chat 的预算证明 |

跨协议支持的是共同子集，不是“所有 OpenAI 字段换名”。`stream_options` 仅接受已映射的 `include_usage=true`，本地转换返回用量而不把该字段错发给厂商；未实现的 false/扩展拒绝。`n=1` 是显式单候选，其他候选数拒绝。`tool_choice`、strict schema、parallel_tool_calls 等未映射选项不做猜测。引用上游功能逻辑不能扩大本矩阵。

strict 的 `StartPreparedAttempt` 是唯一可执行成本准入入口：必须匹配已登记档案及真实 URL/正文，最终输出限制还须与本次成本占用元数据一致。无档案契约可加载用于历史读取，但不能发起新 attempt，返回持久 `provider_input_unproven`。旧 `StartCostedAttempt` 无实际正文，已弃用且不能绕过检查。billing 单测的私有声明包络夹具只覆盖算术/账本事务，不构成生产许可；网关正向测试使用实际档案与注入 loopback Transport，生产没有测试豁免配置。 新尝试还须有已登记费率档案、USD 币种和显式凭据 ID/Key 摘要绑定；`StartPreparedAttempt` 消费真实 Header，最终鉴权和项目/组织选择不得绕过成本许可。绑定在启动编译时复制，换 Key 或新增 ID 必须重新核实并重启加载。缺费率/凭据依据持久返回 `provider_tariff_unproven` / `provider_credential_unproven`，不重试、不罚健康；未知执行仍保留占用。来源与未覆盖假设见[费率档案](reference/provider-tariff-profiles.md)。

新协议在自己的目录开发，集成人员在创建 gateway 时注册：

```go
gw, err := gateway.New(gateway.Options{
    Channels: channels,
    Operations: protocol.Operations(),
    Protocols: protocol.Protocols(map[string]adaptor.Adaptor{"provider-name": providerAdaptor}),
    // 其他依赖由应用入口注入
})
```

注册表在构造时复制，不能通过修改原 map 热替换实例。内建协议由组装根通过 protocol.Protocols 显式提供；gateway 不内置厂商，也不对缺失注册回落。替换已有名称是工厂中的显式构造行为。不能在协议包发 HTTP 请求、访问用户表、记录费用或创建后台 worker。`protocol.Resolve` 仅描述内建默认协议，不等于实例注册表。

### 可选计费消费端口

gateway 只消费 `LegacyBilling`（预扣/快照计价/结算）与 `StrictBilling`（就绪/预留/实际 wire 成本准入/回执/拒绝/收尾），不持有具体 Engine 或访问数据库。财务类型仍来自 billing/model；端口不意味着另建账本或允许替换安全语义。main 负责注入/启动/关闭原引擎及恢复 worker，执行结束由同一协调器收尾。

`Options.Billing` 接收 legacy 接口，`Options.StrictBilling` 可独立注入；为兼容既有组装，后者未指定时仅从同时实现 strict 接口的 Billing 推导。生产 main 显式给两个端口注入同一原引擎。strict 缺接口/未就绪、不能预留或不能记录发送前成本时拒绝，不回落 legacy；已执行但持久回执失败仍保留双 hold/pending，不重试。可选端口 typed nil 按缺失处理；disabled 不调用两条财务路径。成本端口接收正文/Header 快照，不能通过修改入参改变实际发送。实现方须满足并发和有界调用要求，不能把副作用留到可丢日志。

### S3 准入接入：gateway.Admission

```go
type Admission interface {
    Acquire(context.Context, gateway.AdmissionScope) (release func(), err error)
}
```

- `RequestAdmission` 在整次路由/重试之前获取一次；`AttemptAdmission` 在每次选中目标后获取，scope 附带可信 user/token/model 和 channel/credential/尝试序号。
- Request lease 覆盖整个重试循环；attempt lease 覆盖单次网络执行。两者在取消、失败和 panic 路径释放，嵌套释放顺序是 attempt → request。当前协调入口将 request lease 保持到结算和收尾退出；attempt lease 仅覆盖单次执行，不把慢持久化延长为上游网络名额。普通日志队列仍须非阻塞。
- Acquire 必须遵守 ctx 截止时间，不能无限排队。成功必须返回非 nil 的 release；异常/取消时若已拿到租约也必须可释放。release 必须快速、并发安全，不依赖已取消的请求 context；不要在 release 内发起无限期网络请求。
- 拒绝返回 `AdmissionError`：429 为配置限额，503 为暂时容量不可用，可附 RetryAfterSeconds。Message 必须能安全对外显示；普通后端 error 不向客户端回显。准入拒绝不发上游请求、不惩罚凭据。
- `gateway.New` 的可选 Admission 默认仍为 nil，供隔离测试/自定义组装；生产 main 已注入 `gateway.CapacityAdmission`，实现单进程四级并发和用户跨 Key RPM 聚合。服务器上限、拒绝行为、状态表容量和限制见 [S3 运行约定](DEVELOPMENT.md#s3-并发准入与用户请求速率)。当前立即拒绝，不排队；多实例全局限额、用户 TPM 聚合未实现。S5 持久严格结算是另一项能力，已有批次实现但完整成本边界尚未验收，不能从 S3 接口推导。
- 原有速率实现通过 `access.Limiter` 复用；TPM 预留在 `WithTokenReservation` 记录，`FinishTokens` 只调整一次。取消后用独立、最多 2 秒的 context 释放计数，避免每个业务模块再实现一套收尾。

### 健康回写与关闭

`NewHealthWorker(persist, reload, refresh)` 创建独立队列，不再用进程级可变回调。生产入口注入读取最新凭据的 refresh，健康事件在 worker 内串行计算，凭据代次与写入版本条件保护人工变更；数据库 v3 增加内部健康代次，v4 增加健康观察顺序标记，运行快照、worker 与数据库条件写入以同一标记识别新旧状态。每个应用拥有自己的 persister/reloader；Close 幂等并排空已接收事件。回调可有界阻塞，但不能调用自身 Close 形成死锁。纯环缓存仍是调度器内部 memoization，不作为授权或租约账本。

关闭顺序：HTTP Shutdown 等在途请求 → 停止恢复/清理生产者 → Close 健康 worker/限流器/执行器 → flush 日志并关闭 store。main 由集成团队维护，功能团队不要自行在包初始化中启动 goroutine。

### 配置管理用例：internal/management

`management.Configuration` 是已有渠道/凭据/价格操作的真实应用服务，现只消费 `configuration/v1.Repository` 与两个运行快照发布回调，对外实现同组 `Service`，不再引用 model/store DTO；旧管理密钥和新管理员会话路由共用同一实例。保存、发布、状态读取和 Apply 在一个用例锁内排序，store 继续持有校验与事务；回调不得反向进入该实例以免重入。`Change.Saved` 表示数据库已提交，即使随后缓存读取报错；HTTP 层据此返回既有 `saved:true`、publication 头和错误表示，不重新保存。

`Apply` 只重新应用已有 desired revision，不创建资源或增加版本；前端对 pending 与已 applied 的保存后错误分别提示。`ConfigStatus` 的 epoch/revision 是进程运行状态，不是持久事务或 schema 版本。该新增用例及前端风险回归在开发后期集中验证，当前支持/缺口与消费者见 [S6 契约清单](reference/compatibility-contracts.md)。

## 3. 前后端契约

唯一机器可读 HTTP 契约：[openapi.json](openapi.json)，当前已验收 A02 版本 `0.4.0` / 91 个操作（PR #12 / `master/765501a`）；S7-A01 已验收版本为 `0.3.0` / 80 个操作；S6 已验收基线为 `0.2.3` / 79 个操作（管理员对账为 `0.2.2`，首批为 `0.2.1`，S4 为 `0.2.0`，F0 验收版本为 `0.1.0`）。路径沿用现有 `/api/...` 与 `/v1/...`；这个版本标识契约文档，未给现有 URL 强加新前缀。

- 新前端使用 `/api/auth`、`/api/admin`、`/api/me`；旧 `/api/channels` 等路径标记 deprecated，暂保留原管理密钥鉴权。
- `APIKey` 为无明文 metadata，`APIKeyCreated` 才有一次性 key。不要将数据库 model.Token 直接作为前端所有场景的通用类型。
- 所有 PUT 都按文档确认完整更新语义；不假定未发送字段自动保留。policy 空 allowlist 必须全部拒绝。
- 现有 list 在无数据时可能返回 null；契约显式 nullable。分页/status/响应封装以契约和回归为准，不能在 UI 中猜测。
- 流错误/截止时间/取消属于终止结果；已提交的流不会伪造成功 DONE。UI/SDK 必须处理半途失败。
- S4 渠道/凭据及价格/倍率写操作的成功码只表示已保存；响应头 `X-TXAPI-Publication-State` 为 pending 时必须展示未生效提示。管理员通过 config-status 查看/重试，版本只在同一 epoch 内比较；数据库迁移版本与发布 revision 无关。
- S5 第二批扩展为 79 个操作，增加管理员专用账本列表、详情和最终对账；金额单位使用精确字符串，操作人取管理员会话。日志增加 `price_version`、`billing_id`、`billing_state`，调用响应可带 `X-TXAPI-Price-Version` 和 `X-TXAPI-Billing-ID`。价格内容摘要跨重启稳定，不是配置发布 revision；pending 表示保留预算待对账，不能当成免费或已完成结算。人工对账不替代允许请求的完整成本上界。
- S5-C 成本契约批次保持 79 个操作，账本增加独立 provider 累计预算、已占用上限、逐次成本上限和不可变契约快照。`cost_units` 仍为售价快照核算值，不是实际厂商账单；`provider_held_units` 是整次执行的累计保守包络，不是用户余额 hold，结算或人工豁免不将它清零。配置契约是启动快照，修改渠道/映射不能绕过精确目标匹配。
- 后续发送前收费检查不新增 HTTP 字段/路由：400 `billing_fee_limit` 表示固定售价/目标倍率计算的允许请求收费上界无法被持久 hold 覆盖。已有快照字符串使用内部 `format=2` 保存售价版本/模型/分组/金额依据，旧 format=1 保留读取；消费者不能将该不透明 JSON 字符串当作无版本的稳定 DTO。无执行拒绝仍走原收尾，未知执行不能由此退款。
- OpenAPI 的 responses.default 是兼容错误封装；目前错误 type 字段不保证每条旧接口都相同。若需进一步统一错误码，先提交独立兼容方案，不静默改所有 API。

CI 会校验契约与真实 Gin 路由一一匹配、匿名请求受保护、公开 DTO 字段和关键控制面响应符合约定。本仓库实现的是上述子集校验，不宣称代替完整 OpenAPI 标准验证或所有字段语义测试；复杂业务必须有行为测试。

接口变更流程：先更新同一个 PR 的 spec/受影响调用方/契约测试 → 再实现 → 合并后记录变更说明。破坏性改动须注明消费者迁移步骤，不能让前端靠重复阅读 Go handler 跟进。涉及地基公开契约时，必须先完成下一节的提案与确认，不能用同一 PR 内“补上说明”替代前置确认。

### 地基变更请求与并行准入

地基公开接口的新增、签名、行为、权限范围或资源寿命变更，先提供具名提案，至少说明任务六项边界、问题/替代方案、精确接口差异、所有消费者、持久/外部兼容、分批迁移与回退，以及契约/行为/最终 CI 验证。用户明确确认后才实施；确认记录绑定提案与范围，代理不能自行批准。已确认范围内的例行实现不重复索要许可，新增范围外变更重新提出。

当前 [FC-001](proposals/FC-001-foundation-contracts-v1.md) 已按批准范围完成全部迁移、L0/L1/L2 DoD 和独立样例，并通过最终完整 CI/合并，v1 已冻结。所有生产包逐个登记、公开类型和 API 快照持续校验，现有未清例外为零；新变化仍先具名提案确认，不新增临时豁免或由代理自行批准。

并行任务须有独立目录/分支、依赖契约版本、接口说明、测试与集成方法。只可使用已验收公开契约，禁止功能模块互相导入实现、直接访问地基实现或外部上游/数据库；共享模型、迁移、入口与 CI 由集成负责人单点协调。组装根的多层注入与测试集成不构成功能层的越界许可，不强制拆仓或发布外部 SDK。

## 4. 数据库变更协作

- 模型结构集中在 `internal/model`，结构建表仍由 AutoMigrate 管理；需要的数据/索引迁移统一追加到 `internal/store/migrations.go`。
- F0 未执行新 schema 版本；v2 仍是 S2 已发布的 Key 摘要迁移。不要因重构运行重复的数据迁移。
- S5 首批在 S3/v4 基线上追加 v5：不可变价格历史、整数余额、请求预留和尝试成本表，以及普通日志关联字段。历史日志不猜测价格版本；普通日志清理不删这些财务记录。生产接线由 main 注入快照持久化与 billing 恢复 worker，不能另建鉴权、迁移或资源释放链路。
- S5 第二批追加 v6：每笔预留至多一条不可改写的管理员决策，和双余额/预留终态同事务提交；历史决策不从普通日志推断，不因日志或 owner 生命周期级联删除。旧库报错/回滚不扩展专项演练，既有 CI 回归与交付门槛保留。
- 每个版本号由集成负责人分配，同一集成周期按顺序合并；未合并分支不要自行预占同一个已发布版本。已发布步骤只追加不重写。
- runner 在事务中执行一步并登记版本；步骤不允许 Commit/Rollback、不读写真实外部服务。失败既回滚数据也不记版本，重试可重新执行。
- 打开数据库前检查最高版本；比当前二进制新的 schema 将被拒绝，避免先 AutoMigrate 再发现不兼容。结构 AutoMigrate 本身仍不是完整 SQL 迁移框架，备份/恢复规则继续适用。
- A3-04 统一追加 v9 `generation_intents`，原账本与意图受理共享事务；不回填历史任务/预留，禁止 v8 二进制打开。内容清理不删除财务事实，幂等摘要密钥标记不能为更换密钥而手动删除。
- 已验收 A3-05 统一追加 v10 的 claim/取消/输出关联及 credential 加密列/索引。密钥显式配置后的转换/轮换是 store 内同事务功能启用，不是另一套 schema runner；保留 ID/健康代次/原账本，丢钥或启用标记不符拒绝，不明文降级。旧版本回滚必须恢复兼容的一致备份；不能删除迁移/密钥标记降级。
- 新迁移至少有旧 schema 升级、重启幂等、失败回滚和恢复限制说明。绝不能把清空数据库当修复方案。

## 5. 分支、测试与集成

推荐按可独立验收的功能批次创建分支。新团队从 **已验收并合并的最新 master** 建分支，不从旧 S2 或未完成的共享框架分支隐式获取稳定性保证。PR #5/#6 已交付 A1–A4 底座，PR #8 已验收合并 S6/M1 框架，功能头与合并后完整 CI 见 PLAN/S6 记录。S5-03 仍未完成，S7-A 和同类项目新能力复用已验收的框架基线。各团队复用现有业务入口，新增适配同步更新受影响消费者。功能合并必须在最新基线上通过依赖/契约检查、build/vet/gofmt、race、权限/协议/迁移回归与冒烟；纯说明文档依开发节奏执行适用检查。

各团队统一遵守 [开发与验证节奏](DEVELOPMENT.md#开发与验证节奏)：按可独立验收的功能批次开发、集中验证和修复，再交付合并。团队开工时声明批次范围、接口依赖与验收行为；框架先完成验收并合并为共同基线，再基于稳定接入点扩展功能。

批次内按需编译，仅对关键风险或缺陷复现提前运行定向测试；批次稳定后集中执行本地检查。集成负责人汇总各团队的有效验证记录，在最新集成基线上组织完整 CI。共享接口变化后，受影响团队补做契约和联调验证，不等待所有团队完成整个阶段才发现集成问题。不要求每次局部修改或提交执行下列全部命令，也不为重复触发 CI 进行细碎推送；最终合并门槛保持不变。

```bash
# 只测共享边界，不依赖真实上游、Redis 或实际业务数据库
go test -count=1 ./internal/architecture ./internal/access ./internal/core ./internal/protocol/... \
  ./internal/gateway ./internal/scheduler ./internal/store ./internal/server/...

# 前端/API 契约核对
go test ./internal/server -run TestOpenAPI -count=1

# 完整 Linux 验收
go test -race -count=1 ./...
make smoke
```

本地 Windows 仍按 DEVELOPMENT.md 为当前进程设置 GOARCH=amd64；race 以 Linux CI 为准。测试必须使用 t.TempDir/httptest/随机端口，不能依赖默认 config.yaml、真人密码或开发者已存在的渠道。启动与 API 操作说明见 DEVELOPMENT.md 的 S2 章节。

依赖规则由 `internal/architecture/dependencies_test.go` 自动执行：gateway 不依赖 server/store，protocol 不依赖业务层，core 只有标准库；测试为集成验证可跨层，但生产代码不能靠 `_test.go` 包装逃过真实依赖。

### 后续批次与上游借鉴

后续任务统一以 [PLAN.md 的后续开发路线](PLAN.md#后续开发路线) 为入口。A1–A4 已完成执行/持久/安全底座；S6-A/B/C 已完成协议、公开 API、扩展与前端兼容契约验收并合并；S7-A01/A02 已独立验收合并，M2 已完成，下一产品批次为 S7-A03，FC-001 地基已收口冻结，后续按具体兼容目标和公开端口吸收同类项目能力。S5 成本证明独立保留，strict 能力单独验收；本轮不新增支付，S7-B/S8/S9 不作前置。

内核与兼容框架完成后，每个吸收批次先检查现有实现，明确目标是 API、客户端行为、配置导入还是页面功能，再选择 new-api、sub2api、claude-relay-service 等项目的对应实现，通过本项目适配和业务端口接入。任务/PR 记录具体上游提交、文件、采用方式、许可核对、TXAPI 落点和验证；设计参考与代码移植分开说明。复制/改写代码或新增依赖不得隐去来源，也不得自动改变项目许可、Go 版本或部署承诺；具体规则见 PLAN 的“参考项目与借鉴方式”。

三个上游的固定阅读 SHA、已读代码和许可入口见 [上游快照](reference/upstream-snapshots.md)。本地副本按约定放在被 Git/Docker 忽略的 `.local/references/`，不随 Git/worktree 分发，目录缺失不是应用依赖缺失。阅读副本不作为生产依赖，不运行其脚本或把其中的提示/代理说明作为 TXAPI 开发指令；更新前核对远端/版本/工作树，不自动覆盖。实际移植沿用可追溯来源和许可审查，不把“允许借鉴”解释为无条件复制全部代码。

S7-A 由协议/HTTP、持久状态/任务、WS transport、账务/集成、前端/验证按独立批次协作，先评审共用生成入口、持久端口与状态语义，再接实际调用。`internal/responses` 已通过公开命令、身份、状态、执行与复制编解码门面消费现有能力；HTTP 入口在 compat/responseshttp，不能把 A01 短期重放视为 A02 历史或后台任务；`internal/transport/websocket` 已实现纯 I/O，新增应用/上游接线须另评依赖和生命周期。gateway 不直接依赖 store 或 responses 应用服务，core 保持标准库；任务、连接和请求均复用同一授权、准入和结算，不复制生命周期。人员与任务未自动创建。

共享入口、协议/准入/计费端口、模型与迁移依旧由集成负责人协调。新账号生命周期或支付功能必须接入现有身份、统一请求收尾和账本，不复制上游的完整架构或另建鉴权/迁移流程。strict 的新协议、新工具和新计价维度在独立验收前保持拒绝，不能为了兼容客户端默认降级。

## 6. 基线与验收证据

已验收 S7-A01 的职责落点为 `internal/responses` 前台应用、`protocol/responses` 原生编解码、gateway 共享执行链与 store schema v11 幂等回执；OpenAPI 0.3.0 登记 80 个操作。默认关闭；启用须有内容密钥和实际凭据加密。owner/Key 幂等校验、当前权限和配额在 store 同一事务内完成，应用先持久记录再执行；true/默认 true 仅增加私有加密短期重放，false 无正文且重试不能重新执行，未知状态不得自动过期/重发。strict 在计费前拒绝，GET/续链/会话已由 A02 独立验收合并；后台/原生 WS 仍须后续独立批次。具体运行边界见 [Responses 约定](reference/responses-http.md)，实现与集中验证记录见 [S7-A01 批次](history/implementation-s7-a01.md)；本批独立验收不沿用 S6 的 0.2.3 / 79 操作或历史 CI，A02/M2 已完成，A03-A06 仍未完成。

F0 的阶段勾选、提交和原始 CI 已归入 [S0–S5 历史验收](history/acceptance-s0-s5.md#f0-多团队协作框架收口s2-之后)。本轮 S5/S6/A1–A3 切片已通过 PR #5 合入 `master/5004c57`，合并前后完整 CI 通过，具体范围见 [S5–A3 批次记录](history/implementation-s5-a3.md#2026-10-17-合并与最终验证结果)。

A3-05/A4 随 PR #6 完成基础层验收，PR #7 同步了验收记录；这些证据不替代 S5-03 或 S6 的独立验收。S6/M1 已由 PR #8 验收合并，精确功能头、合并提交和分阶段 CI 见 [S6 记录](history/implementation-s6.md)。现有模块继续按本页边界维护；状态/后台/WS 产品和同类项目能力接入沿用同一授权、预算、任务恢复与资源释放。M1 验收证据、完整支持范围和下一步以 PLAN 为准。


FC-001 FND-03 的状态、配置与生命周期冻结入口/消费者、密文/资源语义和迁移退出记录见
[Foundation API v1](reference/foundation-api-v1.md#fnd-03-状态配置与生命周期)。状态原始持久端口只注入可信服务，
不把 owner/claim 快照当作身份；公开功能使用当前授权与归档等受限服务，不能自行关闭共享底层依赖。


### FND-04 应用服务与 HTTP 接线

`controlplane.Service` 通过 control/v1 的账户/Key/统计/模型目录端口执行现有管理规则；
management 通过 configuration/v1 负责保存/发布，账本 HTTP 通过 ledger/v1 复用原决策事务。
server/API/middleware 只消费注入端口，不能导入 Store/Engine/Gateway 或 L3 构造器。
main 负责当前配置/metrics 组装，不把功能策略放进组装 lambda；原限流计数实际代码归 L1 access，
HTTP 层只保留读取/replay 64 KiB 估算样本与拒绝表示，正文关闭仍归 HTTP 入口。

Responses HTTP 通过 `responses/v1.Service.Begin` 获得一次 Call，始终 defer Close，再交付 Command/Delivery。
Call 在读取之前完成身份/容量/历史速率检查；有界 Payload 在资源/版本等原检查点读取。
Close 等 Handle/回调退出才归还容量，不能并发释放尚在运行的工作或复用旧授权执行第二次。
URL、HTTP 头、原生 JSON/SSE 终态与父链表示由 L2 解析/映射；业务只处理语义文档和状态 CAS。
已冻结的公开入口、权限范围和 FND-05 验收条件见 [Foundation API v1](reference/foundation-api-v1.md#fnd-04-应用与表示)。
