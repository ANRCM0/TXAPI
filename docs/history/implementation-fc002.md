# FC-002 原生后台实施与验证记录

最新状态：A03-01/02/03 已依次独立验收，两个追加入口已按 disabled、自包含范围冻结；
见[最终验收与冻结](#fc-002-最终验收与冻结)。下文实施中/未验证文字保留各批原时点，不作为当前待验收状态。

## 批准、范围与基线

2026-10-18 用户明确批准“批准按 FC-002 实施”，覆盖
[具名提案](../proposals/FC-002-native-background-v1.md) 的 A03-01 → A03-02 → A03-03 顺序。
开工与提交前 fetch 的 `origin/master` 均为
`5f90a20a3951b67255a74fa640d9d9f8e92bf8e0`。提案准备提交 `9116207` 随 A03-01 一起评审。
只交付 disabled 自包含输入子集，不勾选完整 A03/M3，不改变收费后台的双 hold/成本证明门槛。

## A03-01：候选契约与持久受理

### 实际实现

- 新 background/v1 为受理、任务/事件投影和纯远端 codec 注册提供候选值类型；responses/v2 组合旧命令并新增取消和流式读取参数，不修改 v1 接口。
- generation 的 BackgroundService 只负责持久受理与当前权限访问。可信 context 与同一 Store 事务内当前 owner/创建 Key/模型校验共同约束写入；未运行 worker，未注册公开后台 API。
- 输入复用原 AEAD ring，独立 domain/AAD；幂等作用域使用独立稳定 HMAC key，不保存原始 Key/幂等键。随机公开候选 ID 不参与请求摘要，同参重试返回原 ID；与 A02/前台回执 ID 在原事务互斥。
- model/store 统一追加 schema v13，未另建迁移或账本。活跃/记录/内容实例与 owner 配额同事务核对；queued 取消阻止派发并释放活跃配额，dispatching lease 失效保守转为 uncertain，不能重发。
- 原子 claim epoch/随机 capability、续租、revision CAS 和原授权校验拒绝伪造/过期写入。到期清除正文/事件但保留 unknown 元数据和在途保护；已知终态元数据按有界批量清理。
- 启动检查 retained 内容/digest key，缺失或变更拒绝。legacy/strict 新受理拒绝，存在未决任务时计费模式切换拒绝；不伪造零金额 hold。
- architecture 精确登记两个新候选公开包及私有存储 seam，负面样例拒绝 L3/L2 直接依赖私有 seam，store 只可依赖该 seam 而不能导入 generation runtime。

### 集中验证与修复

Windows PowerShell，所有 Go/冒烟子进程显式 `GOARCH=amd64`、`GOFLAGS=-buildvcs=false`。
只使用临时 SQLite、测试用户/Key、loopback 假上游和现有本地 Chromium；未接触真实提供商或运行库。

开发期间第一次三包编译发现 execution import 与现存测试 helper 同名，已改为 execv1；
第一次定向风险回归发现 SQLite 空 BLOB 读回 nil 后 GORM Save 违反 NOT NULL，现统一归一化空字节再保存，
保留 NOT NULL 约束。修复后五组后台风险回归通过，不把失败或早期通过当最终代码证据。

首次完整 `go test -json -count=1 ./...` 中 1138 项测试/子用例通过，唯一失败是旧 v12 升级回归的末版本断言。
修正夹具为 v11（删除 v12+ 迁移标记）并显式验证追加 v12、v13；保留原回执、账本及历史不可回填全部断言。
失败项及 store 全包 65 项复验通过，随后最终完整回归继续验证。

实际执行：

| 命令/检查 | 结果与适用范围 |
|---|---|
| `go test -count=1 ./internal/architecture -run TestVersionedPublicSignaturesHideImplementationTypes -record-foundation-api ...` | 候选 API 导出通过；逐项对照 origin/master，原十四组公开包及 L0 完全相同，仅增加批准的两个入口 |
| `go build ./...`; `go vet ./...`; `go mod tidy`; `git diff --exit-code -- go.mod go.sum` | PASS；无依赖变化 |
| 变更 Go 文件 normalized-LF gofmt 对照 | PASS；只格式化本批文件。原始 Windows CRLF 对全仓 gofmt -l 产生换行差异，不因此格式化无关文件 |
| 五个前端/浏览器脚本语法；`node --test web/assets/api.test.mjs` | PASS；10 项 API 行为通过 |
| `python tests/smoke/local.py --go go`（同上 Go 环境） | PASS；真实 main/auth/Chat/SSE/控制台、strict 拒绝、强杀 pending/豁免、凭据加密/恢复和 durable 关闭 |
| `python tests/smoke/compatibility.py --go go --node node` | PASS；生产二进制/临时 SQLite/真实 API 与 Playwright 桌面及移动端、角色/模型/Key 隔离和发布 |

上述主程序/浏览器结果来自非 Markdown 364 路径指纹
`5d10ec59ffa612f27eee0cc4ecc8ad2a3440ca599770f2ec630fdbf70bf47fdd`；
其后只修正旧迁移测试预期，生产实现、配置、接口与运行路径未变，因此复用这些通过证据，不重复冒烟。
最终再次执行 `go test -json -count=1 ./...` 全量通过：1139 项测试/子用例、30 包 PASS，
5 个预期子进程 helper SKIP，无失败。最终来源为同样 364 路径，指纹
`764a01508a39b937fb9a3540edef09da205010de0f7cb5bd37efb3e19362b05f`，验证前后相同；
17 个变更 Go 文件格式通过，30 份 Markdown / 321 本地链接与差异检查通过。
后续提交前只更改说明文档；本地结果不替代 Linux race 或容器交付。

### 当前验收边界

PR #20 将最终功能头 `5119d349034e42ea4b4070857fb75b064451ffa3` 合并为
`master/c1b826453d1ecfb9edc3d58322fbbaf42e172a41`；push `39121239200`、
PR `39121268742` 和合并后 master `39121669860` 均为 check + delivery 全部 SUCCESS，
包含 Linux 全量 race、主程序冒烟与 v13 容器安装/升级/备份恢复/退出。
fetch 核对合并树与最终功能头相同，工作树干净；A03-01 已验收。
新契约仍为 FC-002 候选，当前没有 main 启动、原生后台 submit/inspect/cancel HTTP
或事件恢复承诺；A03-02/03 顺序补齐。
旧 Chat/embeddings、前台 Responses、历史/会话、财务及恢复断言保留。

## A03-02：原生后台接线（实施中，未验收）

开工基线为已验收 `master/c1b8264`。接线审计发现原候选 Service 缺少资源分类与终态删除方法，
不能满足 FC-002 原定的单一资源权威/DELETE 语义。用户已明确批准
[FC-002-A 最小补充](../proposals/FC-002A-resource-ownership-v1.md)：同包新增 Resources.Kind/Delete，
不改原十四组冻结入口、不扩大计费/父链/WS 范围，继续 A03-02 → A03-03。
尚未将该批准或本节文字算作实现与验证通过。


### A03-02 实现范围与集中验证起点

- L1 owner 持久绑定、双 worker、单次真实 submit 与绑定后 inspect/cancel；原 core、选路/目标指纹、准入、健康和 generation RPM/TPM 复用。控制 RPC 不伪造旧 Principal，不走第二次生成/资金链。
- L2 独立原生 parser/codec 保留 background=true；JSON RPC 的 queued/in_progress 不能当任务终态，原同步 decoder 不放宽。L3/v2 与原 v1 共用 Begin/Call 容量及当前授权，main 单点接线、停止和 join。
- FC-002-A 的当前身份资源分类/仅终态 revision DELETE；前台/后台幂等键双向冲突；v14 只追加私有 deleted/共享命名空间 HMAC，保留 v13 摘要和已有 intent/账本/历史密文、AAD、迁移记录。
- background 默认关闭，显式 channel_ids、实际凭据加密/内容密钥及 disabled 才能启用；未决任务时即使关闭开关也不能换计费模式。正文到期的访问拒绝及启动/周期清理不依赖产品开关，unknown 非内容保护不被清除。
- 候选 OpenAPI 0.5.0 / 92 个操作，新增取消路径及临时 TTL 头；只支持非流后台、自包含输入。事件流恢复仍留 A03-03，不提前冻结候选，不勾选完整 A03/M3 或成本能力。

初次测试编译遇到 generation 原测试 helper 名 execution 与新 import 冲突，已用 execv1 别名修复，保留 helper/断言。
第一组原生 loopback/SQLite/HTTP 风险回归通过。扩展风险集的 main、codec、gateway、store、config 均通过，
generation 仅新增删除测试的另一 Key 夹具因 owner 为 m allowlist 却请求 All 被原授权规则正确拒绝；
将夹具改为同 m allowlist 后该失败项复验通过，没有放宽生产授权或删除断言。
实现审计同时补齐开关关闭后的独立隐私清理、同请求 key 跨前台/后台权威冲突、控制凭据轮换拒绝和输出持久失败不惩罚上游。

API 导出并逐项对照 c1b8264，仅候选 background/v1 新增 ResourceKind、两个常量和 Resources 四个符号；
十七个快照项中的原十四组冻结公开包及 L0 与 master/5f90a20 完全相同，无删除或额外公开入口。
`python .local/validation/check-foundation-all-docs.py` 检查全仓 32 份 Markdown / 338 本地链接锚点通过；
`git diff --check` 通过。38 个本批 Go 文件 normalized-LF gofmt 通过，未格式化无关文件；
五个 JS 语法检查与 10 项前端 API 行为通过。

### A03-02 最终本地集中验证

环境：Windows PowerShell / GOARCH=amd64 / GOFLAGS=-buildvcs=false；基线 `master/c1b8264`。
最终来源为 379 个非 Markdown 路径，SHA-256
`e126fee635c8576d12aebcf3c27548e178aef15256adebed91ba3006c7201077`，
完整验证前后及提交前一致。下列结果来自该来源，不复用 A03-01 或早期定向通过。

| 实际命令/检查 | 结果 |
|---|---|
| `go build ./...`; `go vet ./...` | PASS |
| `go mod tidy`; `git diff --exit-code -- go.mod go.sum` | PASS；没有依赖变化 |
| 38 个本批 Go 文件 normalized-LF gofmt；API 导出/原十四组和 L0 对照 | PASS；只有 FC-002-A 批准的四个背景资源符号新增 |
| `go test -json -count=1 ./...` | PASS：1159 项测试/子用例、30 个测试包，5 个预期 helper SKIP，零失败 |
| 五个 JS 语法；`node --test web/assets/api.test.mjs` | PASS；10 项前端 API 行为 |
| `python tests/smoke/local.py --go go` | PASS；真实生产 main/auth/Chat/SSE/控制台、strict 拒绝、强杀 pending/审计豁免、凭据加密轮换/恢复及 runtime 关闭 |
| `python tests/smoke/compatibility.py --go go --node node` | PASS；真实生产二进制/临时 SQLite/API 与 Playwright 桌面/移动、权限/模型/Key/发布与清理 |
| 全仓 Markdown/链接与 `git diff --check` | PASS；32 份 Markdown / 338 本地链接锚点 |

没有本地 Docker 可执行程序；Linux 全量 race 和 v14 容器安装/升级/备份恢复/退出由现有
自动 CI 验证，不把 Windows 普通测试冒充这些结果。没有调用真实提供商、真实预算/生产库或宣称 SDK 认证。
完整本地通过不等于已验收：最终提交头及合并后 master 的现有完整 CI 仍须全部通过，随后才进入 A03-03。

## A03-02 精确头与合并后验收

2026-10-18，最终功能头 `8d28ebc122503d0f302292e16a856fc10fd95ce1` 从已验收
`master/c1b826453d1ecfb9edc3d58322fbbaf42e172a41` 经 PR #21 合并为
`master/8a1da1fd1b27d47bd74e761ea928caa37ba8b0cb`。
实际 fetch 后核对合并树 `50e485dd17e45196d5f83a4eaaad0f939a567f55` 与功能头完全相同；
工作树干净。自动 push `39126429335`、PR `39126434971`、合并后 master
`39126878929` 在对应精确 SHA 均为 check + delivery SUCCESS，含 Linux 全量 race、
主程序冒烟与 v14 容器交付。A03-02 与 FC-002-A 的该范围已独立验收。
未发布 tag/release，未操作真实上游；两组新入口仍候选，完整 A03/M3 与 S5 成本门槛保持。

## A03-03：原生事件与强杀恢复（实施中，未验收）

从已验收 `master/8a1da1f` 创建 `feat/a03-03-background-events`。
六项责任范围见 PLAN：原包内补齐 L2 原生后台 SSE/GET resume，L1 fenced 事件与
真实 owner/gateway/core I/O，L3 只观察已持久协议字节；原十四组冻结入口/L0 和
A03-02 两组候选签名都不修改。统一追加 schema v15 私有 ReplayPending，
不改已发布 v14、AAD 或旧密文；本地临时保留、disabled 模式及保护未知请求语义保持。

当前作者实现包括：原生 typed sequence_number/事件 ID/终态校验、规范化 SSE 字节，
同明文重放复用原密文、序号连续与页尾完成，原生 JSON 终态和终态事件持久分离，
取消/权限撤销内部清理、暂态 RPC 断开只 GET 恢复、明确不可恢复与保守未知，
观察断开/短写不影响任务，以及 L2 的后台 SSE no-store 修正（不修改 L0）。

新回归已编写但尚未执行：真实子进程 Kill 的领取/发送前/上游受理未绑定/事件提交未确认/
终态未落库窗口，loopback 实际 POST 次数，旧 claim fencing、重放/缺失/冲突与取消尾部，
当前 HTTP SSE 断开/重启/显式零游标、短写，以及生产二进制的默认关闭/强杀重启/取消/
模式拒绝/产品关闭后 TTL。保留既有回归与 CI；集中验证结果、失败修复和最终源码另行追加。

### A03-03 集中验证与修复记录

Windows PowerShell，所有 Go 进程及 Python Go 子进程使用 GOARCH=amd64、GOFLAGS=-buildvcs=false。
首轮 build/vet、原生事件/强杀/HTTP 相关六包均通过；旧 gateway 回归发现底层存储错误原样返回
不再匹配 ErrUnavailable。只调整私有错误映射：已知游标/容量/冲突/claim 错误保留类型，
其他存储故障继续映射为原 ErrUnavailable；无重试、无提供商健康惩罚，原断言完整保留。
随后 gateway/generation/cmd 的背景受影响范围集中复验全部通过。

来源 387 个非 Markdown 路径 / `11d539c1e94cbb1149bf468d5348dc710e999109e1d92a83d818c531ed9c40b6`
的首轮完整本地 `go build ./...`、`go vet ./...`、tidy/依赖零差异、
`go test -json -count=1 ./...` 通过：1201 项测试/子用例、30 个测试包、零失败。
主程序 local.py 新增真实二进制的后台 SSE/SIGKILL/重启/取消/游标/模式拒绝/feature-off TTL
并与原 Chat/strict/凭据/持久 runtime 全部一起通过；兼容冒烟真实 API/桌面/移动端通过。
五个 JS 语法及十个前端 API 行为通过；17 个公开/L0 快照导出与 master/8a1da1f 完全一致。

收尾源码审查另修正观察端首次提交前 TTL/取消/写期限失败返回空 200 的边缘路径：
现在提交前返回具名问题，提交后仅关闭、不追加 JSON/伪终态；新增明确前后提交回归。
真实主程序 smoke 的恢复轮询降至每秒一次，避免合法 30 秒遗留租约窗口耗尽用户读配额。
这两处后的最终源码须重新完成集中检查；不得把上一段来源直接当作最终头已通过。
纯前端与 feature-off UI 兼容来源不变，适用的结果可在注明范围后复用。

### A03-03 最终本地集中验证（待精确头 CI/合并验收）

最终来源 388 个非 Markdown 路径，SHA-256
`adc6d18a07911a0d5e324c16bff37a126b9df04ce6ee3883abbd2212d820cd8d`，
验证前后完全相同；基线仍为 fetch 后未漂移的 `master/8a1da1f`。

| 实际命令/检查 | 最终结果 |
|---|---|
| `go build ./...`; `go vet ./...` | PASS |
| `go mod tidy`; `git diff --exit-code -- go.mod go.sum` | PASS；零依赖变化 |
| `go test -json -count=1 ./...` | PASS：1207 项测试/子用例、30 个测试包，6 个预期 helper SKIP，零失败 |
| `go test -count=1 ./internal/responses ./cmd/gateway -run Background -timeout 3m` | 最后观察边界修复的受影响范围 PASS；随后上项全量再次通过 |
| 28 个本批 Go 文件 normalized-LF gofmt；三份 Python AST | PASS；只格式化本批 Go |
| 17 项公开/L0 API 快照与 master/8a1da1f 对照 | 完全不变；原十四组冻结入口与 L0 未改，完整 architecture 回归通过 |
| `python tests/smoke/local.py --go go` | 最终来源重新 PASS；含原全部主程序/strict/凭据/持久 runtime，新增实际后台 SSE/SIGKILL 重启/取消/游标/模式拒绝/feature-off TTL |
| 五个 JS 语法、10 项前端 API 行为 | 复用本批通过；JS 来源完全未改 |
| `python tests/smoke/compatibility.py --go go --node node` | 复用本批来源 11d539c 的 PASS；其后仅后台观察首次提交错误、相应回归和后台 smoke 轮询变更，feature-off 的真实 UI/API/角色/桌面移动布局路径无变化；后台改变另由最终主程序 smoke 和全量覆盖 |
| `git diff --check`；全仓 Markdown 本地链接/锚点 | PASS |

没有本地 Docker 可执行程序；Linux 全量 race 与 v15 容器交付仍待最终头的现有自动 CI。
未手动触发 workflow、未关闭门槛、未操作真实上游/数据库或发布。
本地全部通过不提前冻结或勾完整 A03/M3；最终功能头及合并后 master check/delivery 均通过后才验收。

## FC-002 最终验收与冻结

2026-10-18，三个已批准子批按序完成，没有并行夹带功能或重开原十四组/L0：

| 子批 | 最终功能头 / PR | 实际合并 master | 完整 push / PR / master CI |
|---|---|---|---|
| A03-01 | 5119d349034e42ea4b4070857fb75b064451ffa3 / #20 | c1b826453d1ecfb9edc3d58322fbbaf42e172a41 | 39121239200 / 39121268742 / 39121669860，全 check + delivery SUCCESS |
| A03-02 + FC-002-A | 8d28ebc122503d0f302292e16a856fc10fd95ce1 / #21 | 8a1da1fd1b27d47bd74e761ea928caa37ba8b0cb | 39126429335 / 39126434971 / 39126878929，全 check + delivery SUCCESS |
| A03-03 | 501507d4c070208ed8e234cf6ff9cb19c156e25c / #22 | a44d4d147b7309679fa5b7005ed34c12473e4bf0 | 39130613526 / 39130658258 / 39131280478，全 check + delivery SUCCESS |

最终头自动 [push](https://github.com/ANRCM0/TXAPI/actions/runs/39130613526) 与
[PR](https://github.com/ANRCM0/TXAPI/actions/runs/39130658258) 两个 job 均成功且无失败步骤后，
实际 fetch 核对 clean worktree、基线仍为 `8a1da1f`、精确 head 和 MERGEABLE，执行
`gh pr merge 22 --merge --match-head-commit 501507d4c070208ed8e234cf6ff9cb19c156e25c`。
随后 fetch 验证 [PR #22](https://github.com/ANRCM0/TXAPI/pull/22) 实际合并 SHA 为上述 a44d4d1，
`git diff --exit-code 501507d origin/master` 通过；合并树
`f8fe78117f0163605398748fb97418d427aa99b9` 与已测功能头完全相同。
合并后自动 [master](https://github.com/ANRCM0/TXAPI/actions/runs/39131280478)
也全部成功、无失败步骤，含 Linux 全量 race、主程序后台 SIGKILL/恢复冒烟及 v15 容器安装/升级/备份恢复/退出。
没有手动触发 CI、admin 绕过、删除回归或修改 workflow 触发条件。

依据该匹配来源及全部实际消费者，现冻结 L1 background/v1（Version=1）与应用 responses/v2（Version=2）
的已验收 disabled、自包含原生后台 JSON/SSE 范围；原十四组/L0 签名与许可不变。
59 个生产包逐个登记；十六组当前公开入口/L0 共十七项签名和递归可见性通过，A03-03 没有新的签名增量。
逐项文档/版本、契约测试、最小可运行、扩展、基础约定、限制和独立上层接入证据见
[七项 DoD](../reference/foundation-integration-v1.md#fc-002-追加公开入口冻结) 和
[追加 API 清单](../reference/foundation-api-v1.md#fc-002-追加冻结接口)。

本次后续冻结登记**只改 Markdown**。非 Markdown 来源仍为 388 路径 /
`adc6d18a07911a0d5e324c16bff37a126b9df04ce6ee3883abbd2212d820cd8d`，与上方最终本地检查及合并代码相同；
复用其 1207 项/30 包、真实主程序和精确头/合并后完整 CI，不在文档批次重复本地 Go/main/浏览器。
实际执行 `git diff --check`、`python .local/validation/check-foundation-all-docs.py` 和来源指纹检查：
13 个变更路径全部为 Markdown，全仓 32 份 Markdown / 376 个本地链接锚点通过；
版本/权限/生命周期/状态一致性及原完整 A03 八个未完成总门槛均核对，非 Markdown 来源未变。
本登记仍正常推送/PR/合并，保留既有自动 CI，记录合并本身与功能验收来源区分。

冻结不是完整 A03/M3：收费后台原子双 hold/最终结算、Responses 成本证明、背景父链/会话组合、
原生 WS、真实提供商/SDK、ZDR/生产部署均未由本批验收；store=false 仍按固定 TTL 临时加密保留。
没有创建或移动标签、发布 release/镜像、操作真实提供商、生产库或远端治理。
