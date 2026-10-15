# 多团队开发与框架基线

本页定义 **S2 之后的协作框架 F0**：哪些接口可以依赖、谁改哪些目录、如何联调以及如何验收。当前架构仍是模块化单体；“多个团队”指开发分工，不会给产品新增团队/组织租户模型。业务进度继续以 [PLAN.md](PLAN.md) 的 S3–S5 为准。

F0 是后续功能团队的接入基线，不代表 S3 的公平并发、S4 的前端交付或 S5 的严格账本已经实现。仓库内部 Go 接口不是公开 SDK；本次约定的修改需要调用方一起评审。

## 1. 可以并行推进的分工

| 责任角色 | 主要写入目录 | 可立即推进的工作 | 需要与集成负责人协调的文件 |
|---|---|---|---|
| 平台/集成 | `cmd/gateway`、`internal/core`、`internal/gateway`、`internal/architecture` | 公共请求生命周期、稳定扩展接口、取消/租约、基准、依赖检查 | main、gateway.Options、CI、迁移版本表、OpenAPI |
| 协议团队 | `internal/protocol/adaptor/<provider>`、协议样本/测试 | 新厂商转换、协议能力/错误映射、独立 SSE 解析器 | `gateway.Options.Adaptors` 的生产注册，由集成团队接线 |
| 身份与存储团队 | `internal/access`、`internal/store/access*`、相关 API 功能切片 | 用户/Key 生命周期、权限收敛、owner 校验、迁移回归 | `internal/model`、`internal/store/migrations.go`、OpenAPI |
| 调度与准入团队 | `internal/scheduler`、自己的准入实现包、相关测试 | S3 用户聚合限流、并发租约、调度健康策略与负载测试 | Admission 接口、gateway.Options 和限额配置/模型字段 |
| 管理 API 团队 | `internal/server/api/<feature>.go` 与对应 store 方法 | 按资源增加真实 API、输入校验与隔离测试 | RegisterAccess/registerResources 注册点、OpenAPI |
| 前端团队 | S4 建立后在独立 `web/` 或 `ui/` 目录开发 | 可先依据 `docs/openapi.json` 做交互、页面和联调；当前后端可运行 | 首次框架选择、静态文件嵌入和构建由集成负责人统一落地 |
| 测试/交付团队 | `tests/`、部署脚本、恢复说明 | 模拟上游、启动/升级/恢复、API 工作流、性能基线 | CI 合并门槛和数据库迁移用例 |

表内是责任角色，不是已指定人员。GitHub 成员权限、CODEOWNERS 的真实用户名、分支保护和必须评审人数需要仓库维护者配置；本次不会填入不存在的账号或声称这些设置已生效。

工作切片须有独立写入范围；例如不要让多个团队同时修改 `api/access.go`。新 API 优先放自己的资源文件，由集成人员合入注册点。团队开始前在任务/PR 写明依赖的接口版本与写入目录，不以跨目录复制实现绕过接口。

## 2. 已实现的接口与生命周期

### 身份边界：internal/access

- `Principal` 是已验证的模型调用身份；通过 `access.WithPrincipal` / `PrincipalFromContext` 传递，包含可信 user_id/token_id、模型策略和限额快照。
- accessor 对模型切片做拷贝，不能通过修改返回值污染其他请求；前端 body/header 里的 user_id 不构成身份。
- Request ID 使用 `access.WithRequestID` / `RequestID`。ID 只关联日志，不作为共享流状态或授权依据。
- gateway 不再导入 `internal/server/middleware`。身份/准入团队无需依赖 Gin 的字符串上下文键；现存 Ctx* 镜像只用于旧中间件兼容，新代码不能新增依赖。
- 管理登录会话与调用身份始终分开；普通 Bearer API Key 不能当 session 使用。会话角色/owner 的细节由 server/access 接口处理。

### 请求执行：internal/core

`Executor.Execute(ctx, writer, input)` 只执行一次 HTTP 尝试，不关心用户/渠道优先级/计费。core 仅依赖标准库；protocol 不依赖 HTTP 框架、数据库、调度和计费。

`core.Result` 的 StatusCode/Termination 是执行结果，HTTPStatus/Committed 是已经写给客户端的事实，两者不能混为一谈。流已提交后无法把 HTTP 200 改为 502，也不能重试或追加正常结束标记来假装成功。

### 协议扩展：internal/protocol

