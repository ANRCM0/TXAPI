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

### FND-02 最终 CI 与合并验收

功能头 `196be0eeec1c2282847df7579d9cb8bbdd8c636d` 的自动 push
[39099593057](https://github.com/ANRCM0/TXAPI/actions/runs/39099593057) 与 PR
[39099596254](https://github.com/ANRCM0/TXAPI/actions/runs/39099596254) 的 check/delivery 均 SUCCESS。
核对 clean worktree、未漂移的 master/dcdc56b、精确 head 和四个 green jobs 后，
`gh pr merge 15 --merge --match-head-commit 196be0eeec1c2282847df7579d9cb8bbdd8c636d`
将 [PR #15](https://github.com/ANRCM0/TXAPI/pull/15) 合并为
`e97e458bb52761bb3d8330d8b1383c5e2fa04b4b`；`git diff --exit-code 196be0e origin/master` 通过。
合并后自动 [39099900287](https://github.com/ANRCM0/TXAPI/actions/runs/39099900287) 的
check/delivery 也全部 SUCCESS，含 Linux 全量 race、契约/依赖、构建/vet/格式、主程序冒烟和容器交付。
该结果关闭 FND-02；上文 pending 描述是本地验证时点，不代表后续状态，也不冻结整个 v1。

## FND-03：状态、配置和生命周期端口

从已验收 master/e97e458 新建 feat/fnd-03-state-configuration-lifecycle；六项开工边界见 PLAN。
本批保持原 schema v12、加密关联数据、JSON 字段、CAS/fencing 与预算权威，通过显式值转换连接原事务。
实施和验证结果将在实际完成后登记；目前不记 PASS、不勾选 FND-03。


### FND-03 本地集中验证

最终非 Markdown 来源为 329 路径，SHA-256
`17e3b734b6913489cc295fa3364178943b0d92d7b1bd2bfb52b982814d7aa4d8`，
集中检查前后相同；之后仅修改说明文档。Windows/PowerShell，每条 Go 命令显式 GOARCH=amd64。

| 实际命令 / 环境 | 结果 |
|---|---|
| go test -json -count=1 ./... | 1064 项测试/子用例、28 包 PASS，5 预期 helper SKIP，无失败 |
| go build ./...；go vet ./...；go mod tidy 与 go.mod/go.sum diff | PASS，无依赖漂移 |
| normalized-LF gofmt 比较，仅变更 Go | 46 文件通过，不格式化无关源码 |
| node --check web/assets/app.mjs、api.mjs；node --test web/assets/api.test.mjs | 语法与 10 项 API PASS |
| python tests/smoke/local.py --go go | 真实 main/auth/Chat/SSE/控制台、strict 拒绝、强杀/原双 hold/审计豁免、加密轮换拒绝/恢复、durable 重启/密钥拒绝和关闭 PASS |
| python tests/smoke/compatibility.py --go go | 已安装 Chromium/Playwright，真实临时二进制/SQLite/API/UI、保存/发布、角色/模型/Key 隔离与桌面/移动 PASS |

首次编译因 state 参数遮蔽 import、迁移后的无用 import 与 price.go 其他路径仍需 math 失败，统一修复后 build 通过。
首次测试编译发现旧测试 helper 与 import 重名、reopen 原 store 未包桥和 typed-nil 用例仍传旧实现，迁移为中立端口后测试编译通过。
只编译命令不作为行为测试。新增桥的定向 ownership/fencing/错误回归及 architecture 先通过，最终全量包含它们。
首次 gofmt 写入遇到 Windows 短暂 mapped-section 占用，未全量格式化成功；随后逐个仅变更 Go 以格式结果比较并替换剩余 12 文件，全部 46 文件通过。
静态命令的 Go build/vet/tidy 已通过，后续 node 最初误用不存在的 app.js 路径失败；改为仓库实际 .mjs 后两语法检查和 10 项 API 通过，未因此重跑无变化的 Go 检查。

持久 ORM 行/标签、schema v12、OpenAPI 0.4.0/91 操作、财务/密文格式和依赖/workflow 没有变化；
无真实提供商/生产数据操作。归档、历史、领取/fencing 与预算仍调用原权威一次，转换只承担数据所有权。
本地通过不代替最终头的 Linux race/容器交付和合并后 master，当前不勾选 FND-03，不冻结候选契约。

### FND-03 最终 CI 与合并验收

最终功能头 `3840337e2db1f6b12d4e5b9647d6053f72fd329e` 的自动 push
[39101056074](https://github.com/ANRCM0/TXAPI/actions/runs/39101056074) 和 PR
[39101081820](https://github.com/ANRCM0/TXAPI/actions/runs/39101081820) check/delivery 全部 SUCCESS。
核对精确 head、未漂移 master/e97e458、clean worktree 与四个 green jobs 后，
`gh pr merge 16 --merge --match-head-commit 3840337e2db1f6b12d4e5b9647d6053f72fd329e`
将 [PR #16](https://github.com/ANRCM0/TXAPI/pull/16) 合并为
`3bf25400bac13c6ca563de3373cde9992af417a8`，与功能头树一致。
合并后 master 自动 [39101392941](https://github.com/ANRCM0/TXAPI/actions/runs/39101392941)
的 check/delivery 均 SUCCESS，含 Linux race、契约/依赖、构建/vet/格式、主程序冒烟和容器交付。
FND-03 已验收；以上本地段落的 pending 描述保留其时点，不覆盖后续验收事实。

## FND-04：应用用例与 HTTP 表示隔离

从已验收 master/3bf2540 新建 feat/fnd-04-application-http-boundaries，六项开工边界见 PLAN。
本批将剩余控制面业务与 Responses HTTP 解析/表示分离，L1 仍拥有原授权、限流状态、事务与账本。
实现和集中验证结果在实际完成后登记；目前不记 PASS，不勾选 FND-04。


### FND-04 实施与集中修复

control/v1、ledger/v1、Authenticator/TokenView、admission/observability/text 与 Responses 命令/交付契约
均使用独立公开值；现有 Store/Engine 的事务和财务权威不变。controlplane 接手登录窗口、owner Key 与模型投影，
server/API/middleware 不再导入其实现或 Store/Engine/Gateway；实际组装移入 main，测试组装仍调用同一生产 handler。
原 memoryLimiter 及其全部用例移入 access，保留所有断言；原 history_http 用例改为类型化 history_commands，
路由/头/JSON/SSE/父链与输出构造在 L2。原字段/密文与 91 个 HTTP 操作保持，不新增后台/WS 或 strict 许可。

编译阶段发现并修复无用 import、ResolutionInput 的旧私有方法不能挂到跨包 alias、转换生成的多余括号，
以及最后组装点仍给应用传 NewOutput/直接当 http.Handler 的引用。账本规范化保留为私有函数，未迁走结算逻辑。
控制面和最终全仓只编译检查均通过，但不作为行为验收。新增端口/资源定向回归与依赖拒绝先通过。
首轮完整候选来源为 348 路径 / `536585bdcb2982c83319ace056f4170b4c554b62c3d015976a4b1e88b231020c`：
1071 项/29 包、5 helper SKIP，全量、build/vet/tidy、前端 10 项、main 恢复与真实控制台联调通过。
随后审阅发现 Begin 在自定义身份 Refresh panic 后可能遗留已取得的容量；用交接标记和 defer 保护，
新增下一次 Begin 可重新取得容量的真实回归，先与 Close/join 用例定向复验 PASS，再重新执行相关完整检查。

### FND-04 最终本地验证

最终来源 348 个非 Markdown 路径 / SHA-256
`990a65a7350ea053d97a130652e89bd8627ff638fc503df3a548ebd4ae85d952`，集中复验前后一致。
Windows/PowerShell，所有 Go 命令显式 GOARCH=amd64。

| 实际命令 | 结果 / 适用来源 |
|---|---|
| go test -json -count=1 ./... | 最终 1072 项测试/子用例、29 包 PASS，5 预期 helper SKIP，无失败 |
| go build ./...；go vet ./... | 最终来源 PASS |
| go mod tidy；go.mod/go.sum diff | 首轮来源 PASS；之后仅 Begin 的异常释放和回归，无依赖变化，最终 diff 复核无漂移 |
| normalized-LF gofmt 比较，仅变更 Go | 66 个现存变更 Go 文件通过；移动的限流实现/测试保留，不格式化无关文件 |
| node --check app.mjs/api.mjs；node --test web/assets/api.test.mjs | 首轮来源语法和 10 项 PASS；最终没有 web 或正常 HTTP 表示变化，复用此范围 |
| python tests/smoke/local.py --go go | 首轮来源真实 main/auth/Chat/SSE/控制台、strict 拒绝、强杀/原 hold/审计、加密/密钥拒绝恢复及 durable 重启/退出 PASS；最终仅自定义身份端口 panic 的释放保护，生产认证桥已将 panic 转错误，正常路径不变，复用此范围 |
| python tests/smoke/compatibility.py --go go | 首轮来源真实临时二进制/SQLite/API/UI、配置保存/发布、角色/模型/Key 隔离、桌面/移动及清理 PASS；同上限定复用 |

新增直接应用回归验证关闭 join、单次执行/授权、Begin panic 退回容量和 owner/空 allowlist；
原 HTTP/JSON/SSE、错误/幂等/回放/ETag/CAS、会话/角色/Key/额度、账本决策和旧加密/恢复用例继续运行。
无真实上游或生产数据操作；没有修改 OpenAPI/schema/migration/workflow/依赖或部署格式。
最终头的自动 Linux race/冒烟/容器交付和合并后 master 仍须通过，当前不勾选 FND-04、不冻结 v1。


### FND-04 最终 CI 与合并验收

功能头 `203d80f17adee792f88841dbe0111ab96fbd9626` 的自动 push
[39104078257](https://github.com/ANRCM0/TXAPI/actions/runs/39104078257) 与 PR
[39104216724](https://github.com/ANRCM0/TXAPI/actions/runs/39104216724) 的 check/delivery 全部 SUCCESS。
核对精确 head、clean worktree、未漂移的 master/3bf2540 后，
`gh pr merge 17 --merge --match-head-commit 203d80f17adee792f88841dbe0111ab96fbd9626`
将 [PR #17](https://github.com/ANRCM0/TXAPI/pull/17) 合并为
`14bb9c967e55918c0ce0b221509b588a43b34b4c`，与功能头树一致。
合并后自动 [39104465314](https://github.com/ANRCM0/TXAPI/actions/runs/39104465314) 的
check/delivery 也全部 SUCCESS，含 Linux race、契约/依赖、构建/vet/格式、冒烟和容器交付。
FND-04 已验收，上述本地时点的 pending 描述保留原时点，不覆盖此结果。

## FND-05：全层约束、独立接入与冻结准备

从已验收 master/14bb9c9 新建 feat/fnd-05-foundation-freeze；六项开工边界见 PLAN。
逐包登记所有生产 Go 包、公开入口与版本，递归类型检查和 API 签名快照不扩大私有实现入口。
L3 summary 与 L2 codec 分别只消费公开值；测试组装使用真实临时 Store、当前身份、容量、Gateway、
原 core 和 loopback/真实 HTTP 输出，不修改框架操作 switch。新操作严格计费仍在发送前拒绝。
实现细节和 DoD 位置见 [接入说明](../reference/foundation-integration-v1.md)。
当前实施候选，不记最终 PASS，不冻结；集中验证与最终 CI 结果按实际完成后补记。


### FND-05 本地最终集中验证

最终非 Markdown 来源为 356 路径，SHA-256
`2b714cf86fb8f420bed280eff6c57902cfddaeb1faaef9d5051cb11d5fec64b4`，
集中检查前后相同；此后仅写说明文档。基线 master/14bb9c9，Windows PowerShell，Go 命令显式 GOARCH=amd64。

| 实际命令与环境 | 结果 |
|---|---|
| go test -json -count=1 ./... | 1124 项测试/子用例、30 个有测试包 PASS，5 预期 helper SKIP，无失败 |
| go build ./...；go vet ./...；go mod tidy 与 go.mod/go.sum diff | PASS，无依赖变化 |
| normalized-LF gofmt 比较，仅本批变更 Go | 8 文件 PASS，无无关格式化 |
| node --check app.mjs/api.mjs；node --test web/assets/api.test.mjs | 语法和 10 项 API PASS |
| python tests/smoke/local.py --go go | main/auth/Chat/SSE/控制台、strict 拒绝、强杀与原双 hold、审计豁免、凭据轮换拒绝/恢复、加密 durable 重启/密钥拒绝/关闭 PASS |
| python tests/smoke/compatibility.py --go go | 已安装 Playwright/Chromium，临时二进制/SQLite/真实 API/UI、保存/发布、角色/Key/模型隔离与桌面/移动 PASS |
| python .local/validation/check-foundation-all-docs.py；git diff --check | 全仓 28 份 Markdown / 300 个本地链接锚点与差异检查 PASS |

真实样例与负面检查先定向验证，最终全量再包含它们。首次样例编译发现 Capacity.Close 无返回值，
改用原 Close 的清理函数后通过；首次格式命令误指不存在的 json.go，改为实际 validate.go 后全部 8 文件比较通过。
递归审阅补上标准库泛型实参的检查，并以真实 type-checked atomic.Pointer[core/ORM 类型] 拒绝夹具验证，
不因标准库具名类型被允许就漏掉其类型参数。API 快照记录常量值、字段标签及导出方法，正常测试禁止静默漂移。
全包 56 个精确登记、14 个公开入口及 L0 共 15 个签名快照包，没有未清依赖豁免。

core 原机制缺少源码版本标记，本批只增加 Version=1，机制实现与运行参数均未变化；json 同样补齐已有纯规则的版本。
这属于 FC-001 已批准的版本/冻结范围，无新增 L0 执行功能。无需持久迁移，回退可整体回到前一匹配版本。
Go/HTTP/存储和浏览器回归使用原测试及临时资源，无真实上游、真实凭据或生产库操作。
现有 OpenAPI 0.4.0/91 操作、schema v12、密文格式、财务权威、依赖、CI workflow 与生产 main 保持。
历史提案的 gateway/handler.go 已迁走，其原差距链接改指经过 git cat-file 验证存在的 4312f77 精确历史源码；未改批准范围。

上述是本地验证，最终自动 Linux race/冒烟/容器与合并后 master CI 仍待通过；此时不冻结、不勾选 FND-05。
