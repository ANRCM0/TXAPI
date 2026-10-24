# TXAPI 开发方案

更新日期：2026-10-24。本文是当前唯一开发方案，记录当前能力、剩余工作、架构约束和验收门槛。批次过程、精确提交与运行证据保留在对应 PR/提交记录，本文只保留必要索引，不另建平行方案或进度文档。第三方来源与许可证保留在文末。

## 目标与范围

建设可扩展的 AI 网关，保持 **Go/Gin 模块化单体、单二进制、单实例 SQLite**，通过已登记的 L0–L3 契约扩展功能。以核心转发正确、协议兼容、故障可定位、容量有边界为主线，优先从固定参考源码原生化实现，适配既有 owner、契约和生命周期。

2026-10-21 用户将当前产品收敛为类似 new-api 的 **静态 API Key 中转**：配置兼容上游、模型、调用 Key、售价和额度，经现有唯一执行链完成 HTTP/后台/WS 转发、用量与结算。OAuth、SIWC 托管账号、自动刷新和账号池退出当前产品范围，正常交付不提供授权命令、刷新 worker 或托管推理入口；历史实现、数据库和已登记契约保留兼容，不删除历史凭据或账本。

当前验收不要求真实 Provider、真实账号或 OAuth 调用，全部使用受控 API 上游和合成 Key；不发起真实付费请求。成本政策由运营者显式声明并绑定版本、有效期、渠道/凭据、模型、操作和最坏尝试包络，运行时执行相应边界；此声明不是实际 Provider 账单认证。部署与安装验收环境为 **Linux Docker**，覆盖安装、升级、停写备份恢复、TLS/反代、持久卷、取消/关闭及强杀恢复。2026-10-23 用户已确认控制台 HTML 预览并启动完整前端交付；继续使用内嵌 ES Modules 与单二进制。支付/套餐、媒体、多实例和外部 Runtime 后置。

## 当前基线与能力边界

当前功能主线为 `master/e668d01a5ec19d10236b43726e9c83c228ce5585`（PR #98），独立 Verify `39993140738` 全六项任务成功。前端功能头 `597b52a9c8c3e3564c9a4a1fdcecee8cfc041cfb` 的 push Verify `39986551033`、PR Verify `39986556506` 各六项成功，Reference Comparison `39986556473` 成功；功能头、受测合并树与实际主线文件树一致。后台/WS 与成本政策原验收范围见 PR #97 和主线 Verify `39518759589`。证据只覆盖对应源码和受控环境，后续迭代独立验收。

[OpenAPI](openapi.json) 为 `0.14.0` / 117 操作，数据库 schema v22。静态 API 中转、显式成本政策、后台/WS 声明子集收费和 Linux Docker 安装/运行/恢复已完成受控验收。接口数、测试数和已完成子批数不换算为项目完成百分比；代码合并、CI 验收、版本/镜像发布与实际部署分别记账。

P0/P7/P8 的本产品验收范围随 PR #97 交付；P6-A/P6-B、P4-A1/P4-A2 分别见 PR #92/#93/#94/#95。历史托管账号实现仅保留兼容与回归，不作为当前能力或待开发路线；参考来源及许可仍按文末记录保留。

| 能力 | 已验收范围 | 当前限制 |
|---|---|---|
| 公共地基 | A1–A4、S6、FC-001；FC-002 原生后台子集 | 仅按明确批准范围冻结，后续候选入口不推断全量冻结 |
| 基础推理 | models、Chat JSON/SSE、embeddings、声明范围的文本/function tools；完整事件和原生用量 | 支持按操作、源/目标协议、传输和计费模式判断，不推断跨协议全量支持 |
| Responses HTTP | 独立模型、JSON/SSE、owner/Key 幂等、加密历史、Conversations/items、续链；声明范围的工具与不透明 reasoning | 默认关闭，legacy/disabled 保持；strict 已验收静态、自包含文本/顶层 function 子集；其余 strict 组合拒绝 |
| 原生后台 | 持久受理、原生提交/查询/取消、事件序号恢复、fencing、强杀恢复、任务用量元数据；P6-A 父历史输入与 P6-B 显式 store=true 的 completed 文本/function 结果原子历史发布 | 默认关闭；disabled 保持，strict 已验收自包含文本/function 预留与结算，legacy 拒绝；严格模式不接受父输入，旧任务不补收费 |
| 原生 WS | 双端原生连接、逐轮授权/准入、串行文本与声明工具；显式持久续链、HTTP/WS 跨模式、prewarm/reasoning、固定 SDK/Codex 与受控镜像回归 | 默认关闭；disabled 保持，strict 已验收独立文本/function 轮次结算；strict 不允许父链/prewarm/持久历史，legacy 与任意多路复用拒绝 |
| 执行与路由 | 唯一执行事实、资源代次/排空、资格/优先级/权重/亲和/健康、诊断与 shadow；阶段超时、有限响应头和重放审计 | shadow 默认关闭且不控制路由；无准入等待队列，不代表外部 Runtime |
| 历史托管账号 | R04a/P1/P1-A/P2/P3 的既有受控实现与加密状态保留兼容 | 已退出当前产品范围；正常二进制不接 OAuth、刷新或托管推理，不作为待完成验收 |
| 用量与计费 | Responses/Chat/embeddings 原生 Token 明细、不可变售价、定点价格、双 hold、幂等结算、对账/未知保护；WS 每轮明细和后台任务用量 | 缺失与部分用量保持未知；通用用量历史仅 disabled；静态政策、后台/WS 声明子集收费及恢复已验收，S5-03 在当前产品范围内完成 |
| 管理与交付 | 任务控制、路由诊断、配置发布、个人财务/账本读取、占用指标；固定客户端和实际镜像回归 | Linux Docker 安装、升级、持久化、备份恢复和负载/故障已验收；不推断任意环境或生产网络 SLO |

### 当前交付与后续工作

| 范围 | 已完成 | 后续边界 |
|---|---|---|
| P0 / Linux Docker | Chat/Responses 1/4/8/16 并发八档、取消/过载及全槽位恢复；实际镜像 TLS HTTP/1.1 与 H2 各十五分钟持续运行；安装、升级、备份恢复和故障回归 | 16 为本次测试上限；更大规模、长周期和生产网络 SLO 按新配置另行验收 |
| P8 / 显式成本政策 | 静态 API 的版本、有效期、实际目标/凭据、输入/输出及失败/附加费用包络；统一 prepared-request 校验 | 运营者声明是输入假设，不认证实际最高费率、tokenizer 或外部账单 |
| P7 / 后台与 WS 结算 | 逐生成持久预留、attempt 成本、已知/零用量结算、未知双 hold、幂等和强杀恢复 | strict 仅声明的自包含文本/function；父链、prewarm、持久历史及未声明维度不继承收费许可 |
| 发布与部署 | 功能主线及 Linux Docker 验收完成，安装/升级/恢复入口见下文 | 版本标签、Release、镜像分发与具体部署按独立交付记录判断，不由 CI 绿灯推断已发布 |

新增 Provider/协议组合、WS 池/多路复用、准入等待队列及其他后置能力按实际需求和各自前置条件立项；OAuth、托管账号、自动刷新与账号池不在当前产品路线内。

## F1–F3：已确认控制台前端交付

2026-10-23 用户确认 `web/preview.html` 视觉基准并要求制定方案和目标、完成前端。真实界面与现有 API 消费者已交付，PR #98 的功能头、受测合并树及主线均已通过对应完整 CI；具体提交与运行索引见当前基线。静态预览不作为生产入口或验收结果。

- **层级/目录：**L2 `web/assets`、内嵌 `web/embed.go`，适用 `tests/browser`、`tests/smoke/compatibility.py`、Makefile/CI；现有 L1/L3 授权、配置、用量和财务 owner 不迁移。
- **公开依赖：**复用已登记 HTTP/OpenAPI、SessionBearer 和 APIKeyBearer 入口；客户端只访问同源 API，不存储登录令牌/调用 Key，不新增框架构建运行依赖，不从前端复制路由/账本决策。
- **地基影响：**先在现有端口接入完整消费者。缺少数据时呈现未知/未启用及恢复操作；新增后端契约须另记录具体原因与影响，不为图表新建统计或财务权威。
- **接口/迁移/回退：**原用户/管理员能力与路由保留，新增 UI 路由只消费现有 API；不改变 schema、计费许可和后台/WS 生命周期。角色撤销后重新核对身份，旧请求/对话框不得回写新视图；切页/登出取消生成与私有读取，密钥只展示一次且不进入 URL、导出、截图或日志。回退为旧嵌入资源，后台数据和账本保持原语义。
- **测试/验收：**保留并适配原导航/账本/真实二进制浏览器回归；新增个人账本、原生用量、管理员 Key 与报价消费者覆盖。验证登录/登出/401/403/角色变化、加载/空/错误/未启用、过时响应、分页/重试、危险操作确认与发布状态；核对 320/390/1440px、键盘/焦点/对话框与减少动画。所有指标来自真实 API，有限日志样本标明范围，不伪造增长率/渠道健康/最终费用；未知数量/预算与明确零值分开。固定受控上游和临时 SQLite，经真实主程序完成用户与管理员闭环，再验证 Linux Docker 内嵌交付及最终头 CI。
- **其他模块与来源：**主视觉基准为本项目已确认预览，SHA-256 `975265821cc4e9e42c7f42854e9fda78a01feaebdd64c9e857ebef655d158833`，浅色工作台、石墨文字、绿色状态和紧凑表格；独立原生重组现有组件和真实数据。外部机制主参考已登记 new-api `25372e30b1f94255b1f818aeaaf006f042572580` 的 `web/src/components/layout/components/app-sidebar.tsx`（按当前视图组织导航）与 `web/src/features/dashboard/components/models/log-stat-cards.tsx`（筛选、加载、统计分离）。仅对照流程与边界，不复制/翻译其 AGPL 源码、图标或样式；不引入其 React/状态存储/财务模型，保留既有来源声明。

