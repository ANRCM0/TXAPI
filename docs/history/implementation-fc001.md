# FC-001 四层地基实施与验证记录

## 批准与基线

2026-10-18 用户明确批准：“好批准实施提案。请你按照顺序完成提案”。
批准来源为本任务用户消息，覆盖 [FC-001](../proposals/FC-001-foundation-contracts-v1.md)
的 FND-01 至 FND-05 顺序、四层/公开契约、迁移和验收约束；范围内不重复索要许可。
原本地 FND-00 文档提交 `c4ba10c` 保留；开工 fresh-fetched `origin/master`
为 `4312f774e48ad8f5066f1e238589666549b3a533`，工作树原为干净。

活动状态以 [PLAN](../PLAN.md#地基符合性收口fc-001) 为准，版本/接口/剩余债务见
[Foundation API v1](../reference/foundation-api-v1.md)。本文件记录实际来源与结果，
不因批准提案或已有 S6/A02 CI 就声称新地基验收通过。

## FND-01：公开执行、身份与输出边界

本批负责 L1 execution/identity 契约、原 core/Gateway 转换桥与版本化 HTTP 输出门面、
L2 输出工厂、Responses/generation 实际消费者及 main 组装；不改变 L0 行为、外部
HTTP/财务/持久语义。公开契约不导入实现、不通过别名泄漏类型；旧实现向新公开值类型
的迁移别名保留原消费者编译，禁止新的功能依赖这些旧入口。

实际执行 Windows/PowerShell Go 命令均显式 `GOARCH=amd64`。仅使用临时 SQLite、
loopback 假上游、测试凭据与现有本地浏览器工具；不操作真实提供商或生产数据库。

### 实施期间的编译检查

- 首次 `go build ./...` 发现迁移 Policy 后 access 的无用 strings import，修复后构建通过。
- 首次 `go test -run '^$' ./...` 仅编译测试，发现旧 cmd GenerationIntent 参数与一个原生状态字段引用；更新消费者后再次全仓编译通过。此命令没有执行行为测试。
- 提前定向风险验证中，foundation/identity、foundation/runtime、architecture、responses 通过；新加旧归档回归的 import 名与现有 execution 测试 helper 冲突，generation 编译失败，已改为 execv1。该检查不是完整通过证据，失败包与最终全量在修复后继续复验。
- 已编写结果映射/复制、缺身份/短写/取消、当前主体绑定/权限/异常、契约依赖负面样例和旧加密归档格式读取回归；最终集中结果在源码稳定后记录。

### 最终本地集中验证

最终冻结来源为 306 个非 Markdown 路径，SHA-256
`ca4094b8b9fa8f69eea922f9ab44bd434e7588bfa1574e7e96ec3a12733c234c`，
集中验证前后相同；后续提交前仅更新说明文档。

| 实际命令 / 环境 | 结果 |
|---|---|
| `GOARCH=amd64 go test -json -count=1 ./...` | 1037 项测试/子用例、26 包 PASS；5 个预期子进程 helper SKIP，无失败 |
| `go build ./...`; `go vet ./...`（同进程 GOARCH=amd64） | PASS |
| `go mod tidy`; `git diff --exit-code -- go.mod go.sum` | PASS，无依赖漂移 |
| 仅变更 Go 的 normalized-LF gofmt 比较 | 44 文件 PASS；没有格式化其他 Go 文件 |
| `node --check` app/api；`node --test web/assets/api.test.mjs` | 语法 PASS、10 项 API 消费行为 PASS |
| `GOARCH=amd64 python tests/smoke/local.py --go go` | 真实 main/auth/Chat/SSE/控制台、strict 拒绝、强杀 pending/审计豁免、凭据加密/轮换拒绝恢复、durable restart 与正常退出 PASS |
| `GOARCH=amd64 python tests/smoke/compatibility.py --go go` | 已安装 Playwright/Chromium，真实临时二进制/SQLite/API/UI、配置发布、模型/Key 限制、角色隔离、桌面/移动与清理 PASS |

此前定向风险回归的 generation 编译冲突已修复，generation 单包完整复验 PASS；
最终上述全量覆盖修复后的源码。Windows 无本地 race/容器实跑声明；匹配最终头的
自动 push/PR Linux race/主程序冒烟/容器交付及合并后 master 仍须实际通过。
本地通过尚不能勾选 FND-01 或把候选 v1 标记 frozen。
后续 FND-02 注册与协议隔离、FND-03 状态/配置/生命周期、FND-04 功能迁移及 FND-05
最终 DoD 仍独立验收；完整 S5、A03/A04 产品与真实客户端能力未扩大。
