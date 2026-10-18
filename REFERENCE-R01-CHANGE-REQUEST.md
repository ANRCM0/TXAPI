# R01 具体变更请求：执行事实与单次协议交付

状态：2026-10-18 用户明确批准“R01 具体变更”，现按下述范围实施。总目标已获准推进；本文件保留新增契约、迁移与验收边界。批准不等于实现通过或接口冻结。

## 1. 本批结果与不足的现有扩展点

保留原 execution/v1 和 L0 core 签名，追加 `internal/foundation/contracts/execution/v2`。
新增端口表达三件事：原始编码数据只交付一次；缺失/部分/完整上游用量可区分；L3 消费者能读取每次 attempt 的发送、协议、交付事实。

v1 的 Frame 只有 `Data/Usage/Complete`；Usage 是三个非可选 int。nil 可以表达没有完整用量，不能保留部分字段或说明该零值是否由上游报告。Observer 只有最终快照，不能表达同一解码批次的原始数据和事实来源。把这些信息塞入响应头、日志或厂商 JSON 的通用 map 都无法成为稳定的上层接口。

L0 已经提供保守的 Phase、ProtocolComplete、Committed、BytesWritten，足够完成本批。新增 L1 decoder bridge 将 v2 信封投影到原 core.Decoder；不增加网络派发器，不修改 L0 或其 frozen 签名。WS 的机械执行仍是后续 R03a 单独变更。

## 2. 六项边界

| 项目 | 本批范围 |
|---|---|
| 层级/目录 | L1 execution/v2、gateway 桥；L2 protocol/responses 与注册适配；L3 独立 facts 接入样例；architecture 登记/签名；相关测试 |
| 依赖版本 | execution/v1、identity/v1 和现有公开 HTTP 输出；v1 继续 frozen，v2 本批为候选 |
| 地基触碰 | 新增 v2 公开入口、L1 桥、L2 decoder 的可选能力；L0 不改；不变更原 v1 签名 |
| 接口变化 | 第 3 节列出的值类型与 Decoder/StreamDecoder/Executor；通过已注册 decoder 的显式类型能力接入 |
| 验证 | JSON/SSE、部分用量、终态/交付失败、attempt 隔离与独立 L3 样例；集中完整本地与最新基线 CI |
| 并行影响 | protocol 注册、gateway 公共桥、架构登记由本批单点修改；无 schema/迁移、API 路由、支付、OAuth 或 WS 产品变更 |

## 3. 候选公开签名

以下签名为待实施的完整最小范围；v2 只引用标准库和已公开的 v1 值契约，不引用 core、gateway、model、store、协议实现或 ORM。

```go
package execution // import .../foundation/contracts/execution/v2

import (
    "context"
    v1 "minigateway/internal/foundation/contracts/execution/v1"
)

const Version = "2"

type Completeness string
const (
    Unknown Completeness = "unknown"
    Partial Completeness = "partial"
    Complete Completeness = "complete"
)

type UsageSource string
const (
    SourceUnknown UsageSource = "unknown"
    ProviderReported UsageSource = "provider_reported"
)

type TokenCounts struct {
    InputTokens *int64
    OutputTokens *int64
    TotalTokens *int64
}

type UsageObservation struct {
    Counts TokenCounts
    Completeness Completeness
    Source UsageSource
    Profile string
}

type Envelope struct {
    Data []byte
    Usage *UsageObservation
    Native *v1.NativeCompletion
    ProtocolTerminal bool
    Failed bool
}

type Decoder interface {
    DecodeEnvelope([]byte) (Envelope, error)
    NewEnvelopeStream() StreamDecoder
}

type StreamDecoder interface {
    DecodeEnvelopes([]byte) ([]Envelope, error)
    EndEnvelopes() ([]Envelope, error)
}

type AttemptFacts struct {
    Number int
    Dispatch v1.Phase
    ProtocolComplete bool
    DeliveryCommitted bool
    BytesWritten int64
    Usage UsageObservation
    Native v1.NativeCompletion
}

type Outcome struct {
    Legacy v1.Outcome
    Attempts []AttemptFacts
}

type Executor interface {
    Execute(context.Context, v1.Output, v1.Invocation) Outcome
}
```

不引入 callback、持久事件仓库或任意 extension map。构造/校验/复制由具体实现负责；若实现需要额外公共方法或改变上述权限/寿命，先更新本请求再确认，不通过快照更新偷偷扩大契约。

## 4. 接入与所有权

