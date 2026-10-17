# 可选运行组件与持久端口

本页保存 generation worker、加密输出记录、WS transport 和加密意图的调用/生命周期/容量/恢复约定。组件已经实现，但当前 main 没有启用这些消费者；不能将本页当作公开后台 API、生产密钥配置或 Responses 产品使用说明。当前状态和执行顺序见 [PLAN.md](../PLAN.md)，开发命令及验证节奏见 [DEVELOPMENT.md](../DEVELOPMENT.md)，跨模块写入边界见 [COLLABORATION.md](../COLLABORATION.md)。

这些组件复用原授权、准入、执行和账本，不承诺完整持久任务平台。以下保留 A3 批次编号以便对应 [实现与验收记录](../history/implementation-s5-a3.md)，编号不表示整个架构已经验收。

## A3-01 进程内消费者边界

`internal/generation` 是最小真实 worker 消费者，构造时注入 `Store.ResolvePrincipal` 与 `Gateway.ExecuteTo`。服务 context 拥有 worker；`Submit` 的 context 只控制交接前取消与 Request ID，不把观察者身份、限流预留或取消状态带入生成。交接只复制有界正文、会话键和可信 user/Key 绑定，不能把客户端提交的 ID 当授权。派发前重新读取当前 owner/Key/系统策略及限额，失效或存储异常均不执行；当前在飞生成仍按本轮授权快照运行，撤销不自动等于取消，后续持久任务的撤销传播策略尚未实现。

worker 数、队列长度、单请求正文和排队起算的取消时限均有界；配置要求 queued+active 的正文副本预算不超过 64 MiB，输出实现仍须自行有界。队列不占 gateway 的 generation/network attempt 槽，派发后复用既有准入与账本。`Future.Wait` 取消只停止观察，`Future.Cancel` 显式取消生成；只有一次结果接收，不能复用其中的可变指针。输出在收到执行结果或 runner 完整关闭前始终归生成独占。满队列直接拒绝，不建额外等待 goroutine，也不保证逐用户排队公平。

关闭顺序是 runner 停接/取消/等待收尾 → gateway/共享资源 → store。`Close(ctx)` 超时只表示尚未等完，不能提前关闭后两者；可信回调忽略取消时无法强杀。gateway 自身的财务收尾仍可能在生成取消时限后继续。进程退出会丢失未执行的内存交接；它没有持久受理、fencing、事件游标或重启重派语义，**不能用来实现公开 `background=true` 的受理承诺**。当前 main 未启动此 runner，HTTP 前台断开策略保持不变。后续生产后台入口仍须任务意图/预留原子事务与恢复验收。

可定向执行 `go test -count=1 ./internal/generation ./internal/store`；使用临时 SQLite 和隔离 transport，无真实上游/凭据。关键回归包括排队后撤销/权限收紧、一致身份快照、观察者断开、正文副本、满队列/排队到期、取消/并发关闭、提交后 panic 和 strict 未知执行保留双 hold；Linux race 随现有完整 CI 执行。

## A3-02 可选加密输出记录

本批为上述真实 worker 增加 `Archive` 和 `SubmitRecorded`，不是新后台 API。默认 `Submit` 不存正文；只有组装者明确注入 Archive 并调用新方法才记录编码后的响应元数据、JSON/SSE 片段和最小结果摘要。输入正文、调用 Key、权限快照和任意原始异常不进入新表。schema v8 在 store 统一迁移中新增 `generation_records` / `generation_segments`；不回填历史日志或改写旧账本。v7 及更老二进制不得打开 v8，回滚恢复升级前一致备份，不能删除版本标记降级。

