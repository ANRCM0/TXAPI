# Foundation v1：接入、归属与 DoD

## Reference Adoption Additions

R05a-3 appends candidate `usagehistory/v1` trusted Recorder and session-authorized
Repository/Service values. Existing execution/v1/v2/v3 and all earlier public
declarations remain exact. Foreground host recording and real L3 usageconsole
consume the new port; neither introduces financial or replay authority. See
[durable metadata integration](usage-history.md) and the
[implementation decision](../../REFERENCE-R05A3-IMPLEMENTATION.md).

R05a-2 appends candidate `quotation/v1` Reader/Service values for version-pinned,
read-only selling calculations. L1 billing owns arithmetic and history; L3
quoteconsole consumes current sessions/shared admission, and the independent
facts sample consumes the reader around a real JSON/SSE generation. No existing
registered signature changes or new freeze are included. See the
[selling quotation guide](usage-quotations.md) and
[implementation decision](../../REFERENCE-R05A2-IMPLEMENTATION.md).

R05a-1 adds candidate `execution/v3` for optional native token breakdowns,
with a real JSON/SSE decoder and independent L3 consumer. Existing registered
declarations remain exact; this is not a new freeze. See
[quantity semantics and integration](usage-observations.md) and the
[implementation decision](../../REFERENCE-R05A-IMPLEMENTATION.md).

The R01/R03 batches append execution/v2 and native WS mechanisms without
changing the original frozen declarations. R03b-2 registers responsesws/v1 as
a candidate public application port, consumed by the real L3 client and L2
HTTP adapter; it is not a new foundation freeze. See the [runtime contract](responses-websocket.md)
and [exact acceptance record](../../REFERENCE-ADOPTION-PROGRESS.md). Historical
package and entry counts below describe their original freeze checkpoints;
the architecture inventory and signature tests verify the current source.