| 子批 | 页面与交付内容 | 当前状态与退出条件 |
|---|---|---|
| F1：界面地基 | 中文登录、用户/管理员分组导航、顶栏/全局页面搜索、响应式侧栏、表格/表单/对话框、状态与提示 | 实现及本地回归完成；保留认证/导航竞态检查，覆盖 320/390/1440px、焦点与键盘导航，最终头 CI 见 PR |
| F2：真实消费者 | 总览（保留日志汇总与有限样本趋势）、我的 Key、模型搜索、调用测试、请求日志、原生用量记录、个人资金/预留、后台任务、路由诊断；管理员用户/全站 Key/渠道凭据/售价倍率与发布/严格账本核对/报价诊断 | 实现及本地回归完成；所有操作调用既有 API，校验普通用户隔离、定点精度、未知/零值、一次性 Key、发布状态及只读报价；真实主程序闭环覆盖渠道/凭据/Key/调用/日志/用量详情/个人账本 |
| F3：集中验证与交付 | API 单元行为、真实二进制/临时库浏览器、截图审阅、移动/键盘及 Linux Docker 内嵌回归；完整精确头 CI、可审查提交和 PR 记录 | `make web-check` / `make browser-check` 与独立 `console-browser` CI 已接入；浏览器运行时 Playwright 1.62.1 隔离安装，不进入生产依赖。最终验收以同提交 Verify 六项最终结果及控制台截图制品为准，合并与发布分别记账 |

## I1-I4：持续质量与安全迭代

2026-10-24 用户授权自行扫描前端、后端、安全和漏洞，制定并执行迭代方案。基线为 PR #98 合并主线 `e668d01a5ec19d10236b43726e9c83c228ce5585`，独立 Verify `39993140738` 已成功；旧的单次长流提前结束仍保留诊断，不因后续复测成功宣称根因已修复。

| 顺序 | 范围与首批发现 | 执行与退出条件 |
|---|---|---|
| I1 前端 | API 路径字符串检查遗漏浏览器对反斜杠、TAB/换行的 URL 规范化 | 在附加凭据及 fetch 前拒绝；正常同源路径和编码 ID 保持；沿用 CSP；单元及浏览器回归。此为客户端边界加固，不宣称当前 CSP 下存在已利用的凭据外传 |
| I2 后端入口 | Gin 默认信任代理头，攻击者可轮换 X-Forwarded-For/X-Real-IP 改变 IP 限流分桶 | 明确禁用代理信任，以 TCP 对端分桶；真实 engine 回归证明伪造头不能绕过、不同对端隔离、探活保持可用 |
| I3 漏洞与依赖 | 扫描当前模块和实际调用链，区分依赖存在、被引入与可达符号 | 使用 govulncheck 留存工具/数据库时间及 JSON；按结果修复并复扫；工具或数据库不可达必须记为未完成，不以依赖升级次数作为安全证明 |
| I4 后续运行质量 | 权限撤销、私有数据清理、正文/流资源边界、诊断输入及部署配置 | 按可复现问题切片，优先修复实际权限/泄漏/资源缺陷，再做有基线的性能优化；热路径变化继续适用 Linux Docker 验收 |

**CR-I02 / 入口代理语义：**L2 server 的 IP 兜底使用 TCP 对端，不再读取未授权代理头；没有 L0、L1 公开契约、数据库或收费变化。反代部署的多个客户端会共享代理 IP 的保守兜底预算，运营者应在反代实施真实来源 IP 限流，并按代理流量配置网关 IP RPM；Token/owner 授权及限流继续独立生效。以后支持可信代理须显式配置 CIDR、头覆盖与直接访问边界，不能恢复默认全信任。

I3 首次 source/symbol 扫描使用 govulncheck v1.9.0、漏洞库更新时间 2026-10-21T00:27:29Z、本地 Go 1.26.3。原始 995 条 finding 含模块/包/调用链重复，不能当作 995 个漏洞；去重后有 28 个可达 advisory/module 组合，涉及标准库、x/net、x/text。构建基线升为 Go 1.26.10，x/net v0.60.0 按模块依赖同时升级 x/crypto v0.57.0、x/text v0.42.0、x/sys v0.48.0。Docker builder 使用已核验的 golang:1.26.10-alpine 多平台索引摘要；CI 跟随 go.mod，同批加入固定 govulncheck v1.9.0 的 Linux/Windows 可达漏洞门槛和报告制品。工具链最低版本提高是本批明确兼容变化；不修改数据库、公开契约或业务收费语义。复扫及全量回归以本批最终记录为准；操作系统镜像包、供应链及不可达模块告警另行扫描，不继承 Go source 扫描的结论。

本批本地结果：Go 1.26.10 / GOARCH=amd64 的 build、vet、全量 Go 测试通过；18 项 Node 单元测试、导航/工作台 Chromium 回归及真实二进制/临时 SQLite/API UI 闭环通过。升级后 source/symbol 复扫为零可达、零被导入包告警；保留仅模块级 GO-2026-5932（x/crypto，当前调用链不涉及）的告警，不宣称所有依赖无漏洞。Linux Docker、race 与双平台扫描的最终 CI 另按本批精确提交判断。

首批机制来源为当前 `web/assets/api.mjs` 和固定依赖 Gin `v1.10.0` 的 `gin.go`（SetTrustedProxies）、`context.go`（ClientIP）。已登记参考项目没有本项目无框架客户端和入口代理信任的直接适配实现，因此在现有 owner 原生修正；不引入第三方功能或复制外部源码。计划和实现同批交付，验证、CI、合并和发布按精确提交分别记账；不将一次扫描称为全项目无漏洞认证。

## 架构与契约

| 层级 | 责任与主要目录 | 依赖规则 |
|---|---|---|
| L0 | `internal/core` 及登记的机械执行 | 一次发送、读写、取消、背压、提交事实；不放用户/账号/路由/会话/账本策略 |
| L1 | access、gateway、billing、generation、scheduler、store、secrets、upstreamidentity | 持有唯一授权/准入、attempt、迁移、预留/结算、凭据和资源生命周期 |
| L2 | protocol、compat、server、transport、web | 协议编解码、错误/事件、HTTP/WS、展示；通过登记入口调用能力 |
| L3 | responses、responsesws、管理/查询用例 | 只消费登记的 L1/L2 公开端口，不依赖 L0、地基私有包或其他功能实现 |
| 组装根 | `cmd/gateway` | 构造、注入、注册、启动和关闭；不承载功能规则 |

精确层级和 owner 以 [layers.json](../internal/architecture/layers.json) 为准，公开类型/字段/标签/签名以 [public-v1.json](../internal/architecture/public-v1.json) 和[契约源码](../internal/foundation/contracts)为准。新增包先登记，不新增私有依赖或临时豁免；跨层测试不能掩盖生产依赖违规。

- FC-001 已冻结 admission、configuration、control、execution、identity、json、ledger、lifecycle、observability、responses、state、text 的 v1，以及 L2 `foundation/httpoutput/v1`、`compat/responses/v1`。L3 接收输出端口，不持有 HTTP writer。
- FC-002 追加冻结 `background/v1` 与 `responses/v2`，仅覆盖已验收的 disabled、自包含原生后台子集，不扩大收费许可。
- execution/v2/v3/v4、responsehistory/v1、background/v2、responsesws、routes、taskcontrol、ownerledger、quotation、usagehistory、upstreamauth 等后续入口只按既有批准范围消费；登记、导出、版本常量和 CI 均不等于全量冻结。
- 地基公开接口新增或签名、语义、权限、生命周期变化，先记录具体变更请求，列明原因、影响、消费者、迁移、兼容/回退与验证。2026-10-20 用户在当前完成本方案任务中已授权后续同类开发契约调整按最优方案直接实施、集中验证修复，不再逐次请求批准；其他任务仍按其明确授权。真实账号、预算与数据、收费许可和范围后置要求继续按各自约束判断。
- 新冻结必须具备文档/版本、契约测试、最小运行样例、扩展说明、基础约定、限制清单及独立上层消费者。L0 变化额外说明现有扩展点为何不足和上层迁移；本路线优先保持 L0 稳定。

## 运行不变量

1. **当前授权。** 每次生成，包括后台执行及每个 WS 轮次，重新核对可信身份、当前 Key/模型权限与准入；用户提交的 owner、余额或绑定值不是认证。会话登录与调用 Key 分离，跨 owner/Key 读取不可枚举。
2. **唯一执行权威。** 功能不自行发送 HTTP/WS，不另建迁移、刷新或账本流程。协议终态、生成终态、财务终态、发送事实和下游提交分别记录；提交后不改目标重试，未知执行不补零、不盲目重发。
3. **协议保真。** 同协议保持语义，跨协议仅开放已声明共同子集；Chat、embeddings、Responses 使用独立模型。无法表示的字段在发送前拒绝，不提高输出限制、不静默改变服务等级、不伪造完成。工具由客户端执行，不在网关建立任意工具执行器。
4. **独立寿命。** 连接、轮次、生成、会话、后台任务和 Provider 状态分别管理。后台断开只停止观察；WS 前台断开按当前轮次的取消/未知事实收尾。网络重连不能证明旧生成可重放。
5. **有界资源。** 正文、消息、事件、队列、连接、保留期、退休代次均设上限。配置先准备后原子发布，失败保留旧代次；退休资源排空，worker/连接取消并 join 后才能关闭共享 gateway/store。
6. **独立资金事实。** Token 允许未知、零和部分完整，不默认补零或推导总量。售价、估算 Provider 成本、实际账单、当前 hold、历史预留与最终收费分开；观察/日志不成为财务或恢复权威。

### 路由与用量