- 组装者通过 `NewArchive(ArchiveOptions)` 注入真实 store、小身份解析函数和独立密钥环：每把 32 字节 AES 密钥、1–32 字符非秘密 key ID、明确 active ID；无默认/临时生成/明文回落。密钥不存入 SQLite、不复用管理员密码或调用 Key，调用者修改原 map/切片不能改写运行实例。当前 main/YAML/环境变量没有生产接线，不能仅设置一个虚构环境变量来启用它。
- 每段使用随机 nonce 的 AES-256-GCM；版本、key ID、记录/owner/创建 Key、创建/过期时间、容量、序号和段类型纳入认证。轮换后保留旧 ID/旧密钥用于历史读取，新段使用新 active ID；旧钥缺失/错误、篡改或顺序不完整均拒绝且不返回部分明文。必须在同一密钥累计加密达到 2^32 段前轮换；尚无自动 KMS/轮换、批量重加密或生产密钥恢复工具。密钥与数据库分离保管/备份；丢失旧钥的数据不可恢复，密钥和数据库同时泄露不在此保护范围。
- 接收前在事务中预留每条最大密文字节数，保留到删除/清理。配置明确 owner/实例记录数及总预留字节；单条 16 KiB–8 MiB、最多 256 段、正文每段最多 32 KiB，另留结果段与 4 KiB 空间。容量满在执行前拒绝；输出过大在写入处失败并沿原链收尾，不静默截断为成功。逻辑密文限额不是 SQLite/WAL/索引/备份的物理磁盘配额或安全擦除保证。
- 保留期明确为 1 分钟–7 天且长于 generation 时限；读取/追加过期内容立即拒绝，过期但未清理的记录仍占预留容量。启动/清理由组装者显式调用，`CleanupGenerationRecords` 每批最多 1000 条并级联删片段；当前 main 不启 worker/清理服务。删除不证明上游未执行或已取消，也不回退账本；备份仍可能保留旧密文。
- `Read`/`Delete` 使用当前身份解析和可信 owner/创建 Key 绑定，其他用户或同用户其他 Key 均不能靠 ID 取回/删除；读取还要求当前模型策略允许原模型。摘要/正文/元数据加密存储；摘要仅含有限操作/模型、阶段/提交/终止、协议/账本观察与白名单原因，不是财务权威。认证是本次读取快照，不承诺撤销瞬间中断在飞读取。
- 每次写入以序号 CAS 与计数更新在一个事务中完成；段写失败回滚计数，确认丢失不盲目重新追加。`open` 只有追加最终观察后变为 `sealed`；sealed 不等于协议成功或 billing settled。关闭 runner 会等待正在准备存储的交接和执行收尾，存储 I/O 不持有停接锁；超时仍不得关闭 store。
- 单实例独占启动、任何 producer 启动前，分批调用 `InterruptGenerationRecords` 把遗留 open 记为 interrupted；该操作只说明观察不完整，不返回可执行任务、不重发、不退款。中断/不完整记录不返回部分正文；完整 sealed 记录重启后可解密读取。最终观察写失败时即使 API 返回归档错误，生成也可能已完成并结算；若确认丢失但事务实际成功，重新读取可得到完整记录。不得以归档错误自动重试生成或撤销账单。

原账本仍是唯一财务权威。真实隔离 transport/SQLite 回归分别验证：输出写失败且上游 usage 未知，双 hold/provider 占用保留 pending；生成已结算而最终观察写失败，既有结算不回退。另有真实子进程在部分 SSE 加密片段持久后被强杀，重启后旧句柄被拒绝、记录为中断、真实租约到期恢复不重发/不退款。测试没有降低 strict 档案或凭据门槛。

这不是持久会话、durable task acceptance、任务领取/续租/fencing、原子任务意图+预算预留或事件游标；不保留可用于重发的输入，也没有自动恢复生成。S7-A 的 store=false/后台原生状态与内容策略仍需自己的产品接线和验收，不能静默把客户端请求送入本记录器。现有上游凭据明文存储未因本批加密输出记录而修复。

## A3-03 有界 WS 传输与连接生命周期

`internal/transport/websocket` 只管理服务端 WS 握手、物理连接和同步消息 I/O；不导入业务包、不处理身份/协议事件/账本。固定使用 `github.com/coder/websocket v1.8.15`（ISC），Go 依赖校验保持开启。已核对官方发布及该版本 Accept/Read/Write/CloseNow/SetReadLimit 源码：取消正在进行的 I/O 会关闭连接，Ping 的发起端也必须有读取器接收 Pong，CloseNow 不等待普通关闭握手。第三方库不进入标准库 core，生产依赖约束限定在本 transport 包；该包不是原生 Responses WS 上游执行器。

