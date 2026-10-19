# 参考方案实施记录

目标：按已确认的参考吸收方案逐批交付、验证和集成。2026-10-18 用户要求设置目标并完成方案；R01 具体接口变更已另获明确批准。

本记录区分本地检查、功能提交 CI、合并及合并后 CI。R02 以后仍需各自真实消费者和进入条件；完成 R00 不代表整套方案完成。

## R00：可解释调度观测

### 范围与工作协议

- L1 `internal/gateway` 与 `internal/metrics`，共六个 Go 文件及本记录。
- 依赖现有调度/身份/准入及指标实现；不新增公开契约，不修改 L0、schema、配置或网络重试规则。
- 选择结果独立于实际 attempt：记录候选、凭据可用性、优先级回退、实际 picker、目标与耗时；指标仅固定取值标签，Debug 日志无密钥/会话原文。
- 保留同一资格判断和选择权威；扩展只能选当前快照内合格凭据 ID，不能替换材料或修改已试集合。
- 回归覆盖优先级回退、扩展接受/回退、标签基数、并发计数、预检查和准入拒绝零 attempt，以及日志脱敏。
- 不混入原工作区的文档删除、OpenAPI 路径迁移和注释整理；共享入口不变，无其他模块并行写入。

### 来源与本地验证

集成工作区：`C:\Users\mn139\.codex\worktrees\reference-adoption\TXAPI`。
最新基线：`origin/master/1f9b2320161797afc90da3f601ec879543b7fa0b`。
与原 `536bf2b` 源树相同；主线追加的是 PR #23 的合并提交。
R00 六文件归一化 LF 的清单摘要：`c568e2427005ee20dfac65ac19742699ed036e5ee2d60f90243abab2393066e9`。
清单和原始日志留在仓库外 `D:\my-project\TXAPI-references\validation-r00`，不提交临时日志或二进制。

2026-10-18，Windows PowerShell，Go 1.26.3，`GOARCH=amd64`、`GOOS=windows`、`CGO_ENABLED=0`：

