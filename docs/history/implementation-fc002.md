# FC-002 原生后台实施与验证记录

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
