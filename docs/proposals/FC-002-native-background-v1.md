# FC-002：原生后台任务的公开契约与首批接入

状态：**提案待用户确认，尚未实施**。核对日期：2026-10-18。
核对基线：`master/5f90a20a3951b67255a74fa640d9d9f8e92bf8e0`，fetch 后未漂移。
用户指示“继续推进”，授权继续 A03 的评估与提案准备；尚未针对本文件的新地基契约确认实施。
本提案不修改或重新打开 [FC-001 冻结 v1](../reference/foundation-integration-v1.md)。
排期仍只维护在 [PLAN](../PLAN.md#s7-a03持久后台任务与事件恢复)。

## 1. 本批交付与六项边界

本提案建议首先交付 **A03 的 disabled 模式原生后台链路**：本地持久受理、真实上游
`background=true`、查询/取消、原生事件游标与重启恢复。该链路通过后也只验收相应子批，
不勾选完整 A03/M3；计费模式的原子双 hold、Responses 成本证明及完整历史/会话组合仍须独立完成。

| 必填项 | 提议范围 |
|---|---|
| 所属层级与目录 | L1 新 `foundation/contracts/background/v1`、既有 generation/gateway/store 的 owner 实现；L2 原生后台 codec 与 HTTP；L3 responses；main 单点组装 |
| 依赖的公开接口 | 冻结 execution/identity/state/lifecycle v1 继续使用；新增 background/v1 与 responses/v2 是本提案候选，不提前标记冻结 |
| 是否触碰地基 | 是：新增异步任务与远端控制的小端口、持久事实和有界 runtime；复用原调度/当前身份/网络准入及单次 core I/O，L0 不改 |
| 契约变化 | 增加具名后台操作、任务/事件投影和 HTTP v2 应用命令；现有十四组公开入口签名与语义保持，不向原 interface 加方法 |
| 测试与验收 | 原回归保留；临时 SQLite/假原生上游的实际受理、领取/fencing、查询/取消、事件与强杀矩阵；集中完整本地及精确头/合并后现有完整 CI |
| 并行影响 | 新契约未验收前不让其他功能依赖它；主线新增注册、迁移、配置、OpenAPI、main 由本批统一协调，A04/WS 不夹带实施 |

disabled 表示网关明确不向用户计费，不表示上游免费或厂商成本已有严格上界。
legacy/strict 的后台创建继续在持久受理、网络请求和资金变更之前明确拒绝；不得伪造零金额双 hold，
不得把 disabled 的成功套为原计划“收费任务与双预算原子受理”已完成。

## 2. 已核对的源码缺口

| 证据 | 当前行为 | 必要变化 |
|---|---|---|
| [billing/intents.go](../../internal/billing/intents.go)，AdmitGenerationIntent | StrictReady 才可受理，只接受 Chat/embeddings；期限最多 5 分钟，并与原 hold 租约匹配 | 不能把该端口直接解释为 Responses 后台；保留旧语义，新增显式后台受理能力 |
| [gateway/coordinator.go](../../internal/gateway/coordinator.go)，executeTo/execute | claimed 执行要求 strict；Responses claimed/strict 明确拒绝 | 原拒绝保持；后台生命周期不能靠普通 Execute/Finish 提前结算 |
| [state v1](../../internal/foundation/contracts/state/v1/state.go) | IntentLeases 是原生成 claim；Cursor 是 sealed 编码字节游标 | 不把字符串 cursor 解释为原生 sequence_number，不把原 claim 当后台任务许可 |
| [Responses decoder](../../internal/protocol/responses/decoder.go)，Decode | queued/in_progress 在同步路径不是合法完成 | 新后台 codec 分开处理一次 RPC 结束和整个远端任务结束，原同步 decoder 不放宽 |
| [responses v1](../../internal/foundation/contracts/responses/v1/responses.go) | 无取消命令或 stream/starting_after 的读取投影 | 新 v2 应用入口承载扩展，旧 v1 对未知命令及查询仍保持原拒绝 |
| [前台扩展边界](../reference/responses-http.md#background-extension-boundary) | 前台 receipt 不是 runnable intent，也不能授权后台记账 | 后台由新 owner 持久任务事实授权，不能借前台 receipt 建第二条执行链 |

## 3. 官方协议依据与本地产品范围

2026-10-18 实际读取的官方依据：
[OpenAI Background mode](https://developers.openai.com/api/docs/guides/background)。
指南明确后台创建、按 ID 获取、取消，以及流式后台结果的游标恢复；原生状态须保持 queued/in_progress 与终态的区别。
只有最初流式创建的任务支持流式恢复。取消已完成任务应返回其终态，不能改写已完成结果。
官方还说明后台会有临时存储；`store=false` 也不代表零保留，后台不满足 ZDR。
官方描述的保留时长是近似值，不作为本项目 TTL、可靠恢复或成本证明的保证。

首次实现范围：标准 API Key、显式配置为支持后台的原生 Responses 上游，文本/函数工具的既有可表示子集。
不根据提供商名称或 OpenAI-compatible 标签自动断言能力；注册缺失/不兼容/typed nil/缺必要回调均拒绝启动或派发。
不将 background 改成同步请求，不让 L3 发 HTTP，不引入任意工具执行。

先支持自包含输入；后台创建中的 previous_response_id/conversation 在本批明确拒绝，
保留 A02 前台续链原行为。后台任务资源与 A02 长期历史的联合提交/续链另行验收，不异步偷偷升级为长期历史。
真实 OpenAI/SDK 兼容仍需获授权的独立测试 Key、预算与数据范围；本地假上游通过只证明实现行为。

## 4. 公开契约差异

### 4.1 新增 L1 background/v1

所有 ID/状态/字节都是投影，不是授权；方法从可信 context 重新校验 owner、创建 Key 和当前模型权限。
本地公开任务 ID 与上游 ID 分离；上游绑定、凭据、claim token 和 DB 均不暴露给 L3。

以下为本提案需确认的完整核心签名；`Version="1"`。示例中的 execution 是冻结 execution/v1。

```go
type Submit struct {
    ID string
    Operation execution.Operation
    Body []byte
    IdempotencyKey string
    Stream bool
    Store bool
    Deadline time.Time
}

type Receipt struct {
    ID string
    Revision uint64
    Stage Stage
    Model string
    Document []byte
    CancelRequested bool
    CreatedAt time.Time
    ContentExpiresAt time.Time
}
type Stage string
const (
    StageQueued Stage = "queued"
    StageDispatching Stage = "dispatching"
    StageRemoteActive Stage = "remote_active"
    StageTerminal Stage = "terminal"
    StageUncertain Stage = "uncertain"
)

type EventQuery struct {
    After *uint64
    Limit int
}
type Event struct {
    Sequence uint64
    Data []byte
}
type EventPage struct {
    Events []Event
    Next *uint64
    Terminal bool
}
type Service interface {
    Submit(context.Context, Submit) (Receipt, error)
    Read(context.Context, string) (Receipt, error)
    Cancel(context.Context, string) (Receipt, error)
    Events(context.Context, string, EventQuery) (EventPage, error)
}
```

Stage 仅描述 queued/dispatching/remote_active/terminal/uncertain 等任务事实；取消请求是独立标记。
它不是公开原生 Response.status；后者由 L2 根据已持久的原生文档/观察映射，不能把 RPC 成功当任务完成。
Document/Data 是已校验并复制的有界协议字节，禁止 any、任意对象/事务回调或共享可变缓存。
接收时的 Store/Stream/Model 必须与纯 parser 从不可变正文读取的值匹配，不能信任单独的布尔投影。
Submit.ID 由受信任的 L3 应用随机生成公开 response ID，不从客户端正文采纳；L1 在与 A02 资源同一事务中验证唯一。
幂等请求摘要不包含该随机候选 ID；同键同参的重复接收返回原 ID，不生成第二个任务。
内部任务 ID 仍独立，L2 解码器通过已持久的公开 ID 重写原生身份字段，原生上游 ID 留在私有绑定。
Events 默认最多 64 条、上限 128 条且整页最多 64 KiB；单事件超过上限须明确失败。
After/Sequence 仅接受原生可表示的非负有符号 64 位范围，不把任意 uint64 原样发上游。

新增错误限定为 ErrUnavailable、ErrUnauthorized、ErrNotFound、ErrConflict、ErrCapacity、
ErrUncertain、ErrCursorUnavailable、ErrUnsupportedBilling、ErrUnsupportedCapability。
错误消息脱敏，errors.Is 身份稳定；未授权/跨 owner/跨 Key 与不存在对外统一不可枚举。
同幂等键异参冲突；相同请求返回原任务；查询、取消和事件读取不创建新 generation。

### 4.2 新增纯 codec 伴随端口

不修改冻结 execution.Adapter/Request/Input，也不向 Invocation 塞入跳过授权或结算的字段。
新 background/v1 同组增加如下注册值，由 L2 实现、main 注入，L1 复制后只读持有：

```go
type RemoteAction string // submit / inspect / cancel / resume
type RemoteCommand struct {
    Action RemoteAction
    Body []byte
    RemoteID string
    After *uint64
}
type RemoteState string // accepted / running / succeeded / incomplete / failed / cancelled
type Observation struct {
    RemoteID string
    State RemoteState
    Document []byte
    Events []Event
    Usage *execution.Usage
}
type Observer interface { Drain() Observation }
type Prepared struct {
    Input execution.Input
    Progress Observer
}
type Codec interface {
    Prepare(RemoteCommand, execution.Target) (Prepared, *execution.Error)
}
type Registration struct {
    ID string
    Version string
    Operation execution.Operation
    Parse execution.Parser
    Codec Codec
}
```

RemoteCommand 只由 owner runtime 根据持久任务和内部动作构造；L3 不获得 RemoteID 或 Target 选择入口。
每次 Prepare 独立创建 decoder/observer；Drain 同步取出本次新增且有界的观察，不能跨请求复用或自行 I/O。
L1 通过原 core 执行 Prepared.Input；在输出回调/收尾中先持久观察、绑定与事件，再交给观察客户端。
Drain 重复调用没有新事件；codec 输出与 wire target 不匹配、序号冲突或越界均拒绝，不静默丢弃。
一次 HTTP RPC 的 phase/committed 与任务终态分开，原终态与 usage 都持久成功后才结束任务。
不复制旧协调器：复用原当前身份、选路/绑定、请求/attempt 准入及 core I/O 的 owner helper；
若实现时需要额外修改冻结签名或突破本文件的计费/权限边界，先补充具体提案，不能自行扩大确认。

### 4.3 新增 L1 responses/v2 应用命令

保留 responses/v1 的全部常量、签名、DTO 与已有消费者；不因为新功能重定义旧查询行为。
新 v2 组合旧 v1 的值，L3 实现和 L2 HTTP 消费；`Version="2"`：

```go
const CancelResponse appv1.Action = "response.cancel"
type Command struct {
    Base appv1.Command
    Stream bool
    StartingAfter *uint64
}
type Call interface {
    Handle(Command, appv1.Delivery)
    Close()
}
type Service interface {
    Begin(context.Context, appv1.Action) (Call, *appv1.Problem)
}
```

v1/v2 用不同 facade 组合相同内部用例，不令同名 Begin 返回不同类型，也不重复创建容量或授权链。
v2 CancelResponse 只能交给 v2 facade；旧 v1 仍拒绝。原 appv1.Delivery 保留同步有界写、Call.Close join 和单次执行规则。
URL、query、SSE/HTTP 状态/头仍在 L2；事件查询必须显式区分缺游标与零序号，拒绝重复、负数、溢出和非流式 starting_after。

最终公开清单新增 background/v1、responses/v2 两项，旧十四项和 L0 快照不得漂移。
architecture 按显式登记的包/版本验证 v2，不把“所有 /v2 都允许”作为新豁免。
公开类型继续递归校验别名、私有字段、泛型实参和回调；本提案候选在全部消费者与最终 CI 验收前不冻结。

## 5. 事务、权限与恢复

1. L3 在原 Begin 授权/容量保护下读取有界正文，再由注册 parser 验证自包含输入和后台能力。
2. L1 单事务重新检查当前身份/模型、幂等/HMAC 命名空间及 owner/实例容量，提交加密输入和 queued 事实。
   首批固定 disabled；不生成 billing hold，不调用 PreDeduct，不写另一套余额。
3. worker 只在有空闲槽时原子领取，lease/epoch/token 与派发状态条件更新。领取并不证明上游未执行。
   queued 取消可在事务内阻止派发；转入 dispatching 后，客户端断开或 lease 过期都不能视为安全重发条件。
4. 每次新的生成派发刷新当前 owner/Key 权限，选路和目标指纹固定在任务；取得原请求/attempt 准入。
   原子持久 dispatching 后才发送真实 background=true。排队不持有网络槽；远端未终态仍占后台在途容量。
5. 原生上游 ID 及渠道/凭据/模型指纹须 fenced 持久后才对外确认该远端事实。
   POST 可能发送但 ID 未持久时标为 uncertain；没有已验证上游幂等保证，绝不重新提交。
6. 重启后对已绑定 ID 仅 inspect/cancel/resume；这些是有独立读取配额的控制 RPC，不是新 generation，不计第二次网关生成 RPM/费用。
   接口不接受任意远端 URL/ID；凭据被删除/轮换或绑定改变时拒绝换账号，保留不确定诊断。
7. Key/用户撤销后禁止新派发及用户数据访问。owner 内部 cleanup 仅能对已有绑定任务 inspect/cancel，
   不能创建生成、代替管理员向用户授权或把旧 Principal 当永久凭证；该控制权限不可公开构造。
8. 取消先 durable cancel_requested，再向已绑定上游发送取消；已终态返回同一终态，未知不改成 cancelled。
   完成/取消/迟到 usage、lease 丢失和重复事件均通过 epoch/revision CAS 解决。
9. sequence_number 唯一且单调；相同序号不同字节视为完整性错误。事件/游标先事务提交再公开。
   只有原始 stream=true 才允许恢复；缺事件、过期、上游不支持或不可重建均明确失败，不补造 DONE/缺失事件。

本地 queued 表示已持久受理，并不宣称远端已经受理。原生事件仅来自真实上游；本地队列状态不能伪造上游事件序号。
DB 写失败须有界失败/背压，不无限缓冲。未知任务保护幂等墓碑和容量，不能 TTL 清理后重新提交同一未知生成。

## 6. 数据、HTTP、配置及关闭

- 新任务、绑定和原生事件使用同一 Store 的统一迁移、唯一约束及 CAS；实施前按最新 schema v12 追加下一版本，提案不抢占迁移号。
  新表/索引只新增，不复用前台 ResponseRequest 做 runnable job；已有 v9/v10 intent 和 v11/v12 密文/AAD 保持可读。
- 内容复用现有密钥 ring/稳定 digest key，使用独立 domain/AAD，明文 provider Key、请求和输出不进日志。
  Metadata/幂等 HMAC 不保存原始 Key 或原始幂等键；公共 ID 也不替代身份。
- 建议初值：2 worker、实例/owner 后台活跃上限 16/2、记录上限 1024/128、加密内容容量 64/16 MiB、
  单任务输入 256 KiB、事件/结果共 2 MiB/8192 事件；控制 RPC 最长 10 秒，单个任务最长 5 分钟，轮询间隔至少 1 秒且有退避。
  参数有明确合法范围和全局读取上限，不能用每个任务的定时器数量绕过实例并发控制。
- 临时恢复内容默认保留 10 分钟（本地策略，不等于上游保证）；响应/文档必须说明后台 store=false 也会临时保存。
  到期清除加密正文/事件；未决任务可先在截止时间发起取消，未确认不伪造结束或释放保护。
  到期后保留有界非内容 uncertainty/幂等元数据并明确不可恢复；不得为了恢复无限延长已声明正文保留。
- `responses.background.enabled` 建议默认 false；显式开启还要求 Responses 总开关、实际凭据加密、挂载内容/digest 密钥以及 billing=disabled。
  legacy/strict 配置启用后台须启动失败；存在未决后台任务时切计费模式须拒绝，不能静默改变其接受时语义。
- HTTP 扩展同批更新 OpenAPI：现有 POST 创建增加后台子集；GET 读取任务或流式事件；新增 POST `/v1/responses/{response_id}/cancel`。
  ID 空间需在 owner 事务内保证与 A02 资源不冲突，GET 按资源种类只走一条权威路径，不以查找顺序跨库漏数据。
  删除与取消不同：本批对未终态后台 DELETE 明确冲突；终态删除/过期不复活任务，也不删除 protected uncertain 元数据。
- 新公开取消操作使契约预计从 0.4.0/91 操作扩为 0.5.0/92 操作；实际以实施时的真实路由/字段对照验证为准，不由本文提前发布。
- main 唯一启动/停止 owner runtime；先停受理，停止/等待 worker 和控制 RPC，再关闭共享执行器、日志与 Store。
  客户端断开只结束观察；服务关闭不等于取消远端。Close 复用 lifecycle/v1.Drainer 的有界 join，不允许超时就关闭在用依赖。

## 7. 按顺序实施与验收

| 批次 | 可独立验收结果 | 不提前宣称 |
|---|---|---|
| A03-01 | 新候选契约、精确包归属、owner 持久受理/lease/fencing 与当前权限；disabled 模式、容量与重启拒绝矩阵 | 不等于已经有公开原生后台 |
| A03-02 | 显式注册原生 submit/inspect/cancel、现有 HTTP/应用 v2 接线、真实假上游断开与重启查询/取消 | 不等于事件恢复或真实提供商认证 |
| A03-03 | 原生 sequence 持久恢复、TTL/背压/密钥/关闭/强杀窗口、OpenAPI 与完整交付验证 | 仅验收 disabled 子批，不勾完整 A03/M3 或 S5 成本项 |

每批从最新已验收 master 开工，批量实现后集中验证/修复；不在每次编辑后全量 race、冒烟或手动触发 CI。
保留原 Chat/embeddings、前台 Responses、历史/会话、财务及恢复全部断言，不能靠删回归制造成功。
关键负面/故障覆盖：伪造 owner/claim、权限缩减、原生 queued 被误当完成、旧 lease 写入、任务已提交未发送、
上游已接受未保存 ID、保存 ID 后断开/重启、终态未落库、事件提交未送达、取消/完成/usage 竞争、密钥变更、磁盘写失败、慢读和到期清理。
最终精确头须通过现有契约/依赖/API 快照、构建/vet/格式、Linux 全量 race、主程序冒烟及容器交付，合并后 master 再通过。

后续收费后台必须单独完成原 Engine 的原子任务+用户/Key 双 hold、异步最终结算、长任务租约与 Responses 成本许可；
不能通过本次 disabled 路径免除。背景父链/会话、WS 与指定真实 SDK/提供商组合也保留原计划的独立验收。

## 8. 替代方案、回退与确认范围

拒绝的替代方案：前台请求外套 goroutine、背景请求改同步、直接用 strict Chat intent、
上游 queued 立即结算、L3 自建 HTTP/SQL、修改冻结 v1 的未知行为、未知后盲重发或把 store=false 宣传成零保留。
纯透传虽然改动少，无法提供任务所有权、恢复/取消、事件持久和容量边界；本提案采用显式 owner 端口。

回退先关新接收并 drain/停写备份，保存未决任务/幂等与密钥；不得删除表/未知记录、重发远端生成或改变旧账本。
有未决任务时保留支持新 schema 的查询/取消恢复版本，不用旧备份覆盖已接收任务事实。
退回旧二进制须另核对 schema/密钥兼容与停写迁移，不能宣称旧程序能直接启动或远端任务已结束。
不自动操作真实数据库、真实上游、远端治理或凭据，也不引入新依赖。

请求用户确认：允许按本文 A03-01 → A03-02 → A03-03 实施两个新公开契约包及所列迁移/注册/配置/HTTP 接线，
首批仅 disabled、自包含输入；保持现有冻结 v1、legacy/strict 拒绝、后续双 hold/成本与完整 A03 验收要求。
确认只覆盖本文具体边界，额外签名/权限/持久格式或产品范围变化重新提出。

## 9. 本次提案准备的验证记录

环境为 Windows PowerShell；基线 master/5f90a20，fetch 后未漂移。
实际执行 `git diff --check`、`python .local/validation/check-foundation-all-docs.py` 和
`python .local/validation/s7-source-fingerprint.py`：差异/状态一致性与全仓 29 份 Markdown、319 个本地链接/锚点通过。
356 个非 Markdown 路径 SHA-256 仍为
`2b714cf86fb8f420bed280eff6c57902cfddaeb1faaef9d5051cb11d5fec64b4`，与已验收地基相同。
只新增本提案并更新 PLAN 的待确认入口；未运行 Go/浏览器行为测试，未修改运行代码、公开 Go 契约、
API 快照、OpenAPI、schema、配置或 workflow，未手动触发 CI，未操作真实提供商或生产数据。
本批只作本地提案提交，未推送/创建 PR；不将文档验证或旧代码 CI 作为 A03 功能完成证明。
