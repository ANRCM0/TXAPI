# 开发指南

本页记录可执行命令、运行约定与开发验证节奏。目标设计见 [ARCHITECTURE.md](ARCHITECTURE.md)，阶段状态见 [PLAN.md](PLAN.md)。

2026-10-17 按架构优先更新：本页的 S1–S5 标题按功能/运行主题保留，不表示仍停留在当时开发阶段。当前 S0–S4/F0 已验收，S5 未完整收口；S6 协调/能力/工具已有通过 CI 的切片，S7-A 仅独立 Responses 协议实现，生产状态型功能仍待开发。A1/A2 已增加输出端口、操作信封、能力检查及计费小接口；A3-01 增加共享身份解析及真正调用 `ExecuteTo` 的有界进程内 worker，A3-02 增加可选加密输出记录与统一 schema v8，A3-03 增加有界 WS transport 与独立连接容量，A3-04 增加加密意图+双预算原子受理与统一 schema v9；尚未接公开后台/WS 路由、意图派发或生产 worker；完整底层架构仍待验收。历史提交和 CI 证据见 [验收归档](history/acceptance-s0-s5.md)；下列命令是操作指南，不代表本次文档批次重新执行。

## 工具与工作目录

- Go 1.23 或更高版本，以 `go.mod` 为语言基线。
- Python 3.10+ 用于标准库冒烟脚本；可选 OpenAI SDK 检查另见下文。
- Node 20+ 仅用于控制台语法/行为验证；编译、部署和运行网关不需要 Node，也不需要安装前端依赖。
- Make 是便捷入口，不是程序运行依赖。
- 所有命令从仓库根目录执行，数据库相对路径按进程工作目录解析。

```bash
make init
make build
make check
make smoke
```

`make init` 只在缺少配置时复制模板。`make clean` 仅移除 `bin/`，不会删除本地配置和数据库。可以用 `make GO=/path/to/go PYTHON=/path/to/python3 check` 指定工具。

## S4 控制台与配置发布

控制台位于 `/`，静态资源位于 `/ui/`，由 `web/embed.go` 直接嵌入二进制。原生 ES Modules/DOM 是本项目唯一前端实现，无 CDN、前端开发服务器或打包依赖。未知 API、未知资源不会回退为 HTML。

先按 S2 步骤初始化管理员，再访问根路径登录。管理员用用户页创建账号并授权、渠道页配置上游及凭据；用户登录后创建自己的 Key，在模型页确认权限、测试页用调用 Key 发非流式请求，再到概览/日志刷新查看用量。测试会真实消耗所配置上游资源，管理会话不能代替调用 Key。浏览器会话只在页面内存中保存，刷新需重新登录；密码/Key 不写 localStorage、sessionStorage 或 URL。

配置发布接口均要求管理员 Session Bearer：

| 接口 | 行为 |
|---|---|
| `GET /api/admin/config-status` | 返回 epoch、channels/prices 的 desired_revision、applied_revision、last_error、updated_at |
| `POST /api/admin/config-status/apply` | 对当前已保存配置重新发布，不增加 desired revision；任一失败返回 503 并附状态 |

渠道/凭据和价格/倍率每次成功写入后同步尝试发布；修改响应头含 `X-TXAPI-Desired-Revision`、`X-TXAPI-Applied-Revision`、`X-TXAPI-Publication-State: applied/pending`。成功码表示保存，不保证已应用。失败保留旧运行快照，last_error 是不含路径/密钥的安全提示，详细原因只记服务端日志。两个资源分别发布，不承诺跨资源原子事务。单个非法启用渠道阻止整批渠道发布，可修正或停用后重试。

revision 从每次进程启动重新计数，仅在同一 epoch 内可比较，不写数据库、不替代 schema 版本，不表示可用性或上游鉴权已验证。用户/Key/模型权限仍由数据库在下一次鉴权生效；YAML 的进程设置必须重启，不属于控制台快照发布。后台健康持久化和恢复只刷新已应用凭据的同代健康状态，不能顺带发布等待中的渠道配置。

凭据 PUT 的 api_key 现在可以省略/为空以保留原密钥；仅修改 name/weight 不需要取回明文。不要将掩码字符串作为新 Key 提交。渠道编辑页不发送已有 api_key，避免替换整个凭据池。

Key 更新可传 `preserve_expiry: true` 保留精确过期时刻（包括已过期状态）；控制台编辑默认保留，只有明确选择替换才按保存时刻计算新期限。省略/false 保持旧接口的 expires_in_seconds 完整替换语义，0 表示永不过期。

渠道/凭据已提交但随后缓存刷新/读取失败时，返回 500、`saved: true` 和安全错误提示，并照常推进发布状态。此时不能把失败当成事务回滚而盲目重复创建；先查询配置状态及资源。渠道快照独立读取数据库，并串行更新管理缓存，避免旧的并发缓存重载覆盖较新的快照。

### 观测与日志背压

`gateway_attempts_total{channel,status,termination}` 和 `gateway_attempt_duration_seconds{channel}` 统计实际执行的 attempt（包含重试）；本地准入拒绝不算上游尝试。原请求指标仍按最终逻辑请求统计。

普通访问日志队列容量 4096，批量 200，正常每 200ms flush；队列满/关闭时立即丢弃并计数，不退回请求线程同步写 SQLite。正常关闭排空已接收记录；写库失败先逐条重试，最终失败计数。指标包括 `gateway_log_queue_depth`、`gateway_log_queue_capacity`、`gateway_logs_dropped_total`、`gateway_logs_failed_total`。计数随进程重启归零，应监控增量。数据库失败和进程崩溃仍可能丢日志；本人用量是保留日志的聚合，绝非可靠账本。

默认日志保留 30 天，YAML 显式 `log_retention_days: 0` 或同名环境变量为 0 均关闭自动清理。不要将停止清理等同于备份。

### 本地验证与隔离预览

```bash
make check
make smoke
# 只检查控制台，不安装依赖
make web-check
# 完成全部冒烟后保留临时服务，手动检查页面；Ctrl+C 关闭并清理
python tests/smoke/local.py --go go --preview
```

预览使用随机本地端口、临时库、假上游与固定的测试账号（命令会输出），不读个人 config.yaml。冒烟子进程会清除继承的 GATEWAY_*/TXAPI_ADMIN_* 部署变量，避免误用真实数据库、监听地址或凭据。不能把模拟兼容性当成真实厂商验收。

冒烟在 2026-10-17 输入门槛收口后分两层：生产 `go build` 二进制验证真实启动、原转发与无依据 strict 拒绝；strict 正向结算和强杀恢复由 `go test -c ./cmd/gateway` 的测试二进制调用同一生产 main，通过 `_test.go` 中的 loopback-only 网络夹具模拟已登记官方目标。夹具仅拨本次随机本地端口，禁止其他网络目标并关闭部署代理；它不认证真实 TLS、厂商行为或费率。生产二进制不包含测试入口/环境开关，没有降低成本门槛。预算、事务、迁移、路由、收尾和恢复仍用实际生产代码与临时 SQLite，不改成 mock 账本。

可选浏览器竞态回归：已有 Playwright 环境中执行 `node tests/browser/navigation.cjs`（必要时用 NODE_PATH 指向其模块目录，PLAYWRIGHT_EXECUTABLE_PATH 指定本机浏览器）。脚本使用独立浏览器上下文、随机本机端口与模拟 API，不读取真实会话或调用上游；验证旧测试页的延迟模型列表不能覆盖当前页面清理逻辑，离开页面必须取消当前调用。此项不是编译/部署依赖，也没有冒充为现有 CI 已执行的检查。

账本页面回归：同一环境执行 `node tests/browser/billing.cjs`，验证精确金额、未知成本提示、历史价、完整 ID 确认、显式零用量、最终决策只读和角色菜单，以及桌面/手机布局。可选 `TXAPI_BROWSER_SCREENSHOT` 指定本地截图路径；截图不提交。现有 CI 只检查这两个浏览器脚本语法，真实浏览器通过证据单独记录，不要求生产安装 Playwright。

### 安装、容器与升级边界

本机全新安装：`make init` → 编辑本地配置/环境中的管理密钥（可清空示例 channels/tokens，改用页面创建）→ `make build` → `-init-admin` → 正常启动 → 访问 `/`。S4 发布时升级 S3 不增加 schema（v4）；当前分支追加 S5 v5/v6/v7、A3-02 输出记录 v8 和 A3-04 任务意图 v9，见下节。旧库无主 Key 的认领规则不变。

Compose 显式使用 `GATEWAY_DATABASE_PATH=/app/data/gateway.db`；此环境变量优先于 YAML 的 database.path。已有部署若实际库在 `/app/.local/data/` 或其他位置，直接重建可能看到空库，**不是数据自动迁移**。先停止旧进程，确认实际路径并一致备份主库及 WAL/SHM，再恢复到目标卷、检查所有权为容器 app 用户（UID 10001），验证数据后切换；切勿删除旧容器/卷或执行 down -v 来“修复”。

首次容器初始化（已准备本地 config.yaml、GATEWAY_ADMIN_KEY 和新数据卷）：

```bash
docker compose build
export TXAPI_ADMIN_USERNAME=admin
read -rsp '管理员密码: ' TXAPI_ADMIN_PASSWORD; echo
export TXAPI_ADMIN_PASSWORD
docker compose run --rm --no-deps -e TXAPI_ADMIN_USERNAME -e TXAPI_ADMIN_PASSWORD \
  gateway -config /app/config.yaml -init-admin
unset TXAPI_ADMIN_PASSWORD
docker compose up -d
```