厂商适配器实现 `adaptor.Adaptor` 和需要的 `ProtocolAdaptor`，有状态 SSE 实现 `SessionStreamer.NewStreamSession`。适配器实例必须可并发只读；解析状态只属于本次 attempt。未知字段透传/不支持能力的显式拒绝由协议测试约束。

新协议在自己的目录开发，集成人员在创建 gateway 时注册：

```go
gw, err := gateway.New(gateway.Options{
    Channels: channels,
    Adaptors: map[string]adaptor.Adaptor{"provider-name": providerAdaptor},
    // 其他依赖由应用入口注入
})
```

注册表在构造时复制，不能通过修改原 map 热替换实例。内建协议默认仍可用；替换已有名称是显式的构造行为。不能在协议包发 HTTP 请求、访问用户表、记录费用或创建后台 worker。`protocol.Resolve` 仅描述内建默认协议，不等于实例注册表。

### S3 准入接入：gateway.Admission

```go
type Admission interface {
    Acquire(context.Context, gateway.AdmissionScope) (release func(), err error)
}
```

- `RequestAdmission` 在整次路由/重试之前获取一次；`AttemptAdmission` 在每次选中目标后获取，scope 附带可信 user/token/model 和 channel/credential/尝试序号。
- Request lease 覆盖整个重试循环；attempt lease 覆盖单次网络执行。两者在取消、失败和 panic 路径释放，嵌套释放顺序是 attempt → request。请求租约不把响应后日志落库与结算变成受限网络并发的一部分。
- Acquire 必须遵守 ctx 截止时间，不能无限排队。成功必须返回非 nil 的 release；异常/取消时若已拿到租约也必须可释放。release 必须快速、并发安全，不依赖已取消的请求 context；不要在 release 内发起无限期网络请求。
- 拒绝返回 `AdmissionError`：429 为配置限额，503 为暂时容量不可用，可附 RetryAfterSeconds。Message 必须能安全对外显示；普通后端 error 不向客户端回显。准入拒绝不发上游请求、不惩罚凭据。
- 默认 Admission 为 nil（未启用）；这表示扩展接入点可用，不表示并发控制已实现。多实例全局公平性、用户跨 Key 聚合、队列与租约算法属于 S3 团队。
- 原有速率实现通过 `access.Limiter` 复用；TPM 预留在 `WithTokenReservation` 记录，`FinishTokens` 只调整一次。取消后用独立、最多 2 秒的 context 释放计数，避免每个业务模块再实现一套收尾。

### 健康回写与关闭

`NewHealthWorker(persist, reload)` 创建独立队列，不再用进程级可变回调。每个应用拥有自己的 persister/reloader；Close 幂等并排空已接收事件。回调可有界阻塞，但不能调用自身 Close 形成死锁。纯环缓存仍是调度器内部 memoization，不作为授权或租约账本。

关闭顺序：HTTP Shutdown 等在途请求 → 停止恢复/清理生产者 → Close 健康 worker/限流器/执行器 → flush 日志并关闭 store。main 由集成团队维护，功能团队不要自行在包初始化中启动 goroutine。

## 3. 前后端契约

唯一机器可读 HTTP 基线：[openapi.json](openapi.json)，版本 `0.1.0`。路径沿用现有 `/api/...` 与 `/v1/...`；这个版本标识契约文档，未给现有 URL 强加新前缀。

- 新前端使用 `/api/auth`、`/api/admin`、`/api/me`；旧 `/api/channels` 等路径标记 deprecated，暂保留原管理密钥鉴权。
- `APIKey` 为无明文 metadata，`APIKeyCreated` 才有一次性 key。不要将数据库 model.Token 直接作为前端所有场景的通用类型。
- 所有 PUT 都按文档确认完整更新语义；不假定未发送字段自动保留。policy 空 allowlist 必须全部拒绝。
- 现有 list 在无数据时可能返回 null；契约显式 nullable。分页/status/响应封装以契约和回归为准，不能在 UI 中猜测。
- 流错误/截止时间/取消属于终止结果；已提交的流不会伪造成功 DONE。UI/SDK 必须处理半途失败。
- OpenAPI 的 responses.default 是兼容错误封装；目前错误 type 字段不保证每条旧接口都相同。若需进一步统一错误码，先提交独立兼容方案，不静默改所有 API。

