# Foundation API v1：实施清单与迁移边界

状态：FC-001 已由用户批准，FND-01 实施中；本清单是 v1 候选，不是 frozen 发布。
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

`Executor.Execute(ctx, output, invocation)` 继续调用原 Gateway.ExecuteTo 一次。
Invocation 提供 operation/body/session/output ID/受限 target，不接受用户/Key/价格或余额作为授权。
可信身份由原认证边界产生，Authority.Refresh 在每次 Responses 交付时重新读取；
generation 在派发前仍用原 resolver 校验当前主体，新的投影值不替代这些检查。
授权、RPM/并发准入、原目标约束、尝试、预算/结算与释放继续由原协调链持有。

Output 的 Commit/Write/Flush/State 与可选写 deadline 保留原寿命规则：
提交状态只能前进；短写/读错/取消不伪造完整协议成功；attempt 不关闭 writer。
L2 HTTP 工厂在组装时注入 Responses 的既有 HTTP 交付入口，功能不直接构造 core.HTTPOutput。
HTTP/业务包的完全分离仍在 FND-04，不能把本次移除 core 依赖宣称为全部分层完成。

Outcome 显式映射 phase、原始和已公开 HTTP 状态、committed、protocolComplete、
termination、nil/已知 usage、错误码原始 JSON、耗时/字节数和每次尝试。
NativeCompletion 保存原生终态/ID，Target 保存原目标连续性，BillingID/PriceVersion/BillingState
只投影原账本/价格关联，不授权发起新执行或修改预算。公开 Error.Code 是受限 JSON 表示，
不是任意 Go 值。源 usage、策略/模型切片、错误、attempt 与元数据不与消费者共享可变引用。

`foundation/runtime.Executor` 是 L1 私有转换实现，不能成为 L3 导入入口。
其 ExecuteClaimed 暂时接受旧 model.GenerationClaim，仅供原 durable runtime 组装使用，
FND-03 将迁入状态契约；它没有进入 execution v1 的公开 Executor 接口。

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
| gateway 的 protocol.Request、操作 switch、Responses Observation，及同包 Gin handler | FND-02 |
| generation 的 protocol 意图校验和 model claim/记录、access 解析实现 | FND-02/FND-03 |
| responses 的 generation.Archive、model 快照/回执、secrets 与原生 item 解析 | FND-03/FND-04 |
| management.ConfigurationStore 的 store 输入 DTO；server API 的直接 store/billing 调用 | FND-03/FND-04 |
| 全生产包四层映射、公开签名递归可见性、独立新增操作样例与零例外冻结 | FND-05 |

这些是已知迁移来源，不是新增模块可复制的接入方式；本清单不降低旧检查或最终 CI 门槛。