先过滤授权、模型、能力、绑定和凭据资格，再在有效优先级池选择。故障回退不能突破账号范围，资格变化在发送前复核。`route_diagnostics` 与 `shadow_route_scoring` 默认关闭；shadow 仅为有界前台 HTTP 对比，不改变真实优先级、权重、亲和、重试，样本不代表 Provider 总负载。

`usage_history` 默认关闭且仅允许 disabled，覆盖前台 Chat/embeddings/Responses 与原生 WS 每轮；不覆盖原生后台或 durable claimed 执行。查询用 transport 区分 HTTP/WS，用 protocol_status 区分协议快照与宿主终态；后台用量保存在任务元数据中，不重复写入通用用量历史。实例父记录上限 1..65536、owner 上限不大于实例、保留期 1..2592000 秒；零配置采用有限默认值，每调用预留有限 retry 子槽。预留/写入失败沿既有准入拒绝，不退化成可丢日志；记录数限制不是磁盘/WAL 字节保证。

### 持久状态与历史账号兼容

R04a/P1/P2/P3 的既有账号代码和加密状态只保留历史兼容与受控回归。当前主二进制不再注册 `-upstream-account`，`upstream_accounts.inference.enabled` / `refresh.enabled` 为 true 时启动明确拒绝，不启动 OAuth、自动刷新或托管推理；旧账号不会转成静态 Key。现有数据库迁移和账号记录不回滚、不删除，旧 CLI 的授权/刷新语义不成为当前 API 中转入口。

历史绑定当前 owner、创建 Key 和模型权限，内容加密、CAS/TTL/容量受限。`previous_response_id` 与 conversation 明确上下文来源，原生父 ID 和本地全上下文不能重复叠加；只有完整可重建、重新准入和计价的上下文才允许显式重放。`store=false` 不变永久历史，原生后台仍按声明临时保存恢复内容。后台受理先持久化任务和适用准入/预留事实再确认；内部任务状态、公开协议状态和财务终态分别映射。事件序号先持久提交再公开，按 `sequence_number` / `starting_after` 恢复；事件过期、丢失或不可重建时显式报错。

迁移只追加版本，不修改已发布步骤。schema v22 升级前停写并一致备份；旧 schema21 二进制拒绝打开 v22，回退需恢复匹配的停写备份，不能删账号或改写账本/迁移修复。v22 追加 reservation 政策摘要、后台财务绑定和控制 RPC 计数；旧任务保持 disabled，旧账本不补造政策或收费。凭据/内容密钥、备份/WAL、内存和公网 TLS 的边界独立维护，管理掩码不构成加密。

## 严格计费边界

strict 已验收范围包括已登记的官方 OpenAI 文本 Chat/embeddings 档案、显式运营者政策绑定的静态兼容 Chat/embeddings，以及 HTTP/后台/WS 的自包含 Responses 文本/顶层 function 子集；Anthropic/Gemini、未知模型/计价维度不继承许可。legacy 不提供 strict 持久预算保证且继续拒绝后台/WS；disabled 只关闭计费，保留身份和准入。

`StartPreparedAttempt` 校验最终真实 URL、正文、输出限制和鉴权头，是唯一可执行成本准入入口。无最终正文的 `StartCostedAttempt` 不能开启新尝试。输入、费率或凭据证明缺失分别返回 `provider_input_unproven`、`provider_tariff_unproven`、`provider_credential_unproven`，不发送、不处罚健康、不换凭据绕过。

| 现有输入档案 | 实现约束，非远端最新保证 |
|---|---|
| `openai_embeddings_v1` | 官方根 URL、openai/embeddings；三个精确模型 `text-embedding-3-small`、`text-embedding-3-large`、`text-embedding-ada-002`；整请求输入包络至少 300000 Token，文本数组最多 2048 条，不接受 token ID 数组/混合输入 |
| `openai_chat_v1` | 官方根 URL、openai/chat、`gpt-4o-mini-2024-07-18`；输入包络至少 128000，`max_completion_tokens` 至多 16384；正文 `service_tier: "default"`，严格文本、`n=1`；工具/媒体及未登记缓存、推理、等级扩展拒绝 |
| `operator_chat_v1` / `operator_embeddings_v1` | 绑定静态兼容 API，沿用各自 strict 文本闭合字段；整正文大小有界，政策声明完整输入包络、显式输出上限及费用；不自动支持 Chat 工具或新计量维度 |
| `operator_responses_v1` | 显式 `max_output_tokens`、自包含文本或已配对顶层 function，最多 64 个定义、128 对调用/结果；HTTP/后台/WS 实际 wire 共用校验。strict 不继承父 ID、conversation、prewarm、reasoning、custom/namespace/search/compaction 或媒体许可 |

档案数字来自 2026-10-17 登记的实现约束，详见[输入档案](../internal/billing/input_profile.go)和[费用档案](../internal/billing/tariff_profile.go)。原核对来源为 OpenAI Create embeddings、GPT-4o Mini、Chat Completions、Priority processing、Pricing Standard 文档；修改适用范围时重新核对，不把旧快照或模拟上游当真实价格认证。

先证明不可变售价/目标倍率覆盖，再核对每次尝试和整请求累计的已配置上游成本包络；缓存折扣不降低最坏预留，未知尝试继续占用，定点溢出或覆盖不足在发送前拒绝。异常实际用量保留 pending 与审计。S5-03 已按当前产品范围完成显式运营者成本政策、实际发送校验、全部尝试包络及持久预留/结算/恢复的受控验收（PR #97）。实际最高费率、税费和外部账单认证不再作为开发完成门槛，也不能由配置或模拟测试声称已获认证。

### 严格计费配置

以 [gateway.example.yaml](../configs/gateway.example.yaml) 为准。费率档案 `openai_standard_usd_2026_10_17` 使用 USD、显式 `credential_bindings` 和完整 Key 的小写 SHA-256，发送前核对实际 Bearer Header；更换 Key/ID 须重新核实并加载。内置价格下限不能证明账号最高适用价格。Provider 金额精度为 `1e-9`，用户售价采用独立账户单位，不自动换汇或相互比较；真实密钥、摘要清单和个人配置不提交。

兼容 API 使用 `operator_declared_tokens_v1`，完整绑定 channel/baseURL/protocol/actual-model/operation、credential ID/Key 摘要、三位大写币种、正输入/适用输出费率、`policy_version`、未来 Unix 秒 `policy_expires_at`、`max_request_bytes` 与显式 `additional_cost_per_attempt`（可为零）。正文上限为 1..1048576 字节且不超过声明输入包络；非 loopback 使用 HTTPS。未知维度、过期政策、实际 URL/body/model/Header 漂移和全部尝试预算不足在发送前拒绝。政策在预留时固定，排队任务不得静默采用新版本。额外费用声明覆盖一次生成的失败和最多 32 次持久控制 RPC；轮询/取消不形成第二个生成或重复扣费。这些是运营者输入假设，不是远端 tokenizer 或账单认证。

## 参考对照与规划依据

以下是固定版本、具体源码路径的对照，区分机制存在、TXAPI 已实现和本项目验收；不把功能数量、源码规模或参考项目自己的测试换算为产品成熟度。除既有 new-api 共同 Chat JSON/SSE 对照外，本轮没有运行参考程序或做性能排名。

| 对照方向 | 固定参考源码与可用机制 | TXAPI 当前落点与差距 | 对规划的影响 |
|---|---|---|---|
| 转发与转换 | new-api `relay/helper/stream_scanner.go`、`relaykit/relayconvert/text_converter_registry.go` 的流收尾、转换路线/质量声明；codex-proxy-rs 的 raw frame/观察分离 | 完整 SSE、跨 Provider 现有转换、阶段超时、背压和有限头已交付；[协议目录](../internal/protocol)与[阶段实现](../internal/gateway/http_stage_timeouts.go)继续持有规则 | 复用现有链路，只修具体现象；不再把完整事件、阶段超时列为待建框架。质量标签不能替代字段保真或成本证明 |
| 续链与连接范围 | codex-proxy-rs `backend/crates/gateway-core/src/engine/continuation.rs::NativeContinuationPin` 区分 Persisted/ConnectionLocal 并绑定客户端/Provider/账号；pool `lease.rs` 分离 reservation、连接与 lease | [StoredRounds](../internal/generation/response_history_stored.go) 已有 owner/Key、目标约束与加密历史；P6-A 由 [background owner](../internal/generation/background_history.go) 展开父历史，再交原 BackgroundParser 验证完整正文 | P6 拆为输入继承和结果发布；采用 scope/绑定规则，不把参考 affinity pin 作为本地持久历史，也不假设参考已有 TXAPI 式后台事务实现 |
| 客户端默认路径 | codex-proxy-rs `backend/crates/gateway-api/src/openai/models.rs::models_response` 按 client_version 返回目录；sub2api `openai_codex_models_service.go` 有有界缓存/权限过滤；new-api 有独立 Responses compact 路径 | [ListModels](../internal/compat/generationhttp/handler.go) 仅标准列表；[固定 Codex 消费者](../tests/smoke/codex_conformance.py) 部分使用 model_catalog_json；server 未注册 standalone compact；固定 Codex 0.163.0 V2 实际使用普通 Responses input 触发 | P4-A 按实际客户端请求补长会话场景；V2 范围独立登记而复用唯一 Responses owner，传统独立路径另有消费者才实施，不从普通 Responses 推断所有压缩模式 |
| 历史账号机制 | sub2api 的刷新/资格与 codex-proxy-rs 的 revision/执行绑定曾用于账号实现对照 | 现存账号代码、密文和契约保留历史兼容，正常产品入口不启用 | 不列入当前路线；来源声明、许可证及隔离/迁移回归保留 |
| 工具、错误与用量 | CRS `openaiResponsesRelayService.js` 的事件/用量提取和 scheduler 的会话失效可提供回归场景；其选定路径缺 usage 时补零、直接 res.write 的做法不能证明严格用量与背压 | 声明工具、原生部分/未知 usage、同步背压和 WS/后台元数据已实现 | 保留已验语义；P4 补默认客户端字段及失败场景，P7 不复制补零或按流事件直接扣费 |
| 收费与恢复 | new-api `service/billing_session.go::Settle/Refund` 的分步提交/异步退款；sub2api 的多维计价场景 | PR #97 已验收显式成本政策、持久双 hold、未知占用及后台/WS 强杀恢复 | 采用场景对照，保留本项目整数事务账本；声明不构成实际 Provider 账单认证 |
| 承载与运维 | sub2api WS pool 与 codex-proxy-rs lease 的有界租借/排空机制 | PR #97 已完成 Linux Docker 分档容量、TLS H1/H2 持续运行、安装升级和故障恢复 | 受控上限不作为最大生产容量；池化、排队和新运维平台须有数据及真实消费者 |

