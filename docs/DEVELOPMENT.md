# 开发指南

本页记录可执行命令、运行约定与开发验证节奏。目标设计见 [ARCHITECTURE.md](ARCHITECTURE.md)，阶段状态见 [PLAN.md](PLAN.md)。

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

可选浏览器竞态回归：已有 Playwright 环境中执行 `node tests/browser/navigation.cjs`（必要时用 NODE_PATH 指向其模块目录，PLAYWRIGHT_EXECUTABLE_PATH 指定本机浏览器）。脚本使用独立浏览器上下文、随机本机端口与模拟 API，不读取真实会话或调用上游；验证旧测试页的延迟模型列表不能覆盖当前页面清理逻辑，离开页面必须取消当前调用。此项不是编译/部署依赖，也没有冒充为现有 CI 已执行的检查。

### 安装、容器与升级边界

本机全新安装：`make init` → 编辑本地配置/环境中的管理密钥（可清空示例 channels/tokens，改用页面创建）→ `make build` → `-init-admin` → 正常启动 → 访问 `/`。升级 S3 不增加 schema（仍为 v4），但仍须先停写一致备份；旧库无主 Key 的认领规则不变。

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
- 删除进程状态后重新启动，确认原 Key、用户、日志和用量仍在数据卷中；故意将 YAML database.path 指向不可写位置，验证 Compose 的卷内路径环境覆盖真实生效。
- 运行旧 S3 镜像创建用户/渠道/凭据/Key，完成非流式和 SSE 调用；停写、正常关闭后备份，再用 S4 镜像打开同一个卷，验证原身份、授权、Key、凭据 ID 和用量保留，schema 仍为 v4。
- SSE 在途时发起 Compose stop，要求完整 DONE、退出码 0 且已接收日志排空；Compose 为正常关闭和日志排空预留 30 秒，不将该时间承诺为任意负载下的恢复保证。
- 升级后先另存一份备份，再恢复升级前主库及存在的 WAL/SHM；检查 SQLite integrity_check 和关键表逐行一致，重启原 S3 镜像验证原 Key/数据可用，升级后新增数据不在旧备份中。

所有备份只在停止网关并验证正常退出后执行。恢复前确认卷属于本次演练项目，恢复后校正 UID/GID；脚本只删除自己创建的容器/卷/镜像，失败时输出测试容器日志。生产操作仍须按真实路径停写备份，不得对业务卷运行测试清理命令。

本演练覆盖 S3/v4→S4/v4 的实际二进制升级与备份恢复，不将其冒充为更老 schema 的容器升级证据；v2/v3→v4 迁移与更高版本拒绝仍由现有 store 回归及完整 race CI 验证。这里的正常恢复也不等于崩溃时无损，普通日志和进程级 revision 的限制不变。

本地 Windows 的页面/普通测试不代替 Linux race、完整 CI、容器实跑以及安装/升级/恢复演练。本批阶段状态见 PLAN，不因编写部署说明就标记验收通过。

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

