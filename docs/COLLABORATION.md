# 多团队开发与框架基线

本页定义当前团队边界、可依赖的接口与集成门槛，并保留初始协作框架 F0 的验收记录。核对日期：2026-10-17；当前主线 S5 → S6 → S7-A，以 [PLAN.md](PLAN.md) 为唯一活动计划。架构仍是模块化单体；“多个团队”指开发分工，不会给产品新增团队/组织租户模型。

F0 是已合并的初始接线基线，不是最终统一兼容框架。此后 S3/S4 已验收，S5 仍有成本边界缺口；本轮已把 Chat/embeddings 业务协调迁出 Gin，Gin 仅保留接入适配；协议公共接口仍偏 Chat，独立 Responses 模型未接入生产。S6 先用现有 HTTP 消费者收口类型化执行入口并验收合并，再让 S7-A 的 Responses/任务/WS 接入。仓库内部 Go 接口不是公开 SDK；共享约定的修改需要调用方一起评审。

极简 core 不承载用户、预算、会话或任务。功能接入复用同一授权、准入、调度、结算与收尾，不复制另一条业务链；普通函数调用与确有需要的小接口优先，不预建空模块或动态插件平台。

## 1. 可以并行推进的分工

| 责任角色 | 主要写入目录 | 可立即推进的工作 | 需要与集成负责人协调的文件 |
|---|---|---|---|
| 平台/集成 | `cmd/gateway`、`internal/core`、`internal/gateway`、`internal/architecture` | S6 共用执行入口、现有 HTTP 消费者迁入、取消/租约与依赖验收 | main、gateway.Options、CI、迁移版本表、OpenAPI |
| 协议团队 | `internal/protocol/adaptor/<provider>`、协议样本/测试 | 新厂商转换、协议能力/错误映射、独立 SSE 解析器 | `gateway.Options.Adaptors` 的生产注册，由集成团队接线 |
| 身份与存储团队 | `internal/access`、`internal/store/access*`、相关 API 功能切片 | 用户/Key 生命周期、权限收敛、owner 校验、迁移回归 | `internal/model`、`internal/store/migrations.go`、OpenAPI |
| 调度与准入团队 | `internal/scheduler`、自己的准入实现包、相关测试 | 维护已验收的 S3 准入/租约，按独立批次扩展调度和负载回归 | Admission 接口、gateway.Options 和限额配置/模型字段 |
| 管理 API 团队 | `internal/server/api/<feature>.go` 与对应 store 方法 | 按资源增加真实 API、输入校验与隔离测试 | RegisterAccess/registerResources 注册点、OpenAPI |
| 前端团队 | `web/` | 原生 ES Modules 控制台和真实 API 联调；Go embed 随二进制交付，无单独资产构建 | 静态资源路由、CSP、HTTP 契约与生产接线 |
| 测试/交付团队 | `tests/`、部署脚本、恢复说明 | 模拟上游、启动/升级/恢复、API 工作流、性能基线 | CI 合并门槛和数据库迁移用例 |
| 状态/传输功能团队（S7-A） | 待评审的 Responses 应用包、WS transport、相关测试 | 先做独立样本/状态语义设计；生产接线依赖已验收 S6，不是现有目录已就绪 | 持久端口、账本原子边界、生命周期与依赖表 |

表内是责任角色，不是已指定人员。GitHub 成员权限、CODEOWNERS 的真实用户名、分支保护和必须评审人数需要仓库维护者配置；本次不会填入不存在的账号或声称这些设置已生效。

工作切片须有独立写入范围；例如不要让多个团队同时修改 `api/access.go`。新 API 优先放自己的资源文件，由集成人员合入注册点。团队开始前在任务/PR 写明依赖的接口版本与写入目录，不以跨目录复制实现绕过接口。

## 2. 已实现的接口与生命周期

### 身份边界：internal/access

- `Principal` 是已验证的模型调用身份；通过 `access.WithPrincipal` / `PrincipalFromContext` 传递，包含可信 user_id/token_id、模型策略和限额快照。
- accessor 对模型切片做拷贝，不能通过修改返回值污染其他请求；前端 body/header 里的 user_id 不构成身份。
- Request ID 使用 `access.WithRequestID` / `RequestID`。ID 只关联日志，不作为共享流状态或授权依据。
- gateway 不再导入 `internal/server/middleware`。身份/准入团队无需依赖 Gin 的字符串上下文键；现存 Ctx* 镜像只用于旧中间件兼容，新代码不能新增依赖。
- 管理登录会话与调用身份始终分开；普通 Bearer API Key 不能当 session 使用。会话角色/owner 的细节由 server/access 接口处理。