本轮不从任一参考实现推导“完整后台父链”“所有默认 Codex 模型”“真实账号可用”或“Provider 成本有界”。源码存在、功能接线、受控验收和真实环境证据分别判断；保留本项目已交付的权限、存储、未知执行和账本边界。

## 后端开发路线

P0–P8 按可独立验收的切片推进，已验收子集保留，不重复立项。实现前记录具体契约变化，真实消费者、权限、协议、取消/释放、成本事实和交付回归同批处理；源码完成、本地检查、精确头 CI、合并和运行验收分别记录。

### PR #97 已交付范围与复验条件

PR #97 已按静态 API Key 中转和 Linux Docker 安装部署范围完成下列验收。下方保留具体契约与迁移约束，作为后续变更的复验依据；批次实施过程见 PR，不作为待办重复列出。

| 批次 | 已交付与消费者 | 持续保留的验收条件 |
|---|---|---|
| P0 / Linux Docker | 复用真实 main 的容量 harness、实际镜像及 TLS/H2/反代/WS 消费者 | Chat/Responses 分档容量与过载/取消恢复；同一 Linux Docker 镜像持续运行和资源预算、安装升级、持久卷与停写备份恢复、正常关闭及强杀恢复；保留全部失败样本和 JSON 报告 |
| P8 / 显式成本政策 | 现有 billing owner 的静态渠道/模型/凭据/操作政策，供 HTTP/WS/后台同一成本入口消费 | 完整且有效的声明才能发送；精确 wire URL/body/Header、输入/输出上界和附加费用、不可变版本、溢出/未知/过期拒绝；不把声明当实际 Provider 账单认证 |
| P7 / 逐生成结算 | 现有 WS 每轮与原生后台任务 owner 复用唯一 ledger/quotation/execution | 后台受理时持久预留、WS 每轮独立预留；已知用量幂等结算、取消/未知继续 hold、轮询/事件/重连不重复扣费，强杀恢复不重发未知生成；兼容 disabled，legacy 长生命周期收费继续拒绝 |

### API-ONLY-001 已交付契约：静态中转交付入口

- **层级/目录：**组装根 `cmd/gateway/main.go`、启动配置 `internal/config`、示例配置和 OpenAPI；不修改 L0 或既有存储权威。
- **公开依赖：**沿用既有静态配置、execution/ledger/identity 与 HTTP/WS/后台端口；不新增 OAuth 或账号端口。
- **地基影响：**正常产品不再注册上游账号 CLI、不注入托管推理/管理员托管仓库、不启动刷新 worker；显式开启历史配置立即拒绝。公共冻结契约及加密历史仍保留兼容。
- **接口/迁移/回退：**旧 `-upstream-account` 为未知参数；旧 inference/refresh enabled=true 报明确错误；管理托管兼容端点保持不可用，不生成或消费 OAuth Token。不删除存量账号、凭据或迁移；回退只按实际 schema 与停写备份规则。
- **验证：**真实 main 子进程验证帮助/旧命令拒绝先于配置/数据库/网络，启动配置覆盖完整旧前置条件也不能重新开启；静态渠道、撤销、额度、既有迁移与恢复回归保持。
- **协作影响：**真实 Provider/账号验收和 OAuth 路线移出当前完成门槛；受控 API 协议与资金验证、Linux Docker 安装部署保留。

### P0-C 已交付契约：Linux Docker 承载与持续运行

- **层级/目录：**既有 `tests/smoke`、`tests/upstream/nativews` 测试辅助和 Docker/部署文件；CI/Makefile 统一组装，不为测量新增生产转发路径。
- **公开依赖：**复用当前实际 main、镜像、capacity/stream/process_resources、delivery/native_ws_delivery；主参考 new-api 固定 `25372e30b1f94255b1f818aeaaf006f042572580` 的流/交付机制，具体来源随实现记录。
- **地基影响：**先实现测量与真实消费者；无实测瓶颈不改 L0、不删除鉴权/准入/日志/账本来跑分。
- **接口/迁移/回退：**受控 Chat/Responses 按 1/4/8/16 档采样，预算/停止边界在运行前固定；Linux Docker 依次覆盖 TLS HTTP/1.1 与 H2 持续流及反代/WS。资源报告与持久数据分离，不新增数据库迁移。
- **验证：**全部计划/开始/完成/失败、首有效输出/事件延迟、CPU/RSS/FD、取消/过载前后全槽位恢复和最终排空；持续阶段与安装升级、停写备份恢复、强杀恢复同环境验收。受控最大通过档位不外推任意机器的生产容量。
- **协作影响：**单独有界 Linux job 运行较长验收，保留现有 check/delivery 门槛；最终集成变化影响热路径时以最终功能头重复适用验收。

### P8-A 已交付契约：运营者声明的静态 Responses 成本政策

- **层级/目录：**L1 `billing` 的输入/费率/发送证明，`config` 的 billing 配置及 `cmd/gateway` 映射；P7 直接消费同一 Engine。
- **公开依赖：**沿用 ProviderCostContract、不可变 snapshot、Reserve/StartPreparedAttempt/EndAttempt/Finish 与双 hold；新增候选政策字段先登记，不增加第二个资金 owner。
- **地基影响：**新增 `operator_declared_tokens_v1` 费率政策与 `operator_responses_v1` 输入档案；精确静态渠道/baseURL/model/credential、Responses 文本/function、显式输出上限。原官方档案不被兼容代理自动继承。
- **接口/迁移/回退：**新增 `policy_version`、`policy_expires_at`、`max_request_bytes`、`additional_cost_per_attempt`；版本和有效期绑定 snapshot，整正文与输出有界，附加包络包含一次生成约定的失败/控制 RPC 费用。schema v22 追加 reservation policy version，旧行不补造声明；旧历史结算仍使用其原 snapshot。
- **验证：**实际发送 URL/path/body/model/输出/鉴权/WS beta、过期/漂移/未绑定/未知字段、全请求/全 attempt 累计、定点溢出和异常 usage；未知或证据缺失发送前拒绝，发送后不确定仍保留 hold。
- **协作影响：**声明是运行政策及运营者输入，不是外部真实费率或 tokenizer/账单认证；不要求真实账号或付费验收。P7 只开放该明确子集，不自动扩展媒体、搜索、reasoning 等额外维度。

### P7 已交付契约：后台与 WS strict 逐生成结算

- **层级/目录：**L1 `gateway`/`generation`/`store`/`billing`，后台 owner 私有 finance seam 与候选 WS/执行端口；组装根注入，L2 只提取原生用量。
- **公开依赖：**复用原账本的预留、prepared attempt、终态、历史报价和恢复；冻结 `background/v1` 的公开调用签名保持，BackgroundOptions 注入同 owner Finance，不能从 L3 访问私有账本。
- **地基影响：**新增 strict 静态 Responses 文本/function 子集；WS 每轮重新授权/准入/预留；后台受理前保存预留关联，queued 不占网络 attempt 槽。disabled 保持；legacy 后台/WS 收费继续拒绝。
- **接口/迁移/回退：**schema v22 统一追加后台 BillingID/BillingMode、控制 RPC 计数与 reservation policy version，旧 disabled 任务不补收费。dispatch 重查当前权限、原持久预留、原售价/政策版本及有效期；最多 32 次原生控制 RPC，费用包络由 P8 覆盖。
- **验证：**受理/hold/task 写入与 dispatch/ID/用量/结算的强杀窗口；queued 预留失效后不能发送；完整/零/缺失/部分用量、重复/迟到终态、取消竞争、跨 Key 隔离、重连/轮询/事件不重收费，unknown 不重发且不补零。
- **协作影响：**P7/P8 共用端口及资金权威；共享迁移、公开快照、配置/OpenAPI、运行组装和 CI 统一协调。回退恢复匹配版本停写备份，不逆改历史账本。

### P6-A 已交付契约：后台输入历史快照

PR #92 已交付；下列约束用于后续兼容与回归，实施过程见该 PR。

- **层级/目录：**L1 `generation`/`generation/backgroundstate`、`store`、`gateway`；L2 `protocol/responses`；组装 `cmd/gateway/responses.go`。L0 和现有历史存储权威保持。
- **公开依赖：**冻结 `background/v1.Service.Submit` 签名不变，Body 语义增加同 owner/Key/模型的已保存父历史；候选 `responsehistory/v1` 增加纯 `BackgroundCompiler.CompileBackground`，复用既有 Repository.Read、state 历史版本。`generation.BackgroundOptions` 注入 History/Compiler，实际消费者为现有后台 HTTP/SDK 路径。
- **地基影响：**受理前读取 ready、非 busy/active 且可完整重建的父历史；受理事务再次验证当前授权与父版本。原请求幂等身份与展开后的执行快照分离，幂等命中先于父重读；worker 只消费已受理密文中的完整输入和目标约束，发送前重新授权，不换渠道/凭据绕过固定目标。
- **契约/迁移/回退：**仅 disabled、静态 Key、previous_response_id；conversation 继续拒绝。父删除/过期发生在受理前则失败，受理后不影响独立后台快照；不新增历史子记录。复用现有加密 input 包络与容量，无 SQLite schema 迁移；可选父/目标字段兼容旧自包含任务，原自包含请求摘要不变。旧程序不理解父链目标约束，回退前须停止受理并排空父链任务，或恢复匹配的停写备份，不能因 schema 相同直接回退执行 queued 父链任务。
- **测试/验收：**HTTP/显式持久 WS 父与工具上下文；跨 owner/Key/模型、撤销、非 ready/unknown/busy、TTL、父 CAS；父删除后的幂等重放与冲突；展开正文/项目/私有包络上限；固定目标失效零替代发送；受理后删除父及重启、dispatch 强杀窗口。集中执行契约、受影响行为及现有检查，CI/合并/真实环境另记。
- **其他模块影响：**原 BackgroundParser/远端 Codec 继续只收完整自包含正文，不能直接透传本地父 ID；store/stream 按客户端请求保留。此批不发布后台结果为历史，不扩展 P6-B、managed、WS 池或收费；未知发送仍保持 uncertain，不自动重发。