使用外部反向代理终止 HTTPS，关闭流式响应缓冲并将读取超时设为大于请求总预算；应用仍限制 20 MiB body。限制 `/metrics` 的来源，不向公网直接暴露。服务端请求头超时 10 秒、请求体读取超时 30 秒、空闲连接超时 60 秒，不用全局短 WriteTimeout 截断 SSE；上游/整次请求仍受原有预算约束。

SIGTERM 正常关闭等待在途请求最多 10 秒，再排空已接收日志；超过窗口的长流可能被强制退出打断，不能宣称该路径日志绝不丢失。维护前停止入口流量并等待在途请求完成。回滚前保存升级后备份，涉及更高 schema 时恢复旧一致备份；S4 普通日志改为有界丢弃，不能依赖日志重建严格账本。

### Actions 容器交付演练

现有 Verify workflow 保留完整 `check`，新增独立 Linux `delivery` job；push/PR 自动执行，不需要人工重复触发。开发机无需安装 Docker。具备 Linux Docker、Compose v2、Go 和完整 Git 历史的环境也可运行：

```bash
make delivery
# 不使用 Make 时
python3 tests/smoke/delivery.py --go go
```

脚本从仓库 Dockerfile 构建待验收镜像，并从不可变的 S3 基线 `b0a69a72039414aa1a1103a74d484d24f8123c0c` 的 Git archive 构建旧镜像；不切换或修改工作区。通过实际 Compose 配置派生独立项目，使用随机回环端口、独立命名卷、临时配置及假上游；显式不加载个人 `.env`，清除部署相关环境变量，不读取个人 config.yaml 或既有卷。

验收覆盖：

- 全新卷初始化管理员、拒绝重复初始化、UID 10001、镜像健康检查、内嵌静态资源和管理员→用户→Key→调用→本人用量闭环。
- 重启容器进程，确认原 Key、用户、日志和用量仍在数据卷中；故意将 YAML database.path 指向不可写位置，验证 Compose 的卷内路径环境覆盖真实生效。
- 运行旧 S3 镜像创建用户/渠道/凭据/Key，完成非流式和 SSE 调用；停写、正常关闭后备份，再用当前镜像打开同一个卷，验证原身份、授权、Key、凭据 ID 和用量保留。当前分支预期 v9，历史迁移记录保持原前缀、新增 v5/v6/v7/v8/v9，旧日志不虚构价格版本，新价格快照可核对内容摘要。
- SSE 在途时发起 Compose stop，要求完整 DONE、退出码 0 且已接收日志排空；Compose 为正常关闭和日志排空预留 30 秒，不将该时间承诺为任意负载下的恢复保证。
- 升级后先另存一份备份，再恢复升级前主库及存在的 WAL/SHM；检查 SQLite integrity_check 和关键表逐行一致，重启原 S3 镜像验证原 Key/数据可用，升级后新增数据不在旧备份中。

所有备份只在停止网关并验证正常退出后执行。恢复前确认卷属于本次演练项目，恢复后校正 UID/GID；脚本只删除自己创建的容器/卷/镜像，失败时输出测试容器日志。生产操作仍须按真实路径停写备份，不得对业务卷运行测试清理命令。

S4 已有演练证据覆盖 S3/v4→S4/v4；S5/v5 在功能提交 `5f2ca19` 的 Verify #38838322419 实跑通过，当前 v6 已由 `d47c108` 的 Verify #38842108801 完整实跑通过，不复用旧结果宣称新代码通过。既有升级/恢复检查保留，不额外扩展旧库报错或回滚专项演练。这里的正常恢复也不等于崩溃时无损，普通日志和进程级 revision 的限制不变。

本地 Windows 的页面/普通测试不代替 Linux race、完整 CI、容器实跑以及安装/升级/恢复演练。本批阶段状态见 PLAN，不因编写部署说明就标记验收通过。

## S5 定点价格与严格结算首批

当前包含首批后端和第二批管理员对账/故障恢复实现，非完整收费版本验收。范围为 billing、store/model、gateway/main、管理 API/控制台和关联契约/观测，不改 core/协议的依赖边界，不新增支付或充值。默认模式仍为兼容模式：

```yaml
billing:
  mode: "legacy"  # legacy / disabled / strict
  # strict 必填，单位与价格/额度相同，最多九位小数；必须是正数字符串。
  # max_request_cost: "0.10"
  # max_provider_request_cost: "0.10"
  # provider_contracts: [...]  # 具体字段见 configs/gateway.example.yaml
```

`GATEWAY_BILLING_MODE` 可覆盖模式，其他 billing 配置由 YAML 提供，变更须重启。

| 模式 | 实际行为 |
|---|---|
| legacy | 保留旧额度预扣/结算和可用性优先降级；缺价可免费，不能用于宣称严格预算 |
| disabled | 不查额度、不扣款、不预留；鉴权、授权、RPM/TPM、并发准入与访问日志仍生效 |
| strict | 必需组件/单次收费上限/售价/精确上游成本契约/累计预算不可用则在发送前拒绝，不能自动降级 |

### 价格、收费上限与请求范围

- 价格和渠道倍率从同一数据库快照读取，归一化为 `1e-9` 货币单位；每千 token 价格接口暂保留 JSON number，按其可还原十进制转换，正数不足一单位向上取整。两种 token 维度与倍率以宽整数合并后仅向上舍入一次；额度上限向下取整。显式零价有效，缺价与零价不同。
- 价格上限每千 token `1e9`，渠道倍率须为正且不超过 `1e6`。精确模型优先，通配符按固定字符数最多、同分按字典序匹配。未配置渠道倍率按 1 计算。
- 规范化价格/倍率 JSON 的 SHA-256 为 `price_version`；先持久化不可变历史，再发布内存快照。请求始终使用起始版本，途中改价/倍率不追溯改费，历史日志缺少版本保持空值。快照存储失败保留旧发布版本。
- 每个严格请求原子预留完整 `max_request_cost`，同时检查用户和 Key，而非只预留 max_tokens 的输出费用。实际输入+输出+倍率费用不超过预留才结算并退余；超出则 pending，保留整笔预留，不自动补扣或记为免费。**这是本系统用户收费上界，不是上游厂商成本绝对保证**；即使零价模型也先按同一策略预留整笔上限。
- 当前严格 chat 只接受文本消息、`n=1`，要求 `max_tokens` / `max_completion_tokens` 二者中恰好一个正数；严格 embeddings 只接受字符串或字符串数组。工具、媒体、未知扩展字段返回 400。公开模型名须有价格，实际模型有价则优先，否则回落公开模型价；不能用文本价格覆盖其他收费维度。

S5-C 首批进一步收紧的是请求/转换边界，不是完整成本证明：

- 严格请求须为合法 UTF-8 JSON 对象，各层对象拒绝重复键（含转义后相同的键），最多 32 层嵌套；不接受尾随第二个 JSON 值。消息 content 必须显式为字符串，不把 null/遗漏当空文本；stream 与 include_usage 必须是布尔值。
- embeddings 的单文本及批次每项均非空，拒绝空数组/null 项/Token ID 数组；可选 dimensions 为正整数，encoding_format 为 float/base64。批次总 Token 上界尚待后续模型契约证明，不能由此推断输入已计数。
- 在协议转换后、持久 attempt 开始前验证最终请求的显式输出限制未改变，OpenAI/Gemini 候选数只能为 1。现有跨协议路径仅支持 `max_tokens` 的已验证字段映射；`max_completion_tokens` 只允许现有 OpenAI 兼容路径，跨协议返回 400，不用默认 4096 或另一个字段替代。
- 严格 embeddings 只走现有 OpenAI 兼容操作，其他协议返回 400，不把输入转成 Chat。转换边界拒绝不会启动持久网络 attempt；此前已建立的双预算预留按无执行路径释放，日志仍记录拒绝。

首批切片未修改 legacy/disabled 的兼容策略或增加 schema/配置/依赖。后续成本契约切片增加下列配置、schema v7 与管理字段，但仍未完成 S5-03 或真实模型计量/成本验收。

### 独立上游成本契约与重试预算

strict 每次发送现在还需要 `max_provider_request_cost` 和 `provider_contracts`。它们是启动时加载的独立成本配置，不是只导入一次的渠道种子；详细语法见[配置样例](../configs/gateway.example.yaml)。没有契约时仍可启动以读取/恢复已有账本，但新请求在网络发送前返回 503；配置了不完整、重复、未知协议/操作或溢出契约则拒绝启动。legacy/disabled 不读取这些成本策略，不自动改成 strict。

