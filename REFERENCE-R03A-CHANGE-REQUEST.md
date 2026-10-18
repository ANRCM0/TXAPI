# R03a 具体变更请求：有界 WebSocket 轮次与运行代次

状态：R03a 范围及 2026-10-19 修订签名均已获用户批准，正在实施。R00 与 R01 已分别完成合并验收。

修订内容：Connection.Write 返回已写入的字节数和 error；Round 增加正文已经要求的协议处理回调。两者分别用于部分发送事实和由协议所有者声明终态。下述签名已接入候选生产包；验收以最终提交的 CI 和真实 transport 回归为准，尚未冻结。

## 1. 目标

为一个真实的 Responses WebSocket 消费者提供可验证的基础能力：物理连接有界、每轮生成独立、读写取消和关闭归属明确、部分发送保留未知水位，并能在配置代次切换时让旧轮次排空后再关闭。

本批不定义 OpenAI 事件、thread/session 语义、Provider 原生续接、用户权限、账务或管理路由；这些由后续 L2/L3 批次消费本批机械端口。

## 2. 六项边界

| 项目 | 本批范围 |
|---|---|
| 层级/目录 | L0 新增 `internal/core/websocket` 机械轮次；L1 execution/v2 轮次事实和 resource lease；L2 复用现有 `internal/transport/websocket` 作为连接适配；测试与架构登记 |
| 依赖版本 | execution/v1/v2、lifecycle/v1、现有 websocket transport；v1 与 R01 v2 保持兼容 |
| 地基触碰 | 新增 L0 端口与 execution/v2 类型；不修改现有 L0 HTTP 签名，不把 transport 包导入 core |
| 公开契约 | 第 3 节的 `Round`, `RoundResult`, `Connection`, `RoundExecutor`, `Lease`；具体版本仍为候选，验收后才冻结 |
| 验证 | 真实 transport + 假连接/假协议的连接、读写、取消、部分写、重复关闭、并发轮次、旧代次排空和泄漏回归 |
| 非目标 | OpenAI WS 路由、官方事件兼容、OAuth、strict 收费、持久后台任务、跨进程续接、插件 Runtime |

## 3. 候选契约

追加到 `internal/foundation/contracts/execution/v2`，保持 `v1.Output` 与 v1 身份/账务边界：

```go
type ConnectionID string
type RoundID uint64

type Frame struct {
    Type uint8
    Data []byte
}

type Connection interface {
    ID() ConnectionID
    Read(context.Context) (Frame, error)
    Write(context.Context, Frame) (int, error)
    Close() error
}

type RoundHandler func(context.Context, Frame) ([]Frame, bool, error)

type Round struct {
    Connection Connection
    ID RoundID
    Deadline time.Time
    Handle RoundHandler
}

type RoundResult struct {
    Dispatch v1.Phase
    FramesRead uint64
    FramesWritten uint64
    BytesWritten uint64
    Committed bool
    ProtocolComplete bool
    Termination v1.Termination
}

type RoundExecutor interface {
    ExecuteRound(context.Context, Round) RoundResult
}

type Lease interface {
    Revision() string
    Release() error
}
```

`Connection` 是已通过外层鉴权和 capacity admission 的连接端口；core 不解析 headers、URL、身份或客户端 JSON。`RoundExecutor` 只拥有一个轮次的机械读写和取消；它不创建第二个连接池、账本、重试器或持久队列。

## 4. 机械行为

