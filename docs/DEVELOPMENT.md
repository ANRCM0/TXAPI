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

首版实测：2026-10-15，Windows/amd64，Go 1.26.3，Intel i5-12500H，16 个逻辑 CPU；每场景 100 请求，假上游等待 5 ms，输入 JSON 581–582 B（user 文本 512 B），SSE 8 个文本块。测试在回环 HTTP 上包含协议转换、网络和测试 writer 的开销；慢客户端每次 Write 额外等待 1 ms。此表是基线，不是优化前后对比；100 样本的 p99 只是单次运行观察值，不能用作服务 SLA。

| 场景 | 并发 | 平均 ns/op | p95 ms | p99 ms | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|
| 同协议 JSON | 1 | 5,581,176 | 5.905 | 6.157 | 10,739 | 109 |
| 同协议 SSE | 1 | 5,690,394 | 6.195 | 7.206 | 94,493 | 399 |
| Gemini → OpenAI SSE | 1 | 5,771,455 | 6.390 | 6.827 | 112,271 | 642 |
| 慢客户端 SSE | 1 | 38,883,049 | 40.790 | 41.380 | 92,364 | 398 |
| 并发 SSE | 4 | 1,391,771 | 6.286 | 6.832 | 105,374 | 406 |

并发场景的 ns/op 是总耗时除以请求数，不是单请求延迟；p95/p99 才是逐请求延迟。基准对所有样本排序计算分位数，CI 在日志记录 runner 环境和结果；不要跨 OS/Go 版本将差异直接归因于代码。