| 实际命令 | 结果 |
|---|---|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test -json -count=1 ./...` | 1222 项测试/子用例 PASS，6 个预期 helper SKIP，30 个测试包 PASS；另 30 包无测试 |
| `go mod tidy` + 检查 go.mod/go.sum 无 diff | PASS |
| `go list -deps ./internal/core ./internal/protocol/...` + 禁止依赖过滤 | PASS |
| `gofmt -l` 本批六个 Go 文件 | 无输出，PASS；未格式化其他文件 |
| `node --check` app/api/navigation/billing/live 五个脚本 | PASS |
| `node --test web/assets/api.test.mjs` | 10/10 PASS |
| `python -B tests/smoke/local.py --go go` | PASS：实际 main 普通/SSE/控制台、strict 拒绝、结算与凭据轮换、加密重启、后台 SSE 强杀/重启/续传/取消/TTL |
| `git diff --check` | PASS |

本地没有运行 Linux race 或容器 delivery；这两项由现有完整自动 CI 验证。未调用真实 Provider。

### 集成状态

已验收并合并：功能头 `ab0fe952abc3ff3a8dc4776a9ffb78c9e1e5bafa`，PR #24，合并后 `master/231af3a411246b96c0f4c3a948ea5cad35d868df`。功能头与合并树一致。

| 精确来源 | 自动运行 | 结果 |
|---|---|---|
| 功能头 push | `39140219794` | check/delivery SUCCESS |
| PR #24 | `39140241563` | check/delivery SUCCESS |
| 合并后 master/231af3a | `39140726076` | check/delivery SUCCESS |

均含现有 Linux 全量 race、主程序恢复冒烟和容器安装/升级/恢复；没有手动重复触发 workflow。未制作 release 或发布新标签。

## R01：执行事实与单次协议交付

具体新增签名及范围由 `REFERENCE-R01-CHANGE-REQUEST.md` 定义，用户已于 2026-10-18 明确回复“批准 R01 具体变更”。
实施内容限追加 execution/v2、L1 decoder 桥、原生 Responses 真实消费者与独立 L3 样例；保留 v1 与 L0 签名。
已验收并合并：功能头 `a66a0706034c3bb86d60742931b7fcc8182e7aef`，PR #25，合并后 `master/5938c055ab69097adeab460919b4e5c21410939c`。功能头基于 `master/231af3a`，新增签名快照只增加 execution/v2 的 16 个条目，原有所有冻结条目逐项相同；L0 与 v1 签名不变。

真实 Responses HTTP/SSE 消费者通过显式 v2 decoder 能力使用新桥；独立 L3 facts 样例通过真实 SQLite 身份、当前 Key 撤销、真实 gateway 与假上游证明接入。用量完整性、事实来源、原始表示和宿主派发/交付水位分别保存。

兼容细节：v1 partial usage 仍投影为 nil；新 v2 路径按已批准合同校验每个已知计数，所以负数的 partial usage 会拒绝。合法部分用量与缺失用量继续可交付，不补零、不开放新的 strict 组合。

首轮集中 vet 发现新增测试有跨包未命名字段字面量，已修为命名字段。代码复核补充了传输 panic 的保守 Dispatching 记录与回归；SSE 部分写/Flush 失败保留 Native 终态，但 ProtocolComplete 仍由原 core 的 End 验证决定，不伪造客户端已消费或协议已完整收尾。

### R01 最终本地来源与检查

基线：已验收 `master/231af3a411246b96c0f4c3a948ea5cad35d868df`。环境同 R00：Windows/PowerShell，Go 1.26.3，GOARCH=amd64、CGO_ENABLED=0。
13 个代码/架构来源文件的归一化 LF 清单摘要为 `37a5acd84904bf88799c8e2ed97709eaa8926f097946c34b9f308665cfaf93dd`；清单和原始日志在仓库外 `D:\my-project\TXAPI-references\validation-r01`。

| 实际命令/检查 | 最终结果 |
|---|---|
| `go build ./...`、`go vet ./...` | PASS |
| `go test -json -count=1 ./...` | 1260 项测试/子用例、31 个测试包 PASS，6 个预期 helper SKIP；另 31 包无测试 |
| `go mod tidy` + go.mod/go.sum 无差异 | PASS |
| `go test -count=1 ./internal/architecture` | PASS；包含依赖、公开类型及原签名保持检查 |
| `gofmt -l` 本批 11 个 Go 文件、`git diff --check` | PASS；只格式化本次修改文件 |
| `python -B tests/smoke/local.py --go go` | 最终代码 PASS，包含真实 main 的 HTTP/SSE、strict/密钥/恢复、后台 SSE 强杀与续传 |
| 前端语法与 API 回归 | 复用 R00/ab0fe95 的五脚本语法与 10/10 API 测试；相应脚本、HTTP 路由、前端接口和 v1 输出签名逐字未变，真实主程序冒烟另已复验 |
| 新增方案/变更请求/记录本地链接 | 全部目标存在 |

`go test ./internal/architecture -run TestVersionedPublicSignaturesHideImplementationTypes -record-foundation-api <仓库外候选文件>` 只用于导出候选快照；先逐项确认所有旧公开声明不变，仅新增批准的 execution/v2 条目后才复制进记录。初次架构检查发现版本路径白名单未登记 execution/v2，已仅追加该已批准入口，原越层/类型检查保持。

本地未运行 Linux race、容器 delivery 或真实 Provider。R01 功能头 push `39141714771`、PR `39141745597`、合并后 master/5938c05 的 `39142273005` 均 check/delivery SUCCESS，覆盖 Linux 全量 race 与容器交付；真实 Provider 仍未运行。

## R02/R03a：运行代次与 WS 机械执行

R03a 范围及修订签名已获用户批准。候选实现已接入本地生产包：L0 `internal/core/websocket` 只处理有界读写/取消/部分写，L1 gateway 映射 execution/v2，L2 transport 仍保留连接容量/消息/期限边界；生产 OpenAI WS 路由尚未注册。RuntimeLeaseSet 已覆盖发布、获取、退休和并发释放，真实 WS 产品消费者仍是下一步 R03b。

R03a 首个候选本地检查（基线 `master/5938c055ab69097adeab460919b4e5c21410939c`，Windows/PowerShell，Go 1.26.3，GOARCH=amd64）：`go build ./...`、`go vet ./...`、`go mod tidy`、`go test -count=1 ./...`、架构/公开签名检查、`git diff --check`、真实主程序恢复冒烟均通过；1267 项测试/子用例 PASS，6 个预期 helper SKIP，32 个测试包 PASS。新增代码清单摘要为 `cfda046bea954af44345032bde8e5b17131bff4d99f25f240384e873a57a42c6`，原始日志和清单在 `D:\my-project\TXAPI-references\validation-r03a`。本地未运行 Linux race、容器 delivery 或真实 Provider。

R03a 新增执行快照只增加 execution/v2 WS 成员，并登记 L0 `core/websocket`；既有 v1/v2 HTTP 事实、L0 HTTP、OpenAPI、schema 和计费行为保持。一次架构快照检查发现新 L0 包缺显式 Version，已补登记并重新通过；一次 lease 溢出测试误把无引用 current 当作退休占用，已改为同时持有两代 lease 后验证拒绝。

### R03a 合并前复核与最终本地检查

2026-10-19，在相同 Windows/PowerShell、Go 1.26.3、GOARCH=amd64、CGO_ENABLED=0 环境集中复核。首个功能头 `3b66cae` 的 push `39144385351` 与 PR #26 `39145020494` 已完整成功，但这些旧头证据不覆盖下述修复。

- core 拒绝无 error 的短写；写调用开始即保守保留可能已交付，确认字节只取合法下界，不把 transport 返回的零下界当作未发送。callback 取消后不再写出或报告成功。
- gateway 拒绝相同服务端 connection ID 的重叠轮次，结束后释放槽；lease 拒绝复用仍被持有的退休 revision，覆盖并发 Release。追加的 `execution/v2.Lease` 是原已批准签名的漏项补齐，原有公开声明不变。
- 新增真实 `httptest` WS 客户端/transport/gateway 集成：同连接两轮、在途 N/N+1、SQLite 身份逐轮刷新与 Key 撤销、重复关闭和容量归零。测试协议不是生产原生 OpenAI WS。

最终实际执行 `go build ./...`、`go vet ./...`、`go mod tidy` 及 go.mod/go.sum 无差异、`go test -json -count=1 ./...`、架构/公开签名检查和 `git diff --check` 均通过：1275 项测试/子用例、33 个测试包 PASS，6 个预期 helper SKIP，零失败。真实主程序 `python -B tests/smoke/local.py --go go` 再次通过，包含普通/SSE/控制台、strict 拒绝、结算、凭据轮换、加密重启及后台 SSE 强杀/恢复/续传/取消/TTL。前端沿用 `3b66cae` 的五脚本语法和 10/10 API 证据，相应脚本和 HTTP 接口未变。只格式化本批 Go 文件。最终 11 个非文档来源文件（含 L2 适配和真实 WS 集成测试）的 LF 归一化清单摘要为 `826a06fe8e0d89698c0240bf01e49b5d2f804271be3a9e2740b6c992921b78b2`；原始日志和 `source-manifest-final.json` 在仓库外 `D:\my-project\TXAPI-references\validation-r03a`。

本批只完成机械 round 与 lease 标识模型，尚未冻结新增契约。R02 的真实可关闭资源、Prepare/Publish/Discard、管理 applied 接线和 drain deadline 尚未实现；不能凭本批内存 lease 与真实测试连接宣称 R02 或原生 WS 产品通过。不运行真实 Provider、不发布镜像或标签。

R03a 最终功能头 `2111467540ef31ee8ad9515be2298d1a5e7d5673` 的 push `39146611206` 与 PR #26 `39146612931` 均 check/delivery SUCCESS，覆盖 Linux 全量 race、主程序恢复冒烟与容器交付。PR #26 已合并为 `master/656362e378be66c6ab1a7cb98e8ddd42ab2c11bf`，远端合并树与功能头完全一致，tree 为 `3f5cb1f046890a1379fa688e70a1aeec47327483`。合并后自动运行 `39147165823` 的 check/delivery 也已 SUCCESS，机械子批的功能头、PR、合并及合并后完整 CI 闭环完成；不扩大为 R02 或原生 WS 产品验收，也不表示新增契约已冻结。

## R03b-1：原生 WS exchange 与真实资源消费者

2026-10-19，用户明确批准 R03b-1，并进一步授权“后续不需要我的批准，你自己比较最优方案进行选择”。本任务后续设计自主比较并记录取舍，不再逐项请求方案或签名批准；兼容、分层、验收和外部证据门槛保持。具体范围见 [R03b 变更请求](REFERENCE-R03B-CHANGE-REQUEST.md)。

本批已在本地实现 L0 单次 exchange、execution/v2 桥、受限上游 Dial、纯 native WS codec 与私有真实资源 owner。受控 host 逐轮刷新身份、校验模型/billing disabled，复用 RPM 和并发准入；每轮独立上游连接，暂拒绝续接和 TPM 限额身份，generate:false 缺少确认终态 fixture 因而明确拒绝。没有新增产品路由、schema、OAuth 或收费。

真实 WS 客户端/host/上游回归证明 response.create、HTTP 推理调用为零、流中 N/N+1 保活与最后 lease 后关闭、下一轮新资源及 SQLite Key 撤销。真实 management.Configuration 与 SQLite 保存/Apply 测试证明 prepare 失败时 desired 已保存、applied 和旧路由/资源保留，重试只发布不重复保存。该接线目前是受控测试组装，main 尚未注册新 owner；不据此宣称生产 R02 或原生 WS 产品已完成。

### R03b-1 最终本地检查

基线 master/321272bb087d6a13439acc990782d0edb1451513。Windows/PowerShell，Go 1.26.3，GOOS=windows、GOARCH=amd64、CGO_ENABLED=0。18 个非文档来源文件的 LF 归一化清单摘要为 68e108c852f6ad1561ea048d3c0623914f57d82768e854e6ba643bc0e6d3ca2d；原始日志、阶段结果和 source-manifest-final.json 位于仓库外 D:/my-project/TXAPI-references/validation-r03b1。

| 实际检查 | 最终结果 |
|---|---|
| go build ./...、go vet ./... | PASS |
| go mod tidy + go.mod/go.sum 无差异 | PASS；没有新增依赖 |
| go test -json -count=1 ./... | 1304 项测试/子用例、34 个测试包 PASS，6 个预期 helper SKIP；另 31 包无测试，零失败 |
| 架构/可见性/精确签名 | 包含在最终全量测试；原声明逐项保留，仅增加 L0 的 3 项和 execution/v2 的 5 项 |
| go list -deps ./internal/core ./internal/protocol/... + 禁止依赖过滤 | PASS |
| gofmt -l 本批 Go 文件、git diff --check | PASS；未格式化无关文件 |
| python -B tests/smoke/local.py --go go | PASS：真实 main 普通/SSE/控制台、strict 拒绝、结算/密钥轮换、加密重启及后台强杀恢复/续传/取消/TTL |
| 前端五脚本语法和 10/10 API 回归 | 复用 R03a/2111467 的适用证据；相应五脚本、API 测试、HTTP 表示层、main 组装和 OpenAPI 与该提交无差异 |

首轮集中通过 1303 项后，复核移除 codec 重复校验、为 prepare 增加 10 秒上界，并补充 prepare/drain 竞争及测试 close-worker join；随后重新执行最终 build/vet/tidy/全量 1304 项检查。主程序冒烟已通过，之后的私有 owner/codec 修复尚未接入 main，HTTP/SSE/background 组装与对应实现未变，因此其证据继续适用；新 owner 的最终行为由全量测试中的真实 WS/管理发布和排空用例覆盖。

R03b-1 功能头 `08c4c28f9cb1cfa4da0b817f9f4ba608667c6501` 的 push `39152357936` 与 PR #28 `39152398277` 均 check/delivery SUCCESS；覆盖 Linux 全量 race、主程序恢复冒烟和容器交付。PR #28 已合并为 `master/0ec80e0139125bf60901f7a9779459bc8a476598`，合并树 `2abc9c873a80ff60b17dfa48a0afcbebe4c954f1` 与功能头完全一致。合并后自动运行 `39152906538` 已 check/delivery SUCCESS。文档 PR #29 合并为 master/b74ef4a，其合并后 39153559035 也已完整 SUCCESS。真实 Provider 未运行，新增契约尚未冻结，未发布标签或 release。

## R03b-2: Native WS Product Integration

Base: master/b74ef4acea70716ebe6c5b9b73c70c5dfcc224c8. Worktree:
C:/Users/mn139/.codex/worktrees/reference-adoption/TXAPI. Original 5506 edits remain untouched.
The user authorized autonomous follow-on design decisions. See
[the six-part implementation decision](REFERENCE-R03B2-IMPLEMENTATION.md) and
[operating guide](docs/reference/responses-websocket.md).

- L1 adds candidate responsesws/v1 and a native session/resource owner; L2 owns
  HTTP upgrade, codec and close status; L3 owns a bounded downstream reader.
  Main/config/OpenAPI/architecture are integrated by this batch only.
- GET /v1/responses is default-off, billing-disabled, credential-encryption-only,
  explicit actual-model allowlist and official OpenAI endpoint only. One
  upstream socket is reused within a session; every round reauthorizes and
  reacquires request/rate/attempt admission. No HTTP inference fallback.
- Continuation uses only the same session's latest completed response ID,
  binding model, channel, credential material/generation and resource revision.
  Foreign/old/reconnected/rotated handles reject before upstream send.
- TPM continuation estimation includes verified prior total usage; uncertain
  dispatch retains its estimate, known non-dispatch refunds. Parent admission
  and mutable token reservations are not inherited by a new round.
- Management prepare failure preserves saved desired state, old applied route
  and pinned resources; retry Apply publishes the saved snapshot. Main stops
  and joins sessions/transports before closing shared dependencies.
- OpenAPI 0.6.0 adds one GET operation and six native WS schemas. All 92 old
  operations and old schemas are unchanged. Public snapshot adds ten declarations
  in responsesws/v1; all old declarations remain exact. Schema stays v15.

### Final Local Evidence

Windows/PowerShell, Go 1.26.3, GOOS=windows, GOARCH=amd64, CGO_ENABLED=0.
Final source manifest: 29 non-Markdown files, LF-normalized SHA-256
`79e7f0f425b788f851188689b40d1c52374fe115d2728ad8f48b77888ec7460d`.
Manifest and raw logs: D:/my-project/TXAPI-references/validation-r03b2.

| Executed check | Result |
|---|---|
| go build ./...; go vet ./... | PASS |
| go test -json -count=1 ./... | 1333 tests/subtests, 35 packages PASS, six expected helper SKIP; 33 packages without tests |
| go mod tidy; unchanged go.mod/go.sum | PASS; no added dependency |
| core/protocol dependency filter; architecture/signature/OpenAPI checks | PASS |
| touched-Go gofmt; git diff --check | PASS; unrelated Go files untouched |
| five Node syntax checks; node --test web/assets/api.test.mjs | PASS, 10/10 API tests |
| python -B tests/smoke/local.py --go go | PASS, including feature-off WS 404 and existing forced-kill/recovery cases |

The final native session tests additionally prove fresh rate admission, bound
continuation estimation and no refund of uncertain dispatch. A temporary test
edit duplicated helper declarations; it was consolidated before the final
build/vet/full run. The ordinary smoke and controlled WS assembly are separate
evidence; neither proves the real OpenAI endpoint or official SDK.

### Exact Head CI And Merge

Functional head: `81d2afe6cc31531665a63f4cf09dbd0662b3c330`, based on
master/b74ef4a. PR #30 merged to `master/a5c7a5f5286175976cb37fd762d94d02810cc794`.
Remote merged tree and functional-head tree are identical:
`38c2624664c572cfefbdd4be2bee68f102b2788f`.

| Exact source | Automatic run | Result |
|---|---|---|
| Functional push / 81d2afe | 39164752515 | check/delivery SUCCESS |
| PR #30 / 81d2afe | 39164788204 | check/delivery SUCCESS |
| Merged master / a5c7a5f | 39165344718 | check/delivery SUCCESS |

All three accepted runs include Linux full race, coverage report,
main recovery smoke and container install/upgrade/backup/restore. The container
workflow also exercises the disabled WS 404 through the shared console fixture.
The controlled native WS assembly is covered by Go race tests; real Provider
and official SDK compatibility remain unverified. No workflow was manually
redispatched, disabled or weakened. New responsesws/v1 remains a candidate;
this is not a full A04 or foundation-wide new freeze.

No real Provider or official SDK test, release, tag, OAuth, billing, persistent
reconnection or multiplexing is claimed. Full A03/A04 and R00-R07 stay open.

## R06a: Background Task Console

Base: master/2a6d162decaed5343322c13ae688c89f73408dc5 (PR #31 documentation
merged; master run 39181190626 SUCCESS). The original 5506 worktree is untouched.
See [six-part decision](REFERENCE-R06A-IMPLEMENTATION.md) and
[operating contract](docs/reference/background-console.md).

- L3 taskconsole consumes new candidate taskcontrol/v1 plus existing session
  and rate ports. L2 adds five session-protected metadata/control operations.
  Store rechecks active session/user/admin role inside resource transactions.
- Owner list/detail and revision-CAS cancellation; administrator scope is
  read-only across owners. No content/decrypt/provider-handle/claim-secret API.
  Creation-Key revocation does not prevent owner cleanup of already-bound work.
- Cancellation reuses the existing locked mutation/worker. Queued tasks stop
  before send; remote tasks use bound cancellation; uncertain stays uncertain.
  No redispatch, financial refund, second worker or migration. Schema remains v15.
- OpenAPI 0.7.0 / 68 paths / 98 operations: all 93 old operations and schemas
  remain exact. Public snapshot only appends 14 taskcontrol/v1 declarations;
  all old declarations remain exact. The new port is not frozen.
- Console paging, filters, exact string revisions, CAS refresh, owner/admin
  metadata and explicit cancellation preserve existing session navigation.

### Final Local Evidence

2026-10-19, Windows/PowerShell, Go 1.26.3, GOARCH=amd64, GOOS=windows,
CGO_ENABLED=0. Twenty-two non-Markdown sources, LF-normalized SHA-256
`ea745f2acabbe3b5ddb06fd62a21cb01025e83ab550fa1177ce1b3b5cb45753a`.
Manifest/raw logs/screenshots: D:/my-project/TXAPI-references/validation-r06a.

| Executed check | Result |
|---|---|
| go build ./...; go vet ./...; go mod tidy and unchanged modules | PASS |
| go test -json -count=1 ./... | 1345 tests/subtests and 36 packages PASS; six expected helper SKIP |
| architecture/public signatures/OpenAPI and dependency filter | PASS |
| touched-Go gofmt and git diff --check | PASS; unrelated Go untouched |
| seven Node syntax checks; node --test web/assets/api.test.mjs | PASS, 10/10 API cases; unchanged-source evidence reused; changed live browser script syntax rechecked |
| browser tasks.cjs, navigation.cjs, billing.cjs | PASS on local Chromium, desktop/mobile; reused unchanged-source evidence |
| python -B tests/smoke/local.py --go go with TXAPI_TASK_BROWSER=1 | PASS, including actual-main task console desktop/mobile cancellation, old HTTP/SSE/strict and forced-kill/background recovery |

SQLite tests cover owner isolation, role demotion/logout/disable, explicit
metadata SELECTs excluding content, pagination and concurrent CAS. A real
background worker cancels queued/remote tasks after creation-Key deletion
without another submit. Browser cases cover failed-page retry, revisions above
2^53, stale conflicts without automatic POST retry, administrator read-only and
late detail response disposal. The actual-main browser uses isolated credentials
and loopback upstream only; screenshots were visually inspected outside Git.

The first full run exposed a snapshot serialization mismatch, not a changed old
declaration: Python JSON output differed from Go's byte-reviewed snapshot. The
Go-emitted candidate was compared semantically against all old declarations,
then copied byte-for-byte. Architecture and the final full suite passed. Initial
UI regressions found a long-ID mobile overflow; scoped wrapping fixed it before
the final browser/main smoke. Temporary test compile/fixture naming errors were
fixed before final verification; no old tests or CI gates were removed.

Pre-commit security review reproduced a direct repository bypass for a
soft-deleted user whose active status and session remained: Table/Scan did not
apply the User model's soft-delete scope. The transaction now explicitly
requires users.deleted_at IS NULL. The new regression rejects owner/admin
list/detail and owner cancellation without changing the task revision or intent.
After the fix, directed TaskControl tests, build/vet, full Go tests, tidy/module
cleanliness, dependency checks, touched-Go formatting and the actual-main live
browser/recovery smoke were rerun and passed. Final raw logs are
tests-softdelete-final.jsonl and smoke-softdelete-live.log; the manifest is
source-manifest-softdelete-final.json in the validation directory above.

The prior local source manifest
b72c60ff4b547a8e07aa448fb62058e0dec57e08531ec47bbe29cfe61a1571a0
differs only in internal/store/task_control.go and its test. Node syntax/API
and mock browser results therefore remain applicable: their scripts, web
assets, HTTP contracts and public signatures are byte-identical after LF
normalization. The actual-main smoke additionally revalidated the final server.
Static checks confirmed all 93 old operations/schemas and 20 public package
declarations unchanged, three smoke Python ASTs valid and 129 local links in
the six batch Markdown files resolving. No extra workflow run was dispatched.

The first committed head e11ce9d exposed a smoke fixture ordering race in push
run 39187167410 and PR run 39187195359: their Linux race suites and container
delivery passed, but the
console-cancel scenario asserted exactly three dispatches without waiting for
the third task to leave queued state. A fast cancellation is allowed to prevent
that dispatch. The fixture now waits for remote_active before testing remote
cleanup, verifies exactly three posts before cancellation and exactly two remote
cancels afterwards; queued no-send cancellation remains covered by the real
worker regression. No production code or CI gate changed for this correction.
The final script passed both the default headless path and the optional
desktop/mobile browser path. Logs are smoke-ci-order.log and
smoke-ci-order-live-final.log; source-manifest-ci-order-final.json records the final
digest. The complete Go/build/vet/contract evidence is reused from e11ce9d
because every Go file, module, public declaration and HTTP contract is exact.
One optional browser rerun timed out with the old name-only error reporter;
the detailed diagnostic repeat passed. The live test now reports its stack and
handles worker revision conflicts with at most three explicit user clicks,
waiting for each 409 refresh before another action. It does not catch unrelated
errors or add automatic product POST retries. This final live script's syntax
and actual-main browser/recovery workflow passed; mock browser/API sources
remain unchanged and retain their earlier evidence.
The final automatic push/PR CI still has to accept the corrected head.

### R06a Integration

Final head 8edb499df2c0c781c691bd31435aff87dc4623ec passed complete automatic
push 39188152830 and PR 39188155828 (check/delivery SUCCESS). This includes
Linux full race, the corrected actual-main smoke and container installation,
upgrade/recovery and shutdown. No workflow was manually redispatched.
PR #32 merged as master/3d55065d9fcf63265aa0b7576c22c118d9964371, tree
2fdefaa9eedba9a5b6aab5dc37d4c0cbca16ae38, exactly matching the tested head.
Independent merged-main run 39188756506 also passed check/delivery SUCCESS.
R06a's exact-head, merge-tree and post-merge gates are complete.

No real Provider, official SDK, billing expansion, OAuth, external Runtime, tag
or release is claimed. R06's other pages and the full R00-R07 goal stay open.

## R06b: Recent HTTP Routing Diagnostics

Started on the tree-verified merged R06a baseline master/3d55065. Scope,
specific query interfaces, authority, bounded observation, compatibility and
rollback are recorded before implementation in
[R06b decision](REFERENCE-R06B-IMPLEMENTATION.md). This is a separate functional
batch and carries the verified R06a integration record without repeating
unchanged Go checks merely for documentation. Its new sources require their
own concentrated local verification and exact-head full CI before merge.

### R06b Local Verification

2026-10-19, Windows/PowerShell, Go 1.26.3, GOARCH=amd64, CGO_ENABLED=0.
Base remains master/3d55065. The 28 changed/new non-Markdown source files have
LF-normalized manifest digest
`88c854dabfae415122a24830e9a97908e73cb77406f458518e31ebb73b420e46`.
Raw logs, manifest and screenshots are outside Git in
`D:\my-project\TXAPI-references\validation-r06b`.

| Actual Command / Check | Result |
|---|---|
| go build ./...; go vet ./... | PASS |
| go test -json -count=1 ./... | 1369 tests/subtests and 37 test packages PASS; six expected helper SKIP |
| go mod tidy; module cleanliness; core/protocol dependency filter | PASS |
| architecture, public signature and HTTP contract tests | PASS in full suite; all 21 old public package declarations and all 98 old HTTP operations/schemas remain exact |
| gofmt -l on 19 touched Go files; git diff --check | PASS; unrelated Go files untouched |
| seven Node syntax checks; node --test web/assets/api.test.mjs | PASS; 10/10 API cases |
| node tests/browser/routing.cjs | PASS, desktop/mobile owner/admin projections, failed-page retry, disabled state and delayed detail disposal |
| additional external browser probes | PASS at 1440/390/320px, bounded HTML-like/long metadata, ordinary-user scope and delayed administrator list/detail after switching to owner |
| python -B tests/smoke/local.py --go go with TXAPI_ROUTING_BROWSER=1 | PASS, actual-main owner/admin routing console desktop/mobile plus existing strict, encryption, forced-kill and native background recovery |
| Python AST, existing OpenAPI equality and batch Markdown links | PASS |

Directed behavior includes concurrent equal Request IDs without shared scope,
TTL/eviction/copies, no recording for non-HTTP or nested consumers, preflight/
admission/budget rejection without dispatch, retries, partial SSE failure and
cleanup panic preserving committed facts. Real SQLite authority tests reject
logout, current demotion, disabled and soft-deleted users; creation-Key deletion
does not erase authorized historical metadata. A read-only subagent backend
review found no actionable issues. The frontend subagent strengthened the
delayed-response regression to require an actual held request before navigation.

The initial live browser fixture logged in once per role and viewport, exceeding
the existing five-login/minute gate after the API setup logins. It now logs in
once per role and reuses that real session across both viewports, with an explicit
login status assertion. The gate and product behavior are unchanged. Final live
smoke passed in smoke-live-final.log. Full Go/build/vet evidence remains applicable
because only the two browser regression scripts changed afterwards; their syntax
and final mock/live behavior were revalidated. Screenshots were visually inspected.

Local Linux race, Docker delivery, real Provider and official SDK checks were not
run. Automatic exact-head CI and merge are still pending; this local evidence
does not mark full R06 or the R00-R07 objective complete.

### R06b Integration

Final head d12123d41dfc5110db85ec108c0a8f66a1d3b3a8 passed complete automatic
push 39191699174 and PR 39191725219 (check/delivery SUCCESS). PR #33 merged as
master/deb9e211c338288b288addb8d710617cab182590; tree
4cefcdd1ab10f2f7a809eed43b0a1c81b2d7b94d exactly matches the accepted head.
Independent merged-main run 39192304586 also passed check/delivery SUCCESS.
R06b's exact-head, merged-tree and post-merge gates are complete. No manual
redispatch, real Provider/SDK, release or full-goal claim.

## R02 Runtime Cleanup Recovery

A post-R06b requirements audit found an actual runtime recovery gap: failed
candidate/retired Close references were retained correctly, but production Apply
never invoked the retry path. The next independent batch repairs this before
R06c presentation work. Exact ownership, compatibility and acceptance requirements
are recorded before source changes in
[R02 cleanup decision](REFERENCE-R02-CLEANUP-IMPLEMENTATION.md). Existing real WS
resources and publication are already implemented through R03b; historical
statements about missing R02 resources must not be read as current conclusions.

### R02 Cleanup Final Local Evidence

2026-10-19, Windows/PowerShell, Go 1.26.3, GOARCH=amd64, CGO_ENABLED=0.
Base is master/deb9e211c338288b288addb8d710617cab182590; its independent
post-merge check/delivery has now passed. Six changed/new Go source files have
LF-normalized manifest digest
`ef70e5f0c97e240a807d20de036cde35178a06866ca347cf870d755973a7f9f2`.
Manifest and raw final tests/smoke logs are outside Git in
`D:\my-project\TXAPI-references\validation-r02-cleanup`.

| Actual Command / Check | Result |
|---|---|
| gateway publication/resources/session/management directed tests, -count=3 | PASS after reproducing old Publish failure (expected two cleanup calls, observed one) |
| main TestResponsesWSMainJoinRetriesCloseBeforeSharedDependencyTeardown | PASS using real WS assembly/runtime and SQLite |
| go build ./...; go vet ./... | PASS |
| go test -json -count=1 ./... | 1376 tests/subtests and 37 test packages PASS; six expected helper SKIP |
| go mod tidy; module cleanliness; core/protocol dependency filter | PASS |
| architecture/public signatures/OpenAPI | PASS in full suite; signatures and HTTP contract also byte-equal to accepted d12123d |
| six touched Go files gofmt; git diff --check; batch local links | PASS |
| python -B tests/smoke/local.py --go go | PASS, including strict/encryption, forced kill/pending recovery and native background/SSE recovery |
| Node/API/mock and actual-main routing browser evidence | Reuse accepted d12123d/R06b: web assets, browser tests, API tests and HTTP contracts are unchanged; final server rebuilt and smoke revalidated |

The implementation retries eligible failed cleanup through serialized production
Publish, waits for an existing worker within the finite cleanup context, and
keeps ownership on failure/timeout. New tests prove candidate and retired
recovery, current/pinned preservation, factory exclusion during uncertain cleanup,
partial prepare cancellation, concurrent duplicate Release and short Close
deadline with callback/lease retention. Main's retry test blocks the second Close
and proves dependency teardown cannot advance while SQLite remains usable.
No production main change, new worker, public contract, schema or financial
behavior was required. The requirement mapping is in the decision document.

Local Linux race/Docker and real Provider/SDK were not run. Automatic exact-head
full CI, merge-tree verification and post-merge results remain to be recorded;
the local correction is not a candidate-contract freeze or full-goal completion.

### R02 Cleanup Integration

Final head 135e6578a3d98e7412254564fa1efc81cf5bd8ce passed complete automatic
<<<<<<< HEAD
push 39193105666 and PR 39193153559 check/delivery. PR #34 merged as
master/252c3792de2786045500073f84a2161e6c911e54 with the exact accepted source
tree. Independent post-merge 39193688299 also passed check/delivery.
No manual redispatch or new Provider/SDK/financial acceptance is implied.

## R05a-1 Native Token Breakdown Observations

The user prioritized backend completion and deferred frontend. The fully local
R06c presentation work is checkpointed at feat/reference-r06-ledger/28fc3ec;
it has no push/PR/merge. Later frontend batches should prioritize codex-proxy-rs
structure, interaction and suitable mature implementation. OAuth's real-account
gate stays closed at the user's explicit request.

R05a-1 starts from accepted master/252c379 and implements the six-part
[decision](REFERENCE-R05A-IMPLEMENTATION.md). It adds candidate execution/v3,
owned host snapshots, optional native Responses JSON/SSE token breakdowns and
an independent L3 consumer with current real identity refresh. All 22 existing
registered package declarations stay exact; the candidate adds ten declarations.
L0, HTTP operations, schema v15, financial permissions and frontend are exact.
Source adaptations from fixed codex-proxy-rs events.rs retain their license and
[specific attribution](THIRD_PARTY_NOTICES.md). No sub2api source/dependency is copied.

Quantities distinguish unknown from explicit zero, validate strict nonnegative
integers and checked base/child bounds, and retain cumulative high-water marks
across unknown snapshots. Historical children constrain a later explicit parent;
old parents never bound a later child when the current parent is absent. Both
wire projections must agree on base quantities. V3 failure never falls back to
another decoder or duplicates delivery. Returned projections do not alias.
The existing terminal base projection alone can enter existing accounting;
details do not open new prices or charging. Native WS stays on its v2 path.

First complete local pass: go build/vet, tidy/module/dependency checks, full
ordinary suite (1459 tests/subtests, 38 packages, six expected helper SKIP) and
actual-main smoke passed. Final review found one additional impossible partial
snapshot case, cached=5/total=3 with unknown input. Validation and the host now
also check known child lower bounds against partial totals without synthesizing
missing parents. Directed contract/protocol/independent-consumer checks passed;
the final affected full checks are rerun before committing. The first pass is
not treated as final-code acceptance. Raw logs and manifests remain outside Git
under D:\my-project\TXAPI-references\validation-r05a-1.

### R05a-1 Final Local Evidence

2026-10-19, Windows/PowerShell, Go 1.26.3, GOARCH=amd64, CGO_ENABLED=0.
Latest fetched origin/master equals base 252c3792de2786045500073f84a2161e6c911e54.
The 18 changed/new non-Markdown sources have LF-normalized manifest digest
`f86b0a21443267e05f4fb6f01d7ad07bb1c32ec2820ad6710c5bfb7e5d2d4f94`.
Final logs/manifests are external in the directory above, not shipped assets.

| Actual Command / Check | Final Result |
|---|---|
| go build ./...; go vet ./... | PASS after partial-total correction |
| go test -json -count=1 ./... | 1476 tests/subtests and 38 test packages PASS; six expected helper SKIP |
| go mod tidy; module cleanliness; core/protocol dependency filter | PASS on first pass; reused because imports/modules are exact after arithmetic-only correction |
| architecture, recursive visibility, signature snapshot and HTTP contracts | PASS in final full suite; all 22 old registered packages exact, ten candidate v3 declarations added |
| gofmt -l on 15 touched Go files; git diff --check | PASS; no unrelated Go formatting |
| JSON/OpenAPI equality, core source equality, batch Markdown targets | PASS; 132 local file targets resolve |
| python -B tests/smoke/local.py --go go | PASS after correction, including actual-main strict/encryption, credential rotation, forced kill/recovery and native background/SSE |
| Node/API/browser evidence | Reuse accepted d12123d/R06b: all web assets, API tests, browser scripts and OpenAPI are unchanged; final server rebuilt and actual-main smoke revalidated |

Directed coverage includes native JSON/SSE unknown/null/zero/invalid integers,
all containment and transitive lower bounds, cross-event/cross-attempt snapshots,
six cumulative high-water marks, explicit old projection equality, exact single
decoder/delivery selection, failed v3 without fallback, deep copies, retries,
cancel/short-write/panic and real SQLite identity revocation in the independent
L3 consumer. A separate read-only agent review found no remaining actionable
gateway/consumer issue. Native base fields retain the old machine-int range;
full int64 coverage is accepted on amd64. Enclosing containers retain Go JSON
compatibility case matching; new nested child lookup is exact, without aliases.

An initial directed gateway compile found an unused test import, fixed before
the passing runs. An initial API snapshot byte check found HTML-escape formatting
lost by JSON reserialization; semantic comparison showed every old declaration
exact, and the exact Go-generated candidate bytes were copied before final
architecture/full checks. Neither failed intermediate result is called accepted.

Local Linux race/Docker and real Provider/SDK were not run. Exact-head automatic
full CI, merge-tree and independent post-merge results remain separate gates.
Pricing history, durable observations, new management APIs and R05b cost/charged
background remain later backend batches; R05a-1 does not complete all R05 or R00-R07.

### R05a-1 Exact-Head Integration And Main Failure

Final head a440a4d006335548aece183d93a4d892eb6d5314 passed complete automatic
push39196504025 and PR39196509658 (check/delivery SUCCESS). PR #35 merged as
master/5998e43a7e80a32aa755bd81980bf2b7889c9130; tree
ba32cc587716055ad5237ac81a8b1fea561b3a19 exactly equals the accepted head.

Independent merged-main39197079265 passed delivery but failed the existing
generation race regression TestArchiveHandoffDoesNotHoldShutdownLockAndCloseJoinsStorage
with "canceled storage operation committed: 1". It is not a green post-merge
result and no manual redispatch was used to conceal it. Inspection found that
Runner preparation cancellation depended on an asynchronous context.AfterFunc;
an already-canceled Close waiting context could return before storage itself
was canceled. The independent lifetime correction is defined before changes in
[the shutdown decision](REFERENCE-ARCHIVE-SHUTDOWN-IMPLEMENTATION.md).
Raw failure evidence is external in validation-r05a-1/ci-postmerge-failed.log.

R05a-2 versioned selling quotes were started next, with an explicit decision,
candidate contract, immutable format2 calculation, current-admin L3 service,
strict string-count HTTP adapters and startup-only wiring. Directed billing,
quoteconsole and API/config checks passed; full integration was not complete.
This work is preserved only locally at feat/reference-r05a-quotation/2c42b11,
without push/PR. It resumes from the corrected main after shutdown acceptance.
R06c remains local/deferred. No durable usage, charging or OAuth gate is opened.

## Archive Preparation Shutdown Correction

Scope and unchanged public/lifetime boundaries were recorded before source in
[the shutdown decision](REFERENCE-ARCHIVE-SHUTDOWN-IMPLEMENTATION.md). The runner
now registers preparation cancellation with pending admission under its shutdown
mutex and synchronously cancels all registered preparations before Close returns.
Storage I/O and join stay outside that mutex; the two-second budget still starts
at storage, not parsing. Successful handoff atomically transfers pending to the
queue. A unified defer releases registration/pending on errors and Parser/Model
panic, with submissions.Done last so a failed archive finish remains joined.
Existing observer values/deadlines and the single upstream execution are retained.

### Reproduction And Final Local Evidence

2026-10-19, Windows PowerShell, Go1.26.3, GOARCH=amd64, CGO_ENABLED=0.
Latest fetched origin/master equals base5998e43a7e80a32aa755bd81980bf2b7889c9130.
Only runner.go and archive_test.go are changed Go sources, normalized manifest
digest `8436da9fd73b9c994027d7ffaa1002f3da0ef7d25850c59e2a26928fb5ac03f0`.
Raw full-check logs and the source manifest are external in
D:\my-project\TXAPI-references\validation-archive-shutdown. Agent-directed
command/result summaries are explicitly summaries, not invented raw logs.

| Actual Command / Check | Result |
|---|---|
| strengthened old TestArchiveHandoffDoesNotHoldShutdownLockAndCloseJoinsStorage, -count=30 -timeout60s | Expected FAIL: repeated Close returned with uncanceled storage context; 7.649s |
| four final shutdown/admission/failure groups, -count=30 -timeout90s | PASS, 53.109s; the command hard cap covers repeated tests, product storage budget stays two seconds |
| go build ./...; go vet ./... | PASS |
| go test -json -count=1 ./... | 1486 tests/subtests and 38 test packages PASS; six expected helper SKIP |
| gofmt -l two touched Go files; git diff --check; batch local links | PASS; 83 local Markdown file targets resolve |
| old public/architecture/OpenAPI/core source/module equality | Exact; final full suite includes boundary/visibility/signature/HTTP tests |
| module/dependency and frontend/browser evidence | Reuse accepted a440a4d/R05a-1: no module/production import or contract/web/browser changes; final build and full tests rerun |

Final `python -B tests/smoke/local.py --go go` PASS, including actual main,
strict/encryption/credential rotation, forced-kill recovery and native background
SSE/cancel/TTL. Linux race/container delivery, exact functional head, merge tree
and independent merged-main result remain CI/integration gates, not local claims.

Regression coverage includes eight simultaneous Close calls, two preparations,
external parent shutdown after observing owner stop, pre-canceled and post-close
admission, ParseRequest/Model panic, parser/storage failure and storage panic,
registry/pending cleanup, capacity reuse and later successful Close join. The
original shutdown-lock, join and zero-row assertions remain. Independent read-only
review found no actionable issue. Already committed observations are not deleted,
and cancellation is not claimed globally atomic with database commit.

### Archive Shutdown Integration

Final head `9bca697ce6310c2f662ee69ff37168fe3d0f977d` passed complete push
39198978768 and PR39199006870 check/delivery. PR #36 merged it as
`master/c14178dec7d57aeabacf43da53c0ac6e16989cc6`; the two trees are equal.
Independent post-merge39199597911 also passed complete check/delivery,
including Linux full race, actual-main recovery smoke and container delivery.
No workflow was manually repeated and no image/tag/release was published.

## R05a-2: Versioned Dimensional Selling Quotations

The backend-first batch resumes on accepted master/c14178d. The original local
2c42b11 checkpoint was cherry-picked as eaba025 before final integration; neither
checkpoint is accepted evidence. Scope and exact candidate Reader/Service/API
signatures were recorded before implementation in the
[six-part decision](REFERENCE-R05A2-IMPLEMENTATION.md). Running configuration,
arithmetic, unknown quantities, history, permissions and rollback are described
in the [selling quotation guide](docs/reference/usage-quotations.md).

L1 billing compiles canonical format2 dimensional documents and persists them
before publication. The new bounded store reader verifies exact byte size and
digest. It retains old format1 payloads unchanged. L3 quoteconsole consumes
registered current-session/shared-admission ports; the two new administrator
routes are read-only hypothetical calculations, with canonical decimal string
quantities and amounts. The real L3 facts sample captures a price version before
one JSON/SSE generation and quotes the actual model/group afterward. It rejects
ambiguous retry bindings without repeating the already executed generation.

No new schema, L0 source, balance/hold, strict reservation price, old settlement
algorithm, original HTTP operation/schema, existing public signature or frontend
is changed. Quotations are separate from committed charges and Provider spend.
The new quotation/v1 is candidate, not frozen; full R05a/R05b/R00-R07 remain open.

### R05a-2 Final Local Evidence

2026-10-19, Windows PowerShell, Go1.26.3, GOARCH=amd64, CGO_ENABLED=0.
The 25 changed/new non-Markdown source manifest has LF-normalized digest
`f0fa3dea1edff934aa5c442fb0a2092531fff71ca67fe16b780e97a4e8cc1d36`.
Logs/manifests are external in D:\my-project\TXAPI-references\validation-r05a2.

| Actual Command / Check | Final Result |
|---|---|
| go build ./...; go vet ./... | PASS |
| go test -json -count=1 ./... | 1533 tests/subtests and 39 test packages PASS; six expected helper SKIP |
| go mod tidy; module diff; core/protocol dependency filter | PASS; no new third-party module |
| architecture/visibility/public signatures and HTTP contracts | PASS in complete suite; all 23 previous packages and all 102 previous operations/schemas remain exact; one candidate contract and two operations appended |
| touched-Go gofmt; git diff --check; batch Markdown links | PASS; only batch Go files formatted, all referenced local targets resolve |
| final python -B tests/smoke/local.py --go go | PASS, including actual quotation main composition, admin/owner permissions, exact amount28/unknown, existing strict/encryption/rotation and forced-kill/background recovery |
| default-disabled actual-main shared console workflow | PASS after the fixture correction; empty quotation metadata, calculation503, owner403 and original console path |
| independent real-main HTTP probe | 30 rounds of full-body owner POST403, owner GET403 and administrator quote28 PASS; no retries |
| frontend syntax/API/browser | Reuse accepted d12123d/R06b: web assets, browser scripts, API tests and all pre-existing HTTP operations are exact; final actual-main smoke rerun |

Focused SQLite/facts commands were run by agents and are recorded as command
summaries, not invented raw logs: `go test ./internal/store -run '^TestQuotationHistory'
-count=1` passed in2.987s; the two quotation consumer tests in
`./internal/examples/facts` passed in 8.408s. The latter publish N+1 from the
actual upstream, retain N's quote44 versus explicit N+1 quote170, verify one
wire/upstream execution, reopen the database and preserve nonzero used/reserved
balances and an existing reservation. The final complete suite includes both.

The first formatting check detected CRLF restored by the checkpoint cherry-pick;
gofmt corrected only batch files and all normalized tested source hashes stayed
equal. The first Windows smoke hit ConnectionResetError when urllib forced
Connection:close while ordinary-user authorization rejected an unread POST body.
The isolated old transport reproduced it; ordinary keep-alive real HTTP passed
30 complete-body rounds. The new smoke authorization negative now sends an empty
POST and still requires 403; complete-body unit/probe coverage remains. No network
retry or shared authorization/transport change was added. This smoke-only change
was followed by the final passing complete smoke; Go sources remain those of
the passing complete suite. Linux race/container delivery and final-head merge
are still the existing CI gates, not local claims. A final independent read-only
backend review found no actionable issue; it did not run additional tests.

The first pushed head f770c56 exposed a shared smoke-fixture error: both delivery
jobs in push39201155517/PR39201190081 expected enabled selling quotations in the
default-disabled container install. Full race passed, but those heads are not
accepted. `control_workflow` now takes an explicit default-false expectation:
the enabled local-main fixture checks amount28/unknown, and the ordinary delivery
fixture checks disabled metadata and calculation503. Neither fixture changes
production defaults. The corrected complete enabled smoke and an independent
default-disabled actual-main console workflow both passed. All Go/module/API
source hashes remain exact to the passing full suite; only the Python consumer
changed. This correction requires the new final head's full automatic CI,
including Linux race and container delivery, before merge.

### R05a-2 Integration

Corrected final head `f794dfa` passed complete automatic push39201855006 and
PR39201860012 check/delivery. PR #37 merged as
`master/b30bd5760bfd9522709cd84a20baa7c97da21b9f`, with an identical source tree.
Independent merged-main39202515426 also passed complete check/delivery, including
Linux full race, actual-main smoke and container installation/upgrade/recovery.

## R05a-3: Durable Invocation And Attempt Usage Metadata

The backend-first batch builds on accepted master/b30bd57. Its six-part scope,
candidate Recorder/Repository/Service and synchronous admission/failure policy
were recorded before implementation in
[R05a-3](REFERENCE-R05A3-IMPLEMENTATION.md). Operational behavior, exact nullable
quantities, session permissions, retention and rollback are described in the
[usage history guide](docs/reference/usage-history.md).

L1 gateway records trusted foreground invocations through the existing execution
path. The store appends migration16 and two bounded metadata tables, reusing the
existing current-session transaction checker. L3 usageconsole consumes the new
candidate usagehistory/v1 and existing current-session/shared-rate ports. OpenAPI
0.10.0 appends four read-only owner/administrator operations; all104 previous
operations/schemas and all24 previous public package declarations remain exact.
No L0, Provider decoder, old price format, balance/hold owner or frontend changes.

Capture defaults off and is accepted only with billing disabled. Native
background, claimed durable execution and native WS are excluded by trusted
internal source. Unknown counts remain null, exact zero remains "0", and a
selling quotation digest is pinned before execution when available. Capture
never derives v3 facts from old billing estimates or replays generation to
repair persistence. Expired live records remain counted until orphan recovery
or an ordinary terminal write; startup marks uncertain records interrupted.

Focused real SQLite/fake-upstream and actual-main tests exercise current
permissions, exact counts, distinct retry target/group bindings, quotation
publication/restart and unchanged nonzero financial state. Gateway tests kill
real child processes at three controlled persistence points; the separate
actual-main smoke kills after its first delivered SSE chunk and explicitly
does not claim the other two kill points. Final complete local and exact-head
Linux/container acceptance are recorded separately after execution.

The independent read-only review found a panic-path metadata defect: a current
attempt that never returned could leave the invocation summary with the default
500 or an earlier retry's result. The focused correction uses the conservative
in-flight host facts while preserving actual delivery state and returned facts;
it does not change the legacy execution Outcome. This includes pre-dispatch
adapter and metadata handoff panics, while ordinary returned400/503 and a
finishRequest panic after success preserve actual returned facts. Final
verification follows the corrected source. New contracts remain candidate; full R05a/R05b and the
overall reference-adoption goal are not declared complete.

### R05a-3 Final Local Evidence

2026-10-19, Windows PowerShell, Go1.26.3, GOARCH=amd64, CGO_ENABLED=0.
The41 changed/new non-Markdown source manifest has LF-normalized digest
`b208ea58dd86d673dda57860a40a4f12e950cd79487e222769077ce9bf8642b0`.
Raw logs, manifests and helper results are external in
D:\my-project\TXAPI-references\validation-r05a3.

| Actual Command / Check | Final Result |
|---|---|
| go build ./...; go vet ./... | PASS |
| go test -json -count=1 ./... |1600 tests/subtests and40 test packages PASS; seven expected subprocess helper SKIP;37 packages without tests |
| go mod tidy; unchanged modules; core/protocol dependency filter | PASS; no added third-party module |
| architecture/visibility/public signatures and HTTP contracts | PASS in final complete suite; all24 prior public packages and104 prior operations/schemas remain exact; one candidate package and four read operations appended |
|34 touched-Go gofmt; git diff --check; Python AST/JSON and batch Markdown links | PASS; unrelated source not formatted;137 local links resolve |
| python -B tests/smoke/local.py --go go | PASS; actual-main JSON/SSE/session isolation, sealed restart, first-chunk kill without replay, capture disabled with retained reads, unchanged financial state and all existing strict/encryption/background recovery scenarios |
| frontend syntax/API/browser | Reuse accepted d12123d/R06b evidence: web assets, browser scripts and every previous HTTP operation/schema are exact; final main smoke is independently rerun |

The final directed gateway recording command passed in4.462s after the complete
panic correction. Earlier risk/facts/three-process-kill combinations and the
full initial1595-test source passed before that correction; their logs/manifests
are preserved with before-final-panic-fix names and are not used as evidence
for later source. The corrected source was rebuilt, vetted and completely tested
again. A final independent read-only review found no actionable issue; the later
capacity correction also received its own independent read-only review.

Actual store owner command summaries (not invented raw logs): the complete store
package passed in21.330s, upgrade/usage groups in7.702s and its later additional
Begin/MayDispatch failure test in2.360s. The final complete suite includes them.
The real independent L3 consumer covers JSON/SSE missing/null/zero/full/large
and breakdown-only observations, one wire per generation, N-to-N+1 selling
publication, owner/admin/revoked identity behavior, reopening SQLite and exact
preservation of pre-existing financial state. No real Provider was called.

Final automatic Linux race, main smoke, schema16 container upgrade/backup/restore,
exact-head acceptance, merge-tree comparison and independent main CI remain
integration gates. No workflow trigger, old regression or financial rule was
removed or weakened. No image, tag or release is published by this batch.

Before merge, head f406503 passed complete automatic push39206390975 and
PR39206431769 check/delivery, but a separate capacity audit found a missing
retention-progress regression. Begin deleted at most1000 expired parents inside
the admission transaction; a subsequent owner-capacity rejection rolled those
deletions back. An older unrelated owner's records could therefore prevent a
full owner from ever progressing to its own expired rows. The regression on
the old code failed in2.588s with1002 parents/children still retained.

The focused fix commits successful bounded cleanup on a capacity rejection,
then returns ErrCapacity without admitting the invocation. Current Key/model
authorization and the SQLite write lock remain in the same transaction; actual
SQL/insert/commit failures retain rollback/error precedence. The new test proves
the first rejected pass removes1000 expired parents/children, the next request
removes the remaining two and admits one new parent, and an injected insert
failure still rolls cleanup back. Existing nonzero balances/holds, price
history, users and Key projections remain exact. The new directed test passed
in2.489s and all UsageHistory groups in7.139s. These are owner command summaries,
not invented raw logs. The full f406503 results are preserved separately and
do not accept the corrected source; final local and automatic CI run again.

## R05a-3 Final Integration And R06d Scope

R05a-3 final head906f6e4c20ec51f87c46b7fdfe1b6908a27f23a2, tree
7b505d1a2cd152503d829112409a554d1f318e80, passed complete automatic
push39207672342 and PR39207678441 check/delivery. PR #38 merged as
master/ef7a1da5660a661fd90067b757e54af15ec91c1a with the identical tree;
independent main39208662187 also completed check/delivery success. Final
local1600 tests/subtests in40 packages, build/vet and actual-main smoke passed.
Evidence outside Git is validation-r05a3; olderf406503 passes remain separate.

R06d then adds current-owner financial reads per its prior six-part decision:
new ownerledger/v1, L3 ownerbilling, Store projections, API/assembly, OpenAPI
0.11.0/111 operations and the owner guide. No prior public declaration/HTTP
operation, schema16, model/migration, billing writer or frontend change.
The independent consumer and Store risk groups have directed passes; exact
final local and automatic CI/merge acceptance are still pending.

A controlled official Python SDK check on accepted R05a-3 source passed nine
bounded fixture subsets. Optional background retrieve returned None once;
that run did not capture its raw response. Later diagnostic polls were200
JSON objects, so the original SDK failure remains an unaccepted observation.
Independent code review found persisted dispatch Document:null can be returned
as200 JSONnull. A separate corrective worktree owns its deterministic regression
and acceptance; no successful retry is used to hide that defect. External
SDK evidence is sdk-validation-2026-10-19. No real Provider/OAuth was called.
=======
push 39193105666 and PR 39193153559 (check/delivery SUCCESS). PR #34 merged as
master/252c3792de2786045500073f84a2161e6c911e54; tree
c856f65f1a605784a57344401fbd3adf03f5951e exactly matches the accepted head.
Independent merged-main run 39193688299 also passed check/delivery SUCCESS.
No manual redispatch, real Provider/SDK, charging or public-contract freeze.

## R06c: Ledger Amount Meaning And Reconciliation Visibility

Started on tree-verified master/252c379 after the R02 corrective gate. The
six-part scope, exact display projection, compatibility and rollback were recorded
before UI edits in [R06c decision](REFERENCE-R06C-IMPLEMENTATION.md). It consumes
existing ledger/v1 and administrator APIs without changing financial authority.
The [amount guide](docs/reference/billing-console.md) distinguishes final charge,
current retention, original reservation and provider envelopes. This is a new
independently verified UI batch; it does not open R05 pricing/cost permissions.

The user explicitly keeps OAuth account sources/acceptance closed. Subsequent
work continues on R06c and R05a without claiming an OAuth account consumer.
The user also permits borrowing mature reference code to accelerate follow-on
work; fixed provenance, compatible integration and required notices remain part
of each actual adoption. R06c reuses TXAPI's existing exact BigInt formatter and
ledger authority; targeted upstream usage-parser reuse is being evaluated for
R05a, without running the reference projects.

### R06c Local Checkpoint And Deferral

The user subsequently directed backend completion first and deferred frontend
work, with codex-proxy-rs as the preferred future frontend reference. Preserve
this batch on local branch feat/reference-r06-ledger without pushing, creating
a PR or claiming remote acceptance. No further page expansion is scheduled now.

Local Windows amd64/Go 1.26.3 checks passed: build/vet/tidy/module/dependency
checks; full Go suite 1376 tests/subtests and 37 packages, six expected helper
skips; main strict/encryption/forced-kill/background recovery smoke; five Node
syntax checks; 12 API/amount cases; desktop/mobile billing browser matrix and
visual screenshot review. Existing confirmation/financial-command, unknown/zero,
waiver, legacy/malformed/max-int64 amounts, filters and role cases remain.

An independent review found the old configured-currency explanation conflated
account units and provider contract currency. The final UI now separates them
without inferring historical currency or conversion. Final browser assertions,
screenshots, syntax and the embedded Go build passed after this wording fix.
The earlier full Go/vet/smoke evidence remains applicable to identical Go/module/
HTTP sources; only explanatory UI strings and their browser regression changed.
Raw logs and screenshots are in D:\my-project\TXAPI-references\validation-r06c.
The final source manifest there identifies four non-Markdown source files.
No Go/API/schema/financial behavior changed. Remote CI/merge remain unperformed
because frontend integration is explicitly deferred.
>>>>>>> 28fc3ec (wip(console): preserve verified ledger presentation for later integration)

## 后续批次

R02 资源代次、R03 原生 WS、R04 账号授权、R05 计量/收费后台、R06 管理产品、R07 外部 Runtime 仍按方案依赖逐批推进。
OAuth 真实账号证据、Provider 成本证明及外部扩展的独立发布需求各有进入条件；模拟通过不替代真实验收。

## Background Dispatch Null Read Correction

Accepted baseline is R05a-3 master/ef7a1da5660a661fd90067b757e54af15ec91c1a,
identical to final906f6e4 tree7b505d1a2cd152503d829112409a554d1f318e80.
Complete automatic push39207672342/PR39207678441 and independent
main39208662187 check/delivery all passed. This integration is confirmed,
not inferred from the earlierf406503 code.

A frozen actual main and locked official Python SDK, against loopback fixtures
with no automatic retries, passed nine bounded compatibility subsets. Optional
background retrieve returned None once. Its HTTP body was not captured at the
failure time; later successful probes cannot accept it as stable or directly
prove the original cause. External evidence is sdk-validation-2026-10-19.

An independent source review found a deterministic path: Prepare persists a
snapshot with nil json.RawMessage Document encoded asnull. Unmarshalling the
existing encrypted JSON makes it a nonempty slice containingnull; the receipt
and HTTP renderer previously treated it as an actual document and returned200
JSONnull. The new real SQLite/BackgroundService/HTTP regression on unchanged
production code reproduced this in3.448s (exit1). Harness alias/digest/history
setup errors before this valid reproduction are not behavioral evidence.

Only four Go files change: generation snapshot decoding normalizes JSONnull
to absent, responses rendering uses its established synthetic-object fallback
for absent/null/whitespace, and two focused test files protect persisted
legacy Document:null and unchanged actual object bytes. No public signature,
L0, schema16, encryption format, target/identity scope, state transition,
financial behavior or retry changes. No background generation is resent.

Owner tool command summaries (not disk raw logs): the two new regression groups
passed in generation3.633s/responses1.304s, and all matching TestBackground groups
passed in generation17.176s/responses1.772s. Actual-body preservation and existing
terminal fallback remain covered. Root complete local checks and automatic final
CI/merge/postmerge remain pending. No real Provider/account/invoice was called;
no paid background/WS or general SDK compatibility is claimed.

Final local correction source is exactly four Go files, LF-normalized digest
54d43e65627c3df51444bb2f496fd8783468a39089ac4ef151b79fd7ef43f503.
Windows Go1.26.3, GOARCH=amd64, CGO_ENABLED=0: go mod tidy plus semantic Git
module diff, core/protocol dependency guard, go build ./..., go vet ./... and
go test -json -count=1 ./... all pass. The full suite passed1602 tests/subtests
in40 packages (7 expected process-helper skips;37 no-test packages),93.977s.
Actual main smoke passed50.053s, including strict refusal/settlement,
credential/encryption restart, forced kill, background recovery/SSE/cancel/TTL
and durable foreground metadata recovery. The four changed Go files have no
gofmt output; diff/link checks pass. Exact old public signatures/OpenAPI/schema
and web/module bytes remain unchanged. Prior frontend evidence is reused only
for that unchanged source; automatic CI still runs its normal checks.

One initial external validation helper incorrectly read Windows line-ending
status after go mod tidy as a module content change. git diff was empty; the
helper now checks semantic diff and the complete run passed. No module was
changed. Raw full validation output, source manifest and command/test summaries
are outside Git in validation-background-null. Independent read-only review
found no actionable regression in this correction. It preserves the old
no-document status fallback and leaves unrelated status-mapping decisions out.
Final automatic head/merge/postmerge checks remain pending at this checkpoint.


### Full CI Found An Immediate-Next-Round WS Boundary Defect

Correction headf8a139f31082b0be1507af431b3b804401e9b0f0,
treebb245170cf72b5cc29e7d122aa7ffd498ced52b0, was pushed as PR #39. Complete
push39212734118 and PR39212774462 both failed Linux full race in existing WS
main integration tests. Both delivery jobs succeeded; architecture/API/build/vet
and formatting steps passed. No CI rerun or merge bypass accepts this head.

Push failed TestResponsesWSPublicationRetainsOldStreamAndRejectsOldSessionBoundary
at responses_ws_lifecycle_test.go:72 withbusy instead of continuation_invalid.
PR failed TestResponsesWSProductRoundAndContinuation at its immediate next
request withbusy. Original failed logs and job JSON are retained in
validation-background-null/ci-push-failed.log and ci-pr-failed.log. This is a
second concrete ordering defect, not evidence that the null regression failed.

The L3 reader rejects new input until Session.Execute returns. However, the
terminal native frame can already be read by the client while that Execute is
still doing local cleanup. The [preimplementation WS boundary decision](REFERENCE-WS-TERMINAL-BOUNDARY-IMPLEMENTATION.md)
therefore keeps one reader/sequential worker and frozen contracts, with an
L2-classified terminal boundary allowing exactly one next frame to wait for
cleanup. Midstream overlap and extra frames remainbusy; delivery/cleanup failure
or cancellation discards that boundary frame without starting a new generation.
Final combined correction source must repeat full local and automatic CI gates.
Earlierf8 local success remains tied to its source and is not reused for the new
client/codec/main behavior. Official SDK fixed probe waits for that accepted head.


The WS correction now implements a pure L2 IsTerminal(frame) predicate through
the existing decoder and injects NewClientWithTerminal from main. Old NewClient
and every registered contract declaration remain exact. The client state is
idle/running/finishing/pending, with a single bounded boundary slot. Terminal
classification happens before Write, because a peer may already receive its
bytes before Write returns. The worker waits for prior Execute return; CAS
cleanup cannot erase a reader's pending reservation. Midstream and excess input
remainbusy, and failure/cancellation discards the pending input.

A deterministic old-source proof uses an external Go overlay of f8a139f's
unchanged client implementation plus a constructor-only shim forwarding the
new factory to old NewClient. The test expectations remain unchanged. Both
cleanup and terminal-write windows fail immediately withbusy (package0.863s,
exit1); this is retained in terminal-boundary-old-probe/old-code-failure.log.
The shim changes only test compilation and grants no terminal awareness.

Owner command summaries: complete client/codec packages pass1.024s/1.065s.
The actual-main ProductRoundAndContinuation, PublicationRetainsOldStream,
RevocationAndForeignContinuation and DisconnectCancelsActiveGeneration groups
each pass10 repetitions, combined10.503s. Exact command and raw output are in
validation-background-null/terminal-boundary-main-directed.log. Independent
read-only review finds no actionable issue across the five WS Go files and
its tests. The original four background files are unchanged from testedf8.
Final combined full checks run again in validation-response-boundaries-final;
no earlierf8 pass is used to accept the newly changed client/codec/main source.


Final combined correction is exactly9 Go files with LF-normalized digest
51989daacf14e9ef697b837f77a075ddd5f477bdf6e00e9f4a37c7567a8adc75 on
unchanged latest master/ef7a1da. Windows Go1.26.3 GOARCH=amd64 CGO_ENABLED=0:
module tidy/content diff, dependency boundary, go build ./..., go vet ./...,
full go test -json -count=1 ./... all pass. The final suite passed1613 tests/
subtests across40 packages in92.230s (7 expected process helpers skip;37 no-test
packages), followed by full actual-main smoke PASS57.343s. This repeats all
checks for the combined source rather than borrowingf8's old pass.

Nine changed Go files have no gofmt output. Committed old public declarations,
OpenAPI0.10.0/108 operations, schema16, core, modules and frontend sources remain
exact. Diff/doc links pass. Raw final logs, commands and source manifest are in
validation-response-boundaries-final; originalf8 CI failures remain separate.
Normal automatic final-head Linux race/container delivery and merge/main checks
still gate acceptance. No manual workflow rerun, reduced regression or approval
bypass is used. The optional fixed SDK probe will use that accepted binary.


## Response Boundary Correction Integration And Owner Rebase

Final corrective headbb5a00630accd7127f87c2464841b4aad88633b2 and
tree2f56eaeee85a222a9fcb740504c43891706d5b2a passed complete automatic
push39215267374 and PR39215271764, including full Linux race, coverage, main
smoke, benchmarks and container delivery. No failed steps remain in either run.
PR #39 merged as master/755128df7612a00bed5f62c91c6b34a96fbd3a1b with the
identical tested tree. Independent main39216287290 is running and remains a
separate check; previousf8 failures are retained and not relabeled as success.

A single fixed official SDK probe against binarySHA256
cdd490ca784be693f2bda22815ce2a3c3ee82f347f5f78a904d18d8ee9906a68 from
that exact corrective head passed7.37s. The Provider was held after real dispatch;
SDK rawGET returned200/application-json/197bytes queued Response, notNone.
After one Provider response and durable terminal, the second fixed rawGET returned
completed Response/531bytes with unchanged output text. Exactly one generation
POST and two reads, no SDK/gateway retries; Provider errors empty and thread
joined. Source/script/binary hashes and all raw bodies are in
sdk-validation-2026-10-19/background-fixed-153006. This accepts only the held-
dispatch/terminal JSON subset. Other nine SDK subsets retain their original
906f6e4 source binding; no real Provider/OAuth/WS/replay/cancel/charging claim.

Owner R06d authored commit52cbe2c was rebased as04799e8 onto this merged baseline.
Its implementation and prior direct evidence remain distinguishable. Complete
local owner checks, including actual-main owner metadata smoke, now run against
the rebased source before a final owner commit/automatic CI/merge acceptance.


### R06d Final Local Acceptance

Independent corrective main39216287290 completed full check/delivery success
on755128df7612a00bed5f62c91c6b34a96fbd3a1b, with no failed steps. Its job JSON
and merge-tree proof are preserved separately from final-head push/PR evidence.
The longer main race run completed successfully; no manual rerun was used.

Owner final18 non-Markdown source files (14 Go) have LF-normalized digest
d744f6447cfbfd2b0ecfc543af1780d8cce5dd5adbe73235e25e507eeb012c54.
On actual latest accepted master/755128d, Windows Go1.26.3 GOARCH=amd64
CGO_ENABLED=0: module tidy/content diff, core/protocol dependencies, build/vet
and full go test -json -count=1 ./... pass. Final1642 tests/subtests across41
packages pass83.250s, with7 expected process helpers skip and38 no-test packages.
Actual-main complete smoke passes47.427s. It now checks unknown/empty personal
reads and the real strict forced-kill pending hold, then existing idempotent
waiver followed by the personal historical ceiling/zero charge/released user
hold projection. Those reads preserve Provider privacy and never resolve funds.

Public snapshot adds only ownerledger/v1's14 declarations; all prior25 packages
remain exact. OpenAPI0.11.0 adds only3 operations to111 and4 owner DTO schemas;
all prior108 operation/schema values remain exact. Core, model, billing writes,
schema16/migrations, modules, workflows and frontend sources are unchanged.
Fourteen changed Go files have no gofmt output; source identity, diff and links
pass. Prior browser/frontend evidence is reused from exact unchanged main755128d
source; normal automatic CI continues its normal Node/API checks. No Docker or
Linux race is claimed locally; final automatic head check/delivery remains the
owner merge gate. Raw commands/full logs/manifest are external validation-r06d-owner.


## R06d Integration And Shadow Follow-on Boundary

Final owner-ledger headcee15e066eb112f2365e07b344d71fc9bf2c0d4e and
tree026daba87277e8baadd3d0279e72a0b9779cd05f passed complete automatic
push39217668258 and PR39217752127 check/delivery, with no failed steps.
PR #40 merged as master/9a1264291a789e2e8d86c82614dd86f1ef12d20d with the
identical tested tree. Independent main39218975413 also completed full
check/delivery success. Exact job JSON and merge proof are external
validation-r06d-owner. This accepts only the personal financial read backend;
R06/frontend, R05b paid execution, real OAuth/Provider and Runtime gates remain
separate. No release, tag or image publication is part of this batch.

The next backend slice records its [concrete decision](REFERENCE-R00-SHADOW-IMPLEMENTATION.md)
before source work on this accepted baseline. It adds opt-in bounded shadow
load/error comparisons through existing operational metrics/debug consumers,
without changing production selection, frozen declarations, L0 or schema16.
Implementation and full acceptance of that source are pending at this checkpoint.


### Shadow Source, Consumers And Performance Checkpoint

The private L1 sampler has eight slots,64 outcomes each and a ten-minute
window. Exact rational comparisons consume the already filtered priority pool;
production weighted draws and extension/affinity calls remain unchanged. Both
startup flags are required. Epoch/reset, retired/untracked activity, cap
saturation and late token consumption are bounded under a sampler-only mutex.
Successful configuration publication stamps an immutable epoch before atomic
publication; failed Publish and health refresh retain prior identity. Shadow
overflow never fails the real configuration or changes an actual selection.

Directed sampler risk groups pass3.292s. Nine gateway integration groups pass
5.982s: deterministic selection trace, genuine HTTP rejection versus body-read
failure, excluded local/protocol/cancellation/panic paths, finalizer occupancy,
multi-publication retirement, real concurrent publication/health, debug privacy,
JSON/SSE wire parity and real SQLite strict success/pending-receipt-fault parity.
The new actual-main four-flag matrix passes6.536s with metrics/debug consumers,
exact POST/output counts and unchanged disabled financial state. Initial fixture
issues were corrected before acceptance: ownerless seed token lacked the
existing claim step; legacy Chat passthrough is not a strict JSON decoder; SSE
required its existing finish frame. No production code was changed to accept
those invalid fixtures. Directed command summaries and raw final integration
JSON are separate in external validation-r00-shadow.

Metrics fixed-reason/concurrency/privacy tests pass0.739s. Recording counters
allocate zero; sampler Begin/End plus full8-slot/64-live-outcome Compare also
allocate zero in1000 AllocsPerRun iterations (directed package3.322s).
The complete selection path retains its existing allocations, rather than
claiming the entire scheduler is allocation-free.

Performance uses Go1.26.3 installed windows/386 with GOARCH=amd64 CGO_ENABLED=0,
Windows10.0.26300.0, Intel i5-12500H,10000 serial selections per case and full
64-outcome rings per enabled channel (one HTTP error every four outcomes).
The first ordinary time.Now/Since per-call report could not resolve p50/p95:
its zeros are not accepted as latency evidence. Its mean/alloc logs remain
preserved. A Windows-only external Go overlay then substitutes
QueryPerformanceCounter timing (frequency10000000) only in the measurement
fixture, preserving the actual selection implementation. Before-source overlay
uses master/9a12642 selection bodies plus only a private decision field and an
uncalled new-test compile shim. It adds no production shadow call. No Windows
measurement API, overlay or downloaded code is committed to the repository.

| Source/path | Pool | Mean ns/op | B/op | Allocs/op | QPC p50 ns | QPC p95 ns |
|---|---:|---:|---:|---:|---:|---:|
| accepted before / disabled |2|643.1|464|5|600|1200|
| accepted before / disabled |8|1004|528|6|800|1500|
| final source / shadow off |2|646.7|464|5|600|1300|
| final source / shadow on |2|1368|464|5|1600|2600|
| final source / shadow off |8|902.3|528|6|800|1300|
| final source / shadow on |8|4358|528|6|4300|5700|

Enabled added p95 is1.3us for2 and4.4us for8 channels on this synthetic host;
no selection allocations are added. Timing includes counter overhead; this
single non-race/serial host measurement proves neither contention behavior nor
traffic benefit. The10us added-p95 target is met only for these recorded cases.
Full commands, raw before/after logs, measurement overlay hashes and source
binding are external performance-*-commands.json and performance-environment.json.
The final checked-in timing report explicitly marks unresolved clocks instead
of presenting zero quantiles; that log-only change leaves all measured loops
and selection source unchanged. Default enablement and any actual scoring
algorithm remain outside this batch.

Final full local checks and actual-main smoke now run on the unchanged latest
accepted master/9a12642 baseline. Complete exact-head automatic CI, merge-tree
verification and independent main checks remain pending.


### Shadow Final Local Acceptance

Final16 non-Markdown sources (13 Go) have LF-normalized manifest digest
202abef4f8f0e8a18157a8951a6c0a09e90f79956b56ee34531bf2778db20a4c.
Latest origin/master was fetched and remains9a1264291a789e2e8d86c82614dd86f1ef12d20d.
On that accepted baseline, Windows/PowerShell GOARCH=amd64 CGO_ENABLED=0:
go mod tidy with semantic module diff, core/protocol dependency list/guard,
go build ./..., go vet ./... and full go test -json -count=1 ./... all pass.
Final1707 tests/subtests in41 packages pass89.455s; seven expected process
helpers skip and38 packages have no tests. Full actual-main smoke passes61.402s,
including the new four-flag/metrics/debug scenario and all existing strict,
credential/encryption, forced-kill/recovery, background/SSE/cancel/TTL and
usage-history checks. No local Linux race or container execution is claimed.

All13 changed Go files have no gofmt output. Python AST and152 local links in
five changed Markdown documents pass; diff whitespace checks pass. Existing
registered public snapshots, OpenAPI0.11.0/111 operations, core, models,
schema16/migrations, modules, CI workflows and frontend bytes remain exact.
Earlier frontend/browser evidence is reused only for that unchanged source;
automatic CI still executes its ordinary Node/API checks. Independent read-only
sampler/integration review found no actionable remaining issue. Raw full logs,
commands and final source manifest are external validation-r00-shadow/full-final.
Final automatic push/PR and independent merged-main verification remain the
acceptance gates for this source; no manual workflow run or reduced CI is used.


## Shadow Final Integration And Whole-Program Status

Final head6dc08bd9363cd6434c5f889ebdc709f747fd1060 passed complete automatic
push39223342711 and PR39223350698 without failed steps. PR #41 merged as
master/8ac00c95bfa74d72de83090c4c23f2f3443a5600 with identical tested tree
ebf82faa0f306dd35e4aabbe571688ffcbef57ec. Independent main39224567863 also
completed full check/delivery success with no failed steps. Remote master was
fetched and compared after completion. Final source and job/merge evidence are
external validation-r00-shadow; earlier pending checkpoints remain historical.

One fresh frozen official Python SDK run on a newly compiled final-tree binary
passed all10 preserved controlled cases in8.106s. It made13 simulated Provider
POSTs, zero Provider GETs, no automatic SDK/gateway retries or Provider errors,
and joined its Provider thread. Source, binary/script hashes, embedded VCS
limitations and exact untested scope are in the
[complete status/evidence index](docs/reference/reference-adoption-status.md).
This refresh does not relabel earlier failures or prove real Provider/OAuth,
native SDK WS, charging or universal compatibility.

The overall R00-R07 objective is incomplete. The index maps every original
batch and section8 risk row to implementation/evidence and remaining gates;
R04/R05b/R07 still lack real code as well as entry evidence, and frontend is
deferred by the user. Further WS/background quantity capture is a separate
optional R05a slice, not a hidden missing consumer of the accepted profiles.

### Status Documentation Batch Boundary

1. Layer/directories: documentation only, PLAN, this log, the shadow decision
   and docs/reference/reference-adoption-status.md.
2. Dependencies: read existing versioned contracts and accepted source/CI;
   no public or candidate interface is changed or frozen.
3. Foundation: no executable source, schema, configuration or CI edit.
4. Contract: no runtime/API/permission/lifetime change.
5. Verification: Markdown differences/links/evidence consistency and exact
   non-Markdown source equality with accepted master/8ac00c9. Reuse1707 tests,
   full main smoke and automatic head/main CI from the identical source above;
   no manual Go, race, browser or workflow rerun for explanation-only changes.
6. Parallel impact: no shared source or deferred frontend modification.

This corrects the active PLAN's stale pending label and records final
acceptance inside the repository. The documentation commit follows normal
push/PR automatic workflow triggers; those triggers are not changed or bypassed.

Documentation validation on2026-10-19: git diff --check passed; a Python
Markdown path/heading check resolved all113 local links and anchors across
the four changed documents. Git changed/untracked path checks confirmed only
those four Markdown files; every non-Markdown source is identical to accepted
master/8ac00c9. Saved merge/CI/SDK JSON consistency checks passed. The exact
check command and result are external validation-doc-status/document-check.json;
no new local executable test or manual CI was run for this documentation batch.