### 请求执行：internal/core

`Executor.Execute(ctx, writer, input)` 只执行一次 HTTP 尝试，不关心用户/渠道优先级/计费。core 仅依赖标准库；protocol 不依赖 HTTP 框架、数据库、调度和计费。

`core.Result` 的 StatusCode/Termination 是执行结果，HTTPStatus/Committed 是已经写给客户端的事实，两者不能混为一谈。流已提交后无法把 HTTP 200 改为 502，也不能重试或追加正常结束标记来假装成功。

### 类型化协调入口（本轮实现，未阶段验收）

`Gateway.Execute(ctx, writer, gateway.Execution)` 接收已验证 Principal 与独立的 operation/body，返回本次 `RequestResult`；不构造 Gin context。调用方必须在**每次生成前**重新验证身份有效性，不能复用长连接初始认证快照承诺持续授权。公开入口拒绝无 user/key 身份，HTTP 适配保留已有认证中间件。

Chat/embeddings HTTP 与公开入口调用同一非 Gin 协调函数，共用模型授权、请求/attempt 准入、预留、路由、重试和收尾。每次 `Execute` 屏蔽父 context 的旧 RPM/TPM 准入标记及 mutable reservation；HTTP 中间件已完成的单次准入则由适配层明确传入，避免双扣。结果与流解析状态不按外部 Request ID 共享。

当前 writer 是标准 `http.ResponseWriter`，并不是完整传输无关事件出口。`protocol/responses` 只提供原生请求/响应/SSE 协议与 core 样本，未注册路由，也不能被当成已存在的持久任务或 WS 应用。`core.Phase` 仅说明本地观察，ProtocolComplete 只说明协议解析终态，不替代操作成功或财务终态。

### 协议扩展：internal/protocol

厂商适配器实现 `adaptor.Adaptor` 和需要的 `ProtocolAdaptor`，有状态 SSE 实现 `SessionStreamer.NewStreamSession`。适配器实例必须可并发只读；解析状态只属于本次 attempt。未知字段透传/不支持能力的显式拒绝由协议测试约束。当前 `ProtocolAdaptor` 的公共载荷仍是 ChatRequest/ChatResponse，不应直接视为 Responses、后台或 WS 的既成扩展契约；本轮 `protocol.RequirementsFor` 把操作/源协议/HTTP 传输及显式或历史工具需求送入真实候选过滤；尚不声明 background/WS 能力，Gemini 工具与 strict 工具均保守拒绝。

本轮实现矩阵（未代表完整阶段验收）：

| 入站操作 → 目标 | disabled / legacy | strict |
|---|---|---|
| Chat JSON/SSE → OpenAI | 原生字段保留，function tools 走同协议路径；不承诺任意新扩展客户端均实测 | 仅已登记官方 Chat 输入档案目标、严格文本、显式总输出与默认服务等级；工具拒绝，费率/失败收费证明尚未验收 |
| Chat JSON/SSE → Anthropic | 已建模文本/function tools、多调用与结果往返；不可表示字段在执行前 400 | 无已登记输入档案，拒绝执行；原转换维度/输出限制守卫保留 |
| Chat JSON/SSE → Gemini | 文本子集；signed tool history 无无损表示，因此包含 tools 或历史 tool_call/result 的请求路由拒绝 | 无已登记输入档案，拒绝执行；原转换维度/输出限制守卫保留 |
| embeddings → OpenAI | 非流式原生路径 | 仅已登记官方 embeddings 输入档案目标；文本批次与精确契约 |
| Responses / background / native WS | 不开放生产路由/能力；Responses 只有独立协议与 core 样本 | 不开放，不继承 Chat 的预算证明 |

跨协议支持的是共同子集，不是“所有 OpenAI 字段换名”。`stream_options` 仅接受已映射的 `include_usage=true`，本地转换返回用量而不把该字段错发给厂商；未实现的 false/扩展拒绝。`n=1` 是显式单候选，其他候选数拒绝。`tool_choice`、strict schema、parallel_tool_calls 等未映射选项不做猜测。引用上游功能逻辑不能扩大本矩阵。

