# API Gateway

面向个人与多用户分发的轻量 LLM API 网关。目标是以独立转发内核为中心，用清晰的模块边界接入权限、凭据调度和管理能力。

**身份模型只有管理员与普通用户。** 当前代码仍为原型，目标架构与实施步骤见下列文档；文档中的规划不代表功能已经实现。

| 文档 | 唯一职责 |
|---|---|
| [架构设计](docs/ARCHITECTURE.md) | 模块边界、管理员—用户模型、接口原则 |
| [开发计划](docs/PLAN.md) | 当前状态、分阶段任务、验收标准 |
| [开发指南](docs/DEVELOPMENT.md) | 构建、测试、配置和目录约定 |

## 当前能力

- Go + Gin，SQLite + GORM，渠道与令牌内存缓存。
- `/v1/chat/completions`、`/v1/models`，客户端 Bearer 令牌与模型限制。
- 渠道优先级、加权选择、模型映射和失败换渠道。
- OpenAI 兼容透传，以及 Anthropic/Gemini 上游转换原型。
- 渠道/令牌管理 API、配置热更新和用量日志。

目前没有完整的管理员登录、普通用户管理、额度结算和凭据池调度。管理 API 尚未启用鉴权；流式转换与缓存还有[已知缺陷](docs/PLAN.md#s0-修复正确性与安全边界)。当前版本仅适合受控本地开发，不应直接暴露公网。

## 本地启动

需要 Go 1.23 或更高版本。从仓库根目录执行：

```bash
make init
# 编辑 config.yaml，替换上游地址、模型名和两种密钥占位符
make build
./bin/gateway -config config.yaml
```

`make init` 不覆盖已有 `config.yaml`，只创建本地配置和运行目录。`config.yaml` 不提交；可复用的示例在 `configs/`。渠道与令牌仅在数据库空时从 YAML 导入，之后通过管理 API 更新。

不使用 Make 时：

```bash
mkdir -p bin .local/data
# 仅在没有 config.yaml 时复制，避免覆盖已有配置
test -f config.yaml || cp configs/gateway.example.yaml config.yaml
go build -o bin/gateway ./cmd/gateway
./bin/gateway -config config.yaml
```

## 无真实密钥的检查

```bash
make check
make smoke
```

`make check` 执行构建、vet 和 Go 测试；当前尚无 Go 单元测试，编译通过不能代表业务正确性。`make smoke` 使用 Python 标准库管理本地假 OpenAI 上游和临时数据库，验证启动、鉴权、非流式与流式转发，不会调用外部模型或使用真实密钥。

## 目录

| 路径 | 内容 |
|---|---|
| `cmd/gateway/` | 程序入口 |
| `internal/` | 当前业务实现；按计划逐步提取内核 |
| `configs/` | 可提交的示例配置，不含真实密钥 |
| `tests/configs/` | 各场景独立数据库的测试配置 |
| `tests/upstream/` | 本地假上游 |
| `tests/smoke/` | 冒烟检查与可选 SDK 检查 |
| `docs/` | 架构、开发计划和操作指南 |
| `bin/`、`.local/` | 本地生成物，不提交 |

入口已从根目录 `main.go` 移至 `cmd/gateway/main.go`；原 `go run .` / `go build .` 请改为 `go run ./cmd/gateway` / `go build ./cmd/gateway`。Go module 暂保留 `minigateway`，避免目录整理时额外修改所有导入路径。