### P6-B 已交付契约：后台终态历史发布

PR #93 已交付；下列约束用于后续兼容与回归，实施过程见该 PR。

- **层级/目录：**L1 generation/backgroundstate、store，L2 protocol/responses，L3 responses 及 cmd/gateway 组装；沿用唯一后台任务与加密历史 owner，L0 不变。
- **公开依赖：**候选 responsehistory/v1 增加纯 BackgroundHistoryProjector 的输入准备/完成投影和 Repository.Limits；冻结 background/v1 签名不变。私有 backgroundstate.Record 携带受理事务使用的历史容量预留，Update 携带同事务的历史 CAS；不另建发布 worker 或数据库权威。
- **地基影响：**新受理、显式 store=true 且在文本/function 可重建子集内的请求，同事务预留完整历史槽与字节；completed 结果、事件、后台终态和历史 ready 同事务提交。失败、未知、不可重建、撤销或未发出的任务释放预留，不补零、不重发未知生成。省略/false、旧任务及已有幂等命中不补建历史。
- **契约/迁移/回退：**历史保留期从首次发布计算，独立于后台临时内容；同一公开 ID 在临时内容有效时归后台读取，过期后可读取仍有效历史。input_items 读取已发布历史，cancel/events 不伪装成前台操作；显式删除原子删除两份内容，自动临时 TTL 清理保留已发布历史，重放不复活删除或重设 TTL。新字段在加密 input/snapshot 中可选，schema 不变；旧程序不理解预留/发布标记，回退前须停止受理并排空本批任务，或恢复匹配停写备份。
- **测试/验收：**真实 HTTP/持久 WS/固定 SDK 消费，完成前拒绝续链，跨 owner/Key/模型及撤销，容量与 CAS，终态提交前后强杀、重启/事件恢复仅发布一次，后台 TTL 与历史 TTL 分离，显式删除不复活，旧/省略/false/失败/unknown/不支持输出不生成 ready 历史。
- **其他模块影响与来源：**沿用 P6-A 同目标约束和当前授权，仅 disabled、静态 Key、文本/function；现有其他后台工具仍可执行但本批不发布为历史。主参考固定 codex-proxy-rs 60570883007febebe732597a9881397098d724f5 的 NativeContinuationScope/NativeContinuationPin；参考无本项目 SQLite 原子发布机制，此部分在既有 store 内原生实现。托管账号、conversation、收费、池化与其他结果类型继续单列。

### P4-A1 已交付契约：标准 Codex HTTP 与缓存搜索声明

PR #94 已交付。固定官方 Codex 0.163.0 的必要自定义 Provider 配置探测只请求 POST /v1/responses，不请求模型目录；默认包含 external_web_access=false 的 web_search。此批验证该实际路径，不从目录缺失推导默认客户端不可用。

- **层级/目录：**L2 protocol/responses，L1 gateway 费用范围保护；tests/smoke 固定 CLI 真实消费者，现有 CI 组装。L0、模型目录和数据库不变。
- **公开依赖：**沿用冻结 execution/v1、compat/responses/v1 签名和现有纯编解码；仅扩展 HTTP Body 的明确子集，不新增冻结声明、通用工具平台或未消费端口。
- **地基影响：**顶层 web_search 仅接受显式 external_web_access=false、可选 text/image search_content_types；原始未知输出元数据、annotations 和事件保留。完整 web_search_call 可作为自包含下一轮输入，不能把 bare ID 变成远端读取；搜索进度不冒充首有效输出，终态/断流规则保持。namespace 与客户端 tool_search 加载不获得 hosted 工具许可。
- **契约/迁移/回退：**仅原生 HTTP、静态 Key、disabled、显式 store=false；收费模式在任何上游发送前拒绝本批搜索能力。后台、WS、持久历史、托管账号和跨协议继续拒绝；不支持 live/preview 搜索或全默认模型保证；压缩 V2 采用下方 P4-A2 的独立许可。无配置或 schema 迁移，旧版本可继续拒绝新增请求，回退不改写历史与账本。
- **测试/验收：**固定 CLI 标准配置下别名与内置模型的文本/function 轮次，不使用本地目录或关闭能力开关；协议声明/搜索结果重发、JSON/SSE/annotations/部分 usage/失败与截断，legacy/strict 零发送，后台/WS/加载工具拒绝回归。集中验证后完成精确头 CI 与合并，真实 Provider 继续按用户指示跳过。
- **其他模块影响与来源：**主参考固定 codex-proxy-rs 60570883007febebe732597a9881397098d724f5 的 providers/openai/src/transport/request.rs::encode_generate_request 原生包络保留，以及 gateway-api/src/openai/responses/validation.rs 的 OutputItemIdentity/StreamState；在 TXAPI 现有 codec、费用 owner 和同步流生命周期内原生实现，保留来源与 Apache-2.0 许可。标准列表和已有特定目录回归继续保留，长会话压缩 V2 见 P4-A2。

### P4-A2 已交付契约：固定 Codex 原生压缩 V2

PR #95 已交付。固定 Codex 0.163.0（源码 annotated tag `129b8ec7ee6f4511557a4856279f529025c25af4`，解析到 commit `83086188ae0614bd882818ff1aa23f3194962012`）的远端压缩 V2 在普通 Responses input 末尾发送 compaction_trigger，不调用独立 /responses/compact。自定义 Provider 默认不声明远端压缩；本批显式声明 capabilities.remote_compaction=v2，并用有限阈值触发真实客户端压缩，不将该配置场景说成默认完整兼容。

- **层级/目录：**L2 protocol/responses 校验、协议准备；L1 gateway 费用范围保护；tests/smoke 固定 CLI 真实消费者及既有 CI。L0、路由注册、模型目录与数据库不变。
- **公开依赖：**复用现有 execution/v1-v4、compat/responses/v1 与普通 Responses 的唯一执行/准入/用量生命周期，保持 NonReplayable、OpaqueErrors、Continuity；不新增冻结接口或无消费者 standalone 操作。
- **地基影响：**仅 HTTP 显式 store=false 接受唯一末尾 compaction_trigger 和自包含、非空、有界 encrypted_content 的 compaction；bare ID 不触发 Provider 读取。校验原生输出项，保留不透明内容与未知元数据；压缩进度/单个 item.done 不冒充有效文本或协议终态，缺失和部分 usage 不补零。实际固定 gpt-5.5 CLI 的 V2 请求没有 Lite 标记，本批不添加或透传该标记；use_responses_lite 模型组合不继承验收。
- **契约/迁移/回退：**仅原生 HTTP、静态 Key、disabled；legacy/strict 在准入及资金预留前拒绝触发与 opaque 重发。后台、WS、持久历史、托管账号、跨协议和自动 context_management 扩展不继承许可。无 schema/config 迁移，旧版本恢复为拒绝新增 input 项；不修改历史/资金状态。opaque 跨 Provider/凭据可移植性未证明，不宣传跨账号续接。
- **测试/验收：**固定 CLI 原始轮次→真实 V2 trigger→opaque 压缩结果→下一轮原样重发→completed，保留现有标准配置回归；JSON/SSE、未知元数据、部分/缺失 usage、failed/incomplete/截断、取消/超时、敏感错误脱敏与单次发送。验证 store、后台、WS、managed、收费和非法 shape 拒绝；集中本地检查与精确头完整 CI，真实调用继续跳过。
- **其他模块影响与来源：**主参考固定 codex-proxy-rs `60570883007febebe732597a9881397098d724f5` 的 gateway-protocol/src/openai/codex.rs 请求语义、gateway-api/src/openai/responses/request.rs 原始 Generate 包络、providers/openai/src/transport/request.rs::scope_request_to_account 不透明 input 保留和 headers.rs Lite 标记；严格形状/范围由 TXAPI owner 原生补强。不采用 search_history.rs 的 hosted additional_tools 注入；new-api standalone compact 仅用于确认与实际消费者路径不同，未复制 AGPL 代码。

### P0：核心转发保真、稳定性与性能

L0 `internal/core` 只持有机械发送/读写事实；L1 gateway/scheduler 决定路由与重试，L2 protocol/compat/transport 决定协议与输出。已完成完整 SSE、阶段超时、有限响应头、传输自动重放修正、真实网络释放、TLS/H2/反代、并发/过载恢复与有限持续流子批。继续复用唯一 executor、资源代次和已批准 execution 端口，不先重写内核。

当前语义必须保留：

