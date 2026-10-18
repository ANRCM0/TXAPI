# R03b 具体变更请求：默认关闭的 OpenAI 原生 Responses WS

状态：R03b-1 已获用户明确批准，正在实现和验证，尚未登记冻结或宣称原生 WS 产品可用。2026-10-19，用户先回复“批准 R03b-1 具体变更”，随后明确“后续不需要我的批准，你自己比较最优方案进行选择”；本任务后续设计由实现者比较取舍并先记录具体边界，不再以逐项用户审批阻塞。分层、兼容、验证和外部证据门槛不变。R03a 功能头 `2111467`、PR #26 及合并后完整 CI 已通过。

本轮实现决定：R03b-1 为受控单轮 host，每轮新建上游 WS 并在返回前关闭，暂拒绝 previous_response_id 和配置了 TPM 限额的身份；不以零估算绕过 TPM。generate:false 缺少已确认的终态 fixture，明确拒绝；generate:true 保持普通生成。R03b-2 再实现持久会话、续接、生产路由与 TPM 策略。管理发布由真实 Configuration/SQLite 测试组装证明，尚未在 main 注册新的资源 owner。

依据：[吸收设计 5.3/5.4](REFERENCE-ADOPTION-DESIGN.md)、[R03a 已批准请求](REFERENCE-R03A-CHANGE-REQUEST.md)。官方协议来源由集成任务查阅：[OpenAI WebSocket mode](https://developers.openai.com/api/docs/guides/websocket-mode)。本文没有真实账号或官方客户端兼容证据。

## 1. 可独立评审的两步

| 批次 | 写入范围与结果 | gate |
|---|---|---|
| R03b-1 | L0 `internal/core/websocket` 增量单次双向 exchange；L1 `internal/gateway` host/资源 owner、`internal/generation` 受控运行接线；L2 `internal/transport/websocket` 上游 native dial、`internal/compat/responsesws` codec；组装根和公开契约 | 本请求获明确批准，R03a 合并验收；真实 WS 测试消费者先证明 R02 prepare/publish/close |
| R03b-2 | L3 `internal/responsesws`；L2 `internal/server` 的 GET `/v1/responses` upgrade 和表示层；main 注册、OpenAPI、客户端回归 | R03b-1 验收后自主记录后续产品具体契约；默认关闭且 billing disabled；只启用一个 Provider 和显式模型 allowlist |

R03b-1 可以有真实测试客户端和可控原生 WS 上游，但不能先注册产品路由或称真实 OpenAI 账号验证通过。R03b-2 当前仅为后续独立批次；按用户自主决策授权记录具体契约并验收后再注册，不混入 R03b-1。其候选范围只开放文本输入/输出及客户端自执行 function tool 的事件与回传；不做托管工具、音频、图片、视频、background、跨连接持久续接、OAuth、收费或抢占。若模型/事件范围不足，明确拒绝，不能偷偷转 HTTP。

## 2. 六项边界与已有能力不足

1. 层级与目录见上表；新增包必须登记精确层级/owner。L3 只消费 `foundation/contracts/*` 和自己的用例，不导入 gateway/core/transport/compat 实现。组装根注入 L1/L2；L2 不建执行账本、调度器或第二条结算链。
2. 复用 execution/v1 的 Phase/Termination、R01 execution/v2 Envelope/UsageObservation、R03a Connection/Lease、identity/v1 Authority、admission/v1 RequestGate、现有连接容量与生命周期端口。冻结状态以集成时登记为准，Go 导出符号不构成冻结。
3. 触碰地基：下面的 execution/v2 增量 exchange 及相应 L0 机械执行新增，需要本次明确批准；responsesws/v1 候选须后续独立记录取舍并验证；不改已有 v1/v2 签名、不引入 execution/v3。
4. R03a Round 是“读同一连接 -> Handle -> 回写同连接”。它没有首个上游 response.create 的写入步骤，不能分别表达上游派发与下游交付；不能拿下游 Read 成功冒充上游已派发。现有 transport 只有服务端 Accept；需要受限的上游 WS dial。RuntimeLeaseSet 仅有 revision/ref 计数，没有资源 close、候选准备/失败清理或 management applied 接线，不能视为 R02 已验收。
5. 验收见第 8 节：单次机械行为、真实 WS、权限/容量、发布排空、现有完整 CI 分开提供证据。
6. HTTP/SSE/background 的行为、账本/schema 不迁移；仅配置发布组装点由集成 owner 串行修改。其他团队继续消费已有冻结端口，不依赖尚未批准的新增接口。

## 3. 最小公开机械签名：追加 execution/v2

### 3.1 L0 精确新增签名

`internal/core/websocket` 保留现有 Frame（Type uint8、Data []byte）与 Connection（Read/Write，没有 ID）；只依赖标准库 context/time/io/errors，不导入 execution/v2、协议、身份或账本。追加以下完整签名，不改变现有 Round/Execute：

```go
type Exchange struct {
    Upstream Connection
    Deadline time.Time
    Initial Frame
    Decode func(context.Context, Frame) ([]Frame, bool, error)
    Deliver func(context.Context, Frame) (int, error)
}

type ExchangeResult struct {
    Dispatch string
    UpstreamWriteStarted bool
    UpstreamBytesWritten uint64
    DeliveryWriteStarted bool
    DeliveryBytesWritten uint64
    ProtocolComplete bool
    Termination string
    Err error
}

func ExecuteExchange(context.Context, Exchange) ExchangeResult
```

L0 顺序为检查参数/context -> Initial 一次 Write -> 循环 Read/Decode -> 对 Decode 输出顺序 Deliver -> complete 后结束。Decode 的 bool 只表示协议所有者声明终态；在 Deliver 之前记录 ProtocolComplete，因此最终终态交付失败时仍保留上游已终态事实。两向 WriteStarted 均在调用网络写/交付之前设置，Dispatch 在 Initial Write 开始前为 NotDispatched、开始时为 Dispatching、首次成功 Read 后为 ResponseReceived；字符串值复用现有 L0 常量。任何短写或非法字节计数均报错，合法部分计数保留；上游写失败为 Failed，下游写失败为 ClientWriteError，context 取消/到期为 Canceled，声明终态且全部交付成功才为 Success。Err 保留实际原因；短写没有底层 error 时使用 io.ErrShortWrite（因此实现另依赖标准库 io），参数错误返回 error 且不进入网络调用。

L0 没有 ID/guard/租约/重试/关闭连接权限；L1 在进入 RoundExecutor 或 ExchangeExecutor 前共享同一个 ConnectionID guard，确保两种入口不能重叠。所有 Decode/Deliver 同步返回且服从 context；返回时本次工作已 join。

### 3.2 L1 execution/v2 签名及桥

下列片段位于现有 package execution，使用现有 Frame、Connection、RoundID、Envelope 和 v1.Phase/v1.Termination；imports 为 context、time、execution/v1。它们是完整新增集合，现有 RoundExecutor 保留。

```go
type FrameDelivery func(context.Context, Frame) (int, error)
type FrameDecoder func(Frame) (Envelope, error)

type Exchange struct {
    Upstream Connection
    ID RoundID
    Deadline time.Time
    Initial Frame
    Decode FrameDecoder
    Deliver FrameDelivery
}

type ExchangeResult struct {
    Dispatch v1.Phase
    UpstreamBytesWritten uint64
    UpstreamWriteStarted bool
    DeliveryBytesWritten uint64
    DeliveryCommitted bool
    ProtocolComplete bool
    Termination v1.Termination
    Usage UsageObservation
    Err error
}

type ExchangeExecutor interface {
    ExecuteExchange(context.Context, Exchange) ExchangeResult
}
```

L1 桥把 v2.Connection 的 Read/Write 与 Frame 逐字段投影到 L0；忽略 ID 的适配不是丢弃所有权，ID guard 在调用前已经持有。L0 Decode 闭包先调用 v2 FrameDecoder，把 Envelope.Usage 累计快照收集到本轮 L1 facts；Envelope.Data 非空时转为一份同类型 L0 Frame，空时返回空帧集合；Envelope.ProtocolTerminal 映射 complete。Envelope.Failed 标记由 L1 记录并把最终成功终止映射为失败；该原始失败终态仍允许交付一次。只有 codec 可以确定这些语义，L0 不看 Envelope 或 Usage。L0 Deliver 闭包逐字段转换 Frame 后调用 v2 FrameDelivery。结果中 Dispatch/Termination 经显式已知常量映射，UpstreamWriteStarted/两向 Bytes/ProtocolComplete 原样复制，DeliveryWriteStarted 映射 DeliveryCommitted，Err 原样保留内部，Usage 使用闭包最后已验证累计快照；未观测则明确 Unknown/SourceUnknown。错误未知常量 fail closed，不能转成 Success。每次 ExecuteExchange 新建闭包状态，不在多个轮次共享可变 usage。

- L1 获准且选定目标后调用；L0 只做 Initial 的一次写、顺序读、调用 decoder 与一次 deliver、取消及水位，不解析 Provider、身份或计费。不新增 L0 Provider callback 注册中心；禁止在现有 Round.Handle 中藏上游网络 Write，再把下游读取水位冒充宿主派发水位。
- 写之前置 UpstreamWriteStarted 和 Dispatching；即使 Write 返回 0+error，已开始网络写也不能证明未执行。完整写成功才记精确字节；短写保留已报告字节并返回错误；不重试。上游 Read 首个事件后可进入 ResponseReceived。绝不把下游交付水位当作上游派发证明。
- Decode 返回 Envelope.Data 是要交付的一份原始 JSON，不再另编码/重复交付。Usage 是累计快照而非增量；缺失保留 unknown。ProtocolComplete 仅来自 decoder；重复终态不再次交付或结算。Err 为内部原因，不能直接序列化给客户端。
- Deliver 调用前保守置 DeliveryCommitted；部分写、0+error 都保留可能交付事实。ProtocolComplete 和交付失败可以同时成立。失败不能伪造 response.completed。
- 同一 Upstream.ID 全局最多一个 active exchange，与已有 round guard 共用连接占用所有权；不允许两个 API 同时读同一物理连接。所有回调同步、有界、服从 context。ExecuteExchange 返回前本次读写/回调全部 join；不自动 Close 可复用连接，是否销毁由 L1 owner 决定。
- 现有 transport Write 只有全成/失败时，不捏造精确部分字节；适配器失败可报 0，但 WriteStarted/Committed 必须保留不确定性。

## 4. 后续独立批次的 L1 原生会话候选：responsesws/v1

新路径 `internal/foundation/contracts/responsesws/v1`，layer L1、owner foundation；imports 为 context、time、identity/v1、execution/v2。此接口同时供 L3 用例及 L2 表示层消费，Provider/目标/密钥保持 L1 私有。

```go
const Version = "1"

type Limits struct {
    MaxMessageBytes int64
    IdleTimeout time.Duration
    RoundTimeout time.Duration
    MaxLifetime time.Duration
}

type Problem struct {
    Code string
    Message string
    CloseCode uint16
    RetryAfterSeconds int
}

type Open struct {
    Binding identity.Binding
    ConnectionID execution.ConnectionID
}

type Create struct {
    JSON []byte
}

type Result struct {
    Facts execution.ExchangeResult
    Problem *Problem
}

type Session interface {
    Execute(context.Context, Create, execution.FrameDelivery) Result
    Close(context.Context) error
}

type Service interface {
    Open(context.Context, Open) (Session, *Problem)
    Limits() Limits
}
```

Binding/ConnectionID 来自可信握手和服务端随机 ID，不能取自客户端 JSON；Binding 是投影而不是权限证明。Open 必须通过 Authority.Current/Refresh 验证可信上下文和绑定一致；不能让任意构造的 Open 产生认证。L2 在鉴权、连接容量与 Origin 检查完成后才 upgrade。L3 仅协调本会话调用与关闭，不自己发送上游请求。

每次 Execute 必须按顺序做：取得唯一轮次槽 -> Authority.Refresh -> 严格解析允许字段/模型 -> 当前模型权限与 billing disabled 校验 -> RequestGate.Admit -> 用户/Key 并发准入 -> 当前代次/目标绑定复核 -> 单次上游 exchange -> Finish/释放。任一步失败不启动上游 attempt；已获得资源都对称释放。并发准入是现有 owner 的能力，不能在 L3 复制计数器。

R03b-1 的真实受控消费者由 L1 测试 host 通过现有权限/准入 owner 与组装注入的固定测试目标执行，不注册产品路由，也不需要先实施本节 Service。后续产品批次首次执行从现有调度 owner 选择一个支持 native Responses WS 的 OpenAI 目标，L1 在内存固定 Provider/channel ID/credential ID、稳定账号身份版本、认证材料版本、实际模型、上游 connection ID、运行 revision 和 expiry；这些不暴露给用户。后续轮次逐项复核目标仍启用且允许当前模型。模型或稳定账号改变时拒绝并要求新连接；认证材料变化在轮次边界关闭旧会话并要求重建，MVP 不热换凭据。删除重建的同名账号绝不复用。

Session 不持久化正文/handle，不建任务表或账本。每会话只保留最近成功 response ID 及绑定和期限；previous_response_id 只接受本会话已观测、同账号/模型/连接且未过期的最近 ID，未知/旧 ID 拒绝，不向任意账号探测。无 previous_response_id 可独立新轮次并替换最近 ID；跨连接、上游重连、进程重启后全部失效。缓存命中后仍重新授权/准入。

Execute 同时调用返回 busy，不抢占；Close 幂等，阻止新轮次、cancel active、等待 active join、关闭上游、释放连接/代次租约，按此顺序返回。Close deadline 到期返回错误且保留 owner 跟踪/依赖存活；不得假报已排空或先关 store。下游断线使当前前台轮次取消并 join；本批不接受后台任务。

## 5. R02 真实资源：L1 私有生命周期 owner

本批不新增 runtimegeneration/v1、公开 Manager/Factory 或 SDK。真实消费者只有 gateway 内部 host 和组装根，复用已批准 execution/v2.Lease 与现有 lifecycle 契约；资源查找、候选工厂、关闭任务和代次集合均留在 `internal/gateway` 私有实现。必要的组装构造函数可以按仓库规则导出给 assembly，但不登记为供 L3 消费的 frozen public 契约；若必须扩充现有公开端口，先记录精确签名和兼容迁移决定。

私有 owner 按 prepare -> validate -> publish current 管理每个不可变 revision 的真实 native WS host/transport。prepare 在锁外、有期限地创建并验证候选，失败清理全部部分资源；转交成功后由 owner 独占关闭责任。publish 先检查候选和退休容量，最终原子切换 current；失败保留旧 current，discard 清理候选。关闭失败保留可重试的待清理引用，不能丢指针或报已完成。新 Acquire 只针对 current；同 current revision 重复发布幂等，退休同名 revision 拒绝。candidate 最多一个，退休代次最多 2；上限命中拒绝发布，不强杀旧资源。

lease 只标识和保活，不暴露 any、ORM 或 client。旧代次最后 lease Release 后只允许一个关闭任务；关闭先 cancel/join 本代次会话和回调再关闭 transport，成功后才从退休集合移除。失败可重试且 close 幂等，不并行重复关闭。drain 停新 acquire/发布，等全部 lease、会话/网络读写及关闭任务退出；超时保留 owner 跟踪和依赖存活。真实 consumer 必须证明旧代次在最后使用者退出之前不关闭，新轮次在代次边界重新建连。
实际源码核对：`internal/management/configuration.go` 的 publication.publish 在互斥锁内调用既有 `apply func() error`，返回 error 时保留 AppliedRevision 并写 LastError，返回 nil 后直接以内存赋值推进 AppliedRevision；没有持久化 applied 步骤，也没有“发布已成功但 applied 写入失败”的专用恢复端口。`Configuration.Apply` 重试既有 desired revision，不重放保存。因此本批不能捏造该错误路径或承诺不存在的恢复 API。R03b-1 首先以现有组装注入的 apply callback 和既有串行 publication 顺序证明：所有可能失败的 candidate 准备/校验/容量检查先完成，唯一不可失败的最终提交点切换当前完整资源快照，返回 nil 后由既有 publication 推进 applied。现有 gateway.Publish 只校验/Reload 渠道，不是两个运行资源 owner 的原子提交接口；不得先 publish gateway 再 publish runtime（或反序）并声称任意失败保留旧全体资源。如果不能在现有 assembler/单一发布 owner 内实现并证明该最终提交点，则真实 management 接线保持未验收，先记录精确新增签名和单一提交点方案，再按后续批次实现，不将未证明的接线标记已验收。真实测试 host 的资源 Prepare/Publish/close 证据仍须完成，但不替代 management 发布验收。Drain 停 acquire/发布，等待在途 lease 与真实资源 close；超时返回错误，main 不得继续关闭共享依赖。

## 6. 协议、限额及错误

- 上游为真实 `wss://api.openai.com/v1/responses`；使用既有静态 API Key，在服务端注入 Authorization。MVP 不支持自定义上游域名、HTTP CONNECT、系统代理继承或跳过 TLS；可控本地 WS 仅测试注入。下游 GET `/v1/responses` 与现有 POST 并存。
- response.create 字段在顶层，不嵌套 response；只允许 type/model/input/instructions/previous_response_id/tools/tool_choice/parallel_tool_calls/max_output_tokens/temperature/top_p/metadata/generate/stream_id/store。stream_id 只允许省略或固定 "main"，省略时在上游请求中显式写入 "main"；store 只允许 false 或省略，省略时显式写入 false，不能沿用 Provider 默认存储。其他值（含 null）拒绝，不开放多路复用或持久存储。JSON 重复键、非法 UTF-8、超过 32 层深度和多个顶层值按现有有界解析规则拒绝。未知字段、stream/background（包括 false）、binary 帧和未知事件显式拒绝。generate:false 是受准入/绑定约束的 prewarm，不跳过轮次授权；如可控协议证据不能确认终态，先拒绝该字段再更新本请求，不能无界等待。
- input 仅文本 message、function_call_output；tools 仅 function（name/description/parameters/strict），不提供工具执行器。metadata 限 16 对、key 64 bytes/value 512 bytes；其 thread/lane 提示不用于抢占或身份。本 MVP 不接受其他 lane/kind。事件保留已列入 codec fixture 的 response 生命周期、text delta/done、output item/content part、function call arguments delta/done、error；未声明事件 fail closed 并结束本连接。SDK/client 支持范围以实际测试版本记录，不能称所有官方客户端兼容。
- 配置 `responses_ws.enabled=false`；allowlist 默认空，启用必须显式给实际模型名，不写 wildcard/自动挑选模型；配置 billing 非 disabled 时启动校验拒绝启用，逐轮仍复核，legacy/strict 均拒绝。
- 固定首批上限：全局 64 个下游会话、每 Key 2 个、每用户 4 个；每连接一个 active、pending=0（这是 TXAPI 子集限制，官方当前协议另支持 stream_id 多路对话和 response-chain fork，本批不实现）；消息 256 KiB；握手 10 秒、写 10 秒、空闲 60 秒、单轮 120 秒、连接最长 55 分钟（上游文档最大 60 分钟之前主动排空）；drain 130 秒。沿用 transport 总缓冲预算校验；不额外建立无限事件队列。每轮之间保持单 reader 处理控制帧，idle/read deadline 不被无限 prewarm 绕过。
- Origin 仅同源或精确 allowlist；无 Origin 的 native 客户端仍须 Authorization。拒绝 query token/cookie 自动鉴权；不转发客户端 Authorization/Cookie/代理头给上游；不记录正文、token、原始上游错误或完整 response handle。

| 条件 | upgrade 前 HTTP | upgrade 后 Problem.Code / CloseCode |
|---|---|---|
| 功能关闭/无启用模型 | 404 | unavailable / 1013 |
| 无效身份/撤销 | 401/403 | unauthorized / 1008 |
| Origin 不允许 | 403 | policy_violation / 1008 |
| 格式/事件/字段错误 | 400 | invalid_request / 1008 |
| 二进制帧 | 不适用 | unsupported_frame / 1003 |
| 大帧 | 413 | message_too_large / 1009 |
| busy | 不适用 | busy / 0（本轮拒绝，保留会话） |
| RPM/并发/容量不足 | 429 或全局容量 503 | rate_limited / 1013 |
| 续接/目标不匹配 | 不适用 | continuation_invalid / 1008 |
| 上游失败/不确定发送 | 502/503（尚未 upgrade 时） | upstream_error / 1011 |
| 寿命到期/正常关闭 | 不适用 | session_expired / 1000 |
| 服务排空 | 503 | draining / 1001 |

Problem 以 `{"type":"error","error":{"type":"txapi_error","code":"...","message":"..."}}` 表示；CloseCode=0 不关闭。安全消息固定模板，RetryAfterSeconds 只用于客户端之后另起请求，不授权服务器重放。前置校验失败可以保持连接的仅 busy；其余错误按表关闭。上游部分写、写超时、网络失败、token 刷新后都不盲重试或转 HTTP；成功重新建连也不自动重放旧轮次。客户端显式重新提交是新轮次，不能保证上游未执行旧请求。

## 7. 迁移、注册与回退

第 3 节新增公开类型登记 visibility/signature snapshot；第 5 节私有资源生命周期不注册新的公共包；L0 新机械签名同步类型映射/架构检查，已有 execution/v1/v2 consumers 不迁移。L2 codec 和 dial 通过组装注入 L1 host；L3 只见第 4 节端口。该 host 复用现有权限/准入/健康 owner，disabled 模式不持有收费 hold，但必须保留派发/交付 unknown 事实，不另建资金权威。

不新增数据库迁移、不改旧 schema/后台任务、不开放收费。R03b-1 不注册产品 GET；R03b-2 增加配置、OpenAPI 的 upgrade/消息范围、HTTP 错误与关闭码，并同步实际客户端 fixture。关闭功能时停止新握手与新轮次 -> 现有轮次排空 -> 关闭会话/transport -> 资源 lease 归零 -> 回退二进制。旧 HTTP/SSE 路由继续工作；不删除账单/任务/密钥，不把错误退化成 legacy 执行。

## 8. 验收及交付记录

| 风险 | 必须提供的证据 |
|---|---|
| 假原生实现 | 真实 WS 客户端 -> 主程序/真实 host -> 可控 WS 上游；断言上游收到 response.create，HTTP 上游调用计数为零 |
| 派发不确定性 | Initial 零写失败/短写/超时、输出部分写、读中断/无终态；水位保留，attempt 计数不增加，拒绝自动重试 |
| 单次交付/用量 | 原始事件逐份一次；累计 usage 不相加；缺失 unknown；重复终态、未知事件与截断明确失败 |
| 权限/续接 | 每轮撤销、降权/限流、模型映射变化；跨 Key 相同提示、未知 handle、旧连接/旧账号同名、过期 handle 都不泄漏或发往别的账号 |
| 并发/关闭 | same-ID round/exchange guard、busy、慢读者、大帧、idle/prewarm、断线 cancel/join；连接/协程/准入许可有上界且退出归零 |
| R02 真资源 | N 流中发布 N+1；新轮次走新代次（旧会话在边界关闭并要求重连）；失败候选清理；旧真实 dial/reader/transport 最后 lease 后 close；重复发布/退休同名拒绝/close 失败重试/Drain 超时依赖保持 |
| 发布一致性 | management 保存成功但 prepare/publish 失败时 desired 保留、applied 不变；既有 apply callback 内的最终提交点及成功后 applied 内存赋值顺序；无双 owner 半发布，不重复发布/丢失资源；无法安全接线时保持未验收并另提精确请求 |
| 产品边界 | 默认关闭、空 allowlist、legacy/strict 拒绝；OpenAPI/实际错误码；文本/function tool 与 generate:false 用可控事件回归；旧 HTTP/SSE/background 回归 |
| 真实供应商 | 只有获得合法账号后，单独记录真实 OpenAI endpoint/model/client 版本和日期；没有则标记未运行，不宣称供应商或官方客户端兼容 |
| 分层与最终门槛 | 架构/可见性/精确签名及独立 L3 接入；批次稳定后本地完整检查，最新集成基线精确功能头完整 CI/Linux race/冒烟；合并后证据另记 |

实施时记录真实命令、环境、源 SHA、基线、结果、未运行项。本文首次提交时为纯文档；实现后的命令与证据统一记入 REFERENCE-ADOPTION-PROGRESS.md。文档存在不代表验收通过，真实账号证据单列。

## 9. 本任务授权与批次边界

R03b-1 的第 3 节 L0/execution-v2 增量、第 5 节私有资源 owner 和真实受控 WS 消费者已于 2026-10-19 获用户明确批准。随后用户明确授权后续最优方案自主选择，不再逐项请求批准。新增签名、行为和资源寿命仍先记录具体方案与取舍，保持兼容和真实消费者验证；不绕过完整 CI、真实供应商证据或发布门槛。第 4 节 Service、生产路由和持久会话仍作为 R03b-2 独立批次推进，不属于本批已验收能力。