- 契约精确匹配渠道 ID、完整 base URL、协议、**实际映射模型**、操作与输出限制字段。无别名/通配符回落，不从售价、渠道倍率或缓存收益推导上游价格。可执行目标必须另有已登记的输入档案；当前仅官方 OpenAI Chat/embeddings 登记组合。无档案的三协议契约只可加载，不获准执行；embeddings 输出字段/上限须为空/0、输出价须为 `"0"`。
- `input_token_ceiling` 是厂商必须保证的**整次尝试可计费输入总量上限**，包括批量 embeddings 全部条目、协议消息框架及可能收费的错误请求，不是每条消息/数组项限额，也不是本地实际计数。`max_output_tokens` 和 `output_limit_field` 必须界定所有可计费输出（包括适用的隐藏推理）；缓存/服务等级等价格必须由每千 token 的最高包络价覆盖，不能成立的组合不能配置为已支持。
- `input_price_per_1k` / `output_price_per_1k` 是与 provider 预算同币种的非负十进制字符串，最多九位小数。执行还须有 `tariff_profile: openai_standard_usd_2026_10_17`、`currency: USD` 及 `credential_bindings`。低于已登记标准费率、错币种/档案或不完整绑定拒绝启动；零价不能绕过收费模型下限。当前可执行 provider 预算与金额仅为 USD（精度 1e-9），用户售价/预算单位保持独立，不做自动换汇。每次保守上限为 `ceil((整次输入上限 × 输入上界单价 + 本次显式输出限制 × 输出上界单价) / 1000)`，按 `1e-9` 单位使用宽整数计算；embeddings 输出为 0。即使请求很短，也占用完整输入包络，不采用字符平均值。
- `credential_bindings` 是凭据 ID 到上游 Key 小写 SHA-256 的显式绑定；新 ID、换 Key 或实际鉴权头变化不继承旧许可，配置映射在编译时复制固定。获取摘要和适用范围见[费率档案](reference/provider-tariff-profiles.md)。禁止使用真实 Key 或页面掩码；摘要不代表静态加密或价格认证。最终准备头只允许 Authorization、Content-Type、Accept，不允许通过额外项目/组织头切换计费账户。
- `evidence` 必填，记录可复核的厂商约束、模型/接口版本和价格依据，必须逐一覆盖显式绑定凭据的实际适用费率；登记标准费率下限不替代失败收费、税/附加费或未来价格变更核查。**非空说明不等于系统已验证该声明**。仓库没有默认真实厂商契约，样例数值仅示范语法，不能直接用于真实收费验收；不在 evidence / URL 中填写密钥或私密正文。真实模型/计价维度验证仍是 S5-C 未完成项；售价侧的发送前费用检查已接入，但同样依赖这些边界确实成立。
- 请求预留时固定成本策略；每次 attempt 前在同一事务内增加 `provider_held_units` 并插入 attempt 和带 SHA-256 的不可变 JSON 快照，超出 `provider_budget_units` 则不执行。并发/重复插入/数据库写失败不能部分消耗额度或发出未登记请求。上一尝试无论失败、缺 usage 还是成功，都不返还本请求的保守重试占用；这是有意采用的保守策略，不将未知成本记为 0。
- 缺精确契约返回 503 `provider_cost_contract_missing`，无已登记输入档案返回 503 `provider_input_unproven`，无费率档案返回 503 `provider_tariff_unproven`，凭据/实际鉴权头不匹配返回 503 `provider_credential_unproven`，累计包络不足返回 503 `provider_budget_exhausted`，输出超契约返回 400 `provider_cost_limit`；这些本地拒绝不罚渠道健康，也不继续换凭据试探。转换限制检查仍先执行。已执行后收到超输入/输出契约的原始用量，记录 `provider_contract_violation`、保留未知回执与 pending；响应已经提交时不能撤销或拼接错误。不能承诺远端违约时仍绝对零损失。

schema v7 仅追加 reservation 的累计上游预算/占用与 attempt 的上游保守上限/契约摘要/快照字段，并登记迁移；历史行保持 0/空，不回填虚构契约。现有表仍由 store 统一迁移。管理员详情显示这些字段；它们不替代 `billing_balances`，不会因用户结算/人工豁免而清零。低版本二进制不得直接打开 v7；本批只验证隔离临时库，不操作实际运行数据库，也不另开旧库报错/回滚专项。

### 发送前用户收费覆盖检查

strict 执行强制要求已登记输入档案。`input_bound_profile: openai_embeddings_v1` 将官方 embeddings 的已核对接口约束落实为启动/发送检查：只接受官方根 URL 和登记模型，整请求 `input_token_ceiling >= 300000`，而非单输入 8192。最终 URL/正文模型或操作变化、文本批次超过 2048 时拒绝，不产生 attempt；每次仍预留完整输入包络，不做字符估算。gateway 使用 `StartPreparedAttempt` 将真实 URL/正文交给现有成本引擎，无最终正文的入口不能绕过该档案。档案名随既有不可变成本快照持久保存；管理员展开快照即可查询。来源、范围与假设见[输入边界档案](reference/input-bound-profiles.md)。旧无档案契约仍可加载和读取历史，但不能开启新尝试；非空 evidence 不替代输入依据。无实际正文的 `StartCostedAttempt` 已弃用并拒绝执行，最终正文输出字段/数值必须与成本核算元数据完全一致。档案不自动认证费率或失败请求收费策略。

Chat 使用 `openai_chat_v1` 只登记官方 `gpt-4o-mini-2024-07-18` Chat 快照，整输入上界至少 128000，`max_completion_tokens` 至多 16384。请求必须显式 `service_tier: "default"`，防止项目默认付费等级改变；网关不会自动改写服务等级。其他等级、tools/media 等未计价维度继续拒绝。使用模型全上下文而非字符估算，因此短请求也可能需要很大的用户 hold 和 provider 包络；不是每次只占请求正文估计成本。完整 S5 仍未验收。

成本准入的固定拒绝码会先写入现有 reservation `reason`，再返回调用方：`provider_cost_contract_missing`、`provider_input_unproven`、`provider_tariff_unproven`、`provider_credential_unproven`、`provider_budget_exhausted`、`provider_cost_limit`、`billing_fee_limit`。管理员账本列表与详情沿用现有 reason 展示，无新路由或迁移。尚无任何持久 attempt 时，唯一收尾释放双预算、结算为零并保留拒绝码；已有未知执行时保留完整 hold 与 provider 包络，reason 为 `execution_or_usage_unknown:<拒绝码>`。原因写入失败返回 `billing_unavailable`，仍不执行上游。记录仅保存固定码，不保存原始错误、目标 URL、凭据或请求正文；客户端取消后诊断写入最多等待两秒，不延长上游执行。崩溃发生在记录之后、收尾之前，既有过期恢复继续保留该原因；管理员最终审计结算后 reason 仍使用既有 `admin_*` 语义。

strict 现在还会在协议转换完成后、开始持久 attempt 前检查最终正文的文本计价白名单。OpenAI Chat/embeddings 复用严格入口解析；Anthropic 只允许现有文本 messages/system 和基础生成字段，Gemini 只允许文本 contents/systemInstruction 和基础 generationConfig。所有层级的重复 JSON key、未知字段、工具/媒体块、缓存控制或引用、显式 thinking/推理配置、非默认服务等级和输出媒体配置均拒绝；不先删除字段再假装验证通过。embeddings 只允许 OpenAI 文本或文本批次，禁止转换引入 token 数组或混合输入。新正文维度检查不影响 legacy/disabled 的透传行为。

最终正文维度越界、输出限制变化和 embeddings 跨协议拒绝共用 `unsupported_billing_dimension` 持久原因，沿用上述零执行释放/未知执行 pending/故障拒绝规则；均不反馈上游健康、不计为持久执行，也不产生 provider 新占用。此白名单约束的是实际发送正文中的显式字段，不能证明模型默认推理行为、缓存隐含计量、真实输入 Token 数或厂商价格保证；没有新增生产厂商的可计价认证，S5-C02/C03 完整验收仍待完成。

在同一个 `StartPreparedAttempt` 内，除了独立上游成本，还用**请求预留时固定的售价与倍率快照**对整次输入包络和本次显式输出限制计算收费上界；整数计算、一次向上舍入与最终结算相同。实际模型存在匹配售价时优先使用（含既有确定性通配规则），否则保持现有公开模型回落规则；不以廉价上游成本价替代售价，也不因请求字符串很短而缩小厂商总输入包络。embeddings 同样检查全批输入，不因输出为 0 而跳过。

- attempt 的更新/插入事务同时要求持久 reservation 的 `reserved_units >= 收费上界`，不是读取当前全局配置重新决定可收金额；恰好相等允许，超一单位也拒绝。超出或计算溢出返回 400 `billing_fee_limit`，不增大用户/Key hold，不发送、不记网络 attempt、不占用上游包络、不反馈渠道健康。
- 每个重试目标都重新计算，使用该目标的实际模型与渠道分组。先前未知执行后因下一目标收费过高而停止，先前 attempt 与占用继续保留，用户 hold 仍 pending；只检查可见输出或只检查首选渠道均不满足此规则。
- 没有执行就拒绝时，原唯一收尾流程释放用户/Key 双占用，收费为 0。已执行的最终成功仍按真实 usage 收费并退余，不按“最大上界”全额收费；售价侧是单次最终用户收费，不把所有重试上界相加收给用户。
- 发送的成本快照为内部 JSON `format=2`，在已有 provider 契约/输出限制之外记录 `selling_price_version`、`selling_model`、`selling_group`、`selling_bound_units`；金额上界是精确整数字符串。摘要覆盖完整快照，管理员现有展开区域可查看。旧 `format=1` 回执仍可读取，未知格式保守拒绝；不重写历史快照，不新增 schema/HTTP 字段、路由或依赖。

这项检查只证明“**声明的**输入/输出边界以内，售价费用不会超过已预留金额”。厂商是否真的约束全部输入/推理/缓存计量，仍须独立核实；不能用这条守卫宣布 S5-03 完成。异常实际用量、丢失回执和费用异常仍保留原 pending/审计流程。

### 持久化、恢复与可见性

schema v5 增加 `billing_price_versions`、`billing_balances`、`billing_reservations`、`billing_attempts`；普通 logs 增加 price_version/billing_id/billing_state。整数余额是严格模式的权威值，旧 used_quota 仅投影已收费用+未释放预留，因而控制台额度占用包括 pending，个人日志聚合不是权威余额。首次建立某个余额时保守导入现有 used_quota，不把历史消费归零。初始化严格组件后写入 `billing.strict_enabled=true`，同一库禁止启动 legacy 以免浮点写入和整数余额分叉；可选择 disabled，但不会因此删除已有预留。