- 一个 `Round` 只允许一个 reader 和一个串行 writer；writer 可由同一轮的协议适配器调用，但 core 保证写入互斥和 deadline。
- `Read`/`Write` 的 context 取消立即结束当前轮次；连接关闭由 connection owner 决定，轮次结束不能自动关闭仍被其他轮次使用的物理连接。
- 首次成功读到请求帧后记录 `Dispatching`；输出帧写入前的失败仍可能已经派发，不能重试或声称未执行。
- 每次成功写入累计 frame/byte 事实；短写、写超时和连接关闭保留 `Committed`/已写字节，不补终态、不自动重发。
- `Committed` 保守表示写调用可能已经交付，写失败不重置为 false；`BytesWritten` 只表示已确认载荷字节的下界。真实 transport 的底层写失败无法报告部分载荷计数时，保留 `Committed=true` 和已有确认字节，不把零下界解释成没有发送。无 error 的短写同样失败，不计完整帧。
- 只有协议适配器通过显式 callback 返回 `ProtocolComplete`；机械 core 不从 JSON、文本或 close code 猜终态。
- `Close` 幂等，释放 round lease 后才允许旧代次退出；关闭超时不关闭共享 gateway/store。
- 连接容量、消息大小、读/写/握手期限继续由现有 transport Manager 限制；R03a 不放宽这些上限。

## 5. 代次保活

候选 L1 `Lease` 实现以不可变 revision 持有当前运行资源：

```text
Prepare(revision) -> Validate -> Publish current
                       failure -> discard candidate; current remains
old current -> retiring -> last lease release -> close
```

本批先用一个内存 `RuntimeLeaseSet` 验证 acquire/release/retire 的并发和幂等，再由真实 WS 轮次持有 lease。它不替换已有 `management.Configuration` 的保存/发布状态；`applied revision` 成功后才把新资源交给新连接。

- 新连接 acquire current；每个轮次复制 revision 标识。
- 发布 N+1 不影响 N 的已派发轮次；新轮次使用 N+1。
- retiring 代次有上限和 drain deadline；达到上限时拒绝新发布或新连接，不能强杀正在收尾的账务/输出。
- 关闭顺序是：停止新握手 -> 停止新轮次 -> 等待已派发轮次 -> 释放 lease -> 关闭 transport/runtime；超时返回错误并保留依赖存活。

当前批次的交付边界：`RuntimeLeaseSet` 实现内存 revision/ref 模型，真实 WS 测试消费者持有和释放租约；它尚未持有可关闭资源，也未接入配置 applied revision、资源准备/失败清理及 drain deadline。这些 R02 产品接线要求仍为后续 R03b 的进入/验收条件，不能以本批引用计数测试替代。

## 6. 兼容与回退

- 现有 HTTP executor、Responses HTTP/SSE、background runtime 和 `internal/transport/websocket` v1 行为不变。
- 未接入 R03a 的连接继续走现有 transport；没有生产 WS 路由时，新 round executor 不被注册，不能凭接口存在宣称 WS 已支持。
- 回退只需停止注册 R03a consumer、排空连接并删除未使用的候选 revision；不回滚 schema、账本或 v1/v2 事实。
- L2/L3 只能通过公开端口消费 `RoundResult`，不能把客户端 thread/session 或 Provider handle写入 L0。跨连接续接另立契约。

## 7. 验收门槛

- 真实 `internal/transport/websocket` 连接可接入一个假协议 round consumer；慢读、满队列、超时、取消、关闭都在配置上界内。
- 同一物理连接的两个轮次不能并发写；不同连接可并行；重复 Close/Release 不泄漏计数或重复关闭。
- partial write 后 `BytesWritten`、Committed 和 Dispatching 事实保留；没有自动重试或伪造 ProtocolComplete。
- N/N+1 发布期间旧轮次完成、新轮次使用新 revision；失败发布保留旧 current；退休代次最终归零。
- 外层 owner/Key 每个新轮次重新授权的接入点有集成测试；连接握手通过不等于未来轮次永久授权。
- architecture layer/visibility/signature 检查、完整 Go/race、主程序/WS 客户端回归和容器恢复通过后，才冻结新增契约。

## 8. 需要确认的范围

请确认按第 3 至 7 节实施。此次确认只涵盖机械 WebSocket 轮次和运行代次；不包含生产 OpenAI WS 路由、官方客户端兼容、OAuth 或收费 WS。