- 组装者先用现有身份权威认证，再以可信 `access.Binding` 调用 `ConnectionCapacity.Acquire`，随后 `Manager.Accept`。连接限额为 `1 <= Key <= User < Instance <= 4096`，关闭只停接、不丢掉尚未释放的租约；幂等释放。空闲连接不预占 generation/attempt 槽、RPM 或预算。当前没有 main/YAML 配置或公开 WS 路由；不能把这一测试组装当成已启用的产品。
- `Manager` 使用服务 context；HTTP request context 只约束握手检查，handler 返回或 HTTP Server.Shutdown 不拥有已升级 socket。实例连接数 1–4096，单消息 1 字节–20 MiB，连接数乘单消息上限不超过 32 MiB（每方向正文配置预算，非进程总堆内存保证）；禁用压缩。握手/写时限不超过 30 秒，单次读时限不超过 5 分钟，绝对连接寿命不超过 24 小时且不少于读时限，所有值须显式设置。
- Origin 采用已复制的精确 http(s) scheme/host 列表，并允许真实请求的同源浏览器；拒绝 wildcard/null/路径/用户信息/多值或错误 scheme。实际 TLS 决定同源 scheme，不信任 X-Forwarded-*；TLS 终止代理后的浏览器需由组装者显式允许外部 Origin。无 Origin 的 native 客户端仍必须先认证，Origin 校验不替代鉴权。
- `Accept` 同时限制 active/pending，在升级前要求 ResponseController 写截止时间能力；不支持则失败关闭。停接取消握手写入并等待 callback 完成，防止迟到的过期截止时间覆盖升级后的 reset。pending 握手未退出时 Close 可超时，不能宣称资源已释放；忽略 deadline 的可信自定义 Hijacker 仍需退出后再 join。
- 每条连接始终保持一个 reader 以处理控制/关闭帧；并行 reader 明确拒绝。读限额覆盖组装后的完整消息而非单帧；Text 校验 UTF-8，Binary 保持字节。Write 同步串行且总时限包含等待，不复制/排队正文或自行启动写 goroutine；调用方仍须限制并发调用数量。错误仅返回白名单类别，丢弃 peer reason、原始 header/URL/payload。预取消调用不启动 I/O；I/O 中取消/超时会终止物理连接。
- 应用区分物理连接、生成和观察：前台连接丢失时显式 `Future.Cancel` 并等待原 gateway 收尾，未知执行继续 pending/保留双 hold/provider 占用；记录型交接的观察者丢失只取消 Wait，工作继续到完成/期限/runner 停止。Connection Close 与 Manager Close 都不退款、不重放，也不替代 runner 的 join。产品后台仍不能使用 volatile handoff 承诺持久受理。
- 关闭顺序：身份连接容量停接 → manager 停接/关闭/等待握手 → HTTP handlers 停接与 WS 应用 reader 退出 → runner 停接/取消/等待执行和账务收尾 → 应用 goroutine join → gateway/共享资源 → store。manager 或 runner 超时后保持后续资源存活并重试等待。物理连接 Done 仅说明 transport 清理，不说明应用生成/财务已结束。

`go test -count=1 ./internal/access ./internal/transport/websocket ./internal/generation ./internal/architecture` 覆盖真实 TCP/TLS、控制帧/分片/UTF-8、Origin、读/写/绝对寿命、慢读背压、握手/关闭竞争和容量；测试专用应用在真实 socket 上调用原 Runner/Gateway/SQLite/严格账本，验证逐轮撤销/收权、同 ID 并发隔离、前台未知成本和记录型观察者断开。测试帧信封只在 `_test.go`，载荷仍为已有 JSON/SSE 字节；没有新增生产 wire 协议、公开 API、Responses 多轮或原生 WS 上游能力。

## A3-04 加密意图与双预算原子受理

`NewIntentAcceptor(IntentOptions)` 是独立、不可派发的真实持久消费者，当前只接受现有 Chat/embeddings。可信组装者注入原 Engine 的 `IntentAdmission` 和 `Store.ResolvePrincipal`；不导入 GORM、不传事务回调。Admit 在同一事务写 `generation_intents`、稳定摘要密钥版本标记及原账本的用户/Key 双预留，任一失败全部回滚；提交确认丢失后可用相同幂等键读取原回执，不重复 hold。没有先返回 ID 再异步预留，也不复制账本、鉴权或迁移。main/YAML/环境变量尚无消费者与密钥接线，不能宣称公开后台功能已启用。