预留采用内部随机 ID（不信任客户端 Request ID）。开始上游执行前同步记 attempt，完成后同步记已知/未知成本；最终用户收费只结算一次，不将重试成本重复收给用户。用户与 Key 的占用、收费、余量释放和预留状态在一个事务内改变；普通日志背压、丢弃和保留期不能丢弃财务记录。响应可带 `X-TXAPI-Billing-ID`、`X-TXAPI-Price-Version`，日志页展示版本和 pending 提示；账本查询与对账仅对管理员会话开放，不向普通用户或旧 root 管理密钥开放。

租约为整次请求预算+5 秒。main 每 30 秒恢复、启动时先跑一轮；只有从未开始 attempt 的预留可自动全退。最后一条完整成功回执已持久化但尚未结算时，恢复 worker 按历史价格自动幂等结算；执行过但缺 usage/回执、取消、断流或费用超上限保留 pending。收尾和恢复使用独立且有界的 context，客户端取消不等于无成本。内部 `Engine.ReconcileKnownUsage` 仍供可信恢复调用，外部人工决策走下列有审计的接口；**完整成本上界仍未闭环，不得据此对外收费**。

### 管理员账本与最终决策

控制台管理员菜单的 **Billing ledger** 默认列出 pending；支持状态/用户筛选及分页，详情展示逐次执行、售价快照、独立上游包络/契约快照、双预算占用和最终决策。金额使用十进制整数字符串传输，`unit_scale="1000000000"`；浏览器使用 BigInt 展示，不把未知成本显示为免费。字段和错误封装以 OpenAPI 0.2.3 为准。

| 方法与路径 | 行为 |
|---|---|
| `GET /api/admin/billing/reservations` | 默认 state=pending、limit=50；state 空字符串表示全部，limit 1–100、offset 0–1000000，可按 user_id/token_id 过滤 |
| `GET /api/admin/billing/reservations/:id` | 预留、按序 attempt、历史价和可空的最终审计记录；ID 为 32 位小写十六进制 |
| `POST /api/admin/billing/reservations/:id/resolve` | 仅 strict 组件可用、pending 且租约已过期时能首次决策；事务内再次检查管理员角色/有效状态 |

决策 body 必须提供 action、非空 reason（最多 1000 UTF-8 字节）和 evidence（最多 2000 字节）；不要填写真实密钥或私密请求内容。usage 的两项均须显式提供非负整数，0 与遗漏不同；不得传 actor_id、模型、分组或新价格，未知字段和尾随 JSON 会被拒绝。

- `settle_usage`：提供 `usage: {"prompt_tokens":11,"completion_tokens":7}`，按预留历史价格及最后一次持久化目标计费，费用不超过 hold 时结算并退余。
- `cap_usage`：同样提供核实用量，且核算费用必须超过 hold；明确只收 hold，审计保留完整核算费用和封顶收费，不静默忽略差额。不能向用户超额追扣。
- `waive`：不提供 usage；管理员接受未知成本并释放 hold。`cost_known=false` 不等于上游没有执行，也不消除厂商成本。

双余额、终态和一条不可改写的决策审计在同一事务提交；审计写入失败全部保留原占用。相同规范化 body 重试返回原决定和原操作人，不重复收费；不同决策或迟到回调不能改写终态。HTTP 409 表示状态/费用/已有决策冲突，400 表示输入错误；500/超时结果不确定时先查询、再以同一 body 重试，不能换一份决策试探。页面要求输入完整 billing ID 后提交，不能将页面确认当服务端权限校验。

schema v6 仅追加 `billing_resolutions` 和迁移登记，不从普通日志推断历史决策；不级联删除历史审计。初始化 strict 时该表同样是必需组件。disabled 模式可只读查询已有账本，不能提交最终决策，也不会释放原 hold；切回 strict 后才可处理。数据库备份应覆盖这些财务表，不能只备份访问日志；更低 schema 的二进制不能直接打开 v6，既有停写备份/恢复约束不变。

### 故障恢复回归

定向回归可用 `go test -count=1 ./internal/billing ./internal/gateway ./internal/config ./internal/store ./internal/server/...`。`TestStrictForcedProcessDeathRecoveryMatrix` 启动独立测试进程并真实强杀，覆盖预留后未执行、执行中回执未知、成功回执落库后未结算、结算后未确认四个窗口；重启只读持久化事实，等待真实租约到期，重复恢复仍只收费一次。真实 HTTP 假上游确认是否已执行，不伪造崩溃时间戳，不增加生产崩溃钩子。

`python tests/smoke/local.py --go go` 在临时库验证原链路、模式隔离、strict 非流式/SSE 结算，并在真实 gateway 二进制 SSE 中调用强杀、等待实际租约、重启恢复为 pending，再经管理员 API 幂等豁免和核对双占用释放。普通访问日志不作为恢复证据。测试只使用隔离假上游，不承诺真实厂商 exactly-once 或崩溃后零成本。

