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

本地集中验证完成；功能提交、push/PR CI、合并与合并后 CI 将按精确 SHA 更新，不提前标记已验收。

## R01：执行事实与单次协议交付

具体新增签名及范围由 `REFERENCE-R01-CHANGE-REQUEST.md` 定义，用户已于 2026-10-18 明确回复“批准 R01 具体变更”。
实施内容限追加 execution/v2、L1 decoder 桥、原生 Responses 真实消费者与独立 L3 样例；保留 v1 与 L0 签名。
当前尚未实施或验收，不因提案批准标记完成。

## 后续批次

R02 资源代次、R03 原生 WS、R04 账号授权、R05 计量/收费后台、R06 管理产品、R07 外部 Runtime 仍按方案依赖逐批推进。
OAuth 真实账号证据、Provider 成本证明及外部扩展的独立发布需求各有进入条件；模拟通过不替代真实验收。