状态：2026-10-18 **v1 / frozen**；代码基线 master/889ecbd（PR #18，功能头 38a8cce）。
L0/L1/L2 DoD、独立样例、最终头与合并后完整 CI 均通过；冻结记录合并后按本页公开契约开放 L3 并行。
批准范围见 [FC-001](../proposals/FC-001-foundation-contracts-v1.md)，唯一排期见
[PLAN](../PLAN.md#地基符合性收口fc-001)，精确来源及验收记录见
[实施记录](../history/implementation-fc001.md)。本页说明仓库内接入，不发布外部 Go SDK。

## 包级归属与公开入口

[layers.json](../../internal/architecture/layers.json) 逐个登记所有含生产 Go 文件的包，
包括测试假上游和嵌入 web；没有目录通配豁免。新包必须先登记 owner/layer，
失效登记也会失败。测试文件可以组装实现，生产源码始终受规则约束。

| 层级 | 包与责任 | 可消费的项目依赖 |
|---|---|---|
| L0 / mechanism v1 | core 单次 HTTP 执行、输出、解码与提交事实；Version=1 | 仅标准库；L3/L2 不直接导入 |
| L1 / framework | access、billing、config、gateway、generation、logging、metrics、model、scheduler、secrets、store；foundation 契约和桥 | L0/L1；契约包仅标准库和其他公开契约。gateway 不导入厂商 codec |
| L2 / compatibility | protocol 的各独立包、compat 的适配/门面、server/API/middleware、transport/websocket、web；tests/upstream 的受限假上游 | 其他 L2 或 L1 公开入口；不导入 L3、私有桥、Store/Engine/Gateway |
| L3 / features | responses、management、controlplane、examples/summary | 仅登记的 L1/L2 公开契约，不直接导入其他功能实现 |
| assembly | cmd/gateway | 唯一生产组装根；注入现有实现与回调，不放业务规则 |

L1 公开入口为 foundation/contracts 下 admission、configuration、control、execution、identity、json、
ledger、lifecycle、observability、responses、state、text 的十二个 v1 包，以及 foundation/httpoutput/v1。
L2 公开门面为 compat/responses/v1。所有包均声明 Version="1"；httpoutput 门面仅供 L2 使用，
L3 接收 Output/Delivery，不构造 HTTP writer。其余 Go 导出名是实现接线，不自动成为功能可依赖的 API。

FC-002 又按用户批准顺序完成 A03-01/02/03。追加的 L1 `background/v1`（Version=1）
与应用 `responses/v2`（Version=2）现按已验收的 disabled、自包含原生后台范围冻结；
功能头 `501507d`、PR #22 与 `master/a44d4d1` 的完整 CI 均通过。
上述十四组与 L0 没有签名/语义扩张；原 FND-05 证据仍只证明其原范围。
追加入口的七项 DoD、真实独立 L3/L2 消费者和最小运行命令见
[本页追加冻结](#fc-002-追加公开入口冻结)，资源寿命与限制见 [后台约定](responses-background.md)。
接口、DTO、错误、资源规则、消费者与权限语义逐组见 [API 清单](foundation-api-v1.md)。

[public-v1.json](../../internal/architecture/public-v1.json) 保存 L0 机制及十六组当前公开入口（原十四组加 FC-002 两组）的完整导出声明、
常量值、结构字段/标签、接口及方法签名。正常测试比较编译器类型信息与该快照；不允许只改签名后保留绿灯。
新接口、签名、权限或寿命变更仍先提出具名提案并获用户确认，再迁移所有消费者和更新快照；
快照生成工具不构成批准。Go interface 新增方法也可能破坏实现者，不当作天然兼容。

递归检查追踪别名、具名类型、私有字段、容器、回调、方法、泛型参数/约束及标准库泛型实参，
拒绝 core/gateway/protocol/model/ORM 实现类型和无类型逃逸。标准库 Context/Header 等原生契约可用，
不会递归检查整个标准库实现。L0 原有 opaque error code 由 L1 转换为 JSON，不能作为 L3 任意值通道。
编译依赖检查不是不可信插件沙箱；回调仍是经过评审的可信代码。

## 独立操作样例

最小可运行样例由三个独立部分组成：

- [L3 summary 服务](../../internal/examples/summary/service.go) 只依赖 execution/identity v1。每次调用先 Current/Refresh，再调用同一个 Executor；构造 Principal 或沿用上次结果不构成授权。
- [L2 summary codec](../../internal/compat/summarycodec/codec.go) 只依赖公开值与标准库，接收组装者给的 operation ID；验证有界、无重复/未知字段的 model/text，输出准备后的 wire 值，每次 attempt 新建 decoder。它不导入 L3、创建网络 client 或记账。
- [独立集成 harness](../../internal/examples/summary/integration_test.go) 创建临时 SQLite、真实 Store 身份、CapacityAdmission、Gateway/PublicExecutor、HTTP Output 与 loopback 上游。只有 harness 可导入各层实现。

运行方式（Windows PowerShell）：

```powershell
$env:GOARCH = 'amd64'
go test -count=1 ./internal/examples/summary
```

Linux/macOS 直接运行同一 go test 命令。无需实际凭据、外部提供商、配置文件或生产数据库。
样例不是新生产路由；真实入站 HTTP consumer 由集成 harness 临时启动，入口可把同一服务/输出契约注入自己的 L2。

组装时创建 summarycodec.Registration(summary.Operation) 与 summarycodec.Protocol("openai", summary.Operation)，
通过现有 Options.Operations/Protocols 注入。这里 openai 是临时渠道使用的注册键，
表示用样例 codec 替代该测试实例的绑定，不宣称 summary wire 是 OpenAI 兼容协议。
框架源码没有新增操作 switch、默认回落或自动路由。缺操作、缺协议/能力、未证明的 strict 均在网络发送前拒绝。

样例验证实际模型映射、鉴权头、原始文本、独立 decoder、输出字节/usage/提交事实；
还验证伪造 context、当前 Key 撤销、未知字段/重复 JSON/模型越权、并发达到上限与完成后容量归还。
财务、unknown、恢复和持久 fencing 继续由原全量回归验证，样例使用 disabled，不创建第二套账本或 strict 豁免。

## 分层 DoD 证据位置

以下各项已由 FND-05 的精确功能头与合并后完整 CI 验收；命令、来源、初次失败修复与边界见实施记录。

| DoD | L0 | L1 | L2 |
|---|---|---|---|
| 版本/公开类型 | core.Version=1，L1-only 机制声明及 API 快照 | 十二组冻结契约与输出门面 Version=1、递归类型检查 | responses/v1 复制门面、操作/协议/HTTP 的中立端口；其余接线为私有实现 |
| 关键行为/最小运行 | [执行/取消](../../internal/core/core_test.go)、[输出/短写](../../internal/core/output_test.go)，样例实际派发原 core | [注册](../../internal/gateway/registered_test.go)、[容量](../../internal/gateway/capacity_test.go)、原 billing/generation/store 全量与独立样例 | [协议输入](../../internal/protocol/request_test.go)、[工具保真](../../internal/protocol/tools_test.go)、HTTP/OpenAPI/前端与真实样例输出 |
| 扩展与资源寿命 | HTTP attempt 只管本次 I/O；[内部边界](foundation-contracts.md) | 原授权/准入/成本/attempt/收尾、状态 CAS/fencing 和共享关闭；[API 清单](foundation-api-v1.md) | 注册与独立 session；HTTP 输出/Body/Call 的 join、关闭与缓存/头表示；同上清单 |
| 错误/日志/配置约定 | phase/committed/protocolComplete 保持不同事实，原错误不被成功覆盖 | 未证明即拒绝、unknown 保留原 hold；保存后错误/发布状态、请求关联和脱敏仍按 [开发指南](../DEVELOPMENT.md) | 不可表示语义拒绝；流已提交不能改状态/假造 DONE；91 操作/配置与前端契约保持 |
| 范围与限制 | HTTP/SSE 机制；WS I/O 不等于产品执行接线 | 单实例 SQLite/有界资源与既有 strict 许可；不新增成本证明 | [支持矩阵](compatibility-contracts.md)；Responses 默认关闭、strict 全拒绝，后台/原生 WS 未交付 |
| 独立上层证明 | L3 源码无法导入 L0 | summary 只用公开身份/执行端口，实际经过原协调与容量 | codec 不导入功能，真实 loopback/HTTP consumer 验证字节与拒绝路径 |

本次已在最新集成基线上通过自动完整 CI（契约/依赖、构建/vet/格式、Linux 全量 race、
主程序冒烟、容器安装/升级/备份/退出），合并后主线也通过，现记录 frozen。后续变更保留同一完整合并门槛。
S5-03、A03/A04、后台/WS 与每种新厂商能力仍需各自验收，不随地基冻结变为已完成。

## 已批准变更的快照工具

只有在具体提案与消费者迁移获准并完成后，才导出新的 API 快照进行评审：

```powershell
$env:GOARCH = 'amd64'
go test -count=1 ./internal/architecture -run TestVersionedPublicSignaturesHideImplementationTypes -record-foundation-api C:/absolute/reviewed-api.json
```

记录选项只向显式路径导出；正常 CI 不传该选项，始终校验仓库内 public-v1.json。
审阅签名、行为回归与兼容/回退后，同批更新登记、快照、所有消费者和文档；
不兼容接口优先另建 v2，旧冻结版本保留受控迁移期。没有 schema/密文格式变更的批次可整体退回前一匹配版本；
不得借回退重发 unknown、释放原 hold 或删除迁移。

## FC-002 追加公开入口冻结

2026-10-18，A03-03 精确功能头 `501507d4c070208ed8e234cf6ff9cb19c156e25c` 经 PR #22
合并为 `master/a44d4d147b7309679fa5b7005ed34c12473e4bf0`，合并树与功能头完全相同。
push `39130613526`、PR `39130658258`、master `39131280478` 的 check/delivery 全部成功且无失败步骤，
包含 Linux 全量 race、真实主程序冒烟、v15 容器安装/升级/备份恢复/退出。
精确命令、首次失败修复、来源与限制见 [FC-002 最终记录](../history/implementation-fc002.md#fc-002-最终验收与冻结)。

冻结增加两个入口，不重开原十四组/L0，也不发布仓库外 SDK：

| 入口 | 版本与实际消费者 | 冻结范围 |
|---|---|---|
| [background/v1](../../internal/foundation/contracts/background/v1/background.go) | Version=1；L3 Responses 的 Service/Resources、L2 原生 parser/codec，main 注入 L1 owner/Store | disabled、自包含文本/函数工具的受理/读/取消/终态删除及原生事件；Submit/Receipt/Stage、EventQuery/EventPage、具名 RemoteCommand/Observation 和复制后只读 Registration |
| [responses/v2](../../internal/foundation/contracts/responses/v2/responses.go) | Version=2；L3 的独立 V2 facade，由 L2 HTTP v2 调用 Begin/Call.Handle/Close | 组合原 v1 Command，新增 CancelResponse、Stream 和 StartingAfter；原 v1 的签名、查询拒绝、容量/身份/关闭语义保持 |

全部 59 个生产包有精确 owner/layer；当前十六组公开入口与 L0 共十七项类型/签名快照持续校验。
A03-03 与已验收 A03-02 的全部十七项完全相同；A03-02 唯一符号增量为用户另行批准的 Resources 补充。
不存在由候选 Go 导出名推断冻结、任意 v2 通配豁免、L3 私有实现依赖或无类型事务回调。

| 冻结要求 | 真实证据与边界 |
|---|---|
| 文档/版本 | 两包 Version 常量、[完整 API 清单](foundation-api-v1.md#fc-002-追加冻结接口)、layers/public-v1 登记；[批准提案](../proposals/FC-002-native-background-v1.md) 与 [Resources 补充](../proposals/FC-002A-resource-ownership-v1.md)；背景指南规定资源/错误/限制 |
| 契约测试 | [受理/当前权限/fencing](../../internal/generation/background_test.go)、[资源分类/CAS/墓碑](../../internal/generation/background_resources_test.go)、[纯原生 SSE](../../internal/protocol/responses/background_stream_test.go)、[实际 HTTP v2](../../cmd/gateway/background_stream_test.go)、原 v1/A02/全量架构和 OpenAPI 回归；最终本地 1207 项/30 包，六个预期子进程 helper 跳过 |
| 最小可运行样例 | 下方命令使用临时加密 SQLite、挂载测试密钥、真实 owner/gateway/core 与 loopback 原生上游；[生产二进制 smoke](../../tests/smoke/background.py) 由既有 local.py 自动调用，真实 SIGKILL/重启查询/游标/取消/关闭/模式拒绝/feature-off TTL；不依赖真实提供商 |
| 扩展说明 | 新 L2 通过具名 Registration 的纯 Parser/Codec.Prepare 返回每次独立的 Input/Decoder/Observer，main 显式注册/选择渠道，L1 拥有受限派发。v2 是独立 facade，不给 v1 接口强加新方法；进一步签名/语义/权限/寿命变更仍先具名批准 |
| 基础约定 | 当前 owner/创建 Key/模型与事务权威不变；有界 worker/租约/网络/读/写期限，错误脱敏、不记录密钥/正文，固定 TTL 与关闭 join；native sequence 的 nil/0 不同，JSON 任务终态不等于事件尾部完成，持久后再公开，观察断开不取消任务，unknown 不盲重发；细则见后台指南 |
| 限制清单 | 默认关闭，仅 disabled、自包含文本/函数工具、明确 opt-in 的原生 API-key Bearer 渠道；store=false 仍临时加密保留；单实例 SQLite。收费双 hold/成本、父链/会话组合、原生 WS、真实提供商/SDK 与 ZDR 认证均未完成，完整 A03/M3 保持未验收 |
| 独立上层接入 | [L3 Responses](../../internal/responses/background.go) 只消费登记的 background/v1 与应用 v2/其他公开端口，不导入 Store/Gateway/协议/网络；[L2 原生 codec](../../internal/protocol/responses/background.go) 与 [HTTP v2](../../internal/compat/responseshttp/background.go) 不持有业务/SQL。实际 HTTP 集成、[五个强杀窗口](../../internal/generation/background_runtime_test.go) 与 [故障/尾部矩阵](../../internal/generation/background_event_failures_test.go) 穿过真实链路，不用 mock 证明持久性 |

最小运行（仅隔离测试数据，无真实密钥/账号/外部调用）：

```powershell
$env:GOARCH = 'amd64'
$env:GOFLAGS = '-buildvcs=false'
go test -count=1 ./cmd/gateway -run TestNativeBackgroundSSE -timeout 3m
go test -count=1 ./internal/generation -run TestBackgroundNative -timeout 3m
python tests/smoke/local.py --go go
```

L0 未改：一次 RPC 的 ProtocolComplete/Committed 不能证明任务终态。L1 private ReplayPending
只保留已知终态任务的事件恢复义务；L2 只有完整原生 terminal 事件才确认事件日志完成；
L3 发出已持久的不透明协议字节，页尾未追平不能报完整。未知/未持久 ID、缺序号、冲突、过期或不可重建
都不补造 DONE；内容 TTL 不因恢复或财务未知无限延长。读写/关闭及错误边界详见后台指南。

本次冻结记录只改 Markdown，复用同一 388 路径的已验收代码来源
`adc6d18a07911a0d5e324c16bff37a126b9df04ce6ee3883abbd2212d820cd8d`；
只做本地差异/链接/状态一致性，不重复 Go/main 行为测试。正常自动 push/PR/master CI 不修改、不绕过。
