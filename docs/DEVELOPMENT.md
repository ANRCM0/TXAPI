# 开发指南

本页只记录可执行命令与文件约定。目标设计见 [ARCHITECTURE.md](ARCHITECTURE.md)，阶段状态见 [PLAN.md](PLAN.md)。

## 工具与工作目录

- Go 1.23 或更高版本，以 `go.mod` 为语言基线。
- Python 3.10+ 用于标准库冒烟脚本；可选 OpenAI SDK 检查另见下文。
- Make 是便捷入口，不是程序运行依赖。
- 所有命令从仓库根目录执行，数据库相对路径按进程工作目录解析。

```bash
make init
make build
make check
make smoke
```

`make init` 只在缺少配置时复制模板。`make clean` 仅移除 `bin/`，不会删除本地配置和数据库。可以用 `make GO=/path/to/go PYTHON=/path/to/python3 check` 指定工具。

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

## 文件边界与代码习惯

| 内容 | 位置 |
|---|---|
| 入口及组装 | `cmd/gateway`，不放业务实现 |
| 生产代码 | `internal`；单元测试与被测 Go 文件同目录 |
| 场景配置/假上游/冒烟程序 | `tests/configs`、`tests/upstream`、`tests/smoke` |
| 固定协议响应样本 | 按需要在被测包的 `testdata` 添加，不预建空目录 |
| 本地二进制、数据库与日志 | `bin`、`.local`，不提交 |
| 新文档 | 先判断能否更新现有三份文档，不复制另一份计划 |

改动后执行 `git diff --check`、`make check`；涉及入口、配置或转发时再执行 `make smoke`。新增并发代码运行 `go test -race ./...`。只格式化本次修改的 Go 文件，避免把全仓格式变化混入功能提交。

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