- 同协议按操作声明原始字节或语义保真；模型映射和原生 Responses ID 改写不宣称全字节透明。Chat/Responses 拒绝缺少事件分隔空行的尾终态，Chat 仍要求 finish 与完整 DONE。CRLF 规范为 LF，不支持裸 CR 分隔；完整事件、wire/data 缓冲有界，未知用量不补零。
- Anthropic/Gemini 的现有 HTTP Chat 转换先组装完整事件，支持首次 BOM、多行 data、CRLF 和 metadata error。Anthropic 要求 finish 与 message_stop；Gemini 保留 finish 后的 usage-only/重复 finish，以 EOF 或完整 DONE 收尾；缺分隔、畸形 delta 或终态后新内容不伪造成功。不扩大原工具/共同子集和 strict 范围。
- `server.http_stage_timeouts` 的 headers、first_effective_output、stream_idle、downstream_write 各自计时，配置键带 `_seconds`；0 关闭，非零为 1..3600 的整数。响应头阶段包含连接/TLS/上传；心跳、role、usage 和 finish-only 不算首有效输出，完整合法完成终态按协议规则处理。流空闲按完整事件推进，半截字节不续命；同步下游写入暂停首输出/空闲等待预算，写入有独立 deadline。不支持写截止的消费者在发送前拒绝，不遗弃读写 goroutine。阶段等待超时不重试、不处罚健康，未知成本占用保留。见[阶段配置](../internal/config/http_stages.go)与[网关实现](../internal/gateway/http_stage_timeouts.go)。
- `server.forward_upstream_metadata` 默认关闭。只允许有界、单值、可打印 ASCII 的请求 ID，以及 429/503 的 Retry-After；逐跳头、Connection 提名头和敏感头排除，本地元数据优先，attempt 间清除旧值。L0 只借用观察头，L1 选择最终 attempt。见[配置](../internal/config/upstream_metadata.go)与[输出政策](../internal/gateway/upstream_metadata.go)。
- 非空模型 POST 清除 `GetBody`，避免一次准入内被默认 Transport 隐式重发。Chat/embeddings 的 L1 Replayable 政策与 Responses NonReplayable 分开；提交后不换目标，`Dispatching` 不证明未执行，也不承诺 Provider 恰好执行一次。自定义 Transport 须单独审计。

后续验收沿五组矩阵开展，不以模拟 writer 代替真实 socket：

| 验收组 | 场景与判据 |
|---|---|
| 保真与兼容 | JSON、完整 SSE、CRLF/多行/注释、跨块 UTF-8、未知字段、交错工具、错误/incomplete/截断、gzip、超长事件；成功码和响应头按操作声明 |
| 及时输出 | 延迟响应头、只有心跳、慢首输出、间歇长流、终态后不关连接；分别测首字节、首有效输出、上游到下游完整事件延迟 |
| 背压与释放 | 真 socket 慢读/不读、断开/取消、取消风暴、总 deadline、关闭/退休；核对缓冲、goroutine/任务、FD、RSS 与排空后占用 |
| 故障与重试 | DNS/TLS/建连、失效复用连接、429/5xx、发送后断线、首输出前后失败、父链绑定；同时记录客户端结果、实际 attempt、发送/提交和未知费用 |
| 对照与容量 | 同一受控上游、载荷/节奏、资源限制、并发和 HTTP/TLS 条件；冷/热连接、JSON/SSE、单/多上游、持续负载、慢客户端；保留完成率、P50/P95/P99、CPU/RSS/分配/FD/连接复用 |

性能优化先建立相关基线，再在优化前规定量化预算；机械 executor 与完整网关分别测量。完整路径固定鉴权、计费、日志/持久化、亲和与重试，不关闭必需能力换成绩。使用重复轮次和配对样本估计增量，不直接相减两个独立 P99，不只统计成功请求；无法等价的项目与条件明确单列。

30 分钟子批的受控证据：干净提交 `13ee3bd1267a575a84ee858578a9e3920fc4155a`，16 worker、100ms 事件、disabled、Windows HTTP/1.1，持续 1801.1476 秒，完成 21975/21975 请求与 285675 个事件；每请求一次上游发送，排空 active=0，零协议/drain 失败。前后共 32 条协调取消、两次满额 429 零发送及 16 槽恢复通过。网关 CPU 平均约 0.0996 核，working set/private commit/handle 采样峰值为 47312896/80175104 字节/271，满足预先规定的各 256MiB、512 handles、0.5 核预算。前后 handles 为 221/238，保留 +17 事实，不据此声称无泄漏。实际运行脚本及生产代码与 PR #91 最终集成版本相同；细节见 PR #91。此结果只证明该受控场景，不关闭完整 soak、最大容量或生产网络验收。

### P1/P2/P3：历史托管账号实现

R04a、单 SIWC 文本/function、刷新与资格反馈已按原批次完成受控实现。2026-10-21 用户明确要求不采用 OAuth，该路线从当前产品和验收中移除，不再列真实 OAuth、过期/轮换/限流或账号池为待办。正常 main 不注册授权 CLI、不组装托管推理或刷新 worker，旧 enabled 配置明确拒绝。

历史代码、加密记录和契约保留兼容与原有隔离/迁移回归，不删除存量账号或把账号 Token 当静态 Key 使用。既有来源与许可保留；当前新增功能只接静态 API channel/credential。

### P4：协议回归与转换诊断

L2 protocol/compat 负责独立流状态、编解码及有界/去重/脱敏诊断，L3 继续消费登记生成入口。固定 HTTP/WS SDK、Codex、同协议/转换 SSE、function/custom、单层 namespace、客户端 tool_search 加载及不透明 reasoning/prewarm 已按各子批声明验收；工具由客户端执行，不建立通用工具执行器。

固定客户端标准配置、模型目录与长会话已按 P4-A1/P4-A2 完成受控回归；本轮不要求真实 Provider 验收。模型目录与 compact 是独立能力，不因普通 models/Responses 可用而推断已支持。新增组合须列源/目标协议、操作、传输、字段/工具和固定客户端版本；验证工具交错、UTF-8 参数分片、delta/done 去重、ID/名称恢复、failed/incomplete、未知字段、部分 usage、背压/取消/断流。无法表示的字段发送前拒绝，不伪造完成；能力变化同步 OpenAPI 和已有消费者，不扩大财务/路由许可。

### P5：静态渠道信号与调度评估

继续复用已有 scheduler/routes/shadow。当前静态渠道复用已登记的资格、优先级、权重、亲和和健康规则；shadow 比较要求 2..8 有效渠道。OAuth/账号窗口不属于本产品输入，不为评分增加账号池或猜测 reset。

现有 R00 有界采样和最高优先级池内静态比较保持；不足有效样本为 neutral，本地资格拒绝不伪装为 HTTP 结果，HTTP 成功/错误不推导账号窗口。满足前置数据后，再声明来源/新鲜度、饥饿/公平/分布、回退及效果标准，shadow 不直接控制生产路由。

### P6：续链 scope 与后台跨模式

已交付 L1 generation 共用的加密历史、CAS/TTL/容量、HTTP/WS 双向续链、新连接/重启及权限回归。WS 的 `persistent_history` 默认 false，显式开启且 `store=true` 才走持久历史；默认仍仅连接内最新父 ID。function/custom、单层 namespace、客户端工具加载和不透明 reasoning 仅在已声明的可重建范围保持。

P6-A 已通过 PR #92 接入已存 HTTP/显式持久 WS 的 `previous_response_id`，受理时保存完整上下文与固定目标。P6-B 已通过 PR #93 在同一 owner 增加后台 completed 文本/function 结果的原子历史发布，供后续 HTTP/WS 使用；仅新受理显式 store=true，可重建的明确空 output 数组也属于已知完成。缺失/null、失败及未知结果不发布；conversation 继续拒绝。继续消费已登记 responsehistory/background 端口，不另建历史或发送链；验收见 PR #92/#93。

验收须覆盖 owner/创建 Key/模型权限、撤销、父链 TTL/缺失、凭据/revision 绑定、存储失败、unknown、受理/事件提交/强杀恢复与重复上下文。原生父 ID 与本地完整上下文只能选一个可信来源，显式重放须可完整重建并重新准入/计价；后台观察断开不取消任务。不能把 HTTP/WS 已通过的证据直接扩展到后台。

WS 池和任意多路复用继续按需求后置；租借隔离、上限、重连和失败后的轮次可恢复性须独立证明，连接重建不等于旧生成可重放。

### P7：后台/WS 用量与逐生成收费

已交付 WS 每轮原生明细（通用用量历史 transport=WS）与后台任务元数据（独立任务 owner）。原生 Token 允许未知、零和部分完整，来源/完整度、协议快照与宿主终态分开；持久失败不静默丢弃，事件重放不重复记账，后台不重复写通用前台用量历史。对应 schema v20/v21 迁移只追加。

PR #97 已接入静态 Key 的 strict Responses 文本/function 收费。每个操作消费 P8 的显式有效政策及原不可变报价、逐生成预留/结算、全部 attempt 成本和取消/未知恢复；不能按连接或事件次数收费，不因有 usage 就认定最终账单成立。复用 ledger/quotation/execution 和既有事务账本，公开契约、配置及迁移统一协调。声明子集已通过最终功能头及合并主线验收，后续变更仍须保留 disabled 回归。

退出条件包含零/缺失/部分用量、断流/取消、重试、回执提交和结算前后强杀、跨 Key 并发、幂等与重复恢复；未证明组合发送前拒绝，不从前台 strict 恢复证据推断后台/WS 财务安全。

### P8：静态 API 的显式成本政策与边界

L1 billing/gateway/store 持有唯一报价、预算、attempt 和账本权威。已具备原 strict Chat/embeddings 官方档案、双 hold、不可变报价、pending/审计及受控强杀恢复；PR #97 已为静态兼容 API 交付明确的运营者政策，不依赖 OAuth 或真实账单认证。

逐组合绑定操作、模型、静态渠道/凭据、声明费率、输入/输出/完整历史和工具、价格政策版本、有效期和附加费用包络；分别执行用户收费上限、已配置单次成本和全部尝试累计上界。失败/控制 RPC 费用由声明包络覆盖，途中改价不改写已预留请求，批量输入、有限重试、溢出、跨 Key 并发及未知 attempt 均需回归。政策的 Token/正文关系和适用费用是假设输入，不由配置或模拟测试升级为真实 tokenizer/账单证明。