- 每次 Admit/Read/Cancel 解析当前 owner/创建 Key 和模型权限；同用户其他 Key 不能仅凭 ID 读取或取消。鉴权使用事务前本轮快照，不保证已撤销瞬间中断正在受理的事务；后续派发必须再次授权。输入先于可信回调制作副本，JSON 正文最多 64 KiB，拒绝重复字段/过深/非 UTF-8；会话标识最多 256 字节。JSON 对象键排序并消除空白用于幂等，保留数字字面量差别，不把所有数值语义强行等同。
- 幂等键为 1–128 字节可打印非空 ASCII，按 user/创建 Key 作用域唯一；同键不同请求/操作/模型拒绝。只存域分离 HMAC-SHA256，不存原键或明文输入。独立随机 32 字节 DigestKey 必须跨重启/内容密钥轮换保持不变；首次成功受理在同一事务保存其非秘密指纹，后续密钥不符时 fail closed，不能悄悄新建命名空间、重复预留或手删 `generation.intent_digest_version`。当前没有该密钥的轮换/历史迁移工具，即使清理全部内容也不自动重置标记。
- 使用与输出记录相同的独立 AES-256-GCM 密钥环规则，随机 nonce，认证 ID/owner/Key、幂等摘要、请求摘要/摘要版本、操作/模型、billing ID 和创建/截止/过期时间；缺钥/篡改不返回明文，无默认密钥或明文回落。内容密钥与 DigestKey 不得相同，分别与数据库分离备份。可更换 active 内容密钥并保留旧钥读取原回执；同一密钥累计加密达到 2^32 条/段前必须轮换，无自动 KMS 或重加密。
- QueueTTL 明确为 1 秒–5 分钟，且不得超过原 strict reservation 实际租约；Retention 为 1 分钟–7 天且不短于 QueueTTL。TTL 从受理前创建起算，不是长后台执行租约。每条密文最多 68 KiB，实例记录数 2–4096、owner 至少 1 且小于实例；owner 字节限额至少 68 KiB、实例总密文至多 64 MiB。所有尚保留的终态/到期行继续占记录和字节容量，满额原子拒绝；这是逻辑限额，不是 SQLite/WAL/索引/备份物理配额或安全擦除保证。
- 意图只为 `queued/cancelled/expired/uncertain`；queued 不授权执行，也不交给现有 Runner。原 StartAttempt 在账本事务中拒绝任何意图关联 reservation，不能把回执当成 PreDeduct 开始上游调用。Read 仅返回重新授权并认证解密的输入与回执，不授予重发/恢复权限。完整派发仍缺领取/续租/fencing、价格/能力恢复、最大执行时间和上游不确定策略。
- Cancel 先认证密文，再在同一事务证明原 reservation 属于该 owner/Key/模型、仍 reserved、没有任何 attempt/provider 占用，才调用原 finish 以零费用释放双 hold 并改意图状态。异常执行证据只改 uncertain/账本 pending，不重发、不退款、不清零 provider 占用。截止时间到期后 `RecoverGenerationIntents` 每批最多 200 条，不读取正文/不要求密钥/不返回可执行工作；原 `RecoverExpired` 先走此路径并排除所有意图关联账本，避免普通恢复误释放。
- `CleanupGenerationIntents` 每批最多 200 条，只清到期 cancelled/expired 且财务 settled/零费用/同 owner/Key/无 attempt 的内容行；uncertain 永不自动清除，可能持续占容量，须后续明确审计处置。清理不删除 reservation/balances/审计事实；过期保留回执不能创建新 hold，清理后不提供无限期幂等保证。保留期结束、清理和重用键的产品规则仍须公开 API 批次定义。

真实 SQLite 回归覆盖双预算并发、幂等丢确认/并发重试、插入/取消失败的跨表回滚、当前权限收紧、容量、保留期和密钥/元数据篡改。真实子进程分别在提交前强杀（无意图/预留）和提交成功后强杀（重启可解密读取、真实期限到期只回收未执行预算），原数据库/WAL 不包含输入或原始幂等键明文。新迁移为统一 v9，仅追加结构，不伪造历史任务；v8 及更老二进制拒绝打开，恢复旧一致备份的规则不变。本批未操作生产库，不扩展为实际旧库报错/回滚专项。