strict 的 `StartPreparedAttempt` 是唯一可执行成本准入入口：必须匹配已登记档案及真实 URL/正文，最终输出限制还须与本次成本占用元数据一致。无档案契约可加载用于历史读取，但不能发起新 attempt，返回持久 `provider_input_unproven`。旧 `StartCostedAttempt` 无实际正文，已弃用且不能绕过检查。billing 单测的私有声明包络夹具只覆盖算术/账本事务，不构成生产许可；网关正向测试使用实际档案与注入 loopback Transport，生产没有测试豁免配置。 新尝试还须有已登记费率档案、USD 币种和显式凭据 ID/Key 摘要绑定；`StartPreparedAttempt` 消费真实 Header，最终鉴权和项目/组织选择不得绕过成本许可。绑定在启动编译时复制，换 Key 或新增 ID 必须重新核实并重启加载。缺费率/凭据依据持久返回 `provider_tariff_unproven` / `provider_credential_unproven`，不重试、不罚健康；未知执行仍保留占用。来源与未覆盖假设见[费率档案](reference/provider-tariff-profiles.md)。

新协议在自己的目录开发，集成人员在创建 gateway 时注册：

```go
gw, err := gateway.New(gateway.Options{
    Channels: channels,
    Adaptors: map[string]adaptor.Adaptor{"provider-name": providerAdaptor},
    // 其他依赖由应用入口注入
})
```

注册表在构造时复制，不能通过修改原 map 热替换实例。内建协议默认仍可用；替换已有名称是显式的构造行为。不能在协议包发 HTTP 请求、访问用户表、记录费用或创建后台 worker。`protocol.Resolve` 仅描述内建默认协议，不等于实例注册表。

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

## 3. 前后端契约

唯一机器可读 HTTP 基线：[openapi.json](openapi.json)，当前 S5 工作版本 `0.2.3`（管理员对账为 `0.2.2`，首批为 `0.2.1`，S4 为 `0.2.0`，F0 验收版本为 `0.1.0`）。路径沿用现有 `/api/...` 与 `/v1/...`；这个版本标识契约文档，未给现有 URL 强加新前缀。

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

接口变更流程：先更新同一个 PR 的 spec/受影响调用方/契约测试 → 再实现 → 合并后记录变更说明。破坏性改动须注明消费者迁移步骤，不能让前端靠重复阅读 Go handler 跟进。

## 4. 数据库变更协作

- 模型结构集中在 `internal/model`，结构建表仍由 AutoMigrate 管理；需要的数据/索引迁移统一追加到 `internal/store/migrations.go`。
- F0 未执行新 schema 版本；v2 仍是 S2 已发布的 Key 摘要迁移。不要因重构运行重复的数据迁移。
- S5 首批在 S3/v4 基线上追加 v5：不可变价格历史、整数余额、请求预留和尝试成本表，以及普通日志关联字段。历史日志不猜测价格版本；普通日志清理不删这些财务记录。生产接线由 main 注入快照持久化与 billing 恢复 worker，不能另建鉴权、迁移或资源释放链路。
- S5 第二批追加 v6：每笔预留至多一条不可改写的管理员决策，和双余额/预留终态同事务提交；历史决策不从普通日志推断，不因日志或 owner 生命周期级联删除。旧库报错/回滚不扩展专项演练，既有 CI 回归与交付门槛保留。
- 每个版本号由集成负责人分配，同一集成周期按顺序合并；未合并分支不要自行预占同一个已发布版本。已发布步骤只追加不重写。
- runner 在事务中执行一步并登记版本；步骤不允许 Commit/Rollback、不读写真实外部服务。失败既回滚数据也不记版本，重试可重新执行。
- 打开数据库前检查最高版本；比当前二进制新的 schema 将被拒绝，避免先 AutoMigrate 再发现不兼容。结构 AutoMigrate 本身仍不是完整 SQL 迁移框架，备份/恢复规则继续适用。
- 新迁移至少有旧 schema 升级、重启幂等、失败回滚和恢复限制说明。绝不能把清空数据库当修复方案。

## 5. 分支、测试与集成