会话亲和是可选优化，配置键为 `server.session_affinity`，默认 `false`。设置为 `true` 后，亲和 ID 由可信 user ID +（客户端 `X-Session-Id` 的摘要，或 token ID 和首条 user 消息摘要）构成；客户端头不原样进入日志，不同用户的亲和 ID 不相同（共享上游池仍可能哈希到同一凭据）。无会话内容时用加权选择。只有真实上游测量显示缓存命中、延迟或成本收益后才开启；坏目标/已试目标在当前可用池重新映射；没有会话或超过环容量时回退加权选择，不能因为亲和阻断请求。安全与测量边界见 [README 亲和说明](../README.md#5-一致性哈希亲和)。

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
- User 的 `rpm_limit` 在请求级准入中按用户 ID 聚合，所有 Key 共用同一个精确 60 秒滑动窗口。`0` 为不设用户 RPM；通过准入的逻辑请求计数一次，重试不重复计数，响应失败也不退回请求次数。Key RPM 和估算 TPM 保留既有中间件处理，可能在后续并发拒绝前已计入 Key 的请求窗口。用户 TPM 的跨 Key 聚合仍未实现，不宣称已有可靠预算/账本。
- 用户 RPM 状态最多保留 4,096 个有效用户窗口、合计 65,536 个有效事件；下一次准入/观测时回收过期事件。不淘汰仍有效的窗口来放行新用户，满时保守拒绝并返回 `503`；这是状态内存保护上限，高流量部署需在后续方案中评估容量或外部限流实现。
- 客户端取消、写入客户端失败和整次请求预算耗尽不计为凭据故障；请求总预算仍有效时，单次上游超时照常反馈健康状态。准入本身不改用户权限、渠道优先级或凭据状态。

调度健康的持久化队列最多 256 个事件，健康 worker 处理时读取最新数据库记录，校验代次与观察顺序；生产协调层已计算的健康状态直接按原观察结果落库，避免从滞后数据库重复计算。请求协调层在上游失败后立即更新不可变运行快照；异步落库期间的旧快照不能回滚该冷却。人工编辑凭据/设置状态会递增内部 `health_generation`，旧请求结果失效；写入同时检查读取时的 `updated_at`，保护读取后发生的管理变更；内部 `health_observed_at` 使用递增的观察时间标记，阻止较晚提交的旧失败覆盖成功恢复，热重载也按同一标记合并。队列满或数据库异常会告警，持久化不是可靠账本，进程崩溃可能丢失尚未落库的观察结果。

Key/IP 限流共享的窗口表最多 4,096 个桶，每桶最多 4,096 个事件；只回收过期桶，容量不足时保守返回 429。TPM 为原有估算限制，不是严格预算；完成补记在桶容量满时合并到最后事件，表容量不足时告警，不能当作可靠账本。权重小于等于 0 按 1、大于 1,000,000 按 1,000,000 参与选择，防止异常数据库值造成溢出。可选亲和缓存最多 64 个环，每环最多 32,768 个节点；超过 4,096 个凭据的池回退加权随机。

### S3 数据库升级与回滚

1. 停写并备份实际 SQLite 主库及存在的 WAL/SHM，按下文 S2 的一致备份要求操作；不操作真实业务库来执行开发验收。
2. 启动时 AutoMigrate 为 `credentials` 增加内部 `health_generation`、`health_observed_at`（默认 0，不返回 API），迁移注册表依次追加版本 3、4，保留已有渠道、凭据、Key、健康状态和 S2 迁移语义。没有新 HTTP 路由；旧库升级与重复启动有回归。
3. v4 数据库由支持该版本的新程序打开；S2/F0 及仅支持 v3 的 S3 二进制检测到更高版本会拒绝启动。回滚须停新程序、保存升级后数据，再恢复升级前一致备份并启动旧版；会丢失升级后新增/修改的数据。不得删除版本记录伪装成可直接降级。AutoMigrate 的结构变更可能先于事务迁移完成，失败恢复以备份为准。

本批集中验收入口：`make check`、`go test -race ./...`（Linux CI）与 `make smoke`。回归覆盖跨 Key 的用户 RPM/并发、Key 自身上限、其他用户仍可调用、128 个并发获取、重复释放、速率表容量与到期、重试只计一次请求、取消/总超时/panic 释放，以及真实鉴权投影的限制变更。按上文验证节奏在批次稳定后执行，无需每次编辑重复运行。

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

测试覆盖计费、凭据调度、限流、指标格式、日志清理、配置校验，以及 S0 的流式协议、会话隔离、鉴权、密钥脱敏和资源边界。S0 已于 2026-10-15 在 Linux GitHub Actions 上通过全量 race 测试和冒烟验证，验收记录见 [PLAN.md](PLAN.md)。Windows 本地默认 `windows/386` 不支持 race；普通测试可在当前进程设置 `GOARCH=amd64`，race 验收以 Linux CI 为准。

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
- 状态与权限在下一次鉴权时读数据库并生效，不能因为旧缓存而继续放行；现有已开始的流不会主动追杀。此阶段优先隔离正确性；按用户聚合的限流、并发租约属于 S3，当前用户速率上限与 Key 上限按更严格值用于 Key 桶。
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