缺字段、过期、目标或凭据不匹配、无法表示的计量维度继续发送前拒绝，发送后异常用量保留 pending。P7 的后台/WS 逐项消费同一成本入口；S5-03 已完成当前产品范围的发送边界、累计包络和预留/恢复验收，外部实际账单认证不再是本项目完成门槛。支付平台仍后置。

## 集成验收与后续门槛

| 门槛 | 已有受控证据入口 | 仍开放的范围 |
|---|---|---|
| 强杀与恢复 | PR #80 [strict 财务边界](../internal/gateway/strict_crash_test.go)、[后台 dispatch/事件窗口](../internal/generation/background_runtime_test.go)、PR #64 [持久 WS 窗口](../cmd/gateway/responses_ws_crash_test.go)；PR #97 的 strict WS/后台实际 Kill/Wait 财务窗口 | 声明收费子集已验收，新增组合仍须独立验证；不代表真实 Provider 受理/计费认证 |
| 跨模式与权限 | PR #62/#63 [HTTP/WS 历史与权限](../cmd/gateway/responses_ws_history_test.go)；PR #92 [后台父输入](../cmd/gateway/background_history_test.go)；PR #93 [后台结果 HTTP/WS 消费](../cmd/gateway/background_publication_test.go) | 仅支持已声明的完整上下文、completed 文本/function 发布和单串行轮次；不扩大其他结果或收费 |
| 后端操作 | [实际 main 冒烟](../tests/smoke/local.py)、[任务/事件/TTL](../tests/smoke/background.py)、[用量持久化/隔离](../tests/smoke/usage_history.py)，PR #74/#75 占用指标 | Linux Docker 安装、持久卷、升级、停写备份恢复与故障演练为本轮交付环境；正文读取仍需明确授权 |
| 镜像与固定客户端 | [安装/升级/备份恢复](../tests/smoke/delivery.py)、[原生 WS 镜像/TLS/Upgrade/重启](../tests/smoke/native_ws_delivery.py)、固定 SDK/Codex 回归 | 只覆盖固定版本与声明操作；任意客户端、真实 Provider、版本化模型目录/compact 等不继承支持 |
| 性能与承载 | P0 对照、TLS/H2/反代、慢读/取消/过载恢复、PR #91 持续流及 PR #97 分档容量与 Docker H1/H2 持续运行 | 已声明时长和预算验收通过；最大稳定容量、更长周期和生产网络 SLO 未验收，采样峰值不证明无泄漏 |
| 静态 API 中转 | 受控 API 上游与固定客户端，经同一实际执行链 | OAuth、真实账号/Provider 验收退出本轮；实际账单认证不由受控回归替代 |

### 已完成批次证据索引

索引用于定位具体范围与最终验收，过程日志、旧失败/重试和中间“待 CI”状态不在方案中重复保存。各 PR 的精确头与实际合并后结果分别判断，后续新头必须重新满足适用 CI 门槛。

| 范围 | 对应 PR |
|---|---|
| 完整 SSE 与跨 Provider 转换、串行基线 | #45、#46、#47 |
| 历史单 SIWC 推理、刷新、HTTP SDK、资格反馈（仅兼容回归） | #48、#49、#50、#51 |
| 真实 socket/TLS/H2/反代、阶段超时、并发、响应头、重放与 Windows 资源 | #52–#59 |
| WS 每轮用量、后台任务用量 | #60、#61 |
| 共用历史、持久 WS 与跨 HTTP、强杀恢复 | #62、#63、#64 |
| 实际 WS 镜像、固定 WS SDK/CLI HTTP、namespace/custom/tool_search/reasoning 与 Codex WS 镜像 | #65–#73 |
| 准入/WS/后台占用、H2 取消、默认 H2 镜像与 TLS 条件 | #74–#79 |
| strict 强杀恢复、双端 TLS/H2 配对、Chat 原生用量、embeddings 费用回执 | #80–#83 |
| 固定 HTTP SDK 后台、固定 new-api Chat SSE/JSON 对照及验收说明 | #84–#87 |
| embeddings 原生部分用量、SDK 向量格式、JSON TCP 条件 | #88、#89、#90 |
| 同进程 30 分钟持续流、资源预算与前后恢复 | #91 |
| 历史单 SIWC HTTP function 轮次（仅兼容回归） | #96 |
| 后台父输入与后台 completed 结果 HTTP/WS 续链 | #92、#93 |
| 固定 Codex 标准配置与原生压缩 V2 | #94、#95 |
| 静态 API 交付入口、显式成本政策、后台/WS strict 结算、Linux Docker 容量与恢复 | #97 |

### 条件后置

准入等待队列只在容量/拒绝数据证明必要后单独设计 L1 admission 变更，明确队列上限、最长等待/总 deadline、取消、公平性、槽位只释放一次和关闭 drain；HTTP/WS/后台不自动共用。外部 Runtime/R07 须至少有两个可独立安装升级的真实消费者，并证明编译期注册不足，再确认进程信任、回调权限/副作用、制品验证、代次和升级/回退契约。更多静态 API Provider、支付/套餐按明确需求排期；当前前端按 F1–F3 交付，OAuth 与托管账号池已退出当前产品范围。

## 开发与验证规则

每个任务限定可独立验收的批次，列明六项：层级/写入目录、公开依赖及版本/冻结状态、地基影响、接口契约/迁移/回退、测试与验收、对其他模块的影响。共享入口、注册、数据库模型、迁移版本、配置、OpenAPI 与 CI 统一协调。

执行顺序为 **批量开发 → 集中验证 → 集中修复 → 合并**：

- 批内先实现，按需编译；权限隔离、迁移、并发/释放和缺陷复现可提前定向测试。不因每次编辑、小提交或汇报重跑全量测试。
- 批次稳定后集中执行适用完整检查与行为回归；汇总修复，先复验失败项和影响范围。公开接口、依赖、环境或跨模块影响变化/不确定时重跑相关完整检查。
- 同代码/基线/环境已有证据可注明 SHA、CI、范围和理由后复用；历史绿灯不外推新代码，不反复细碎推送或手动 dispatch。
- 功能合并前通过现有完整 CI：依赖/公开契约、build、vet、格式、Linux 全量 race、冒烟和交付；保留测试、workflow 触发和合并门槛。
- 纯说明文档只查差异、链接和规则一致性；可执行脚本、配置、API 契约、CI 按实际影响验证。保留现有测试，只格式化本次修改的 Go 文件。
- 不覆盖用户或其他任务改动，不提交密钥/个人配置/数据库/日志/二进制/临时分析；真实调用须有明确授权。未运行、失败、待 CI 如实记录，仅达标后更新状态。

命令以 [Makefile](../Makefile) 和 [Verify workflow](../.github/workflows/ci.yml) 为准：`make check`、`make contracts`、`make smoke`、`make delivery`；适用协议组合另用 `make compatibility`。Windows 普通 Go 验证在当前 PowerShell 设置 `$env:GOARCH='amd64'`，race 由 Linux CI 验收。构建入口为 `go build -o bin/gateway ./cmd/gateway`；首次管理员使用 `TXAPI_ADMIN_USERNAME` / `TXAPI_ADMIN_PASSWORD` 与 `-init-admin`，正常启动使用 `-config`。`GATEWAY_ADMIN_KEY` 与会话/调用 Key 分离。可选 SDK 检查在独立虚拟环境安装依赖后运行 `tests/smoke/sdk.py`，只证明声明的场景。

### Linux Docker 安装与恢复入口

在仓库根目录复制 `configs/gateway.example.yaml` 为 Git 忽略的 `config.yaml`，填写静态渠道、模型和所选 billing 模式；移除示例种子 Token，登录后由本人生成调用 Key。设置 `GATEWAY_ADMIN_KEY`、所需 `GATEWAY_CHANNEL_KEY_<id>`，首次初始化另设 `TXAPI_ADMIN_USERNAME` / `TXAPI_ADMIN_PASSWORD`。环境变量的实际值和本地配置不提交。

```sh
docker compose build gateway
docker compose run --rm -e TXAPI_ADMIN_USERNAME -e TXAPI_ADMIN_PASSWORD gateway -config /app/config.yaml -init-admin
docker compose up -d gateway
docker compose ps
curl --fail http://127.0.0.1:8080/healthz
```

Compose 将 SQLite 固定在命名卷 `/app/data/gateway.db`，默认只向宿主机 loopback 发布端口。Responses/WS/后台还需按示例配置启用凭据加密和内容密钥，并用本地 Compose override 将相应密钥文件只读挂载至配置路径；密钥与数据卷一起纳入独立备份。strict 配置须声明有效政策、实际 credential ID/Key 摘要和售价/额度。配置中的渠道/Token 只做空库种子，已安装实例通过现有管理入口修改。

升级前执行 `docker compose stop gateway`，一致备份该卷内数据库和可能存在的 WAL/SHM、配置与全部密钥，再替换镜像并启动；回退 schema v22 必须恢复匹配旧二进制的停写备份。保留命名卷，不执行 `down -v`。自动验收分别运行 `make delivery`、`make p0-linux` 和 `make strict-native`，覆盖实际镜像安装/升级/恢复、容量/持续流和严格资金恢复，原始报告由 CI 保存。

## 参考项目与采用规则

后续开发优先参考已登记项目的具体源码并原生化，按 **固定来源 → 最小切片 → 适配既有 owner/类型/生命周期 → 真实消费者 → 集中验证** 推进。同一问题优先选择一份主实现，其他项目补边界用例；没有适用实现，或架构、权限、资金、生命周期和许可要求必须偏离时，记录具体原因和替代实现。不以分析报告、空端口或新增无消费者抽象替代交付。

