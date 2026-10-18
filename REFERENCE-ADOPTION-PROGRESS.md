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
Linux race, container delivery, exact functional-head CI and merge remain
pending at this source commit.

No real Provider or official SDK test, release, tag, OAuth, billing, persistent
reconnection or multiplexing is claimed. Full A03/A04 and R00-R07 stay open.

## 后续批次

R02 资源代次、R03 原生 WS、R04 账号授权、R05 计量/收费后台、R06 管理产品、R07 外部 Runtime 仍按方案依赖逐批推进。
OAuth 真实账号证据、Provider 成本证明及外部扩展的独立发布需求各有进入条件；模拟通过不替代真实验收。
