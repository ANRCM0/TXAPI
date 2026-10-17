# S5–A3 开发批次记录

归档日期：2026-10-17。以下从 `master/5004c57` 对应的 PLAN 移入，保留原批次范围、当时排期、提交/基线、命令、结果与验收限制，仅调整相对文档链接。“本批”“当前分支”“草稿”“未合并”等词指各条记录写入时的状态，不是最新开发状态；唯一活动计划见 [PLAN.md](../PLAN.md)。S0–S5 较早的阶段验收见 [历史验收记录](acceptance-s0-s5.md)。

## 2026-10-17 合并与最终验证结果

PR #5 已按用户明确指示合并到 master，合并提交 `5004c5703cefecfb383cb718ad0377489f047e73`，父提交为原 master `90df76b0340e4cdfa6c332c781f7aa772a04ed07` 与功能头 `8d93855cfbc89759b970346c97ca38ae3fe5b151`。合并树与已验证功能头相同，包含 S5 成本门槛、S6/A1/A2、独立 Responses 协议样本及 A3-01 至 A3-04 切片；合并不代表 S5-03、完整 S6/A3/A4 或 S7-A 已验收完成。

| 验证对象 | 精确提交 | 结果与证据 |
|---|---|---|
| 功能头 push | `8d93855cfbc89759b970346c97ca38ae3fe5b151` | [Verify #39045128297](https://github.com/ANRCM0/TXAPI/actions/runs/39045128297)，success |
| PR 自动验证 | 同一功能头 | [Verify #39045131318](https://github.com/ANRCM0/TXAPI/actions/runs/39045131318)，success |
| 合并后 master | `5004c5703cefecfb383cb718ad0377489f047e73` | [Verify #39045543169](https://github.com/ANRCM0/TXAPI/actions/runs/39045543169)，success |

上述三次 Verify 的 check/delivery 均通过，包含依赖/契约、构建/vet/格式、前端检查、Linux 全量 race、进程冒烟与容器安装/v9 升级/备份恢复/正常退出。最终本地 Windows/amd64 构建、vet、20 个有测试包、4 项 Node 语法/6 项行为测试与进程冒烟通过。原 A3-04 的实际命令、环境与强杀回归保留在文末；[PR 最终证据](https://github.com/ANRCM0/TXAPI/pull/5#issuecomment-6235890293) 记录合并和精确提交核对。没有真实上游/指定客户端/浏览器或生产库验收。

本次归档只整理说明与已有证据，不重新运行功能验收，不将历史绿灯算作文档变更后的新执行结果，也不补勾阶段清单。

## 2026-10-17 文件与文档整理

基于已合并 `master/5004c57`，在 `docs/repository-cleanup` 分支整理现行说明、历史记录和本地生成文件；不实现新功能、不修改远端协作设置、不补勾阶段验收。

- PLAN 的 393 行历史批次原样移入本页（仅重定位相对链接），活动计划从 665 行缩为 275 行；P6 旧方案移入 history。F0 历史细目归入原阶段归档，运行组件完整约定从 DEVELOPMENT 抽入 `docs/reference/generation-runtime.md`。
- README/PLAN/架构/协作与开发指南同步实际合并基线；修正模块目录、旧文档引用，以及“每个 worktree 都存在本地参考副本”的错误假定。源文件仅更新 model 包注释、SDK 文档字符串和测试配置注释，运行逻辑未变。
- Windows/PowerShell 一次性 Python 检查：16 份 Markdown 的 126 个本地路径/锚点通过；历史 PLAN/P6/运行组件正文及 F0 勾选保持；全仓原 101 条阶段勾选和 16 个原 CI URL 全部保留。
- 静态核对 Go 的 package 后正文、Python 除模块文档字符串的 AST、YAML 非注释内容均不变；OpenAPI 0.2.3/79 操作、依赖、workflow、忽略规则不变。仅对改注释的 Go 文件执行 gofmt，`git diff --check` 通过。
- 本地删除 44 个已被最终结果替代的测试日志/中间 JSON，以及 6 个过时检查点/PR 草稿，共 50 个文件、4,089,983 字节；只操作本 worktree 的 `.local` 指定文件，无递归删除。保留 12 个最终测试/CI/冒烟结果与 Markdown 验收摘要，不动配置、密钥、数据库、二进制或原工作目录的参考仓库。

本批未运行 Go 构建/vet/全量测试、race、冒烟、容器或手动 CI；内容与注释整理使用静态验证，不把上节功能基线的 CI 写成本批新执行结果。初次验证脚本将 Git LF blob 与工作区 CRLF 原始字节比较，产生非内容差异；随后改为规范行尾比较，未改契约内容。Go 注释文件只由 gofmt 规范行尾，无业务代码变化。

## 前一批文档整理记录

以下保留历史批次的事实、当时排期和验证，不作为最新优先级；当前顺序统一以本文“架构优先执行顺序”节为准。

2026-10-16，Windows/PowerShell，代码基线 `746f673`，本地 master `90df76b`。本批只清理说明文档：入口概览、最终架构、活动计划、协作规则与历史归档；保留现有未提交方案和上游快照。未改生产代码、API 契约、数据库、配置、测试或 CI；未运行 Go 测试、上游程序、真实模型/支付请求或手动触发 CI。历史记录仅用于说明已验收范围，不作为本批新验证。未提交/推送。

实际检查：`git diff --check` 通过；一次性 `python -` 扫描 12 份 Markdown 的 78 个本地链接/锚点，通过；只读核对 OpenAPI `0.2.2` / 79 操作及未新增 Responses 路由，通过；归档与 HEAD 原记录对照，35 条阶段勾选与 13 处 CI 链接保留，S5 仍为 5/6、后续路线无新增完成勾选，通过。另一次 `python -` 核对三个本地参考库的固定 SHA/远端/干净工作树、Git/Docker 忽略范围与 19 个源码路径，通过。`git diff --name-only` 与未跟踪文件列表确认仅为 Markdown 变更；未将历史功能 CI 写成本次文档已通过 CI。

## S5-C 首批工作记录

2026-10-16，代码基线 `feat/s5-pricing/746f673`，本地 master `90df76b`，未提交工作树。环境 Windows/PowerShell，Go 1.26.3（工具为 windows/386，以下 Go 验证均显式 `GOARCH=amd64`）、Node 24.14.1、Python 3.14.3。保留前一批文档改动。

实现范围：billing 严格 JSON/文本输入校验及输出限制元数据，protocol 最终请求限制校验，gateway 在上游调用和持久 attempt 前拒绝不安全转换；新增对应行为回归，同步 README/指南/现有两个推理操作的 OpenAPI 描述。未改 core、数据库模型/迁移、运行配置、依赖或 CI；无第三方实现代码移植。不是完整 S5-C 验收，也未开始 S6。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/protocol ./internal/gateway -run 'TestStrictRequest\|TestOutputLimit\|TestStrictConvertedLimits\|TestStrictMalformedRequests\|TestStrictEmbeddingsCannot\|TestStrictRejectsUnpricedDimensions'` | billing/protocol 通过；gateway 初次因测试误将 Gemini 模型映射视为正文而非 URL 失败，已修正断言，无生产代码补丁掩盖 |
| `go test -count=1 ./internal/gateway -run '^TestStrictConvertedLimitsBeforeUpstreamAndDurableAttempt$'` | 修正后通过；真实 HTTP 假上游验证限制保留/拒绝、模型映射、attempt 事实、原结算和双预算释放 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 最终生产代码上全部通过，包含依赖/契约、现有强杀恢复及权限/协议/存储回归 |
| `node --check` 四个现有控制台/浏览器脚本，`node --test web/assets/api.test.mjs` | 全部通过，6 项前端/API 行为测试通过；未运行真实浏览器或真实上游 |
| `python tests/smoke/local.py --go go` | 通过；临时库/随机端口、原调用/控制台、strict 持久结算，以及真实 gateway SSE 强杀/重启 pending/审计豁免 |
| `git diff --check`、仅本批 8 个 Go 文件 gofmt 检查、`git diff --exit-code -- go.mod go.sum` | 全部通过，没有格式化无关 Go 文件或改变依赖 |
| 一次性 `python -` 文档/契约一致性检查 | 12 份 Markdown / 78 个本地链接和锚点通过；OpenAPI 保持 `0.2.2` / 79 操作，仅两个现有操作描述变化，路由/请求响应结构不变；S5 仍为 5/6，后续清单未新增完成勾选 |

本批未运行 Linux race、Docker/交付演练或远端 CI，未在线刷新集成基线，未提交/推送。合并前仍须在最新集成基线上通过完整现有 CI；不复用旧功能 CI 为本批背书。下一切片为模型/操作的可证明输入与输出计量边界、独立上游价格及不可变成本契约，再接重试总成本和管理展示；此处的输出限制保真不能替代这些工作。

## S5-C 第二批工作记录：独立成本契约与累计尝试包络

2026-10-17，代码基线 `feat/s5-pricing/746f673`、本地 master `90df76b`，保留前批未提交工作。环境 Windows/PowerShell、Go 1.26.3（宿主默认 windows/386，所有本批 Go 验证显式 `GOARCH=amd64`）、Node 24.14.1、Python 3.14.3；未在线刷新集成基线。

实现范围和依赖：billing 编译启动期不可变成本契约、独立精确价格/总输入包络与逐次成本上限；Reserve 固定策略，gateway 经现有协议最终限制检查后调用 `StartCostedAttempt`，同事务占用累计包络并记录 attempt 后才执行现有 core。模型/store 统一追加 v7 字段/版本，config/main 接线，现有管理员账本 DTO/OpenAPI 0.2.3/控制台展示。保持 79 个操作；不改 core、模块依赖规则、第三方依赖、CI、鉴权和唯一收尾流程，无第三方实现代码移植。

本批支持范围：现有受限文本 Chat 三协议/显式限制的已支持转换，以及 OpenAI 兼容文本 embeddings；仅在精确实际目标/操作有明确配置契约时允许执行。输入按整次厂商计费硬上限占用，embeddings 不按数组单项重复/遗漏计量；独立上游价格不替换售价。契约缺失、漂移或预算不足不发送、不降级、不用其他凭据绕过；失败/未知 attempt 不释放累计重试包络。回执用量超契约时保持不可自动结算。此处的配置声明仍需真实依据，非空 evidence 不等于已验证的厂商保证；没有内置生产契约，不宣称已有可靠 tokenizer 或任意厂商兼容。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/gateway ./internal/config -run 'TestProvider\|TestStrictProvider\|TestStrictConverted\|TestS5StrictGateway'` | 首轮定向通过；覆盖精确金额/舍入/溢出、契约缺失/精确匹配、累计预算、并发原子准入、重复/写入失败回滚、输入批次包络、异常用量 pending、不可变快照及售价不变；新增 embeddings HTTP 场景随随后全量验证执行 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 最终生产代码全部通过；包含新账本 DTO/79 路由契约、依赖边界、完整现有回归及强杀矩阵；新增真实 HTTP 假上游验证 embeddings、目标变化及仅允许预算内的网络尝试 |
| `node --check` 四个现有控制台/浏览器脚本，`node --test web/assets/api.test.mjs` | 全部通过，6 项行为测试通过；未运行真实浏览器 |
| `python tests/smoke/local.py --go go` | 通过；原调用/权限/控制台、模式隔离、strict 缺价拒绝和 JSON/SSE 结算正常；真实 gateway SSE 强杀后，重启保留 `5760000` 上游包络及契约快照、用户 hold 转 pending，再经管理员幂等豁免 |
| `git diff --check`、本批 14 个 Go 文件 `gofmt -l`、`git diff --exit-code -- go.mod go.sum` | 通过；未格式化无关 Go 文件，没有新增依赖 |
| 一次性 `python -` Python AST/文档/契约检查 | 冒烟脚本语法通过；10 份变更 Markdown / 75 个本地链接与锚点通过；OpenAPI 0.2.3 / 79 操作；S5 仍为 5/6，完整 S5-C 清单未新增勾选 |

以上为本次未提交工作树证据，不借用早期 CI 为本批背书。未运行 Linux race、Docker/交付演练、真实上游/客户端或远端 CI；未提交/推送，未操作实际运行数据库，也不新增旧库报错/回滚专项。合并仍需最新集成基线完整现有 CI。

下一切片：对准许模型/操作落实可复核的真实输入/总输出及缓存/推理计价依据，并用该边界检查售价侧是否已被用户 hold 事前覆盖；补齐拒绝依据的持久管理可见性和适用故障验收。当前成本契约配置与重试包络是 C01/C04/C05 的部分实现，不代替真实契约证明或 C02/C03/C06，S5-03 未完成，S6 尚未开始。

## S5-C 第三批工作记录：发送前用户收费覆盖

2026-10-17，继续基于 `feat/s5-pricing/746f673` 与本地 master `90df76b` 的未提交工作树；保留前批实现。Windows/PowerShell、Go 1.26.3（默认 windows/386，以下 Go 验证显式 `GOARCH=amd64`）、Node 24.14.1、Python 3.14.3；未在线刷新远端集成基线。

负责目录与依赖：billing 的固定售价计算/成本准入/持久 attempt，gateway 的拒绝映射与既有唯一收尾，相关回归与文档。每个实际目标按声明的完整输入包络、显式输出限制、请求固定售价/倍率核算收费上界；沿用“实际模型售价优先、否则公开模型”的既有结算规则，并使用同一宽整数公式。开始 attempt 的事务同时要求持久 hold 足以覆盖该上界；超出或溢出返回 400 `billing_fee_limit`，无新增网络调用、attempt、上游占用或健康处罚。精确相等允许，先前未知执行仍保留 pending。内部快照 format=2 持久保存售价版本/模型/分组/金额，旧 format=1 回执保持可读。无新 schema、HTTP 字段、路由、第三方依赖、core 或 CI 变更。

同时修正前批配套遗漏：现有 delivery 脚本仍将新增迁移固定为 `[5,6]`，本批同步为 `[5,6,7]`，未删除/缩减旧断言或改 CI 触发；只做版本一致性维护，不另开旧库专项。浏览器账本测试补齐 0.2.3 必填 provider 金额/快照样本及展开断言，避免真实 `ledgerMoney` 对旧样本 undefined 报错；本批未运行真实浏览器。原 provider 预算测试的 1000-token 输入按售价需要 0.1692 覆盖，隔离 fixture 的用户收费上限调整为 0.2，原 provider 预算/累计占用/实际收费断言保留，另加不足与精确边界回归。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/gateway -run 'TestFeeBound\|TestStrictFeeBound\|TestProvider\|TestStrictProvider'` | billing 通过；gateway 首轮仅新用例误断言成功健康凭据必然回写失败。既有健康逻辑对没有状态变化的成功不回写，修正测试断言，未改生产健康逻辑 |
| `go test -count=1 ./internal/gateway -run '^TestStrictFeeBoundBeforeNetworkAndEveryRetry$'` | 修正后通过：精确上限、少一单位、实际模型高价、倍率及重试换目标；本地收费拒绝不反馈健康，先前 503 仍正常反馈 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 最终代码全部通过；包含新精确舍入/零价/int64 宽输入/溢出、售价与倍率固定、历史 hold 不被新全局上限扩大、旧/新快照及未知格式处理，HTTP embeddings 全批输入拒绝、原依赖/契约/强杀与完整回归 |
| `node --check` 四个既有脚本，`node --test web/assets/api.test.mjs` | 全部通过，6 项行为测试通过；`node -e` 检查当前模块路径没有 Playwright，未安装依赖或冒充运行浏览器测试 |
| `python tests/smoke/local.py --go go` | 通过；原 JSON/SSE/模式隔离/控制台、strict 结算正常；真实 gateway SSE 强杀、重启后核对 format=2、`selling_bound_units="96000000"` 和原售价版本，未知执行 pending，幂等豁免后 provider 累计包络仍保留 |
| 一次性 `python -` 静态检查 | 两个 Python 冒烟脚本 AST 通过；delivery 期望新增版本与迁移注册 `[5,6,7]` 一致；10 份变更 Markdown / 75 个本地链接与锚点通过；OpenAPI 保持 0.2.3 / 79 操作，未新增 S5-C 完成勾选 |
| `git diff --check`、本批 8 个 Go 文件 `gofmt -l`、`git diff --exit-code -- go.mod go.sum` | 通过，未格式化无关文件或改变依赖 |

本批未运行 Linux race、Docker/delivery 实跑、真实浏览器、真实上游/客户端或远端 CI；未提交/推送，未操作实际运行库。不得将脚本/样本修正或静态检查写成容器/浏览器验收通过；最终合并仍须在最新集成基线上通过完整现有 CI。

此检查证明的是**声明边界之内**的售价收费覆盖，不证明厂商实际总输入/推理/缓存约束真实成立。下一批仍须收口 C02/C03 的计量与计价依据、持久拒绝原因展示及适用验收；原异常用量 pending/管理员审计保留。S5 保持 5/6，S5-03 未完成，S6/S7-A 尚未开始。

## S5-C 第四批工作记录：持久成本拒绝诊断

2026-10-17，工作区 `C:\Users\mn139\.codex\worktrees\5506\TXAPI`，detached HEAD `746f673b4b85e5054479792ef30510abe6c6a198`，保留前三批未提交工作。负责 billing 成本准入/唯一收尾与对应 gateway 回归，依赖既有 reservation reason 和管理员账本消费者。Windows/PowerShell，Go 验证显式 `GOARCH=amd64`；未刷新远端基线、提交或推送。

本批在成本拒绝返回前持久保存四种固定原因：缺契约、provider 总预算耗尽、契约输出越界、用户收费覆盖不足。无持久 attempt 时零收费释放双预算并保留原因；已有未知执行时仍 pending，原因记录为 `execution_or_usage_unknown:<拒绝码>`。收尾前中断后的既有恢复和重复收尾均保留诊断；不将拒绝计为实际执行，不释放已占用 provider 包络。诊断写入失败拒绝执行并返回组件不可用；客户端取消后只允许有两秒超时的诊断落库。只记录白名单原因，不持久化原始错误、URL、密钥或正文。

复用现有管理员列表/详情的 reason 展示，OpenAPI 0.2.3 仅补现有字段说明，浏览器 fixture/断言同步；无新 HTTP 字段、路由、模型迁移、依赖、core 接口或 CI 变更，无参考项目实现移植。此批是 C05 的部分实现，不代替真实厂商计量依据。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/gateway -run 'TestProviderRefusal\|TestStrictFeeBound'` | 首轮定向通过：四类原因在收尾前可查询，模拟丢失请求后的恢复/重复收尾，以及真实 HTTP 收费拒绝和重试 pending；后补的诊断故障/取消用例随最终全量检查通过 |
| 默认 `go build ./...`，随后 `go build -x ./cmd/gateway` | 失败；追踪显示 Go 在父目录 `C:\Users\mn139` 执行 `git status --porcelain`，VCS 元数据读取失败。工作区自身 git status/log 可读；未修改仓库或全局 Git 配置 |
| 本命令环境 `GOARCH=amd64 GOFLAGS=-buildvcs=false`：`go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 全部通过，包括新诊断写入故障/取消回归和既有契约、权限、并发、持久化/恢复测试。临时跳过本地产物 VCS stamping，不改变源码、测试或 CI 门槛 |
| `node --check` 四个既有脚本，`node --test web/assets/api.test.mjs` | 全部通过，6 项行为测试通过；新增浏览器原因展示断言仅语法检查，未运行真实浏览器 |
| 同一 Go 环境 `python tests/smoke/local.py --go go` | 通过；真实 gateway 隔离启动、鉴权/JSON/SSE/控制台/strict 结算，以及 SSE 强杀重启、pending 与管理员幂等豁免 |
| 本批 4 个 Go 文件 `gofmt -l`、`git diff --check`、`git diff --exit-code -- go.mod go.sum` | 通过，无无关格式化或依赖变化 |

未运行 Linux race、Docker、真实浏览器、真实厂商或远端 CI，未操作实际运行库；默认 VCS stamping 构建尚未在此工作树环境通过。S5 仍为 5/6，S5-03 与 S5-C 完整验收保持未完成。下一批继续 C02/C03 的可验证输入/总输出与缓存/推理计费依据，再集中完成剩余验收；S6/S7-A 尚未开始。

## S5-C 第五批工作记录：最终正文计价维度检查

2026-10-17，继续在受管工作树 detached HEAD `746f673` 的未提交实现上开发。负责 `internal/billing` 的发送正文计价白名单、`internal/gateway` 的发送前调用和本地拒绝分支，以及相应行为回归。依赖既有协议 Prepare、输出限制守卫、持久拒绝原因和唯一收尾；没有修改 core、协议适配器接口、数据库版本、依赖或 CI，没有参考项目代码移植。

本批将入口的文本计价约束落实到真实转换输出：OpenAI Chat/embeddings 复用严格 JSON/文本解析；Anthropic 与 Gemini 检查顶层生成字段和逐消息/内容块。拒绝重复 key、未知字段、工具/媒体内容、显式缓存/推理或 thinking 配置、服务等级及媒体输出配置；embeddings 仍只准许文本/文本批次。没有先丢弃未知维度再验证。所有检查在持久 attempt 和网络发送前执行；原售价/分组/累计预算检查继续保留。

正文越界、输出限制保真失败及 embeddings 跨协议拒绝共用 `unsupported_billing_dimension` 持久原因，沿用零执行释放双占用、未知执行继续 pending 的规则。本地拒绝不反馈上游健康；首轮测试发现既有该类 400 会走健康回调，已修正真实分支并保留断言。账本原因白名单和既有恢复覆盖新增代码，未另建收尾/迁移流程。OpenAPI 保持 0.2.3 / 79 操作，仅更新现有推理操作及 reason 的描述；README/指南同步支持范围。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/gateway -run 'TestPreparedBillingShape\|TestStrictPreparedDimensions\|TestStrictConvertedLimits\|TestProviderRefusal'` | billing 通过；gateway 新回归发现本地 400 会触发健康反馈，修正本地拒绝分支，没有删除断言 |
| `go test -count=1 ./internal/gateway -run 'TestStrictPreparedDimensions\|TestStrictConvertedLimits\|TestStrictEmbeddingsCannot'` | 修正后通过；使用替换适配器真实构造未计价正文，验证零网络/零 attempt/零 provider 新占用、原因可查询及 legacy 透传 |
| `GOARCH=amd64 GOFLAGS=-buildvcs=false` 下 `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 全部通过；含三协议真实文本转换、空串/中文/长文本、文本 embeddings 批次、22 类未计价/畸形样本、拒绝后的中断恢复/重复收尾，以及现有完整回归 |
| 同一 Go 环境 `python tests/smoke/local.py --go go` | 通过；真实 gateway JSON/SSE、模式隔离、strict 缺价/结算及 SSE 强杀后重启 pending、管理员幂等豁免 |
| 本批 7 个 Go 文件 gofmt、`git diff --check`、`git diff --exit-code -- go.mod go.sum` | 通过；未格式化无关文件或改变依赖 |

本地环境沿用上一批已定位的父目录 VCS stamping 限制，命令临时使用 `-buildvcs=false`，未修改 CI 构建门槛。前端源码和浏览器脚本本批未变，未重复运行 Node 或浏览器；未运行 Linux race、Docker、真实厂商/客户端及远端 CI，未刷新远端基线、提交/推送或操作实际运行库。

此批关闭转换层显式字段绕过的缺口，属于 C03/C05 的部分实现，不能证明默认推理、缓存计量、真实输入 Token 或配置 evidence 的真实性。S5 仍 5/6，S5-03 和完整 S5-C 不新增完成勾选；下一步收口真实计量与价格依据，再完成剩余验收，S6/S7-A 尚未开始。

## S5-C 第六批工作记录：有来源的 embeddings 输入边界档案

2026-10-17，继续在受管工作树 detached HEAD `746f673` 的未提交实现上开发。负责 billing 的输入边界档案/真实执行输入准入、gateway 发送调用方、config/main 启动接线及对应回归。依赖既有独立 provider 成本契约、售价覆盖、原子 attempt、持久拒绝和唯一收尾；无新 core 接口、模型迁移、第三方依赖、CI 或前端变更。

本批先核对 OpenAI 官方 Create embeddings 与 Embeddings guide，依据记录在[输入边界档案](../reference/input-bound-profiles.md)。新增显式可选 `input_bound_profile: openai_embeddings_v1`，只登记官方根 URL 和三个明确模型，要求整请求输入包络至少 300000；不把单输入 8192 当作批次总上限。发送前核对最终 URL、正文模型、文本白名单与批次至多 2048 条。每次占用整个配置输入包络，不按字符估算或先低估执行。最高价格与币种仍由原独立成本契约提供，没有将官方输入约束误写成售价或错误请求免费证明。

gateway 改用 `StartPreparedAttempt` 将真实 URL/正文交给同一成本引擎，旧的无正文入口无法开启带档案的 attempt；未指定档案的旧契约保持运营声明语义。档案随现有 format=2 的 contract 快照保存、由现有账本展开显示，无新 HTTP 字段或 schema 版本。OpenAPI 保持 0.2.3 / 79 操作，只补现有 embeddings 说明。未登记模型、兼容代理、Chat 不自动继承该档案。

| 实际命令/检查 | 结果 |
|---|---|
| `go test -count=1 ./internal/billing ./internal/config ./internal/gateway -run 'TestInputBoundProfile\|TestEmbeddingInputBoundProfile\|TestStrictEmbeddingInputProfile'` | 首轮通过：未知档案/模型、低报总输入包络、非官方地址拒绝；配置非种子；真实最终 URL/操作/模型变更和 2049 批次拒绝；2048 批次允许；无正文入口不可绕过 |
| `GOARCH=amd64 GOFLAGS=-buildvcs=false` 下 `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 全部通过；HTTP Transport 模拟验证短输入仍占用完整 300000 Token 包络，示例费率下 `6000000` provider units，实际回执只收 `100` 用户 units，快照持久保留档案；包括原契约/权限/并发/强杀全量回归 |
| 同一 Go 环境 `python tests/smoke/local.py --go go` | 通过；原 gateway 隔离启动、JSON/SSE/控制台/模式隔离/strict 结算、真实 SSE 强杀重启 pending 与管理员幂等豁免 |
| 本批 8 个 Go 文件 `gofmt -l`、`git diff --check`、`git diff --exit-code -- go.mod go.sum` | 通过，无无关格式化或依赖变化 |

来源读取：旧 platform 文档入口在本机请求遇到 Cloudflare 页面，改由 developers.openai.com 官方 API 参考与指南实际读取核对；另一次只读 `Invoke-WebRequest` 确认官方整请求上限原文。未将挑战页作为来源，没有真实厂商请求或密钥使用。档案测试使用注入 Transport，不是在线厂商验收。

本地沿用已记录的 VCS stamping 环境限制，临时 `-buildvcs=false`；未改 CI 门槛。未重复运行未改动前端的 Node/浏览器检查，未运行 Linux race、Docker、真实厂商/客户端或远端 CI，未刷新远端基线、提交/推送或操作实际运行库。

此批为 C02 落实一条有官方来源的输入约束路径，但不做本地精确 Token 计数、不认证运营最高费率或失败请求收费，Chat 默认推理/缓存及其他厂商仍待独立依据。S5 仍为 5/6，S5-03/S5-C 完整验收保持未完成，S6/S7-A 尚未开始。


## 2026-10-17 主线推进批次：协调、能力、工具与原生协议

范围：沿用既有 S5-C 工作树，新增 `billing/input_profile` 的 Chat 档案；将现有 gateway 业务协调从 Gin 入口中抽离，增加操作/能力路由和 function tools 行为回归；S7-A 只做不依赖生产接线的 Responses 协议实现与样本。不新增支付、生产路由、迁移版本或第三方依赖，不检查实际旧库或扩大回滚专项。

对应用户“1–5”主线的真实进度：

| 项目 | 本轮实际完成的切片 | 尚未完成的阶段门槛 |
|---|---|---|
| 1 S5 收口 | 固定 GPT-4o Mini 日期快照、官方目标、整上下文输入上界及总输出限制；显式标准服务等级，发送前复核 | 未认证自定义包络、运营费率/币种/错误收费假设；S5-03 和完整 S5 仍未验收合并 |
| 2 非 Gin 协调 | `Gateway.Execute(context.Context, http.ResponseWriter, Execution)`；原 Chat/embeddings HTTP 真正调用同一协调函数，每次生成隔离结果、RPM/TPM 预留和收尾；request lease 覆盖预留至最终收尾 | 该入口仍以 HTTP writer 输出，不宣称已具备 native WS executor 或持久任务恢复端口；未完成阶段验收合并 |
| 3 操作与能力 | Chat/embeddings/Responses 操作标识、源协议与传输要求；真实候选过滤；同协议保留原字段，跨协议不再只记日志后丢字段 | Responses/background/WS 未开放能力；完整操作矩阵与应用接线继续随真实消费者验收 |
| 4 function tools | Anthropic JSON 多调用/多结果往返、真实 content_block 起始事件、交错参数分片与孤立 delta 拒绝；原 OpenAI 原生路径保留 | strict 继续拒绝工具；Gemini 的签名/工具历史没有无损表示，因此真实路由拒绝其工具请求，而非假定所有模型兼容 |
| 5 S7-A | `protocol/responses` 独立请求模型与原生 JSON/SSE 解码，经 core 执行样本，不转换成 Chat；事件序号、帧上限、失败/截断识别 | 尚无生产 HTTP 路由、owner/幂等持久状态、加密正文、原生后台任务、native WS、跨模式恢复或指定客户端验收 |

新增 `core.Result.Phase` / `UpstreamStatus` / `ProtocolComplete` 记录本地提交与协议事实；Dispatching 后的错误不能证明上游未执行，ProtocolComplete 不等于已结算。独立 Responses 解码成功也不等于生成成功（例如查询到 failed/queued 对象）；S7 应用仍须处理操作状态，不能直接把 HTTP 200 当完成。原生 SSE 不补 Chat 的 `[DONE]`，失败事件或缺少终态不伪造成功。

兼容性变化：跨协议只准许已建模的共同子集；如 `max_completion_tokens`、`tool_choice`、`parallel_tool_calls`、`response_format`、seed 和不支持的消息/工具字段会在发送前拒绝，不能为了“兼容”悄悄忽略。OpenAI 同协议保留已有原生字段行为。公共 JSON 验证抽至 `protocol/jsonutil`，billing、跨协议解析与 Responses 共同使用，保留重复键/UTF-8/深度/单文档限制。

来源：本轮重新阅读固定快照中的 RelayKit README、sub2api execution scope 和 CRS codexToOpenAI 事件逻辑，仅借鉴职责/失败场景，没有移植第三方代码。协议依据重新核对 OpenAI Docs 的 Chat/模型、Background、WebSocket、Fast mode，以及 Anthropic 官方流式事件；来源见[上游快照](../reference/upstream-snapshots.md)与[输入档案](../reference/input-bound-profiles.md)。仓库阅读副本位于原工作目录 `D:/my-project/TXAPI/.local/references/`，本受管工作树未再克隆副本，不把目录缺失写成上游不存在。

验证环境：Windows amd64，基线 `746f673` 加本轮工作树；Go 命令使用 `GOARCH=amd64`、`GOFLAGS=-buildvcs=false`。

| 实际执行 | 结果与适用范围 |
|---|---|
| `go build ./...`、`go vet ./...` | 最终代码通过 |
| `go test -json -count=1 ./...` | 最终完整普通测试通过，含依赖/契约、身份/账本/恢复和新增协调/工具/原生 Responses 协议；中间一轮因 stream_options 兼容拒绝失败，修复并完整复验后通过 |
| `go test -count=1 ./internal/gateway -run TestTypedCoordinator` | 未发送请求退还 TPM、每轮独立 RPM/TPM、并发状态隔离、panic 释放和结算期间名额覆盖通过；随后完整测试包含最终能力路由回归 |
| 四个现有 `node --check`、`node --test web/assets/api.test.mjs` | 语法及 6 项前端 API 行为通过 |
| `python tests/smoke/local.py --go go` | 临时库/假上游启动、鉴权、JSON/SSE、控制台、模式隔离、strict 结算及实际进程强杀恢复/幂等对账通过；该次在最后的未发送 TPM 收尾修正之前，修改不涉及财务/启动/恢复，复用其适用范围，TPM 已定向及全量复验 |
| 仅变更 Go 文件 `gofmt -w`、`git diff --check` | 通过；另只读比较所有 Go 文件按 LF 规范化后的 gofmt 结果，无偏差，不格式化无关 CRLF 文件 |
| Python AST、Markdown 本地路径及 OpenAPI 结构检查 | 两个冒烟脚本语法通过，80 个本地文档路径无缺失；OpenAPI 0.2.3 / 79 个操作，未注册 Responses 路由 |

远端 CI 结果在提交后单独记录。本地未运行 Linux race、Docker、真实浏览器/真实厂商或指定客户端验收，没有操作实际运行数据库。阶段清单保持未新增勾选，不能把这批代码当作已验收共同基线。

## 2026-10-17 功能提交与 CI 记录

功能分支 `feat/s5-s6-mainline`，提交 `b7939543e55d827e5d9b9ebb4c4545b8e0fcb838` 已推送。该提交包含以上既有未提交工作和本轮协调/能力/工具/原生协议切片，没有覆盖无关改动。推送前 fetch 核对 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07`，且该集成提交是功能分支祖先；尚未合并 master。

[Verify run 39007867301](https://github.com/ANRCM0/TXAPI/actions/runs/39007867301) 对上述精确功能 SHA **通过**，由 push 自动触发，使用 `gh run watch 39007867301 --exit-status --interval 30` 等待完成，退出码 0：

- `check` 通过：模块整洁、core/protocol 与协作依赖、OpenAPI 契约、构建、前端语法/行为、vet、gofmt、Linux 全量 race、覆盖率报告、端到端冒烟及性能基线报告。
- `delivery` 通过：容器安装、升级、备份恢复与正常退出；这是 CI 隔离环境证据，不是实际生产库升级或物理部署验收。
- CI 输出含 Actions Node 20 运行时弃用与 `ubuntu-latest` 镜像迁移提示，未导致失败；本轮未为清除提示而改动无关 workflow。

本记录及 README 的后续说明文档更新复用该功能 SHA 的执行证据：没有再次修改可执行代码、API 契约、配置或测试；只检查文档差异、链接和状态一致性。自动 push/PR CI 触发规则保持原样，不手动重复 dispatch。

**剩余门槛不因 CI 绿灯消失：**未指定输入档案的成本契约仍依赖运营声明，最高费率/币种/失败请求收费没有统一可验证证明；不能将这些契约宣称为已认证，也不能勾选 S5-03。S6 的代码与工具切片已通过回归，但仍待成本边界收口及阶段集成评审；S7-A 的生产 HTTP、持久会话与加密、原生后台任务、native WS、跨模式恢复和指定客户端验收尚未完成。本轮不将“1–5”整体报为完成，也不绕过共同基线门槛提前接线。

## 2026-10-17 继续推进：输入档案成为执行门槛

本批基线 `feat/s5-s6-mainline/01716458af22b50b330b0e55559bd50c500c0c2c`，开工工作树干净；fetch 确认 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07` 仍为分支祖先。继续现有 draft PR #5，不新建平行实现或提前接线 S7。负责 billing 的成本准入与持久拒绝、gateway 错误映射、相关行为回归和进程冒烟；依赖既有档案校验、唯一结算/恢复与资源收尾。无新路由/字段、迁移、第三方依赖、core/CI 变更，也没有移植参考项目实现或操作实际数据库。

实现：

- strict 的每个新 attempt 必须匹配已登记输入档案并提供最终 URL/正文；未指定档案的旧契约可继续加载/读取历史，但仅有 `evidence` 不再获准执行，返回 503 `provider_input_unproven`。Anthropic/Gemini、兼容代理及未登记模型当前没有可执行档案；legacy/disabled 的原兼容路径不受此门槛影响。
- `StartPreparedAttempt` 重新解析最终正文，要求输出字段/数值与本次成本占用元数据完全一致，拒绝低报后发送更大上限；旧 metadata-only `StartCostedAttempt` 弃用且不能准入。继续使用真实原子 attempt、固定售价/分组覆盖和累计 provider 包络，不新增可配置豁免。
- 本地拒绝不发送、不换凭据继续重试、不反馈健康、不生成 durable attempt；未执行请求释放双 hold。已有未知执行则保留原 attempt/provider 占用和 pending；拒绝原因在原账本和崩溃恢复中保持，不用新限制抹除历史财务事实。
- 既有精确算术、重试、预算、快照和事务回归保留。billing 私有测试夹具只调用实际财务实现以构造声明包络/历史事实，不能作为生产输入证明；网关正向用例改为真实档案配合测试专用 loopback Transport，不给生产二进制添加测试开关。

验证环境：Windows/PowerShell，Go 命令均设置 `GOARCH=amd64 GOFLAGS=-buildvcs=false`，沿用已记录的本地 VCS stamping 限制。以下命令针对本批工作树，而非复用 `b793954` 的旧 CI：

| 实际执行 | 结果与适用范围 |
|---|---|
| `go test -json -count=1 ./internal/billing ./internal/gateway ./cmd/gateway` | 首轮定向通过；输入无依据拒绝、正文限制绑定、已发生未知执行后的保守恢复，以及 loopback-only 夹具边界通过。后补的 strict/legacy/disabled JSON/SSE 隔离用例由下列全量覆盖 |
| `go build ./...`、`go vet ./...`、`go test -json -count=1 ./...` | 最终生产代码与本批测试全部通过，18 个测试包通过；包含依赖/契约、并发/账本/恢复与原有协议回归 |
| `python tests/smoke/local.py --go go` | 通过；生产 `go build` 二进制验证启动/鉴权/原调用/控制台/模式隔离及无档案 strict 拒绝；`go test -c ./cmd/gateway` 测试二进制调用同一生产 main，在测试专用本地传输下验证带档案 JSON/SSE 结算、SSE 强杀/重启 pending 与幂等豁免。通过后只修改一条说明注释，复用此执行证据 |
| 本批 Go 文件 `gofmt -l`、`git diff --check`、`git diff --exit-code -- go.mod go.sum` | 通过；只格式化本批文件，没有依赖变化 |
| 一次性 `python -` Python AST / Markdown 路径 / OpenAPI 检查 | 通过；51 个本地路径/锚点和 12 个本批 Go 文件格式检查通过，OpenAPI 与 HEAD 排除 description 后完全一致，保持 0.2.3 / 79 操作，无 Responses 生产路由 |

冒烟的测试传输仅连接本次随机 loopback 假上游、禁用部署代理并拒绝其他目标；不验证真实 TLS、厂商行为或费率。生产构建不包含 `_test.go` 的入口/环境变量与拨号器；真实财务、路由、生命周期和恢复代码没有被 mock。未改动前端源代码，本批未重复运行 Node/真实浏览器；未在本机运行 Linux race、Docker 或真实厂商/客户端验收。

本批远端完整 Verify 由 push/PR 自动触发并按最终 SHA 单独记录于 PR；本地通过不替代该门槛，不手动重复 dispatch。当前仍为 draft，不合并、不新增 S5/S6/S7 阶段完成勾选。**输入无依据仍可执行的缺口已关闭，但完整 S5-C 尚未完成：**最高费率及币种、同渠道凭据的价格上界、失败请求收费假设仍未认证；后续先完成这些可计价边界，再验收共同基线，最后推进 Responses HTTP/持久会话/后台/WS 生产接线。


## 2026-10-17 继续推进：标准费率下限与凭据范围

本批基于 `feat/s5-s6-mainline/478dc7b6265a5d6729c31f19b7cf5ab61acd51ab`，开工工作树干净。重新 fetch 确认 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07` 仍为分支祖先；继续 draft PR #5，不提前合并或接线 S7。上批精确 SHA 的 [push Verify](https://github.com/ANRCM0/TXAPI/actions/runs/39010257470) 和 [PR Verify](https://github.com/ANRCM0/TXAPI/actions/runs/39010260970) 均成功；它们只证明上批，不替本批变更背书。

负责目录及接口：billing 的上游准入/成本快照，config 与 main 的启动接线，gateway 的实际 prepared Header 消费与拒绝映射，相关测试和 `tests/smoke/local.py`。复用既有 provider 包络、双预算、原子 attempt、持久拒绝和唯一收尾/恢复；没有新路由/HTTP 字段、迁移、第三方依赖、core 或 CI 修改，不新增支付，不操作真实库或扩展旧库/回滚专项。

实现结果：

- 新增有来源的 `openai_standard_usd_2026_10_17` 费率档案、`currency` 与 `credential_bindings`。实际读取 OpenAI Docs Pricing Standard 表，为现有登记 Chat/embeddings 目标校验独立 provider 单价下限；不能用缓存折扣、Batch/Flex、错币种或零价低报。低于已登记费率/未知档案/不完整绑定拒绝启动，允许显式更高包络价；新 provider 预算仅 USD，不换算用户售价单位。
- 每个可执行契约显式绑定凭据 ID 和 Key 的 SHA-256；启动编译深复制 map，之后修改原配置不能变更已固定策略。无费率档案的历史契约可加载读取，但新尝试返回 `provider_tariff_unproven`；新 ID、原 ID 换 Key 或实际鉴权不匹配返回 `provider_credential_unproven`。不把所有渠道凭据自动纳入同一成本许可。
- `StartPreparedAttempt` 增加真实 Header 参数；gateway 同批迁入，不只检查 Meta 中的 Key。多鉴权值、大小写重复鉴权、额外组织/项目选择头与未登记头拒绝，不允许适配器改变计费账户后复用旧许可。普通最终标准传输头由现有 executor 管理，可信自定义 transport 不得在校验后替换关键输入。
- 档案、币种与凭据摘要随既有 format=2 快照保存并纳入摘要，不保存真实 Key/鉴权头；旧 format=1/2 仍可读，不回填虚构 USD 证明。新拒绝原因沿用管理员列表/详情和恢复：无执行释放双 hold，先前未知执行继续 pending/保留 provider 占用，不重试、不反馈健康。
- 原算术/并发/重试/快照/恢复回归保留；网关 provider 预算用例改为满足标准费率的包络，继续断言首次/重试预算耗尽与实际网络尝试数。测试用凭据均为隔离假值，生产没有豁免配置。

来源、每千 Token 换算、摘要生成方法和保证边界见[费率档案](../reference/provider-tariff-profiles.md)。这里只建立已核对标准费率下限与凭据绑定，**不是凭据实际最高价或完整发票证明**：运营仍须核实逐账户合同与附加费，错误/拒绝请求可计费 Token 的硬上界尚未由本批来源建立，带日期的价格不会在线自动刷新或保证未来不涨价。不能据此勾选 S5-03 或完整 S5-C。

验证环境：Windows/PowerShell，Go 使用 `GOARCH=amd64 GOFLAGS=-buildvcs=false`；VCS stamping 仍为已记录的本地环境限制。执行记录：

| 实际命令/检查 | 结果及适用范围 |
|---|---|
| `go test -json -count=1 ./internal/billing ./internal/gateway ./internal/config ./cmd/gateway` | 首轮通过；登记模型费率下限、错币种/档案/绑定、实际鉴权头、不可变 map、配置非种子、原账本与 gateway 重试、诊断恢复回归 |
| `go build ./...`、`go vet ./...`、`go test -json -count=1 ./...` | 最终代码通过，18 个测试包通过，含依赖/79 操作契约、权限/并发/协议/财务及恢复回归 |
| `python tests/smoke/local.py --go go` | 通过；生产二进制验证启动/原调用/控制台/模式隔离、无输入/无费率档案拒绝；同一生产 main 的测试专用 loopback 传输验证带档案 JSON/SSE 结算、实际管理 API 换 Key 后拒绝继承、SSE 强杀重启 pending 与幂等豁免 |
| 本批 Go 格式、差异、Python AST、文档路径/锚点与 OpenAPI 结构 | 通过；13 个变更 Go 文件、59 个本地文档路径/锚点及 Python AST 通过；OpenAPI 排除 description 后与 HEAD 完全一致，保持 0.2.3 / 79 操作；go.mod/go.sum、模型/迁移和 CI 无变化 |

未改前端源代码，本批不重复运行本地 Node/真实浏览器；未在本机运行 Linux race、Docker、真实上游/凭据账单或指定客户端验收。冒烟仍仅隔离假上游与临时 SQLite，不认证真实 TLS/价格。push/PR 自动完整 CI 按最终 SHA 另记于现有 PR，不改触发条件、不手动重复 dispatch。S5 保持 5/6，S6/S7 阶段未新增完成勾选；下一切片收口可保证的收费范围及失败收费假设，不把这些外部依据缺口隐藏为已完成。


## 2026-10-17 A1：架构优先与输出边界

用户调整开发顺序后，本批先交付真实执行输出端口，不新增厂商、支付、生产 Responses、会话、后台 API 或 WS 功能。代码基于 `66afe6bb11e43c2fcb594d60e14f88a99d59daa6`；已重新 fetch，`origin/master/90df76b0340e4cdfa6c332c781f7aa772a04ed07` 是祖先，开工时远端功能分支与 HEAD 同步。尚未作为共同架构基线合并。

负责范围：`internal/core`、`internal/gateway` 的输出与 HTTP 适配、现有消费者和风险回归；同步本计划、架构/协作/开发说明与 README。复用既有 core、protocol、access、Admission、billing 和依赖检查；不改模型/迁移、生产注册、HTTP DTO、OpenAPI、依赖或 CI。不是参考项目代码移植。

- 新增 `core.Output`：同步 Commit/Write/Flush/State，分别描述已编码响应的元数据、正文、背压和不可逆提交事实。输出由调用者持有，单次 generation 独占，不允许复用已提交出口；执行器不负责关闭它。
- `Executor.ExecuteTo` 消费输出端口，现有 `Execute` 只做 HTTP 适配；`HTTPOutput` 保留状态码、JSON/SSE 响应头、flush 和 write deadline。非 HTTP 输出不伪造 HTTPStatus，保持为 0。
- `Gateway.ExecuteTo`、协调 scope、准入与错误出口不再持有 HTTP writer。现有 Chat/embeddings 仍调用同一协调函数，经 `HTTPOutput` 输出，原 HTTP `Execute` 保留适配兼容；没有伪造 Gin context 或另起执行链。
- 原有有界正文捕获、重试/健康、授权、RPM/TPM、预算和收尾复用。输出短写、非法计数、提交/flush 失败显式失败，不对已坏输出追加错误或成功终态，不改目标重试、不惩罚凭据；已提交事实不因失败回退。
- 新回归用有界的非 HTTP observer 消费真实协调器和 HTTP executor，覆盖 Chat/embeddings、拒绝/Retry-After、未提交重试、提交失败/短写/flush、取消与资源释放。财务回归使用实际临时 SQLite 和原 strict 引擎，验证正常结算、无依据拒绝释放、输出失败仍保留双 hold/pending；没有降低生产准入。

边界：Output 仍承载 JSON/SSE 编码字节及状态/元数据，不是通用语义事件模型；非 HTTP observer 是测试消费者，不是持久 worker 或原生 WS 实现。core 仍是标准库单次 HTTP executor。操作模型、具体 billing 依赖、小接口与跨模式状态/安全基础仍由后续 A2/A3 收口；不因此勾选完整 S6、S7 或架构完成。

验证环境：Windows/PowerShell，`GOARCH=amd64 GOFLAGS=-buildvcs=false`。本机没有 Make，完整本地检查按 Makefile 的等价命令执行；VCS stamping 使用既有环境规避配置，不改变 CI。

| 实际命令/检查 | 结果和适用范围 |
|---|---|
| `go test -count=1 ./internal/core ./internal/gateway ./internal/architecture` | 初始输出切片通过；原 HTTP/协议/身份/租约行为和新增输出风险回归 |
| `go test -count=1 ./internal/gateway -run TestCoordinatorIndependentOutputPreservesStrictFinancialTruth` | 修正夹具后通过；原 strict 引擎/临时 SQLite/隔离 loopback，未更改计费准入 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 最终本地通过，18 个有测试包通过；含依赖、79 操作契约、权限/并发、协议、账本及恢复 |
| `node --check`（app/api/browser navigation/billing）与 `node --test web/assets/api.test.mjs` | 4 项语法检查、6 项现有行为测试通过；不是实际浏览器兼容验收 |
| `python tests/smoke/local.py --go go` | 通过；生产 HTTP JSON/SSE、控制台与 strict 无依据拒绝；同一生产 main 的隔离 loopback 传输验证结算、换 Key 拒绝、SSE 强杀后 pending 与幂等豁免 |
| 变更 Go 格式、`git diff --check`、文档路径/锚点与保护范围 | 通过；11 个 Go 文件已格式化，63 个本地文档路径/锚点有效；模型/迁移、OpenAPI、依赖和 CI 无变化 |

首轮新增财务用例及首次全量测试因测试夹具不符合既有 strict 规则而失败：未显式 standard service tier、预留上界不足、移除输入档案后仍带不匹配费率档案。已仅修正测试配置（显式 `default`、足额预留、无档案场景清除相应费率声明），定向复验和最终全量通过；没有关闭用例、放宽生产检查或用测试输出冒充免费执行。

本机未运行 Linux race、Docker、真实上游/客户端或浏览器验收。push/PR 自动完整 CI 按最终 SHA 单独记在现有 draft PR，不手动重复 dispatch；尚未合并，不勾选 S5-03、完整 S6/S7 或 A1–A4 整体完成。下一批 A2 收口操作模型与能力契约，不回到扩展厂商价格清单。

## 2026-10-17 A2 架构批次：操作信封、能力与计费消费端口

负责范围：`internal/protocol`、`internal/gateway`、`cmd/gateway` 的既有计费组装、依赖边界和对应风险回归；同步现有开发文档。父提交 `3474d443b49ffc354d777b96392e1c6ba0e4fb30`。复用 A1 输出、既有 access/Admission、core 和账本；不改 HTTP 契约、模型/迁移、依赖、CI 或生产能力注册，不是上游代码移植。

- `protocol.Request` 自持原始正文及匹配的操作事实，Chat/embeddings 使用各自的载荷投影；未知扩展字段不因解析投影丢失。路由、逐次重试、准入和 `PrepareRequest` 消费同一信封，请求/attempt 结果明确记录 Operation。每次转换获得正文副本，适配回调使用独立目标元数据副本，不能改写下一次尝试或解码类型。
- 内建适配器实现类型化 `adaptor.Capability`。现有 OpenAI 入站 HTTP 操作在候选过滤和发送准备处都检查支持范围；仅声明支持不能创造未实现的 Responses/background/WS。旧扩展 `SupportsOperation` 与纯文本 Chat 兼容回落保留。malformed tools/history 保守视为需要工具能力，不能误路由 Gemini 文本路径。
- embeddings 独立走原生请求改写与响应解码；不使用 Chat canonical 请求/响应，不生成 SSE DONE。当前内建生产支持仍只有 OpenAI-compatible embeddings，不扩大到 Anthropic/Gemini。旧整数 Mode 只保留在 HTTP/适配器兼容边界；未知模式及 embeddings SSE 在执行前拒绝，旧 Relay 的路由投影必须匹配正文。
- gateway 不再持有具体 `billing.Engine`，按实际调用拆为 `LegacyBilling` 与 `StrictBilling`。main 显式注入原引擎；旧 Billing 选项仅在实现 strict 接口时兼容推导。strict 不走 legacy 降级，缺接口/未就绪仍 503；所有已登记输入/费率/凭据、最终 URL/正文/Header、成本占用和持久收尾检查不变。日志回调、access limiter、Admission 已是小接口，不另造事件总线。
- 新回归验证独立载荷/字段保真、正文及元数据隔离、发送准备双重能力守卫、重试和准入的操作一致性；实际临时 SQLite/原引擎验证 strict-only 接入可结算、端口修改快照不会改变实际 wire，回执持久化故障不重试、双 hold/provider 占用保持 pending。可选接口 typed nil 和不同计费模式不会相互误调用。架构检查阻止 gateway 再引入具体 Engine 或把 ModelRequest 带回内部协调链。

边界：这是现有操作的 A2 切片，不是所有未来操作已统一建模。Chat 的 `ProtocolAdaptor` 只服务 Chat 转换；独立 Responses 样本仍未接生产。输出仍为编码 JSON/SSE，usage 仍是现有三整数观察；协议终态、输出提交与财务终态分别处理，不宣称已有通用语义事件、原生 WS、持久 worker 或新的严格计量证明。S5-03、完整 S6/S7 与 A1–A4 整体验收均不新增勾选。

验证环境：Windows/PowerShell，`GOARCH=amd64 GOFLAGS=-buildvcs=false`；本机无 Make，执行 Makefile 等价命令。2026-10-17 重新 fetch 后 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07` 仍为祖先，远端本分支与父提交同步，原 PR 保持 draft。未更改集成基线。

| 实际命令/检查 | 结果与适用范围 |
|---|---|
| `go test -count=1 ./internal/protocol/... ./internal/gateway ./internal/architecture` | 初批通过；现有兼容/权限/租约/账本和新增操作/能力/消费端口回归 |
| `go test -count=1 ./internal/gateway -run TestStrictOnlyPortPreservesDurableFinalization` | 最后 wire 快照隔离加固后通过；实际账本/双 hold 与回执持久化故障 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 首轮与最终代码均通过，18 个有测试包；含依赖/79 操作契约、原并发/权限/协议/财务/恢复回归 |
| 4 项 `node --check` 与 `node --test web/assets/api.test.mjs` | 同批首轮通过，6 项行为测试；前端/浏览器脚本与父提交 `3474d44` 未变，最后内部 wire 隔离修改不影响其输入，因此复用，不重复运行 |
| `python tests/smoke/local.py --go go` | 首轮与最终代码均通过；生产 HTTP JSON/SSE/控制台和 strict 拒绝，同一 main 隔离 loopback 验证结算/换 Key 拒绝、SSE 强杀重启 pending 与幂等豁免 |
| 变更 Go 格式、`git diff --check`、文档与保护范围 | 25 个变更 Go 文件格式、最终差异检查与 63 个本地文档路径/锚点通过；OpenAPI/依赖/CI/模型/store/core 无变化 |

首轮完整检查通过后，代码复核进一步为计费端口复制 wire 正文/Header，避免可选实现复用入参影响实际发送；定向及最终完整 Go 检查已重新通过。未关闭测试、放宽 strict 或添加生产测试豁免。

未在本机运行 Linux race、Docker、真实上游/客户端、浏览器或生产库升级验收。push/PR 自动完整 CI 以最终 SHA 单独记录在现有 draft PR，不手动 dispatch、不合并。下一批 A3 按最小真实消费者逐步验证传输、状态、授权、容量、恢复与安全边界，不开始上层产品功能大全。

## 2026-10-17 A3-01：派发身份与最小进程内 worker

负责范围：新增 `internal/generation` 的实际 worker 消费者、`internal/access` 的非密钥绑定、`internal/store` 的共享当前身份解析，及 model 投影/HTTP 鉴权消费者、依赖边界、相关风险回归和现有文档。父提交 `5ea7393c41ad297e2c21d7d750341fcd7e927ea2`；本批重新 fetch 后 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07` 仍为祖先，远端功能分支与父提交同步，现有 PR #5 保持 draft。此处记录本批提交前最终本地验证；最终 SHA 和自动 CI 结果按提交记录于该 PR，不复用父提交绿灯。

- `access.Binding` 只有可信 user/Key 绑定。`Store.ResolvePrincipal` 按当前归属、有效期、启用/删除状态、系统/owner/Key 策略及限额重新生成 Principal；HTTP `LookupToken` 与 worker 共用同一读事务的权威投影，没有第二套鉴权规则、原始 Key 留存或管理员代执行。`TokenView.Principal` 只统一拷贝投影，不增加 schema 字段。取消的读与存储异常不授权；已撤销和存储不可用在内部可区分。
- `generation.Runner` 是具体的固定 worker、有界队列、正文/标识副本和一次性结果交接实现；通过注入的 `Gateway.ExecuteTo` 真实执行现有操作，不另建准入、调度、上游重试或计费链。只有服务 context 控制执行，观察者 context 不被保留；排队起算取消时限，queued+active 正文副本配置预算最高 64 MiB，不为满队列建立等待 goroutine。排队不占共享 generation/network attempt 槽，真正派发时才进入现有生命周期。
- 每次派发先解析绑定，排队后撤销或缩小权限会生效；观察者断开只停止观察，显式 Cancel/期限/服务关闭才取消生成。结果只交付一个接收者，输出在真正结束前仍归执行独占。提交后 panic 保留输出提交事实，保守记可能已派发且不可重放；worker 继续服务，所有正常/拒绝/取消/panic 路径的进程内容量释放。
- 真实临时 SQLite/原账本回归证明：worker 关闭导致已派发执行未知时，只释放进程内容量，双预算 hold/provider 占用仍为 `pending`，没有自动退款或重新提交。并发策略写入发生在 token 与 owner 查询之间时，当前解析仍返回一致旧快照，下次生成才读新快照，不拼接两代权限。并发 Submit/服务取消/Close、满队列、队列过期、关闭超时、错误脱敏和回调身份错配均有风险回归。

明确边界：这是 A3 的第一个进程内消费者切片，不是持久后台产品。main 尚未启动 runner；没有公开后台路由、任务 ID、fencing/领取、原子任务意图+预留、持久事件/游标或重启重派。进程会丢失未执行的内存交接，不能用它承诺公开后台受理。排队不保证逐用户公平；已派发轮次使用本轮身份快照，撤销不会自动中止在飞生成，后续任务撤销传播与安全策略仍待交付。`Close` 超时必须保留 gateway/store，不能伪称可信不合作回调已经停止。HTTP 前台断开行为、strict 许可范围、schema/迁移、OpenAPI 79 操作、core/gateway 执行实现、main、依赖与 CI 均未改变；未移植第三方代码。

验证环境：Windows/PowerShell、Go 1.26.3、Node 24.14.1、Python 3.14.3；Go 命令显式 `GOARCH=amd64 GOFLAGS=-buildvcs=false`。本机无 Make，按 Makefile 执行等价命令。

| 实际命令/检查 | 结果与适用范围 |
|---|---|
| `go test -count=1 ./internal/generation ./internal/store ./internal/architecture` | 通过；worker/一致身份快照/权威拒绝/取消及未知执行 hold、原 store 回归与新增依赖边界 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 最终代码通过，19 个有测试包；含现有 79 操作契约、HTTP/权限/协议/并发/迁移/财务/恢复与新增消费者风险回归 |
| 4 项 `node --check` 与 `node --test web/assets/api.test.mjs` | 通过，6 项行为用例；无前端源代码或浏览器脚本变更 |
| `python tests/smoke/local.py --go go` | 通过；实际生产启动/鉴权/JSON/SSE/控制台和 strict 拒绝；同一 main 的隔离 loopback 结算、换 Key 拒绝、SSE 强杀重启 pending/幂等审计豁免。它不是生产 runner 接线或真实上游证明 |
| 格式/差异/文档 | 仅格式化 11 个变更 Go 文件；`git diff --check` 与 4 份更新文档的 46 个本地链接/锚点通过；新增 generation 依赖规则不放宽原模块边界 |

早期定向测试有夹具字段命名和取消结果断言错误：已修正字段，区分 attempt 的 canceled 与取消后出口不可写的最终 client_write_error；未为测试改变生产取消/账务语义或关闭用例。此后定向及最终完整检查均通过。本机未执行 Linux race、Docker、真实上游/客户端/浏览器或生产数据库验收；由 push/PR 自动触发的最终完整 CI 单独记录，不手动重复 dispatch。完整 A3、S5-03、S6/S7 与 A1–A4 共同基线均不新增完成勾选、不合并。下一切片继续安全持久状态/恢复及最小传输消费者，不扩成上层功能大全。


## 2026-10-17 A3-02：加密输出记录与保守中断恢复

负责目录：`internal/generation` 实际消费端、`internal/model` / `internal/store` 的统一 v8 结构/事务、依赖约束、风险回归及 `tests/smoke/delivery.py` 对新增迁移的交付断言；同步现有文档，不新增另一份计划。父提交 `951aca920c1549a2ad5199289cc315ed39181b2a`，本批 fetch 确认 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07` 是祖先且功能分支同步。沿用现有 draft PR #5；本批不合并、不勾选完整阶段。

- `ArchiveStore` 是加密输出追加/读取/删除的小端口，真实实现使用现有 Store/SQLite；generation 只增加 model 依赖，不导入 store/GORM/billing/Gin，不改 core/gateway 的执行或财务实现。
- `SubmitRecorded` 先原子预留 owner/实例的有界密文容量，再交给原 worker；每次 Commit/Write 都在加密后实际落库，输出故障对原协调器可见。接收不保存输入正文或原始 Key，不代表持久任务/预算受理。普通 Submit 仍不存正文，main 未启用 runner/归档。
- AES-256-GCM 独立注入密钥环、随机 nonce，认证记录/owner/Key/过期/容量/类型/序号；读取/删除重新验证当前绑定，读取检查当前模型策略。无 key 或篡改不回落明文，也不返回部分明文。密钥轮换/备份、TTL、逻辑容量与尚无生产注入/自动清理工具的边界见开发指南。
- 统一 schema v8 只新增输出记录/加密片段；旧迁移/账本不改，不从普通日志补造内容。事务序号 CAS、终态写入、容量预留和级联删除有真实 SQLite 回归；过期立即不可读、清理按有界批次回收容量。
- 输出收尾与账本终态分开：open/sealed/interrupted 是观察完整性，不是生成/收费状态。独占启动恢复只标记中断，拒绝旧写入句柄，绝不返回可重放任务。实际子进程在部分加密 SSE 持久后被强杀，重启、真实租约到期后仍保留未知执行双 hold/provider 占用；另验证归档最终观察写失败不会撤销已结算费用。
- 存储 I/O 不占停接锁，Close 等待尚在准备的交接及 worker/收尾；确认丢失和 finalizer panic 不重试生成、不丢提交事实、不杀 worker。保留已有取消/权限/协议/财务与队列回归，没有生产测试豁免。

验证环境：2026-10-17 Windows/PowerShell，Go 1.26.3、Node 24.14.1、Python 3.14.3，Go 命令显式 `GOARCH=amd64 GOFLAGS=-buildvcs=false`。本机无 Make，按 Makefile 执行等价命令。

| 实际命令/检查 | 结果与适用范围 |
|---|---|
| `go test -count=1 ./internal/generation ./internal/store ./internal/architecture` | 初批最终通过；真实 SQLite 事务/容量并发、迁移/隔离、密钥/篡改/顺序/TTL、丢确认/finalizer panic、关停及强杀恢复 |
| `go test -count=1 ./internal/generation -run 'TestRecordedOutputFailure\|TestArchiveForcedProcessDeath'` | 通过；真实 Engine/账本和实际强杀进程，不是内存状态模拟；原 strict 档案/费率/凭据绑定门槛保持 |
| `go test -count=1 ./internal/generation -run 'TestArchiveModelAndDispatch\|TestArchiveHandoff\|TestRunnerConcurrent'` | 最后快照一致性加固后通过；入队前以同一输入副本绑定归档模型和实际派发，慢存储不持停接锁，旧并发关闭回归保留 |
| `go build ./...`、`go vet ./...`、`go test -count=1 ./...` | 首轮和最终代码通过，19 个有测试包；含依赖/79 操作契约、权限/协议/并发、迁移、财务及恢复 |
| 4 项 `node --check` 与 `node --test web/assets/api.test.mjs` | 同批首轮通过，6 项行为用例；最终内部快照/归档测试不改前端或浏览器脚本及其输入，故复用、不重复运行 |
| `python tests/smoke/local.py --go go` | 首轮及最终代码通过；生产启动/JSON/SSE/控制台与 strict 拒绝，同一生产 main 的 loopback 夹具验证结算、换 Key 拒绝及 SSE 强杀重启 pending/审计豁免；不是生产归档接线 |
| 格式、差异、文档与脚本 | 12 个变更 Go 文件格式、`git diff --check`、5 份文档的 63 个本地链接/锚点及交付脚本 Python AST 通过；core/gateway/server/main/OpenAPI/第三方依赖/CI 未改 |

首轮定向编译有一个未使用 import，首轮 SSE 夹具缺少既有 decoder 要求的 finish_reason；仅修正新测试，没有放宽生产终态。最终复核进一步把输入副本提前到身份/存储回调前，防止归档模型与实际派发正文错代，补充真实协调器回归及最终观察丢确认用例；定向和最终完整检查重新通过。没有关闭既有回归或改动生产账本语义。

本机未运行 Linux race、Docker、真实上游/客户端/浏览器或生产库升级。最终 SHA 的 push/PR 自动完整 CI 单独记录于现有 draft PR，不手动 dispatch；没有把父提交绿灯或本地结果当作本批远端验收。

边界：这是 A3 的真实输出持久消费者，不是通用任务平台、Responses 持久历史/协议事件，尚无任务意图+账本原子受理、领取/租约/fencing、原生后台/WS、生产密钥注入或公开 API。OpenAPI 保持 0.2.3 / 79 操作，依赖与 CI 触发不改；交付脚本只同步 v8 迁移与空归档表的断言。现有上游 Key 明文落库风险保留。后续 A3 继续最小传输和任务原子/恢复边界，A4 才收口最终共同基线，不把本批当作项目完成。

## 2026-10-17 A3-03：有界 WS 传输与连接生命周期

负责范围：`internal/transport/websocket` 纯服务端 I/O、`internal/access` 独立物理连接容量、`internal/generation` 的实际 socket 消费回归、依赖约束与文档。父提交 `9d92d6507caa62f41d45cedc3a84d355b94b0284`；本批重新 fetch 确认 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07` 为祖先、功能分支同步。复核父提交 push/PR CI `39024845146` / `39024848225` 均 success，仅用于确认原基线，不替本批代码背书；沿用 draft PR #5、不合并、不勾选完整阶段。

- 采用固定版本 `github.com/coder/websocket v1.8.15`（ISC），已核对官方发布与所用 API 的该版本源码；生产库只进入 transport，不进入 core/gateway/protocol/generation。无新增传递依赖；保留模块校验，没有修改全局 Go 代理或跳过 checksum。
- Manager 限制 active/pending、消息、读/写/握手与绝对连接寿命，禁用压缩；精确 Origin/TLS 规则、UTF-8/完整消息限额、单 reader/串行有时限写、错误白名单与关闭职责见开发指南。取消握手 callback 完成后才 reset deadline，防止过期写期限污染升级连接。Close 会 join pending 握手，超时不假装完成。
- ConnectionCapacity 只接可信 Binding，限制实例/用户/Key 的物理连接并幂等释放；不把空闲连接当 generation/attempt/RPM/预算。Close 停接且保留尚在占用的租约事实，连接与生成的生命周期独立。
- 测试专用应用通过真实 WS socket 调用原 Runner/Gateway/SQLite/严格账本；每轮重新解析身份，同连接撤销/收权后拒绝、无额外 attempt/预留。同外部轮次 ID 两条连接强制并发时输出隔离；无另一套授权、预算或收尾实现。
- 前台断开显式 Cancel 并等待原收尾，只执行一次；未知成本继续 pending/双 hold/provider 占用，进程槽与连接槽均释放。记录型观察者断开和 Manager 关闭不取消已交接生成，释放上游后可按原 owner/Key 读取完整加密结果；不是公开后台持久受理。
- 真实 TCP/TLS 回归覆盖 HTTP handler 返回/HTTP shutdown 后连接仍独立、控制帧与分片、消息/UTF-8、Origin/Forwarded、读/写超时/绝对寿命、真实慢读背压、失败/阻塞握手及并发关闭；有序关闭须另外 join 应用/runner 再关闭 gateway/store。

验证环境：2026-10-17 Windows/PowerShell，Go 1.26.3、Node 24.14.1、Python 3.14.3；Go 显式 `GOARCH=amd64 GOFLAGS=-buildvcs=false`，本机无 Make，按 Makefile 等价执行。

| 实际命令/检查 | 结果及适用范围 |
|---|---|
| `go test -count=1 ./internal/access ./internal/transport/websocket ./internal/generation ./internal/architecture`，失败项修正后 `go test -count=1 ./internal/transport/websocket ./internal/generation` | 最终通过；物理容量/实际 TCP/TLS/关闭竞争和真实协调器/严格账本集成；其余两包由最终全量再次验证 |
| `go build ./...`、`go vet ./...`、`go test -json -count=1 ./...` | 最终代码通过，20 个有测试包；依赖/79 操作契约、权限/协议/并发、旧迁移、财务及强杀恢复回归保留 |
| 4 项 `node --check` 与 `node --test web/assets/api.test.mjs` | 通过，6 项行为用例 |
| `python tests/smoke/local.py --go go` | 通过；生产启动/原 JSON/SSE/控制台/strict 拒绝及同一 main 的 loopback 结算/凭据轮换拒绝/强杀重启 pending 与幂等豁免；没有生产 WS 接线冒烟 |
| 格式、差异、文档与边界 | 6 个变更 Go 文件格式、`git diff --check`、5 份文档的 64 个本地链接/锚点通过；core/gateway/model/store/server/main/OpenAPI/CI 不改，schema 仍 v8、OpenAPI 仍 0.2.3 / 79 操作 |

首轮控制帧测试遗漏 Ping 发起端的 reader，Origin 成功握手的断言没有等待 handler 关闭，集成用例有一处状态常量名错误；集中修正新 fixture。未知执行断言按既有 Replayable（请求体可重放）与实际取消/账务事实分开，未放宽生产重试或财务规则。实际读写/关闭路径未绕过任何测试门槛。

本机未运行 Linux race、Docker、真实上游/原生 WS 客户端、浏览器或生产库操作。最终 SHA 的自动 push/PR 完整 CI 单独记录于既有 PR，不手动 dispatch。本批实现入站服务端 transport 与最小 socket 消费证据，没有原生上游 WS executor、公开应用 wire 协议/路由、Responses 事件/续链、任务意图+账本原子受理、领取/fencing 或重启重派；main 未启动。A3 后续继续原子任务/恢复边界，再由 A4 收口共同架构基线，S5-03 与完整 S6/S7-A 仍未验收。


## 2026-10-17 A3-04：加密意图与双预算原子受理

负责范围：`internal/generation` 的 IntentAcceptor 实际消费者、`internal/billing` 的类型化原子持久端口与原 reserve/finish 事务复用、`internal/model` / `internal/store` 的统一 v9 迁移、相关风险回归及 `tests/smoke/delivery.py` 的交付断言。父提交 `8c4a2f1e38d111534ad8cc34d52fe6732ca2b9e1`；开工和提交前 fetch 的 `origin/master=90df76b0340e4cdfa6c332c781f7aa772a04ed07` 仍为祖先，功能分支与父提交同步。父提交 push/PR CI `39042296316` / `39042300080` 通过只用于确认原基线；本批代码单独验证，沿用 draft PR #5、不合并、不提前勾选 A3/A4 或完整阶段。

- IntentAdmission 端口以同一事务保存加密输入意图、幂等摘要版本标记及原用户/Key 双 hold；generation 不导入 billing/store/GORM，原账本保持唯一财务权威。原普通 Reserve 和受理共享同一内部 reserve 实现，取消共享 finish 实现，不新增一套余额、鉴权、迁移或任意 DB 回调。
- 当前 owner/创建 Key/模型权限每次重新解析；先于可信回调复制输入，JSON 有界/拒绝重复字段/规范对象键和空白。独立稳定 HMAC 只保存幂等键与请求的摘要；同作用域同键同请求返回原回执，不同请求冲突。首次成功事务保存摘要密钥指纹；重启误换 DigestKey 失败关闭，不能创建新幂等命名空间并多扣一次。
- 独立 AES-256-GCM 密钥环加密正文/会话标识，随机 nonce 和认证元数据绑定 owner/Key/操作/模型/billing ID/期限/摘要。保留旧内容密钥可读取原回执，内容密钥轮换或运行 TTL 变化不改变同请求幂等身份；缺钥/篡改不返回明文，无默认/明文回落。稳定摘要密钥暂不支持轮换，不能删除其指纹标记绕过。
- schema v9 仅新增 generation_intents，不改历史迁移/财务事实、不回填任务。输入、密文、实例/owner 记录数/字节、QueueTTL/Retention 和清理批次有界；终态或到期但仍保留的行继续计入容量。详见开发指南的配置/密钥备份和保留期规则；逻辑容量不是物理磁盘配额或安全擦除。
- 这是不可派发的短期意图，不进入 Runner。原 StartAttempt 事务明确拒绝意图关联 reservation；本批无领取/续租/fencing、任务派发、价格/能力恢复或重启重派，不扩大 strict 可发送范围。Read 返回解密输入不是恢复执行许可。
- Cancel 与到期恢复只在同一事务证明 reservation 仍 reserved、同 owner/Key/模型、无任何 attempt/provider 占用后，调用原 finish 零费用释放双 hold并写终态。异常执行标为 uncertain/账本 pending、保留占用；原普通过期恢复排除所有意图关联行，避免错误退款。恢复不读正文、不需密钥、不返回任务；清理只删可证明零费用且无 attempt 的到期 cancelled/expired 内容，财务事实保留，uncertain 不自动删。

验证环境：2026-10-17 Windows/PowerShell，Go 1.26.3、Node 24.14.1、Python 3.14.3；Go 显式 `GOARCH=amd64 GOFLAGS=-buildvcs=false`，本机无 Make，按 Makefile 等价执行。

| 实际命令/检查 | 结果及适用范围 |
|---|---|
| `go test -run '^$' ./internal/billing ./internal/generation ./internal/store ./internal/architecture` | 开发期编译通过 |
| `go test -count=1 ./internal/generation ./internal/billing ./internal/store ./internal/architecture` | 开发期集中定向通过；真实 SQLite 双预算并发、幂等/丢确认、跨表插入/取消回滚、权限/容量/加密与保守恢复；后续新增用例由下列定向和最终全量覆盖 |
| `go test -count=1 ./internal/generation -run '^TestIntent'`；`go test -count=1 ./internal/store -run 'TestIntentV9\|TestArchiveV8'` | 最终新增意图与迁移回归通过；另因误将慢编译判断为等待，曾定向重跑 forced-process-death 调试，用例仍通过，无生产超时放宽 |
| `go build ./...`、`go vet ./...`、`go test -json -count=1 ./...` | 最终代码通过，20 个有测试包；原依赖/79 操作契约、授权/协议/并发、财务、迁移与强杀恢复回归保留 |
| 4 项 `node --check` 与 `node --test web/assets/api.test.mjs` | 本批执行通过，6 项行为用例 |
| `python tests/smoke/local.py --go go` | 本批通过；生产启动/JSON/SSE/控制台/strict 拒绝及同 main 的隔离结算、凭据轮换拒绝、强杀重启 pending 和审计幂等豁免；不是生产意图接线冒烟 |
| 格式/差异/文档/交付脚本 | 只格式化 12 个变更 Go 文件；`git diff --check`、5 份现有文档的 65 个本地链接/锚点、delivery Python AST 通过。OpenAPI 保持 0.2.3 / 79 操作，无依赖、workflow、core/gateway/server/main 变更 |

真实 OS 子进程分别在双 hold 与意图事务提交前被强杀、和成功受理后被强杀；前者重启无意图/预留/兼容额度占用，后者保留唯一回执、可在相同密钥下解密读取，真实排队期限到期后重复恢复只零费用释放未执行预算，无 attempt。SQLite/WAL 未出现输入与原幂等键的明文标记。测试专用 GORM hook 不进入生产；没有用内存状态模拟崩溃或加生产绕过开关。并发同键仅一条意图/一个 hold，跨 Key 不突破用户预算；人为注入未知 attempt 的回归保留双 hold/provider 占用并拒绝自动清理。

本机未运行 Linux race、Docker、真实上游/客户端/浏览器或生产库操作。最终 SHA 的自动 push/PR 完整 CI 单独记入现有 PR，不手动 dispatch；容器交付脚本已同步 v9 和空意图表断言，实际远端结果不借用旧绿灯。本批不开放持久后台 API/Responses/WS 产品能力，main 未启动受理器，旧上游凭据明文风险保留。下一批 A3 收口领取/租约/fencing 与安全派发/未知恢复，再由 A4 验收共同架构基线；S5-03 和完整 S6/S7-A 仍未完成。