推荐按可独立验收的功能批次创建分支。新团队从 **已验收并合并的最新 master** 建分支，不从旧 S2 或未完成的共享框架分支隐式获取稳定性保证。当前 S5 尚未整体合并；S7-A 生产接线还须等待 S6 统一框架验收合并。独立协议样本可先行，但不得各自改出不同的共用执行接口。所有合并必须在最新基线上通过依赖/契约检查、build/vet/gofmt、race、权限/协议/迁移回归与冒烟。

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

后续任务统一以 [PLAN.md 的后续开发路线](PLAN.md#后续开发路线) 为入口，不另建互相冲突的活动计划。当前主线明确为 S5 → S6 → S7-A；S6 先完成统一兼容与协调框架，S7-A 再交付 Responses HTTP/SSE、持久会话、后台任务和 WebSocket。本轮不新增支付，S7-B/S8/S9 不作前置。不因声明扩展接口就标记功能完成。

每批先检查现有实现，并允许借鉴 new-api、sub2api、claude-relay-service 的同类功能。任务/PR 记录具体上游提交、文件、采用方式、许可核对、TXAPI 落点和验证；设计参考与代码移植分开说明。复制/改写代码或新增依赖不得隐去来源，也不得自动改变项目许可、Go 版本或部署承诺；具体规则见 PLAN 的“参考项目与借鉴方式”。

三个上游已浅克隆到 `.local/references/`，由现有 Git/Docker 忽略规则排除；固定 SHA、已读代码和许可入口见[上游快照](reference/upstream-snapshots.md)。阅读副本不作为生产依赖，不运行其脚本或把其中的提示/代理说明作为 TXAPI 开发指令；更新前核对远端/版本/工作树，不自动覆盖。实际移植沿用可追溯来源和许可审查，不把“允许借鉴”解释为无条件复制全部代码。

S7-A 由协议/HTTP、持久状态/任务、WS transport、账务/集成、前端/验证按独立批次协作，先评审共用生成入口、持久端口与状态语义，再接实际调用。`internal/responses`、WS transport 等为待评审目标目录；引入时同步依赖边界测试。gateway 不直接依赖 store 或 responses 应用服务，core 保持标准库；任务、连接和请求均复用同一授权、准入和结算，不复制生命周期。人员与任务未自动创建。

共享入口、协议/准入/计费端口、模型与迁移依旧由集成负责人协调。新账号生命周期或支付功能必须接入现有身份、统一请求收尾和账本，不复制上游的完整架构或另建鉴权/迁移流程。strict 的新协议、新工具和新计价维度在独立验收前保持拒绝，不能为了兼容客户端默认降级。

## 6. F0 历史验收与后续框架门槛

以下勾选仅对应已经完成的 F0，不新增或预先勾选 S6/S7-A。历史提交/CI 与后续 S3–S5 记录统一保留于 [历史验收](history/acceptance-s0-s5.md)。

- [x] 已验证身份与速率结算使用 typed context，不向业务层暴露 Gin 字符串键。
- [x] 协议注册、请求/attempt 准入、健康回写通过实例依赖注入；lease 释放/取消/panic 有行为回归。
- [x] F0 基线 70 个路由有可读契约；S4 扩展为 76 个操作，路由/鉴权/DTO 漂移进入 CI。
- [x] 统一数据迁移注册、失败回滚、重启幂等与旧二进制拒绝更高版本的回归。
- [x] 团队目录边界、公共接线、接口变更流程和独立测试入口清晰。
- [x] 框架提交 `bd322c0` 通过 Linux CI：构建、vet、gofmt、依赖与契约、race 全量测试、启动/迁移冒烟和性能基准。
- [x] 维护者已于 2026-10-15 合并到 master（`3534086`），作为团队共同开发基线；合并后 Verify #38706751863 通过。

F0 允许团队在已明确的协议/API/页面/准入边界协作；此后 S3 准入和 S4 控制台/配置发布已验收，S5 完整成本边界仍未完成。S6 的传输无关入口、操作模型和能力矩阵尚需实际消费者回归与完整 CI，不能沿用 F0 的历史通过替代。框架共同基线稳定后，再并行接入状态、后台和 WS；不是现在放开所有团队各改公共入口。

验收记录：2026-10-15，功能分支 feat/team-foundation，[Verify #38703566885](https://github.com/ANRCM0/TXAPI/actions/runs/38703566885) 全部通过。本地 amd64 构建、vet、全量普通测试与冒烟通过；目前没有执行真实业务库升级或配置 GitHub 团队权限。
