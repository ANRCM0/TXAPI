# Foundation API v1：实施清单与迁移边界

状态：FC-001 已由用户批准，FND-01/02/03/04 已验收，FND-05 正在完成集中验证与冻结门槛；本清单是 v1 候选，不是 frozen 发布。
最终冻结需 FND-05 全部 DoD、独立上层样例和最新基线完整 CI/合并证明。
排期与勾选只维护 [PLAN](../PLAN.md#地基符合性收口fc-001)，目标范围见
[FC-001](../proposals/FC-001-foundation-contracts-v1.md)，实际验证见
[实施记录](../history/implementation-fc001.md)。

## 公开接口与所有权

| 路径 / 版本 | 内容与实际消费者 | 语义与限制 |
|---|---|---|
| `foundation/contracts/execution/v1` / `Version="1"` | Executor.Execute、Invocation、Output/DeadlineOutput、Outcome/AttemptOutcome/Result、目标连续性约束；Responses、generation worker/归档、HTTP 集成消费 | 输入正文/目标、输出元数据与可变结果跨桥复制；公共签名不引用 core/gateway/protocol/model 实现类型，不允许 `any` 逃逸 |
| `foundation/contracts/identity/v1` / `Version="1"` | Binding、Policy、Principal 信息投影与 Authority.Current/Refresh；Responses 的当前身份与权限检查消费 | 值类型不能认证上下文；Refresh 要求现有可信身份与相同绑定，再解析当前权限/状态。绑定不是凭证，缺身份或跨 Key/owner 不解析目标身份 |
| `foundation/httpoutput/v1` / `Version="1"` | New 返回公开 DeadlineOutput，L2 `compat/httpoutput` 使用 | writer 仍由入口持有，桥接既有 core HTTP 输出；公开返回类型隐藏 core 实现。此为 L1 版本化输出门面，不向功能暴露执行器/连接所有权 |

源码路径均相对 `internal/`。公开契约包仅依赖标准库和其他契约包；L3 不能导入
`foundation/runtime` / `foundation/identity` 等实现包。构造和注入由组装根完成。
旧 access.Policy/Principal/Binding 和 gateway.ResponseTarget 向中立契约的别名仅供已登记
旧消费者迁移；方向是旧实现引用公开类型，不是把实现类型伪装导出到新契约。

## 执行、身份和结果

`Executor.Execute(ctx, output, invocation)` 继续调用原 Gateway.ExecuteTo 一次。FND-02 将该桥归入 gateway.PublicExecutor，原 foundation/runtime 仅保留组装兼容转发。
Invocation 提供 operation/body/session/output ID/受限 target，不接受用户/Key/价格或余额作为授权。
可信身份由原认证边界产生，Authority.Refresh 在每次 Responses 交付时重新读取；
generation 在派发前仍用原 resolver 校验当前主体，新的投影值不替代这些检查。
授权、RPM/并发准入、原目标约束、尝试、预算/结算与释放继续由原协调链持有。

Output 的 Commit/Write/Flush/State 与可选写 deadline 保留原寿命规则：
提交状态只能前进；短写/读错/取消不伪造完整协议成功；attempt 不关闭 writer。
L2 HTTP 工厂由 main 注入 compat/responseshttp，功能只接收语义化 Delivery，不构造 core.HTTPOutput、
HTTP writer 或解析 URL/Header。当前拆分已有集中验证，但整个四层 DoD 仍待 FND-05。

Outcome 显式映射 phase、原始和已公开 HTTP 状态、committed、protocolComplete、
termination、nil/已知 usage、错误码原始 JSON、耗时/字节数和每次尝试。
NativeCompletion 保存原生终态/ID，Target 保存原目标连续性，BillingID/PriceVersion/BillingState
只投影原账本/价格关联，不授权发起新执行或修改预算。公开 Error.Code 是受限 JSON 表示，
不是任意 Go 值。源 usage、策略/模型切片、错误、attempt 与元数据不与消费者共享可变引用。

`foundation/runtime.Executor` 是 L1 私有转换实现，不能成为 L3 导入入口。
其 ExecuteClaimed 现接受 state/v1.GenerationClaim，由 state.ClaimedExecutor 描述；
原 durable runtime 继续调用原 fenced 预留。它没有进入普通 execution.Executor 接口，
不能通过构造相同 ID/Epoch 值获得 claim token 或绕过原 hold/attempt 检查。

## 持久与外部兼容

schema v12、OpenAPI 0.4.0 / 91 操作、幂等回执、归档段类型/JSON 字段、AEAD 关联数据、
历史 Target 的 `channel_id/credential_id/actual_model/fingerprint` 字段均保持。
Operation/Phase/Termination 改为公开值类型后仍编码原字符串；旧归档字节经过真实 SQLite
加密写入再读取验证，不依靠新类型自产自读来声称旧格式兼容。
没有新增财务权威、迁移 runner、配置或第三方依赖；strict 许可与默认开关不扩大。

## 本批检查与剩余债务

FND-01 增加契约实现依赖/无类型逃逸检查、受控拒绝样例、结果复制与未知执行映射、
真实 gateway 的缺身份/短写/取消、Authority 的绑定伪造/权限撤销/异常和旧归档读取回归。
存量权限、Chat/embeddings、Responses JSON/SSE、并发/恢复测试保留，并迁移其真实消费者。
完整结果与精确来源见实施记录；编译成功不等于行为或 Linux race 已验收。

| 剩余依赖 / 来源 | 退出批次 |
|---|---|
| gateway 注册/协议会话与 HTTP 隔离 | FND-02 已验收，PR #15 / master/e97e458 |
| generation 的记录/claim/意图及状态端口已用中立值；access/secrets 仅留 L1 私有实现 | FND-03 已验收，PR #16 / master/3bf2540 |
| Responses 通过命令/Call/Delivery 消费公开状态与编解码门面，HTTP/SSE 表示已归 L2 | FND-04 已验收，PR #17 / master/14bb9c9 |
| controlplane/management 只消费公开基础契约；server/API/middleware 不再导入 L1/L3 实现，构造回调由 main 注入 | FND-04 已验收，PR #17 / master/14bb9c9 |
| 全生产包四层映射、公开签名递归可见性、独立新增操作样例与零例外冻结 | FND-05 |

这些是已知迁移来源，不是新增模块可复制的接入方式；本清单不降低旧检查或最终 CI 门槛。

## FND-02 注册与会话候选

execution v1 同组增加 OperationRegistration/ProtocolRegistration、Parser/Request、Adapter、Target、
Input/Decoder/StreamDecoder/Frame、Observer/Observation 和 ModelCatalog。它们只引用标准库与契约值；
Target 的凭据只在已授权选中后交给可信编译期 codec，不能认证调用或自行发网络。
main 显式调用 protocol.Operations()/Protocols(overrides) 并注入 gateway；注册是构造时复制后只读，
无动态载入、自动路由或财务许可。注册缺失/重复/版本不兼容/typed nil/必要回调缺失拒绝启动。
新操作提供自己的 Parse 与协议 Adapter，不修改 L1 操作分支；L1 将解析投影复制为自持 Request，
并统一执行授权、准入、成本、attempt 和收尾。准备后 body/header 和 decoder/frames 经显式 core 桥，
每次 attempt 必须新建 decoder/session；观察只有中立原生 ID/状态/usage/失败事实，L1 不断言 Responses Observation。

NonReplayable/OpaqueErrors/Continuity 只收紧既有执行安全；错误消息来自可信注册，框架按通用终止原因选择，
不解释厂商状态。strict 新操作默认拒绝，Chat/embeddings 也仍需原成本证明，注册不能绕过 StartPreparedAttempt。
旧 adaptor 的 ProtocolAdaptor/整数 mode 保留在 L2 兼容工厂内；framework 不导入这些实现。
纯 JSON 约束统一为 foundation/contracts/json/v1，旧 protocol/jsonutil 仅转发，避免财务/worker 反向依赖 L2。
该组与 HTTP 接入已由 FND-02 最终头及合并后完整 CI 验收；整个 v1 仍待 FND-05，不据此提前冻结。

HTTP 适配的准入交接由组装根注入的 gateway.NewHTTPExecutor 处理；普通 NewPublicExecutor 保持每次新准入。
私有上下文标记只可绑定当前可信 Key 并原子接管一次，Invocation 无跳过限流字段；原 TPM 预留仍由原 once 收尾。
Request.AffinityText 将旧首条 user/多模态文本投影留在协议侧，scheduler.SessionKey 只做原 owner/Key 哈希。
该迁移不改变显式头与旧回退亲和键字节，也不把亲和摘要当作授权或会话持久化。


## FND-03 状态、配置与生命周期候选

| 路径 / 版本 | 内容与实际消费者 | 语义与限制 |
|---|---|---|
| foundation/contracts/state/v1 / Version=1 | 无 ORM 的生成记录/片段、意图/claim、回执/历史、守卫和限额；ArchiveStore/IntentAdmission/IntentLeases/ReceiptStore/HistoryStore；generation 与 Responses 实际消费 | 持久数据由 foundation/persistence 显式转换后调用原 store/billing 事务，不导出 DB、SQL 或事务回调。快照数据不构成身份/claim 授权 |
| 同上 Archive/Foreground/Recording/Cursor | Responses 通过 generation.PublicArchive 注入，只面对公开输出与记录值 | 记录前台持久输出再交付；Finish 是观察终态，不断言财务成功。读取/删除/游标继续重新授权；Close 只释放该观察快照，不取消生成或退款 |
| 同上 ContentProtection | main 注入原 secrets.Ring 给 Responses 历史 | 同步有界的既有 v1 AEAD 格式及 Active key ID；保持 header、12-byte nonce、16-byte tag 和原 AAD。不是通用替换加密算法或修改历史格式的许可 |
| foundation/contracts/configuration/v1 / Version=1 | Repository、Service、Channel/Credential/ModelPrice 与输入、Change/ConfigStatus/SavedError；management、HTTP API 实际消费 | 隐藏 ORM/软删除/密文和内部健康并发列；特权凭据表示与一次性创建/掩码由原 HTTP 权限路径保持。保存/发布/Apply 仍在同一应用锁排序；提交后错误仍 Saved=true |
| foundation/contracts/lifecycle/v1 / Version=1 | Drainer.Close(context.Context)；main 的 durable runner 所有权与原 Runner 关闭回归消费 | 关闭停止接收并 join，context 返回错误不代表依赖可关闭；所有者继续保留 gateway/store，直到再次 Close 成功。保留现有单点启动和停止顺序 |

转换桥逐字段保留原 ID、owner/Key、原账本/价格、版本、期限、claim epoch/hash/取消/输出关联和错误身份，
复制密文/嵌套片段/策略与配置 map/slice，不共享回调可改数据。旧模型只对无 ORM 的普通限额/claim/错误
引用公开值；ORM 行继续独立定义并保留原标签，不能通过 type alias 把持久实现暴露进契约。
端口注入给可信编译期服务：绑定值和快照不是授权，用户入口必须用当前认证，存储仍持有原事务内权限检查；
领取/续租/隔离/完成继续由原账本权威执行，不能把原始状态端口当成面向不可信客户端的公开 API。

真实风险回归新增密文字节/嵌套片段所有权、fencing/原 hold/unknown/cancel 事实、提交后错误双层识别与保存时间，
以及 typed-nil 后端不隐藏在 wrapper；既有归档/旧密文/重启/强杀/当前授权/CAS/发布测试保留。
公开合同的递归类型检查、全部生产包分类与独立接入证明仍留 FND-05；FND-04 的实际消费者见下节。


## FND-04 应用与表示候选

| 公开候选 / v1 | 实际消费者与边界 |
|---|---|
| identity.Authenticator / TokenView、Policy.Intersect | L2 Auth 只提取 Key、调用注入的认证器并使用返回 context；L1 私有实现调用原当前 Store.LookupToken 和 access 的可信 context。TokenView/Principal 是复制投影，不提供公开的认证 context 构造器 |
| admission.Limiter / RequestGate | 既有 memoryLimiter 与 RPM/TPM 计数、reservation/once handoff 已归 L1 access；HTTP 只转发有界估算回调和映射拒绝。RPM 在估算读取前，TPM 仍走原 once 收尾 |
| text/v1.EstimateTokens、observability/v1.RequestID | 原字符计数和请求关联的纯规则；billing 与 HTTP 复用同一计数，不把关联 ID 当授权 |
| control/v1 Accounts/Keys/Statistics/ModelCatalog/Service | controlplane 拥有登录窗口、owner Key 与模型投影规则；持久适配继续调用原 Store。User/Token/日志/统计/输入均不含 ORM、密码/Key 摘要或 DB，空数组/null 形状保持 |
| ledger/v1.Service 与 DTO/错误/金额单位 | 管理读/对账消费原 Engine 的同一事务；原账本类型名只向普通公开 DTO/错误引用，保留当前管理员决策与双 hold 权威，不增加结算器 |
| responses/v1.Service.Begin / Call / Command / Delivery | 功能只处理具名操作和状态用例；身份、容量与历史速率先检查，正文经 L2 Payload 端口在原业务检查点有界读取。每个 Call 一次 Handle；Close join 后释放，错误或 panic 前未交接的容量由 Begin 退回 |
| compat/responses/v1，Version=1 | 原生输入/终态的复制投影、SSE 终态/父链表示与回放元数据映射；公开签名不泄漏底层 protocol.Request/Decoder 类型。L3 不解析 URL、Header、HTTP 状态表示或 SSE 帧 |

Control/Configuration/账本端口是由组装根显式授予管理入口的能力，不是任意客户端可调用的 API。
L2 保持原管理员会话/旧管理密钥与 owner 会话边界，owner ID 来自当前会话而非正文；
原 Store/Engine 继续持有其事务内当前权限/owner/管理员决策检查。功能接入只注入所需小能力，
不能因拿到一个普通 DTO 或字符串 ID 获得管理权限。

server 的 Deps 只接收公开执行/认证/准入/管理/配置/账本服务、metrics renderer 和 HTTP handler；
main 单点组装原实现、配置发布回调与 metrics provider。Gin 路由/内部构造器是 L2 实现接线，
不作为功能可导入的冻结公共 API。原测试中的 Store/Gateway 构造移入测试组装 harness，仍调用真实生产 handler，
没有用测试文件替代生产实现或删除既有断言。

compat/responseshttp 解析路由、重复查询、If-Match 和幂等头，映射错误/JSON/ETag/缓存/重试头；
正文由其 Payload 实现按 1 MiB+1 读取，所有权交给应用。会话/历史的 CAS、字段限制、加密/AAD、
前台幂等/unknown 和账本关联仍在应用/原事务；不通过搬到 HTTP 层来改变它们。
请求 Scope 的关闭不关闭共享 Gateway/Store，也不把客户端断开解释为无成本或自动重新执行。
新增应用层直接回归验证 owner/空 allowlist、工作 join/单次执行、身份回调 panic 的容量归还及 DTO 空数组/秘密隔离。


## FND-05 完整登记与独立接入候选

所有生产包的精确层级/owner、十四组公开入口、L0 机制版本、递归类型/签名快照、
独立新操作的真实接入与三层 DoD 均见 [接入与 DoD](foundation-integration-v1.md)。
当前仍是实施候选；最终功能头及合并后主线完整 CI 通过后才登记 frozen，不以本地绿灯替代。
