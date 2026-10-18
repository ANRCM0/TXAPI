# FC-001 四层地基实施与验证记录

## 批准与基线

2026-10-18 用户明确批准：“好批准实施提案。请你按照顺序完成提案”。
批准来源为本任务用户消息，覆盖 [FC-001](../proposals/FC-001-foundation-contracts-v1.md)
的 FND-01 至 FND-05 顺序、四层/公开契约、迁移和验收约束；范围内不重复索要许可。
原本地 FND-00 文档提交 `c4ba10c` 保留；开工 fresh-fetched `origin/master`
为 `4312f774e48ad8f5066f1e238589666549b3a533`，工作树原为干净。

活动状态以 [PLAN](../PLAN.md#地基符合性收口fc-001) 为准，版本/接口/剩余债务见
[Foundation API v1](../reference/foundation-api-v1.md)。本文件记录实际来源与结果，
不因批准提案或已有 S6/A02 CI 就声称新地基验收通过。

## FND-01：公开执行、身份与输出边界

本批负责 L1 execution/identity 契约、原 core/Gateway 转换桥与版本化 HTTP 输出门面、
L2 输出工厂、Responses/generation 实际消费者及 main 组装；不改变 L0 行为、外部
HTTP/财务/持久语义。公开契约不导入实现、不通过别名泄漏类型；旧实现向新公开值类型
的迁移别名保留原消费者编译，禁止新的功能依赖这些旧入口。

实际执行 Windows/PowerShell Go 命令均显式 `GOARCH=amd64`。仅使用临时 SQLite、
loopback 假上游、测试凭据与现有本地浏览器工具；不操作真实提供商或生产数据库。

### 实施期间的编译检查

- 首次 `go build ./...` 发现迁移 Policy 后 access 的无用 strings import，修复后构建通过。
- 首次 `go test -run '^$' ./...` 仅编译测试，发现旧 cmd GenerationIntent 参数与一个原生状态字段引用；更新消费者后再次全仓编译通过。此命令没有执行行为测试。
- 提前定向风险验证中，foundation/identity、foundation/runtime、architecture、responses 通过；新加旧归档回归的 import 名与现有 execution 测试 helper 冲突，generation 编译失败，已改为 execv1。该检查不是完整通过证据，失败包与最终全量在修复后继续复验。
- 已编写结果映射/复制、缺身份/短写/取消、当前主体绑定/权限/异常、契约依赖负面样例和旧加密归档格式读取回归；最终集中结果在源码稳定后记录。

### 最终本地集中验证

最终冻结来源为 306 个非 Markdown 路径，SHA-256
`ca4094b8b9fa8f69eea922f9ab44bd434e7588bfa1574e7e96ec3a12733c234c`，
集中验证前后相同；后续提交前仅更新说明文档。

| 实际命令 / 环境 | 结果 |
|---|---|
| `GOARCH=amd64 go test -json -count=1 ./...` | 1037 项测试/子用例、26 包 PASS；5 个预期子进程 helper SKIP，无失败 |
| `go build ./...`; `go vet ./...`（同进程 GOARCH=amd64） | PASS |
| `go mod tidy`; `git diff --exit-code -- go.mod go.sum` | PASS，无依赖漂移 |
| 仅变更 Go 的 normalized-LF gofmt 比较 | 44 文件 PASS；没有格式化其他 Go 文件 |
| `node --check` app/api；`node --test web/assets/api.test.mjs` | 语法 PASS、10 项 API 消费行为 PASS |
| `GOARCH=amd64 python tests/smoke/local.py --go go` | 真实 main/auth/Chat/SSE/控制台、strict 拒绝、强杀 pending/审计豁免、凭据加密/轮换拒绝恢复、durable restart 与正常退出 PASS |
| `GOARCH=amd64 python tests/smoke/compatibility.py --go go` | 已安装 Playwright/Chromium，真实临时二进制/SQLite/API/UI、配置发布、模型/Key 限制、角色隔离、桌面/移动与清理 PASS |

此前定向风险回归的 generation 编译冲突已修复，generation 单包完整复验 PASS；
最终上述全量覆盖修复后的源码。Windows 无本地 race/容器实跑声明；匹配最终头的
自动 push/PR Linux race/主程序冒烟/容器交付及合并后 master 仍须实际通过。
本地通过尚不能勾选 FND-01 或把候选 v1 标记 frozen。
后续 FND-02 注册与协议隔离、FND-03 状态/配置/生命周期、FND-04 功能迁移及 FND-05
最终 DoD 仍独立验收；完整 S5、A03/A04 产品与真实客户端能力未扩大。

### FND-01 最终 CI 与合并验收

最终功能头 `20383f9c8b067d3c0d3ad8136dce6b273bd9dad4` 基于原集成基线 `4312f77`，
包含原提案提交 c4ba10c 与完整 FND-01 实施。自动 push
[39097021786](https://github.com/ANRCM0/TXAPI/actions/runs/39097021786) 与 PR
[39097076557](https://github.com/ANRCM0/TXAPI/actions/runs/39097076557) 的 check/delivery 全部 SUCCESS。
核对 clean worktree、未漂移 master、精确 head 与四个 green jobs 后，
`gh pr merge 14 --merge --match-head-commit 20383f9c8b067d3c0d3ad8136dce6b273bd9dad4`
合并 [PR #14](https://github.com/ANRCM0/TXAPI/pull/14) 为 `dcdc56be9308221c297e12d15947db4437a437a0`；
`git diff --exit-code 20383f9 origin/master` 证明合并树与已验证头一致。
合并后 master 自动 [39097330141](https://github.com/ANRCM0/TXAPI/actions/runs/39097330141)
的 check/delivery 也全部 SUCCESS，含 Linux 全量 race、契约/依赖、构建/vet/格式、主程序冒烟和容器交付。
这关闭 FND-01，本文件前文的 CI 待验收描述是该本地检查时点的记录；不关闭后续四批或冻结整个 v1。

## FND-02：注册操作、协议会话与 HTTP 隔离

从已验收 `master/dcdc56b` 新建 `feat/fnd-02-registered-operations`，用户批准的六项任务边界见 PLAN。
本批 gateway、billing、generation 生产代码移除 protocol/Gin 依赖，protocol 移除 core 依赖；
中立 Request/Input/Decoder/Frame/Observation 与 usage 类型通过显式桥调用原 core 和账务链。
操作和协议注册由 main 注入，构造时复制并检查版本、重复、缺回调/typed nil；没有隐式 Chat 回落。
内建协议工厂保留旧 adaptor 的纯转换能力，原生 Responses 观察与估算留在 L2，strict 许可不随注册扩大。
HTTP Chat/embeddings/models 由 compat/generationhttp 消费公开 Executor/Catalog，旧 HTTP 测试名委托真实新 handler；
仅原直接 Relay 测试 harness 保留其内部组装，用于原故障/准入断言，并非生产入口或依赖豁免。

编译期间发现并修复旧 decoder/usage 签名、已有 newTestGateway helper 名冲突，以及最后一个 probe 的 usage 字段；
`go test -run '^$' ./...` 修复后通过，仅证明测试编译。提前风险检查首次发现 server 的 L2 文本投影 import 未在
依赖表登记；登记同层 public helper 后，architecture、真实 compat handler 和注册矩阵定向复验全部 PASS。
新增注册操作 test.summarize 通过真实 loopback HTTP 并发执行，保留原生载荷，12 次准备使用 12 个独立 decoder；
未知/缺能力/不匹配传输/strict 扩张、重复/错误版本/typed nil、原登记切片改动和 parser 缓冲变更均有实际拒绝/隔离回归。

本批首轮集中验证的源码来源为 318 个非 Markdown 路径，SHA-256
`0754eafcac8b119b2d0790cf5d0265b0196a1ee1b5a4ab4ef53dc2d2abf65fd0`。
Windows/PowerShell `GOARCH=amd64` 正在执行全仓测试、build/vet/mod tidy、前端 API、main 恢复冒烟及真实控制台联调；
在实际完成前不记 PASS，Linux race 与容器交付仍须匹配最终头的自动 CI。

### 集中修复与来源分界

首轮 0754eaf 源码的生产 main/控制台联调通过，但完整 Go 检查发现最后一次测试 harness 委托后遗留的 meta import，
以及 TestS3KeyLimitsApplyWithoutBlockingOtherOwners 的真实 HTTP RPM 重复计数；vet 同样指出该无用 import。
它不是全量通过记录。修复后仅 HTTP 组装执行器接管绑定 Key 的原子一次性 middleware admission，普通执行仍新建上下文；
补入 20 个并发消费者仅一次接管/TPM 收尾，以及同父 context 多次 HTTP/普通执行不能跳过限流的实际回归。
先执行 access/gateway/server 的失败项与相关风险复验 PASS，再重新执行相关完整检查。

修复后来源 319 路径 / `b03ee8bc5137ef37856a13fd8605a34055f1915311424cdc589d6c3879a8312a`
的全仓 Go 为 1056 测试/子用例、27 包 PASS、5 个预期 helper SKIP；build/vet/tidy、10 前端测试、main/控制台联调 PASS。
随后边界审阅发现 gateway 还在解析 Chat messages 获取亲和种子，继续迁移为中立 AffinityText 与 SessionKey，
新增首条 user/多模态投影回归，保留旧摘要算法。该段旧通过不推断为之后源码通过。

最终候选来源为 321 路径 / `da6a3352cabcdc3d95457c2c7e0faaab6faf941407eab9850655ae59912c6a32`；
相应完整验证结果在运行完成后单独登记。没有修改 OpenAPI、schema/migration、依赖、workflow 或真实运行数据。

### FND-02 最终本地验证与性能对照

亲和迁移候选 da6a335 的全仓为 1057 项/27 包 PASS，5 helper SKIP；build/vet/tidy、10 项前端 API 与 main/真实控制台联调均 PASS。
最后构造审阅补齐旧 adaptor 工厂的 nil function/map/slice/channel 判定，避免包成非 nil wrapper 后绕过原 typed-nil 拒绝。
新增 nil function adaptor 实际构造回归并先定向 PASS；该变化不修改 public 签名/HTTP/编解码或有效 pointer adaptor 路径。
最终来源为 321 个非 Markdown 路径 / SHA-256 `22cb9df9cebef67a4bdb31186a567b329ef3ddbea5afc6fa51fb0d4be42340c5`。

| 实际命令 / Windows PowerShell、GOARCH=amd64 | 结果与来源 |
|---|---|
| `go test -json -count=1 ./...` | 最终 1058 项测试/子用例、27 包 PASS；5 预期 helper SKIP，无失败 |
| `go build ./...`; `go vet ./...` | 最终源码 PASS |
| `go mod tidy`; `git diff --exit-code -- go.mod go.sum` | da6a335 PASS，之后仅 nil 构造判定及回归，无依赖变化，最终 diff 再核对 |
| normalized-LF gofmt 比较，仅变更 Go | 最终 97 文件 PASS，不格式化其他 Go |
| core/protocol `go list -deps` 与 architecture 负面检查 | 最终全量包含 architecture PASS；生产依赖不含 Gin/ORM/gateway/store/billing/server |
| `node --check` app/api；`node --test web/assets/api.test.mjs` | da6a335 语法及 10 项 API PASS，最终无 web/HTTP 表示变更，复用此范围 |
| `python tests/smoke/local.py --go go` | da6a335 真实 main/auth/Chat/SSE/控制台、strict 拒绝、强杀/双 hold/审计、加密/轮换拒绝与恢复、durable 重启/退出 PASS；正常 pointer 生产组装路径不受之后 typed-nil 函数拒绝影响，复用此范围 |
| `python tests/smoke/compatibility.py --go go` | da6a335 临时二进制/SQLite/真实 API/UI、发布、模型/Key/角色隔离与桌面/移动 PASS；同上复用 |

Playwright 与 Chromium 使用已安装工具及前批相同 NODE_PATH/PLAYWRIGHT_EXECUTABLE_PATH，无新增依赖或浏览器安装。
所有回归使用临时 SQLite、loopback 假上游与测试凭据；没有真实上游/SDK/生产数据操作或新 strict 许可。
本地通过不替代最终头 Linux race/容器交付与合并后 master CI，当前不勾选 FND-02。

性能使用相同 Windows amd64 / Go 1.26.3 / i5-12500H，
`go test -run '^$' -bench 'BenchmarkExecutor/(same-json|same-sse)$' -benchtime=40x -count=1 ./internal/core`。
基线是 git archive 的精确 dcdc56b，当前是上述最终源码；均通过真实 loopback、固定 5ms 上游延迟。

| 场景 | dcdc56b ns/op / B/op / allocs | 最终源码 ns/op / B/op / allocs |
|---|---|---|
| same-json | 5723755 / 13416 / 113 | 5811055 / 15757 / 122 |
| same-sse | 5869045 / 96829 / 403 | 6134395 / 98095 / 463 |

明确值桥与缓冲所有权复制增加分配；该对照保留成本，不宣传零开销。样本仅 40 次、包含固定睡眠且集中检查并行，
时延不是生产吞吐/SLO 结论，CI 原有多场景性能报告继续保留。旧第一次当前基准不代替最终源码数值。
