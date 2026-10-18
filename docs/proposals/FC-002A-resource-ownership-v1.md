# FC-002-A：后台资源分类与终态删除的最小补充

状态：**用户已于 2026-10-18 明确批准，随 A03-02 实施；尚未验收**。

批准记录：用户对“在现有 background/v1 候选包新增 Resources 端口，提供当前身份校验下的
Kind 查询，以及仅终态可执行、保留幂等墓碑的 Delete（支持 revision 条件）；
原十四组冻结入口不变，批准后继续 A03-02 → A03-03”回答“批准最小补充，继续原定顺序”。

本补充不重开 [FC-001](FC-001-foundation-contracts-v1.md) 的冻结入口，
不扩大 [FC-002](FC-002-native-background-v1.md) 的 disabled、自包含输入范围。
A03-01 已经 PR #20 合并为 `master/c1b8264`，精确验收记录见
[FC-002 实施记录](../history/implementation-fc002.md)。

## 缺口与不采用的替代方案

FC-002 要求 GET/DELETE 按资源种类走唯一权威路径，并要求后台终态删除不能复活任务或删除
unknown 保护。然而已批准的 background.Service 只有 Submit/Read/Cancel/Events：

- Read 的 ErrNotFound 同时覆盖不存在与不可访问，不能把它解释成“这是 A02 前台资源”。
- Cancel 对已有终态返回原终态，不表示删除临时内容；不能把 DELETE 偷换为 Cancel。
- 不允许 HTTP/L3 直接读取 ORM、先后探测多张表、另建鉴权或把 A02 历史表变成 runnable job。

因此在同一个候选包添加用途明确的小资源端口，而不是改变上述四个方法的含义。

## 六项边界

| 项目 | 精确范围 |
|---|---|
| 所属层级/目录 | L1 `foundation/contracts/background/v1`、generation owner、store/model；L3 responses 消费，main 注入 |
| 依赖接口 | 原 identity/execution/state v1、FC-002 background.Service；原十四组冻结入口与 L0 不变 |
| 地基变化 | 只为原计划资源分流/终态删除补齐两个具名方法；不用任意 SQL/事务回调 |
| 契约变化 | 同一 background/v1 候选包新增 ResourceKind、两个常量、Resources；不增加第三个公开包，不给 Service 强加新方法 |
| 验证 | 分类当前身份/owner/创建 Key/模型隔离、唯一资源权威、活跃/unknown 删除冲突、revision CAS、删除重试不可重发、A02 回归；随 A03-02 集中完整验证及精确头/合并后 CI |
| 并行影响 | 两组 FC-002 接口仍候选，待 A03-03 全消费者验收才冻结；共享入口、迁移和 main 仍由本批统一协调 |

## 完整新增签名

```go
type ResourceKind string

const (
    ForegroundResource ResourceKind = "foreground"
    BackgroundResource ResourceKind = "background"
)

type Resources interface {
    Kind(context.Context, string) (ResourceKind, error)
    Delete(context.Context, string, *uint64) error
}
```

Kind 不是授权凭据。它从可信 context 和同一 Store 读事务中的当前身份重新校验
owner/创建 Key/模型；只返回已正向识别且可访问的 Responses 资源种类。
不存在、已删除、过期或跨 owner/Key 统一 ErrNotFound，不附带有用分类。
前台历史/回执只是分类依据，随后仍由 A02 原权威负责实际读取/修改，不能从分类推导正文可读。
后台读取/取消/事件进入 background.Service；后台 DELETE 进入 Resources.Delete。
不能在 ErrNotFound 后按查找顺序探测另一资源权威，也不能将该方法作为管理员内容查询入口。

Delete 只操作本地已知终态后台临时内容，不发起取消或生成，不等于提供商侧删除已完成。
它在原 Store 写事务内重新校验当前权限、资源种类、终态与可选的 expected revision：
未终态/uncertain/revision 不符返回 ErrConflict；非后台/不存在/不可访问返回 ErrNotFound。
终态删除清除加密输入/文档/绑定/事件并释放内容容量；保存非内容的 owner、幂等摘要与终态墓碑
直到原元数据保留期届满。随后 GET 不可读，重复同键受理不能重新执行，只返回结果不可恢复错误。
需要私有 deleted 事实及原 revision 条件，属于本补充明确批准的持久语义，不复用 A02 历史删除标记。
公开 ID、ResourceKind 或预期 revision 均不能取代当前身份。

## 验收与回退

A03-01 的 16 组公开包（其中两个为候选）与 L0 快照只增加上述已批准符号；
原十四组冻结包及 L0 继续逐项对照，不放宽任意 v2、私有实现类型或 untyped 逃逸。
此补充的实现、失败/修复、最终来源与 CI 记录到 FC-002 原实施记录，不另建活动计划。
禁用新接收或回退时保留 unknown/幂等墓碑与密钥，不能靠删除后台表或恢复旧备份消除已经受理的事实。