CI 会校验契约与真实 Gin 路由一一匹配、匿名请求受保护、公开 DTO 字段和关键控制面响应符合约定。本仓库实现的是上述子集校验，不宣称代替完整 OpenAPI 标准验证或所有字段语义测试；复杂业务必须有行为测试。

接口变更流程：先更新同一个 PR 的 spec/受影响调用方/契约测试 → 再实现 → 合并后记录变更说明。破坏性改动须注明消费者迁移步骤，不能让前端靠重复阅读 Go handler 跟进。

## 4. 数据库变更协作

- 模型结构集中在 `internal/model`，结构建表仍由 AutoMigrate 管理；需要的数据/索引迁移统一追加到 `internal/store/migrations.go`。
- F0 未执行新 schema 版本；v2 仍是 S2 已发布的 Key 摘要迁移。不要因重构运行重复的数据迁移。
- 每个版本号由集成负责人分配，同一集成周期按顺序合并；未合并分支不要自行预占同一个已发布版本。已发布步骤只追加不重写。
- runner 在事务中执行一步并登记版本；步骤不允许 Commit/Rollback、不读写真实外部服务。失败既回滚数据也不记版本，重试可重新执行。
- 打开数据库前检查最高版本；比当前二进制新的 schema 将被拒绝，避免先 AutoMigrate 再发现不兼容。结构 AutoMigrate 本身仍不是完整 SQL 迁移框架，备份/恢复规则继续适用。
- 新迁移至少有旧 schema 升级、重启幂等、失败回滚和恢复限制说明。绝不能把清空数据库当修复方案。

## 5. 分支、测试与集成

推荐按团队/功能创建独立分支，例如 `feat/s3-admission`、`feat/protocol-provider`、`feat/control-ui`。新团队从 **本框架分支通过 CI 并合并后的 master** 建分支；从旧 S2 开工会缺少这些接口。所有合并必须在最新基线上通过依赖/契约检查、build/vet/gofmt、race、权限/协议/迁移回归与冒烟。

```bash
# 只测共享边界，不依赖真实上游、Redis 或实际业务数据库
go test -count=1 ./internal/architecture ./internal/access ./internal/core ./internal/protocol/... \
  ./internal/gateway ./internal/scheduler ./internal/store ./internal/server/...

# 前端/API 契约核对
go test ./internal/server -run TestOpenAPI -count=1

# 完整 Linux 验收
go test -race -count=1 ./...
make smoke
```

本地 Windows 仍按 DEVELOPMENT.md 为当前进程设置 GOARCH=amd64；race 以 Linux CI 为准。测试必须使用 t.TempDir/httptest/随机端口，不能依赖默认 config.yaml、真人密码或开发者已存在的渠道。启动与 API 操作说明见 DEVELOPMENT.md 的 S2 章节。

依赖规则由 `internal/architecture/dependencies_test.go` 自动执行：gateway 不依赖 server/store，protocol 不依赖业务层，core 只有标准库；测试为集成验证可跨层，但生产代码不能靠 `_test.go` 包装逃过真实依赖。

## 6. 框架交付门槛

- [x] 已验证身份与速率结算使用 typed context，不向业务层暴露 Gin 字符串键。
- [x] 协议注册、请求/attempt 准入、健康回写通过实例依赖注入；lease 释放/取消/panic 有行为回归。
- [x] 当前 70 个路由有可读契约，路由/鉴权/DTO 漂移进入 CI。
- [x] 统一数据迁移注册、失败回滚、重启幂等与旧二进制拒绝更高版本的回归。
- [x] 团队目录边界、公共接线、接口变更流程和独立测试入口清晰。
- [x] 框架提交 `bd322c0` 通过 Linux CI：构建、vet、gofmt、依赖与契约、race 全量测试、启动/迁移冒烟和性能基准。
- [ ] 维护者审查合并到 master 后，作为团队共同开发基线。

F0 可以让团队开始各自的协议/API/页面/限额实现；生产准入能力、管理 UI、配置发布版本与严格计费仍要按 S3、S4、S5 的验收门槛交付。

验收记录：2026-10-15，功能分支 feat/team-foundation，[Verify #38703566885](https://github.com/ANRCM0/TXAPI/actions/runs/38703566885) 全部通过。本地 amd64 构建、vet、全量普通测试与冒烟通过；目前没有执行真实业务库升级或配置 GitHub 团队权限。