性能对照 `go test ./internal/billing -run '^$' -bench BenchmarkPriceCost -benchmem` 只比较固定价格快照计算与浮点公式，不是历史整网关或账本吞吐基准。实际执行结果见 [S5 批次记录](history/acceptance-s0-s5.md#s5-可选预算与结算)。

## 本地配置

可提交模板为 `configs/gateway.example.yaml`；个人配置为根目录 `config.yaml`，被 Git 忽略。真实密钥只放本地配置/部署秘密配置，不能写回模板。当前程序尚不支持将任意 YAML 字段自动替换为环境变量。

```bash
make init
# 编辑 config.yaml
make run
# 或指定其他配置
make run CONFIG=tests/configs/openai.yaml
```

运行数据归 `.local/data/`。数据库不空时，再修改 YAML 种子不会覆盖渠道和 token。开发需要重新导入时使用新数据库路径，不随意删除已有业务数据库。

当前管理 API 已启用 `X-Admin-Key` 鉴权（`admin_key` 缺失时网关拒绝启动）；
管理密钥从环境变量 `GATEWAY_ADMIN_KEY` 注入，不要写进会进 git 的文件。

## 自动冒烟检查

```bash
make smoke
```

脚本 `tests/smoke/local.py` 自动编译网关和假 OpenAI 上游，在临时目录写测试配置与数据库，使用随机本地端口；结束时关闭自己启动的进程并清理临时文件。它会断言健康状态、无效 key 拒绝、模型映射、完整流文本、结束原因和用量。

这项检查用于确保运行入口和核心 OpenAI 链路可用，不代替协议、并发和权限回归测试。首次执行需要下载 Go 依赖，实际运行只连接本机。

HTTP 请求体限制为 20 MiB。`server.retry_times` 可设为 `0` 关闭重试；上游单次请求超时需设在 1 到 600 秒之间。

## 手工假上游

分别打开终端运行需要的服务：

```bash
go run ./tests/upstream/openai
go run ./tests/upstream/anthropic
go run ./tests/upstream/gemini
```

| 配置 | 网关地址 | 数据库 | 本地客户端 key | 管理密钥 |
|---|---|---|---|---|
| `tests/configs/openai.yaml` | `127.0.0.1:8080` | `.local/data/test-openai.db` | `sk-test-123` | `sk-test-admin` |
| `tests/configs/retry.yaml` | `127.0.0.1:8081` | `.local/data/test-retry.db` | `sk-test-123` | `sk-test-admin` |
| `tests/configs/protocols.yaml` | `127.0.0.1:8090` | `.local/data/test-protocols.db` | `sk-test-p4` | `sk-test-admin` |

上述 key 都是测试占位值。运行前 `make prepare` 创建数据库目录。不同场景独立数据库，避免种子导入被另一场景已有数据跳过。

```bash
make run CONFIG=tests/configs/protocols.yaml
```

Gemini 假上游按独立增量发送文本；回归样本覆盖等长、变长、UTF-8 增量，验证每个块完整输出。协议依据为 Google 官方 streamGenerateContent 文档和 SDK 的逐块拼接示例；模拟测试不等于真实服务的完整兼容性证明。

## 可选 SDK 检查

`tests/smoke/sdk.py` 需要单独安装 `openai` Python 包。建议使用本地虚拟环境，网关和三个假上游要先手工启动。

```bash
python3 -m venv .venv
.venv/bin/python -m pip install openai
.venv/bin/python tests/smoke/sdk.py
```

默认参数与 `tests/configs/protocols.yaml` 一致。可指定其他本地场景：

```bash
.venv/bin/python tests/smoke/sdk.py \
  --base-url http://127.0.0.1:8080/v1 \
  --api-key sk-test-123 \
  --models gpt-4o-mini
```

SDK 检查断言响应非空、finish_reason 与 usage 存在，但不保证所有跨协议语义正确。新增协议须增加独立协议样本与精确输出断言。

## S3 并发准入与用户请求速率

生产入口通过 `gateway.CapacityAdmission` 注入 `access.Capacity`。限制作用于模型转发请求，不是管理 API、健康检查、列模型或原始 HTTP 连接数；计数属于当前进程，重启清零，多副本不会形成全局限额。当前采用立即拒绝策略，没有等待队列或额外后台 goroutine。

会话亲和是可选优化，配置键为 `server.session_affinity`，默认 `false`。设置为 `true` 后，亲和 ID 由可信 user ID +（客户端 `X-Session-Id` 的摘要，或 token ID 和首条 user 消息摘要）构成；客户端头不原样进入日志，不同用户的亲和 ID 不相同（共享上游池仍可能哈希到同一凭据）。无会话内容时用加权选择。只有真实上游测量显示缓存命中、延迟或成本收益后才开启；坏目标/已试目标在当前可用池重新映射；没有会话或超过环容量时回退加权选择，不能因为亲和阻断请求。它不提供持久历史或后台恢复，安全边界见 [架构：调度与计费](ARCHITECTURE.md#9-调度与可选计费)。

```yaml
server:
  session_affinity: false
  concurrency:
    instance: 256
    user: 32
    key: 16
    channel: 64
```

四项为服务器并发上限，省略或 `0` 采用示例中的有限默认值，负数及超过 1,000,000 拒绝启动。必须满足 `key <= user < instance`；调整实例为较小值时，也要相应调整用户和 Key。`channel` 为每个渠道共用的尝试上限，同一渠道的多把上游凭据不会扩大它。当前不支持通过 API 为各渠道单独设置不同的并发上限。

- 管理员设置 User 的 `concurrency_limit`，用户设置 Key 的同名字段。实际并发取服务器上限与非零记录值的较小者；记录为 `0` 时继承服务器上限。已有 Key 的创建/更新仍遵守用户上限校验；字段值不会反映服务器动态计算后的有效上限。
- User 的所有 Key 共用用户计数，单个用户最多占用 `server.concurrency.user` 个转发名额。实例、用户、Key 在同一锁内一起校验和获取；拒绝不留下部分占用。正在执行的请求保留原租约，新限制从下一次鉴权起生效，不重置已有计数或强制终止在途请求。
- 请求租约覆盖全部重试；渠道租约仅覆盖当前 attempt，在换目标前释放。成功、失败、客户端取消、总超时和 panic 路径均释放租约；释放不依赖请求 context，重复释放不重复扣减。请求已取消时拒绝新获取。
- 用户/Key/渠道并发超限返回 `429`；实例容量耗尽或限流状态表容量不足返回 `503`。拒绝含安全的 `admission_error` 和 `Retry-After`（并发一般为 1 秒，仅为重试建议）；不调用上游、不惩罚凭据。当前选中渠道满时立即拒绝，不自动寻找另一渠道或排队。
- User 的 `rpm_limit` 在请求级准入中按用户 ID 聚合，所有 Key 共用同一个精确 60 秒滑动窗口。`0` 为不设用户 RPM；通过准入的逻辑请求计数一次，重试不重复计数，响应失败也不退回请求次数。Key RPM 和估算 TPM 保留既有中间件处理，可能在后续并发拒绝前已计入 Key 的请求窗口。用户 TPM 的跨 Key 聚合仍未实现。本节只描述速率/并发；S5 已实现的持久预算/账本及尚未完成的成本边界见 [S5 约定](#s5-定点价格与严格结算首批)，两者不是同一限额。
- 用户 RPM 状态最多保留 4,096 个有效用户窗口、合计 65,536 个有效事件；下一次准入/观测时回收过期事件。不淘汰仍有效的窗口来放行新用户，满时保守拒绝并返回 `503`；这是状态内存保护上限，高流量部署需在后续方案中评估容量或外部限流实现。
- 客户端取消、写入客户端失败和整次请求预算耗尽不计为凭据故障；请求总预算仍有效时，单次上游超时照常反馈健康状态。准入本身不改用户权限、渠道优先级或凭据状态。

调度健康的持久化队列最多 256 个事件，健康 worker 处理时读取最新数据库记录，校验代次与观察顺序；生产协调层已计算的健康状态直接按原观察结果落库，避免从滞后数据库重复计算。请求协调层在上游失败后立即更新不可变运行快照；异步落库期间的旧快照不能回滚该冷却。人工编辑凭据/设置状态会递增内部 `health_generation`，旧请求结果失效；写入同时检查读取时的 `updated_at`，保护读取后发生的管理变更；内部 `health_observed_at` 使用递增的观察时间标记，阻止较晚提交的旧失败覆盖成功恢复，热重载也按同一标记合并。队列满或数据库异常会告警，持久化不是可靠账本，进程崩溃可能丢失尚未落库的观察结果。

Key/IP 限流共享的窗口表最多 4,096 个桶，每桶最多 4,096 个事件；只回收过期桶，容量不足时保守返回 429。TPM 为原有估算限制，不是严格预算；完成补记在桶容量满时合并到最后事件，表容量不足时告警，不能当作可靠账本。权重小于等于 0 按 1、大于 1,000,000 按 1,000,000 参与选择，防止异常数据库值造成溢出。可选亲和缓存最多 64 个环，每环最多 32,768 个节点；超过 4,096 个凭据的池回退加权随机。

### S3 数据库升级与回滚

1. 停写并备份实际 SQLite 主库及存在的 WAL/SHM，按下文 S2 的一致备份要求操作；不操作真实业务库来执行开发验收。
2. 启动时 AutoMigrate 为 `credentials` 增加内部 `health_generation`、`health_observed_at`（默认 0，不返回 API），迁移注册表依次追加版本 3、4，保留已有渠道、凭据、Key、健康状态和 S2 迁移语义。没有新 HTTP 路由；旧库升级与重复启动有回归。
3. v4 数据库由支持该版本的新程序打开；S2/F0 及仅支持 v3 的 S3 二进制检测到更高版本会拒绝启动。回滚须停新程序、保存升级后数据，再恢复升级前一致备份并启动旧版；会丢失升级后新增/修改的数据。不得删除版本记录伪装成可直接降级。AutoMigrate 的结构变更可能先于事务迁移完成，失败恢复以备份为准。

本批集中验收入口：`make check`、`go test -race ./...`（Linux CI）与 `make smoke`。回归覆盖跨 Key 的用户 RPM/并发、Key 自身上限、其他用户仍可调用、128 个并发获取、重复释放、速率表容量与到期、重试只计一次请求、取消/总超时/panic 释放，以及真实鉴权投影的限制变更。按上文验证节奏在批次稳定后执行，无需每次编辑重复运行。

### A3-01 进程内消费者边界

`internal/generation` 是最小真实 worker 消费者，构造时注入 `Store.ResolvePrincipal` 与 `Gateway.ExecuteTo`。服务 context 拥有 worker；`Submit` 的 context 只控制交接前取消与 Request ID，不把观察者身份、限流预留或取消状态带入生成。交接只复制有界正文、会话键和可信 user/Key 绑定，不能把客户端提交的 ID 当授权。派发前重新读取当前 owner/Key/系统策略及限额，失效或存储异常均不执行；当前在飞生成仍按本轮授权快照运行，撤销不自动等于取消，后续持久任务的撤销传播策略尚未实现。

worker 数、队列长度、单请求正文和排队起算的取消时限均有界；配置要求 queued+active 的正文副本预算不超过 64 MiB，输出实现仍须自行有界。队列不占 gateway 的 generation/network attempt 槽，派发后复用既有准入与账本。`Future.Wait` 取消只停止观察，`Future.Cancel` 显式取消生成；只有一次结果接收，不能复用其中的可变指针。输出在收到执行结果或 runner 完整关闭前始终归生成独占。满队列直接拒绝，不建额外等待 goroutine，也不保证逐用户排队公平。

关闭顺序是 runner 停接/取消/等待收尾 → gateway/共享资源 → store。`Close(ctx)` 超时只表示尚未等完，不能提前关闭后两者；可信回调忽略取消时无法强杀。gateway 自身的财务收尾仍可能在生成取消时限后继续。进程退出会丢失未执行的内存交接；它没有持久受理、fencing、事件游标或重启重派语义，**不能用来实现公开 `background=true` 的受理承诺**。当前 main 未启动此 runner，HTTP 前台断开策略保持不变。后续生产后台入口仍须任务意图/预留原子事务与恢复验收。

可定向执行 `go test -count=1 ./internal/generation ./internal/store`；使用临时 SQLite 和隔离 transport，无真实上游/凭据。关键回归包括排队后撤销/权限收紧、一致身份快照、观察者断开、正文副本、满队列/排队到期、取消/并发关闭、提交后 panic 和 strict 未知执行保留双 hold；Linux race 随现有完整 CI 执行。

### A3-02 可选加密输出记录

本批为上述真实 worker 增加 `Archive` 和 `SubmitRecorded`，不是新后台 API。默认 `Submit` 不存正文；只有组装者明确注入 Archive 并调用新方法才记录编码后的响应元数据、JSON/SSE 片段和最小结果摘要。输入正文、调用 Key、权限快照和任意原始异常不进入新表。schema v8 在 store 统一迁移中新增 `generation_records` / `generation_segments`；不回填历史日志或改写旧账本。v7 及更老二进制不得打开 v8，回滚恢复升级前一致备份，不能删除版本标记降级。

- 组装者通过 `NewArchive(ArchiveOptions)` 注入真实 store、小身份解析函数和独立密钥环：每把 32 字节 AES 密钥、1–32 字符非秘密 key ID、明确 active ID；无默认/临时生成/明文回落。密钥不存入 SQLite、不复用管理员密码或调用 Key，调用者修改原 map/切片不能改写运行实例。当前 main/YAML/环境变量没有生产接线，不能仅设置一个虚构环境变量来启用它。
- 每段使用随机 nonce 的 AES-256-GCM；版本、key ID、记录/owner/创建 Key、创建/过期时间、容量、序号和段类型纳入认证。轮换后保留旧 ID/旧密钥用于历史读取，新段使用新 active ID；旧钥缺失/错误、篡改或顺序不完整均拒绝且不返回部分明文。必须在同一密钥累计加密达到 2^32 段前轮换；尚无自动 KMS/轮换、批量重加密或生产密钥恢复工具。密钥与数据库分离保管/备份；丢失旧钥的数据不可恢复，密钥和数据库同时泄露不在此保护范围。
- 接收前在事务中预留每条最大密文字节数，保留到删除/清理。配置明确 owner/实例记录数及总预留字节；单条 16 KiB–8 MiB、最多 256 段、正文每段最多 32 KiB，另留结果段与 4 KiB 空间。容量满在执行前拒绝；输出过大在写入处失败并沿原链收尾，不静默截断为成功。逻辑密文限额不是 SQLite/WAL/索引/备份的物理磁盘配额或安全擦除保证。
- 保留期明确为 1 分钟–7 天且长于 generation 时限；读取/追加过期内容立即拒绝，过期但未清理的记录仍占预留容量。启动/清理由组装者显式调用，`CleanupGenerationRecords` 每批最多 1000 条并级联删片段；当前 main 不启 worker/清理服务。删除不证明上游未执行或已取消，也不回退账本；备份仍可能保留旧密文。
- `Read`/`Delete` 使用当前身份解析和可信 owner/创建 Key 绑定，其他用户或同用户其他 Key 均不能靠 ID 取回/删除；读取还要求当前模型策略允许原模型。摘要/正文/元数据加密存储；摘要仅含有限操作/模型、阶段/提交/终止、协议/账本观察与白名单原因，不是财务权威。认证是本次读取快照，不承诺撤销瞬间中断在飞读取。
- 每次写入以序号 CAS 与计数更新在一个事务中完成；段写失败回滚计数，确认丢失不盲目重新追加。`open` 只有追加最终观察后变为 `sealed`；sealed 不等于协议成功或 billing settled。关闭 runner 会等待正在准备存储的交接和执行收尾，存储 I/O 不持有停接锁；超时仍不得关闭 store。
- 单实例独占启动、任何 producer 启动前，分批调用 `InterruptGenerationRecords` 把遗留 open 记为 interrupted；该操作只说明观察不完整，不返回可执行任务、不重发、不退款。中断/不完整记录不返回部分正文；完整 sealed 记录重启后可解密读取。最终观察写失败时即使 API 返回归档错误，生成也可能已完成并结算；若确认丢失但事务实际成功，重新读取可得到完整记录。不得以归档错误自动重试生成或撤销账单。

原账本仍是唯一财务权威。真实隔离 transport/SQLite 回归分别验证：输出写失败且上游 usage 未知，双 hold/provider 占用保留 pending；生成已结算而最终观察写失败，既有结算不回退。另有真实子进程在部分 SSE 加密片段持久后被强杀，重启后旧句柄被拒绝、记录为中断、真实租约到期恢复不重发/不退款。测试没有降低 strict 档案或凭据门槛。

这不是持久会话、durable task acceptance、任务领取/续租/fencing、原子任务意图+预算预留或事件游标；不保留可用于重发的输入，也没有自动恢复生成。S7-A 的 store=false/后台原生状态与内容策略仍需自己的产品接线和验收，不能静默把客户端请求送入本记录器。现有上游凭据明文存储未因本批加密输出记录而修复。

### A3-03 有界 WS 传输与连接生命周期

`internal/transport/websocket` 只管理服务端 WS 握手、物理连接和同步消息 I/O；不导入业务包、不处理身份/协议事件/账本。固定使用 `github.com/coder/websocket v1.8.15`（ISC），Go 依赖校验保持开启。已核对官方发布及该版本 Accept/Read/Write/CloseNow/SetReadLimit 源码：取消正在进行的 I/O 会关闭连接，Ping 的发起端也必须有读取器接收 Pong，CloseNow 不等待普通关闭握手。第三方库不进入标准库 core，生产依赖约束限定在本 transport 包；该包不是原生 Responses WS 上游执行器。

- 组装者先用现有身份权威认证，再以可信 `access.Binding` 调用 `ConnectionCapacity.Acquire`，随后 `Manager.Accept`。连接限额为 `1 <= Key <= User < Instance <= 4096`，关闭只停接、不丢掉尚未释放的租约；幂等释放。空闲连接不预占 generation/attempt 槽、RPM 或预算。当前没有 main/YAML 配置或公开 WS 路由；不能把这一测试组装当成已启用的产品。
- `Manager` 使用服务 context；HTTP request context 只约束握手检查，handler 返回或 HTTP Server.Shutdown 不拥有已升级 socket。实例连接数 1–4096，单消息 1 字节–20 MiB，连接数乘单消息上限不超过 32 MiB（每方向正文配置预算，非进程总堆内存保证）；禁用压缩。握手/写时限不超过 30 秒，单次读时限不超过 5 分钟，绝对连接寿命不超过 24 小时且不少于读时限，所有值须显式设置。
- Origin 采用已复制的精确 http(s) scheme/host 列表，并允许真实请求的同源浏览器；拒绝 wildcard/null/路径/用户信息/多值或错误 scheme。实际 TLS 决定同源 scheme，不信任 X-Forwarded-*；TLS 终止代理后的浏览器需由组装者显式允许外部 Origin。无 Origin 的 native 客户端仍必须先认证，Origin 校验不替代鉴权。
- `Accept` 同时限制 active/pending，在升级前要求 ResponseController 写截止时间能力；不支持则失败关闭。停接取消握手写入并等待 callback 完成，防止迟到的过期截止时间覆盖升级后的 reset。pending 握手未退出时 Close 可超时，不能宣称资源已释放；忽略 deadline 的可信自定义 Hijacker 仍需退出后再 join。
- 每条连接始终保持一个 reader 以处理控制/关闭帧；并行 reader 明确拒绝。读限额覆盖组装后的完整消息而非单帧；Text 校验 UTF-8，Binary 保持字节。Write 同步串行且总时限包含等待，不复制/排队正文或自行启动写 goroutine；调用方仍须限制并发调用数量。错误仅返回白名单类别，丢弃 peer reason、原始 header/URL/payload。预取消调用不启动 I/O；I/O 中取消/超时会终止物理连接。
- 应用区分物理连接、生成和观察：前台连接丢失时显式 `Future.Cancel` 并等待原 gateway 收尾，未知执行继续 pending/保留双 hold/provider 占用；记录型交接的观察者丢失只取消 Wait，工作继续到完成/期限/runner 停止。Connection Close 与 Manager Close 都不退款、不重放，也不替代 runner 的 join。产品后台仍不能使用 volatile handoff 承诺持久受理。
- 关闭顺序：身份连接容量停接 → manager 停接/关闭/等待握手 → HTTP handlers 停接与 WS 应用 reader 退出 → runner 停接/取消/等待执行和账务收尾 → 应用 goroutine join → gateway/共享资源 → store。manager 或 runner 超时后保持后续资源存活并重试等待。物理连接 Done 仅说明 transport 清理，不说明应用生成/财务已结束。

`go test -count=1 ./internal/access ./internal/transport/websocket ./internal/generation ./internal/architecture` 覆盖真实 TCP/TLS、控制帧/分片/UTF-8、Origin、读/写/绝对寿命、慢读背压、握手/关闭竞争和容量；测试专用应用在真实 socket 上调用原 Runner/Gateway/SQLite/严格账本，验证逐轮撤销/收权、同 ID 并发隔离、前台未知成本和记录型观察者断开。测试帧信封只在 `_test.go`，载荷仍为已有 JSON/SSE 字节；没有新增生产 wire 协议、公开 API、Responses 多轮或原生 WS 上游能力。

### A3-04 加密意图与双预算原子受理

`NewIntentAcceptor(IntentOptions)` 是独立、不可派发的真实持久消费者，当前只接受现有 Chat/embeddings。可信组装者注入原 Engine 的 `IntentAdmission` 和 `Store.ResolvePrincipal`；不导入 GORM、不传事务回调。Admit 在同一事务写 `generation_intents`、稳定摘要密钥版本标记及原账本的用户/Key 双预留，任一失败全部回滚；提交确认丢失后可用相同幂等键读取原回执，不重复 hold。没有先返回 ID 再异步预留，也不复制账本、鉴权或迁移。main/YAML/环境变量尚无消费者与密钥接线，不能宣称公开后台功能已启用。

- 每次 Admit/Read/Cancel 解析当前 owner/创建 Key 和模型权限；同用户其他 Key 不能仅凭 ID 读取或取消。鉴权使用事务前本轮快照，不保证已撤销瞬间中断正在受理的事务；后续派发必须再次授权。输入先于可信回调制作副本，JSON 正文最多 64 KiB，拒绝重复字段/过深/非 UTF-8；会话标识最多 256 字节。JSON 对象键排序并消除空白用于幂等，保留数字字面量差别，不把所有数值语义强行等同。
- 幂等键为 1–128 字节可打印非空 ASCII，按 user/创建 Key 作用域唯一；同键不同请求/操作/模型拒绝。只存域分离 HMAC-SHA256，不存原键或明文输入。独立随机 32 字节 DigestKey 必须跨重启/内容密钥轮换保持不变；首次成功受理在同一事务保存其非秘密指纹，后续密钥不符时 fail closed，不能悄悄新建命名空间、重复预留或手删 `generation.intent_digest_version`。当前没有该密钥的轮换/历史迁移工具，即使清理全部内容也不自动重置标记。
- 使用与输出记录相同的独立 AES-256-GCM 密钥环规则，随机 nonce，认证 ID/owner/Key、幂等摘要、请求摘要/摘要版本、操作/模型、billing ID 和创建/截止/过期时间；缺钥/篡改不返回明文，无默认密钥或明文回落。内容密钥与 DigestKey 不得相同，分别与数据库分离备份。可更换 active 内容密钥并保留旧钥读取原回执；同一密钥累计加密达到 2^32 条/段前必须轮换，无自动 KMS 或重加密。
- QueueTTL 明确为 1 秒–5 分钟，且不得超过原 strict reservation 实际租约；Retention 为 1 分钟–7 天且不短于 QueueTTL。TTL 从受理前创建起算，不是长后台执行租约。每条密文最多 68 KiB，实例记录数 2–4096、owner 至少 1 且小于实例；owner 字节限额至少 68 KiB、实例总密文至多 64 MiB。所有尚保留的终态/到期行继续占记录和字节容量，满额原子拒绝；这是逻辑限额，不是 SQLite/WAL/索引/备份物理配额或安全擦除保证。
- 意图只为 `queued/cancelled/expired/uncertain`；queued 不授权执行，也不交给现有 Runner。原 StartAttempt 在账本事务中拒绝任何意图关联 reservation，不能把回执当成 PreDeduct 开始上游调用。Read 仅返回重新授权并认证解密的输入与回执，不授予重发/恢复权限。完整派发仍缺领取/续租/fencing、价格/能力恢复、最大执行时间和上游不确定策略。
- Cancel 先认证密文，再在同一事务证明原 reservation 属于该 owner/Key/模型、仍 reserved、没有任何 attempt/provider 占用，才调用原 finish 以零费用释放双 hold 并改意图状态。异常执行证据只改 uncertain/账本 pending，不重发、不退款、不清零 provider 占用。截止时间到期后 `RecoverGenerationIntents` 每批最多 200 条，不读取正文/不要求密钥/不返回可执行工作；原 `RecoverExpired` 先走此路径并排除所有意图关联账本，避免普通恢复误释放。
- `CleanupGenerationIntents` 每批最多 200 条，只清到期 cancelled/expired 且财务 settled/零费用/同 owner/Key/无 attempt 的内容行；uncertain 永不自动清除，可能持续占容量，须后续明确审计处置。清理不删除 reservation/balances/审计事实；过期保留回执不能创建新 hold，清理后不提供无限期幂等保证。保留期结束、清理和重用键的产品规则仍须公开 API 批次定义。

真实 SQLite 回归覆盖双预算并发、幂等丢确认/并发重试、插入/取消失败的跨表回滚、当前权限收紧、容量、保留期和密钥/元数据篡改。真实子进程分别在提交前强杀（无意图/预留）和提交成功后强杀（重启可解密读取、真实期限到期只回收未执行预算），原数据库/WAL 不包含输入或原始幂等键明文。新迁移为统一 v9，仅追加结构，不伪造历史任务；v8 及更老二进制拒绝打开，恢复旧一致备份的规则不变。本批未操作生产库，不扩展为实际旧库报错/回滚专项。

## 开发与验证节奏

默认按“批量开发 → 集中验证 → 集中修复 → 合并”推进。保留现有回归用例和合并门槛，调整的是执行频率。本文是各团队和自动化开发代理的验证节奏基准；[协作约定](COLLABORATION.md) 规定团队分工，根目录 [AGENTS.md](../AGENTS.md) 约束代理执行行为。

1. **划定功能批次**：开工前写清负责目录、接口依赖和可独立验收的结果。一个批次应能独立评审和合并，不把整个大阶段或所有团队的工作积压到最后才验证。
2. **批次内完成开发**：实现功能并补充与风险对应的回归用例；按需编译。权限隔离、数据库迁移、并发/资源释放等关键风险，以及缺陷复现，可提前运行定向测试。普通小改动不默认触发全量测试、race 或冒烟，不为低影响可逆修改新增只复述实现的测试。
3. **批次稳定后集中验证**：格式化本次修改的 Go 文件，执行 `git diff --check` 和一次 `make check`；涉及入口、配置、转发或跨模块链路时执行 `make smoke`，并完成受影响的契约、权限、协议、迁移等回归。新增并发逻辑应验证 race；本地不支持时由 Linux CI 承担，并记录验证范围。
4. **集中修复后复验**：汇总失败统一修复，先复验失败项和受影响范围。跨模块修改、公共接口/依赖/执行环境变化或影响不确定时，重新执行相关完整检查。同一代码与环境已有有效通过记录时，不无理由重复运行全量检查。
5. **合并前完整验收**：最终待合并代码在最新集成基线上通过现有 CI，包含构建、vet、格式、依赖/契约、Linux 全量 race 和冒烟。满足适用范围的已有记录可以复用；代码、基线或环境改变后重新判断有效性，不能将旧提交的绿灯直接当成新代码通过。

纯说明文档修改检查差异、链接和规则一致性即可，不新增单元测试或手动重复执行 Go 全量测试/CI。若修改可执行脚本、CI 配置、API 契约或影响运行行为的配置，则按实际影响验证，不按纯文档处理。

提交和推送按可评审批次组织，不为反复触发 CI 拆成大量细碎推送。现有 workflow 仍会按 push/PR 自动运行；不得为减少次数删除测试、关闭检查或绕过合并要求。CI 已覆盖且记录有效的检查，无需再手动重复触发。

PR/交付记录注明批次范围、实际命令和结果、验证提交/基线及环境；复用记录注明原提交或 CI 运行链接、适用范围和复用理由。未运行或仍失败的检查如实记录，不能把已编写测试写成已通过。

## 文件边界与代码习惯

| 内容 | 位置 |
|---|---|
| 入口及组装 | `cmd/gateway`，不放业务实现 |
| 生产代码 | `internal`；单元测试与被测 Go 文件同目录 |
| 场景配置/假上游/冒烟程序 | `tests/configs`、`tests/upstream`、`tests/smoke` |
| 固定协议响应样本 | 按需要在被测包的 `testdata` 添加，不预建空目录 |
| 本地二进制、数据库与日志 | `bin`、`.local`，不提交 |
| 新文档 | 先判断能否更新现有三份文档，不复制另一份计划 |

检查按上文“开发与验证节奏”在功能批次稳定后集中执行，不要求每次小改动或提交重复全量测试。只格式化本次修改的 Go 文件，避免把全仓格式变化混入功能提交。

测试覆盖现有计费、凭据调度、限流、指标格式、日志清理、配置校验，以及流式协议、每请求状态隔离、身份权限、密钥脱敏和资源边界；后续批次另有配置发布、控制台、账本对账与真实进程强杀恢复回归。各次 Linux race/冒烟和交付的具体提交与范围见 [历史验收](history/acceptance-s0-s5.md)，不将其泛化为未开发功能已通过。Windows 本地默认 `windows/386` 不支持 race；普通测试可在当前进程设置 `GOARCH=amd64`，race 验收以 Linux CI 为准。

## 从旧目录迁移

| 原路径/命令 | 新路径/命令 |
|---|---|
| `main.go`、`go run .` | `cmd/gateway/main.go`、`go run ./cmd/gateway` |
| 仓库跟踪的 `config.yaml` | 模板 `configs/gateway.example.yaml`；个人 `config.yaml` 不跟踪 |
| `config.test.yaml` | `tests/configs/openai.yaml` |
| `config.retry.yaml` | `tests/configs/retry.yaml` |
| `config.p4test.yaml` | `tests/configs/protocols.yaml` |
| `testtools/fakeupstream` | `tests/upstream/openai` |
| `testtools/fakeclaude` | `tests/upstream/anthropic` |
| `testtools/fakegemini` | `tests/upstream/gemini` |
| `testtools/sdktest.py` | `tests/smoke/sdk.py` |
| `docs/SPEC-P3.md`、`docs/SPEC-P5.md` | 当前规则统一见 `ARCHITECTURE.md` 与 `PLAN.md`，旧内容在 Git 历史 |

已有部署升级前先备份本地配置，特别是以前直接修改受 Git 跟踪 `config.yaml` 的情况。保留旧数据库路径即可继续使用原数据，本次整理没有修改数据库 schema。新示例数据库位置不会自动迁移旧 `data/gateway.db`。

## S1 请求边界与性能基线

- `core.Executor` 仅执行一次上游 HTTP 请求；每个 gateway 复用一个 Transport，退出时关闭空闲连接。
- `protocol` 只做 URL/鉴权和纯数据转换，不导入 Gin、store、GORM 或 billing；同协议保持未知字段透传，每条流独立创建解析器。
- `gateway.New(Options)` 显式注入路由、执行器、计费、限流、指标和健康反馈。请求汇总 `RequestResult` 与逐次 `AttemptResult` 分开，身份只在 HTTP 边界读取一次；转发结果不再写 Gin 字符串键。
- `server.timeout_seconds` 是逐次尝试超时；`server.total_timeout_seconds` 是包含所有重试的总预算，未设置时取前者。默认总预算不再随重试次数放大。
- 重放 body 由 HTTP 层限制为 20 MiB 内存块；只有未提交、可重放、策略允许且未取消/未超总预算的失败才重试。上游执行结果未知的超时仍可能有真实成本，本阶段不承诺 exactly-once；严格账本恢复属于 S5。
- 对于真实 net/http 下游，core 设置对应写截止时间；包装 writer 必须实现 `Unwrap` 或透传写 deadline。客户端取消会停止尝试、关闭上游 Body，不惩罚凭据。未知/无终止事件的流不会补成功 `[DONE]`。
- 普通上游响应读取上限 32 MiB，SSE 单行上限 1 MiB；超限在 core 返回失败而非无限缓冲。

可复现命令（Linux CI 含 race；Windows 普通测试先在当前 PowerShell 进程设置 `$env:GOARCH='amd64'`）：

```bash
go test -count=1 ./internal/core ./internal/protocol/... ./internal/gateway
go test -race -count=1 ./...
go test ./internal/core -run '^$' -bench BenchmarkExecutor -benchtime=100x -benchmem
```

首版实测（代码基线 `e10c7f7`）：2026-10-15，Windows/amd64，Go 1.26.3，Intel i5-12500H，16 个逻辑 CPU；每场景 100 请求，假上游等待 5 ms，输入 JSON 581–582 B（user 文本 512 B），SSE 8 个文本块。测试在回环 HTTP 上包含协议转换、网络和测试 writer 的开销；慢客户端每次 Write 额外等待 1 ms。此表是基线，不是优化前后对比；100 样本的 p99 只是单次运行观察值，不能用作服务 SLA。

| 场景 | 并发 | 平均 ns/op | p95 ms | p99 ms | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|
| 同协议 JSON | 1 | 5,581,176 | 5.905 | 6.157 | 10,739 | 109 |
| 同协议 SSE | 1 | 5,690,394 | 6.195 | 7.206 | 94,493 | 399 |
| Gemini → OpenAI SSE | 1 | 5,771,455 | 6.390 | 6.827 | 112,271 | 642 |
| 慢客户端 SSE | 1 | 38,883,049 | 40.790 | 41.380 | 92,364 | 398 |
| 并发 SSE | 4 | 1,391,771 | 6.286 | 6.832 | 105,374 | 406 |

并发场景的 ns/op 是总耗时除以请求数，不是单请求延迟；p95/p99 才是逐请求延迟。基准对所有样本排序计算分位数，CI 在日志记录 runner 环境和结果；不要跨 OS/Go 版本将差异直接归因于代码。

## S2 身份、权限与升级

### 权限与密钥约定

- 登录：`POST /api/auth/login`，JSON `username`/`password`；返回 `session_token`，24 小时过期。会话使用独立 `session-` 前缀、服务端 SHA-256 摘要和 Bearer Authorization，不使用 Cookie、不接收 URL 会话。浏览器应只在内存保留令牌；必须在 HTTPS 或本机回环上使用，不要将令牌写入 URL/日志/公开前端配置。
- 登录限速按真实 TCP 对端计数（不信任 X-Forwarded-For）：每分钟 5 次，返回 429/Retry-After；桶数量有上限。反向代理后的用户会共享代理 IP 的登录桶，不要把未受信任请求头当绕过入口。
- 密码用 bcrypt（成本 10）验证，12–72 字节，无默认口令。初始化命令 `-init-admin` 从 `TXAPI_ADMIN_USERNAME`/`TXAPI_ADMIN_PASSWORD` 读取，仅在尚无管理员时成功，不接受命令行明文密码。
- 用户角色为 admin/user，状态 active/disabled。`PUT` 是完整资源更新：遗漏角色/模型策略将使用安全默认值（user/allowlist），应提交需要保留的字段。禁止停用/降级最后一个有效管理员；修改密码、角色或状态会撤销已有会话。
- `model_mode` 只有 `all` / `allowlist`；后者的空 `models: []` 表示全部拒绝，不能用 `*` 冒充明确授权。实际调用权限 = 系统策略 ∩ 用户策略 ∩ Key 限制，管理员的调用 Key 同样受限。
- Key 由服务端随机生成 256 位，仅创建响应含 `key`；后续只返回 `key_prefix` 等元数据，数据库 `key` 兼容列始终为空。自助创建/更新不能指定别人为 owner、提交自选密钥或放大用户允许的模型/限额；零值限额继承用户上限。
- 状态与权限在下一次鉴权时读数据库并生效，不能因为旧缓存而继续放行；现有已开始的流不会主动追杀。S2 建立身份隔离；此后的 S3 已增加用户跨 Key RPM 与并发租约，Key RPM/估算 TPM 仍独立执行，不能把当前行为简化为只在 Key 桶取用户/Key 更小值。用户跨 Key TPM 尚未实现，详见 [S3 运行约定](#s3-并发准入与用户请求速率)。
- 日志 user_id 取自已验证 Key 的 owner，忽略调用 body/请求头中的 user_id；个人查询始终加 owner 条件。个人用量是本人日志聚合与预算，不等于 S5 的可靠账本。

### API 分区

所有新接口都只使用 `Authorization: Bearer <session_token>`（登录接口除外）。

| 分区 | 接口 | 说明 |
|---|---|---|
| auth | `POST /api/auth/login`、`POST /api/auth/logout` | 登录/销毁当前会话 |
| me | `GET /api/me` | 当前用户资料 |
| me | `GET/POST /api/me/keys` | 本人 Key 列表/创建；可指定 model_mode、models、expires_in_seconds、quota、rpm_limit、tpm_limit、concurrency_limit |
| me | `GET/PUT/DELETE /api/me/keys/:id` | 本人详情、更新、吊销；跨 owner 统一返回 404 |
| me | `GET /api/me/models`、`GET /api/me/logs`、`GET /api/me/usage` | 本人模型、日志和用量；不接受指定其他 owner |
| admin | `GET/POST /api/admin/users`、`GET/PUT /api/admin/users/:id` | 创建/停用用户，配置角色、模型范围与上限 |
| admin | `GET/POST /api/admin/keys`、`PUT/DELETE /api/admin/keys/:id` | 全局 Key 管理（创建必须提供有效 user_id） |
| admin | `POST /api/admin/keys/:id/claim` | JSON user_id，显式认领旧无主 Key，只能认领一次 |
| admin | `GET/PUT /api/admin/model-policy` | JSON `{"mode":"all"}` 或 `{"mode":"allowlist","models":["m1"]}` |
| admin | `/api/admin/channels`、`credentials`、`logs`、`stats`、`prices`、`group-rates`、`abilities` | 复用已有管理能力，以管理员会话保护 |

用户创建示例：管理员会话调用 `POST /api/admin/users`，body 为 `{"username":"alice","password":"请设置独立强密码","role":"user","model_mode":"allowlist","models":["m1","m2"]}`。Alice 登录后 `POST /api/me/keys`，body `{"name":"client","model_mode":"allowlist","models":["m1"]}`；调用 `/v1/*` 使用创建响应的 Key，而不是 session_token。

旧 `/api/*` 路由暂不退役，仍要求 `GATEWAY_ADMIN_KEY`/X-Admin-Key；旧密钥是全局管理权限，必须视为 root 凭据保管。它不能代替新登录会话访问 `/api/admin/*` 或 `/api/me/*`；普通调用 Key 对新旧管理接口均无管理权限。管理密钥目前仍是启动必填项。

### 从 S0/S1 数据库升级

1. **先停写再备份**：停止网关，确认进程退出、异步日志已 flush；对配置中实际 database.path 以及存在的同名 `-wal`/`-shm` 做一致备份。不要只复制运行中的主库文件。将备份保存在受保护位置，备份仍可能包含旧明文 Key。
2. 新版本首次 `store.Open` 在建表/补列后，事务执行 `schema_migrations.version=2`：哈希旧客户端 Key、填展示前缀、保留 token id/user_id/状态/到期/预算和模型限制，清空旧 key 列、替换原明文唯一索引。事务失败拒绝启动，不记录版本完成标记；建表补列可能已执行，恢复时以备份为准。
3. 初始化管理员：用 `-init-admin`（只初始化，不导 YAML、不启动 HTTP）。已有管理员时拒绝重建；旧用户的模型范围采用 allowlist 安全默认，需要管理员明确授权。
4. 若需要导入 YAML，单独执行 `./bin/gateway -config config.yaml -seed-only`。导入 Key 也只存摘要，且 user_id=0，因此不能直接调用。
5. 对每个无主 Key 明确分配 owner：停机维护时 `./bin/gateway -config config.yaml -claim-token 1 -owner-user 1`，或者管理员登录后使用 `/api/admin/keys/1/claim`。参数是数据库真实 ID，不要假定现有库的管理员或 Key 一定是 1；初始化命令会输出新管理员 ID。认领不改变到期、停用或模型限制，不能重新转移已认领 Key；历史 user_id=0 的日志不反向猜测归属，只能由管理员查看。
6. 验证：旧库无主 Key 在认领前 401，认领后按 owner 授权调用；A/B 用户无法互看 Key/日志；停用用户后其 Key 与会话立即失败；新建 Key 只显示一次。正常客户端的原 Key 值保持有效，但旧明文已不可通过管理接口恢复。
7. **回滚限制**：旧二进制只认明文 key 列，不能直接运行于已迁移数据库。必须停新版、保留升级后数据备份，再恢复升级前一致备份后启动旧版本；回滚会丢失升级后新增用户/Key/日志/用量，需提前明确可接受窗口。严禁“重启旧二进制”当作无损回滚。
8. 清空数据库列不等于物理安全擦除：旧 WAL、SQLite 空闲页、磁盘快照和历史备份可能仍有旧明文；需在受控停机窗口 checkpoint/整理库、保护或销毁旧介质备份，并按风险轮换 Key。本版本不宣称自动完成取证级安全擦除。

S2 回归命令：

```bash
go test -count=1 ./internal/access ./internal/store ./internal/server/...
go test -race -count=1 ./...
make smoke
```

`make smoke` 现在会在隔离临时库中执行管理员初始化 → 导入 → 显式认领，再验证旧 OpenAI 完整链路；不接触真实库或真实凭据。