1. 已注册 v1 Adapter 仍只负责 Prepare/Decode。原生 Responses 的已注册 decoder 同时实现 v1.Decoder 和 v2.Decoder；gateway 仅在它显式实现 v2.Decoder 时选择新桥。不是对所有 decoder 反射猜测，也不隐式扩展操作能力。
2. 每次 Prepare 创建独立 decoder。每次执行只调用 v1 或 v2 中一套方法；v2 失败直接按协议失败处理，绝不再调用 v1 重解码、重发或输出第二份载荷。
3. Envelope.Data 是该次 decoder 产出的已有客户端表示：允许沿用当前受控公开 response ID 改写。标准事实不会再次编码成另一份响应。它不是原始上游字节的完整归档承诺。
4. L1 bridge 消费信封、复制事实、将 Data 交给原 core.Frame；只保留当前 attempt 的最新用量/终态，不保存完整响应副本或无限事件列表。
5. Phase、ProtocolComplete、Committed、BytesWritten 最终取宿主真实结果。codec 只能报告协议事实，不能伪造发送水位、输出成功、授权或结算状态。
6. 新的 gateway v2 门面复用原 executeTo、可信身份、准入、预算、路由、收尾；不再次执行请求。原 v1 门面继续返回原 Outcome。
7. 一个新 L3 facts 样例只导入登记的 execution/v2、identity/v1 等公开契约，通过注入 Executor 运行。测试用真实 gateway 与假上游执行 JSON/SSE，验证新消费者不依赖 L1 实现；样例本身不注册生产 HTTP 路由。

## 5. 用量与终态规则

- 本批只支持**累计快照**，不接收增量计数，不添加缓存/推理/图片计量维度。后续维度属于 R05a。
- `Unknown`：没有可用的 provider token 事实；Counts 全 nil，SourceUnknown，Profile 空。不能把 legacy 估算伪装成 provider_reported。
- `Partial`：至少有一个已知 token 字段，但缺少完整三项；已知值非负，Source=ProviderReported，Profile 固定为该 codec 声明的计量档案。
- `Complete`：三项明确存在、非负、求和无溢出且 input+output=total。显式零必须保留；缺字段不可补零。
- 值不合规、重复冲突字段、用量倒退/同一终态冲突按 decoder 错误处理，不用“unknown”隐藏错误。JSON/SSE 的既有错误与未知字段规则继续保持。
- 投影到 v1/core.Usage 时，仅完整、ProviderReported、计数能安全表示为 int 的快照生成非 nil usage；其余为 nil。v1 消费者继续走原本缺用量处理，strict 的允许范围不扩大。
- 新门面遇到未实现 v2 的旧 decoder 时，Facts.Usage 为 Unknown；Legacy.Result 保留原 v1 用量，不能凭旧非 nil usage 声明新档案已经得到证明。
- ProtocolTerminal 只代表 codec 看到匹配终态，最终 ProtocolComplete 还依赖 core 正常验证 End。上游终态可以与交付失败同时成立；两者不相互覆盖。
- 终态缺用量仍可交付合法响应；用量未知与协议成功分别记录。账务继续按现有模式处理，缺费用不自动按零结算。
- 本批没有新的持久格式；AttemptFacts 只在本轮结果内存在，序号来自原 attempt 序列，不使用客户端 request_id 当作去重/隔离键。

## 6. 兼容、迁移与回退

v1 的所有冻结签名、HTTP 路径/方法/错误表达、schema v15、密文与账本格式保持。原 Chat/embeddings 继续使用原 decoder；Responses 默认声明 v2 能力并走同一 HTTP/SSE 输出链，证明该能力有真实消费者。

L2 Responses parser 抽出共享的私有解析结果供 v1/v2 使用，避免 gateway 解析厂商 JSON。v1 ParseResponse 的现有签名不变；新部分用量仅通过 v2 暴露，原接口仍返回 nil。现有回归保留，涉及判错差异时必须单列并说明，不能无声扩大拒绝范围。

回退时移除 Responses 的 v2 能力声明，恢复已验收 v1 decoder 桥；没有数据库回滚或旧账重算。新的 v2 消费者若仍使用候选接口，需同步撤回或继续以 Unknown 明确降级，不能伪造完整事实。

## 7. 验收清单

- JSON 与 SSE 同一上游事件只交付一次，response ID 改写与未知字段保留无回归。
- 完整用量、显式全零、缺一项、全部缺失、负数、不一致、溢出、累计倒退均有明确结果。
- 上游合法终态+下游部分写/Flush 失败同时保留协议与交付事实，不改目标重试。
- 预检查/准入拒绝零 attempt；实际 attempt 与事实一一对应；重试之间 decoder/usage/native 不串扰。
- 取消、超时和注入 panic 保持保守发送水位与既有 hold 规则，不虚构未派发。
- 原 v1 HTTP/前台/后台消费者、架构/签名快照、L3 独立注入样例通过。
- 批次稳定后集中 build/vet/test、适用主程序/前端回归；最终匹配头和最新基线完整 CI，合并后再核验。

冻结 v2 需另外满足真实消费者、扩展说明、限制和 DoD。创建包或本地单元测试通过不直接标为 frozen。

## 8. 需要确认的具体范围

确认后实施第 3 节的追加契约、第 4 至 6 节接入与兼容规则，并完成第 7 节验收。
本次确认不包含 L0 新 API、原生 WS/OAuth 产品、收费范围扩展、数据库迁移或外部 Runtime；这些继续按各批提供具体变更请求。