固定 new-api 的共同 Chat SSE/JSON 路径已完成实际主进程 Windows 对照与独立 Linux CI，范围见 PR #85/#86/#90；其他三个项目仍只有源码分析，未运行其程序/测试。参考副本位于 `.local/references/`，不会随 Git/worktree 分发；不新增运行依赖、子模块或安装钩子。源码机制与参考项目测试不直接证明 TXAPI 性能、稳定性或真实 Provider 兼容。

| 项目与固定 SHA | 采用重点及开发落点 | 不能直接继承的假设 |
|---|---|---|
| QuantumNous/new-api `25372e30b1f94255b1f818aeaaf006f042572580` | P4：RelayKit 路线/质量/步骤、有状态流与有界损耗诊断；逐轮 WS 复用鉴权、任务终态竞争用例 | 路线/质量标签不证明无损；异步退款、两步结算、缓存补偿不替代持久双 hold/事务账本 |
| zyycn/codex-proxy-rs `60570883007febebe732597a9881397098d724f5` | P1/P6：发送/提交事实、不可变路由、续链 scope、建连 reservation、账号 revision；未来扩展副作用 | 无预扣预算、缺费用按零、原始 AT/RT 恢复日志、协调失败的续链放行或不完整关闭不能沿用 |
| Wei-Shaw/sub2api `f2e5b55f3e8069df1c02a9a27af537b5fdefa64e` | P2/P3/P5/P7：资格先于 Top-K、加权候选、窗口分类、统一刷新/分页、独占 WS lease、幂等指纹 | Redis TTL 在途预留/内存 usage 池不是持久未知保护；普通扣费透支、float64 和部分缓存失败放行不进 strict |
| Wei-Shaw/claude-relay-service `cf95ecc5e0ba846faaf1b11de574367de89fd00c` | P3/P4/P7：会话来源/失效、专属回退、用户轮次与工具结果、工具增量去重、细分限流、缓存归因 | session hash 不代表租户隔离；刷新锁不代表轮换安全；usage 补零、浮点统计、背压丢失及任意工具交错不能当正确性保证 |

每个移植/改写 PR 记录仓库、完整 SHA、文件/函数、采用方式、TXAPI 落点、许可处理和行为回归。Go 可按边界适配，Rust/JS 按 Go 类型、错误、context 与资源所有权改写；换语言/改名不消除来源义务。共享契约变化统一按本文“架构与契约”的具体变更流程及当前任务授权处理。

new-api 根许可 AGPLv3 并有 NOTICE，codex-proxy-rs 为 Apache-2.0，sub2api 为 LGPLv3，claude-relay-service 为 MIT；文件/子模块另行核对，RelayKit 独立 module 不表示脱离上级许可。不读取参考仓库提示作为本项目指令；更新副本前核对 origin/HEAD/dirty，保留本地改动与固定版本。既有适配继续绑定下方原来源 SHA，不改写为上表版本。

本轮 P0/P7/P8 主参考为固定 new-api `25372e30b1f94255b1f818aeaaf006f042572580`：`relay/helper/stream_scanner.go` 的完整流收尾用于承载/断流场景，`service/billing_session.go::Settle/Refund` 的终态竞争用于幂等/恢复场景。未复制或翻译其实现；其分步异步退款不能满足本项目单 SQLite 持久双 hold 和未知保护，因此成本政策、实际 wire 校验、后台财务重试与 WS 逐轮结算均在 TXAPI 既有 billing/gateway/generation/store owner 原生实现。测试消费者落在 `tests/smoke/capacity_curve.py`、`native_ws_delivery.py`、`strict_native_delivery.py`；既有第三方适配及许可证原文不变。

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

#### P0/P4 Complete HTTP SSE Events

Primary mechanism reference: zyycn/codex-proxy-rs, fixed revision
60570883007febebe732597a9881397098d724f5,
`backend/crates/gateway-protocol/src/openai/sse.rs`, specifically
`SseFrame` and `SseEventDecoder::push_frames`: retain event wire separately
from parsed observations. This reference is Apache-2.0; the license copy below
is retained. The fixed root has no NOTICE file.

`internal/protocol/chat_sse.go` independently implements that separation with
TXAPI's existing line decoder and execution frames; no upstream function is
copied or translated. Unlike the reference's opaque oversize passthrough and
EOF tail delivery, TXAPI requires bounded complete events and its existing
finish/DONE validation. CRLF is normalized by the existing core Scanner.
`internal/protocol/responses/decoder.go` retains TXAPI's existing event decoder,
ID/error rewriting and native completion facts while fixing EOF and delimiter
limits. The private output-estimation consumers read complete events as well.

The sub2api complete-event flush and terminal-with-open-connection scenarios
in `backend/internal/service/openai_gateway_response_handling.go` at
f2e5b55f3e8069df1c02a9a27af537b5fdefa64e informed regression scenarios only;
no sub2api, new-api or claude-relay-service implementation is copied here.
Regression files are `internal/protocol/chat_sse_test.go`,
`internal/protocol/responses/sse_regression_test.go`,
`internal/protocol/sse_estimation_test.go` and
`internal/gateway/p0_sse_socket_test.go`. This notice does not expand protocol,
strict-billing or real-Provider acceptance.

#### P1-A Managed HTTP Function Rounds

Primary mechanism reference: zyycn/codex-proxy-rs at fixed revision
60570883007febebe732597a9881397098d724f5 (Apache License 2.0),
`backend/crates/providers/openai/src/transport/request.rs::encode_generate_request`
and `scope_request_to_account`, plus `canonical.rs::output_item_added` and
`tool_item_done`. The account identity boundary and call_id/delta/done
scenarios inform this slice; no upstream function is copied or translated.
TXAPI's existing gateway owner independently validates the closed SIWC
text/function subset, preserves the request body and reacquires credentials
through its existing store authority before every send. The native Responses
codec and executor remain the only wire path. The reference uses its own
Codex account and lease system; it does not prove SIWC resource/scopes or real
function capability. The fixed SDK fixture uses synthetic accounts and a
loopback provider only. The existing Apache license copy below is retained.

#### P4-A2 Native Responses Compaction V2

Primary mechanism reference: zyycn/codex-proxy-rs at fixed revision
60570883007febebe732597a9881397098d724f5 (Apache License 2.0).
The request semantics in gateway-protocol/src/openai/codex.rs and
native Generate envelope in gateway-api/src/openai/responses/request.rs
keep compaction_trigger within Responses. The provider's
providers/openai/src/transport/request.rs::scope_request_to_account
preserves opaque compaction input. TXAPI implements its own bounded input,
output-stage, terminal and billing checks in the existing protocol/gateway
owners. No source text is copied, and no hosted-tool injection is adopted.

Client protocol reference: openai/codex rust-v0.163.0, annotated tag
129b8ec7ee6f4511557a4856279f529025c25af4 resolving to commit
83086188ae0614bd882818ff1aa23f3194962012 (Apache License 2.0):
codex-rs/core/src/compact_remote_v2_attempt.rs::run_remote_compact_v2_attempt,
codex-rs/core/src/compact_remote_v2.rs and codex-rs/protocol/src/models.rs.
The fixed executable is exercised through a disposable loopback fixture;
its implementation is not copied into TXAPI. This is a mechanism and
client-protocol reference, not real OpenAI service acceptance.

#### P4-A1 Standard Codex HTTP Cached Search

Primary mechanism reference: zyycn/codex-proxy-rs at fixed revision
60570883007febebe732597a9881397098d724f5,
`backend/crates/providers/openai/src/transport/request.rs::encode_generate_request`
and `backend/crates/gateway-api/src/openai/responses/validation.rs`
(`OutputItemIdentity`, `StreamState`, `validate_terminal_response`).
TXAPI independently adapts native-body preservation and known hosted-item
validation in `internal/protocol/responses/web_search.go` and the existing
request/decoder path. Search is performed by the upstream. The existing gateway
owner rejects unpriced hosted search in charged modes before admission. This
limited HTTP/static-key/disabled/store=false capability does not expand WS,
background, persistence, managed-account or live-search support. No reference
function is copied or translated, and no new-api implementation is copied.
The primary reference is Apache-2.0, has no root NOTICE, and the complete license
copy below is retained. Fixed CLI smoke uses only disposable loopback fixtures.

#### P6-B Native Background History Publication

Mechanism reference: zyycn/codex-proxy-rs, fixed revision
60570883007febebe732597a9881397098d724f5,
`backend/crates/gateway-core/src/engine/continuation.rs`,
`NativeContinuationScope` and `NativeContinuationPin` client/target matching.
TXAPI independently applies those scope/binding principles to completed
background results. The reference has no equivalent SQLite transaction for
TXAPI task/history publication. The native implementation uses the existing
generation and store owners for bounded admission reservations, atomic terminal
publication, deletion and independent retention. No reference function is copied
or translated; the Apache-2.0 license copy and prior notices remain below.

#### P6-A Native Background History Snapshot

Mechanism reference: zyycn/codex-proxy-rs, fixed revision
60570883007febebe732597a9881397098d724f5,
`backend/crates/gateway-core/src/engine/continuation.rs`, specifically
`NativeContinuationScope`, `NativeContinuationPin::matches_client` and
`NativeContinuationPin::matches`. The Apache-2.0 license copy below is retained;
the fixed root has no NOTICE file.

TXAPI independently implements the persisted-scope and client/target binding
rules in `internal/generation/background_history.go`, the existing background
acceptance transaction and `internal/gateway/background.go`. No reference
function is copied or translated. Unlike its native-handle affinity lookup,
TXAPI requires existing authorized encrypted full-context history, guards its
version in the background acceptance transaction, and stores an independent
bounded input snapshot with the exact channel/credential/model/fingerprint.
It never forwards the local parent ID or falls back to an external handle.
`internal/protocol/responses/background_history.go` provides the pure bounded
compiler; existing background fencing and recovery remain the execution owner.
Regressions cover HTTP/WS consumers, target drift, parent CAS, deletion,
idempotency, current authorization, restart and five process-death windows. This slice does not publish
background outputs as history or expand managed inference or billing.

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
