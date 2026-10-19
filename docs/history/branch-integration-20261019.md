# 2026-10-19 保留分支整合

## 批次范围与六项边界

用户在分支清理后明确要求检查并整合保留分支到主线。仓库实际主分支为 master；本批以 `master/290ba5d` 为集成基线，在 `integration/preserved-branches-20261019` 工作，合并前再次核对远端和精确头 CI。

1. 所属层级与目录：R06c 的既有控制台展示/回归；R04a 的 L0 机械 HTTP 原语、L1 候选上游授权契约/协调与存储、L2 纯协议/回调、main 命令组装；相关指南、来源与验收记录。
2. 依赖接口：保留全部原冻结公开签名；R04a 只增加具名方案中的 `core.RawInput/RawResult/ExecuteRaw` 与候选 `upstreamauth/v1`。账本页面继续消费冻结 ledger/v1 和原管理员 API。
3. 地基影响：沿用 [R04a 具体变更方案](../../REFERENCE-R04A-IMPLEMENTATION.md) 中对现有 JSON/model 执行入口不足的说明，机械 form/raw HTTP 仍由原 core.Executor/client 类型持有；不在协议层或 L3 新建网络/数据库/授权/账本。新候选契约登记签名不代表冻结。
4. 契约与迁移：账本字段、路由和财务语义保持；托管账号独立加密、operator-only，追加 schema v17。OpenAPI 仍为 0.11.0 / 111 操作，账号不自动成为可推理凭据，未知刷新结果不重试。
5. 验证：先定向检查回调/身份/加密/迁移/CAS/不确定状态和真实命令消费，稳定后集中 build/vet、全量普通测试、Node/浏览器、主程序冒烟、文档及最终精确头自动完整 CI。真实账号/推理未授权验收，不由模拟替代。
6. 并行影响：本批单点处理 main、迁移、公开快照与前端；原工作树文件/真实 index 保持，原始内容先用临时 index 建立本地恢复标签。不会把旧文档删除、旧 schema 或旧报价覆盖到最新主线。

## 逐分支结论

| 原分支或工作树 | 具体核对 | 整合处理 |
|---|---|---|
| archive/s5-pricing-wip-20261019 / 60a7d25 | 14 个非 Markdown 文件 blob 在主线历史精确存在；原快照涉及的 30 个测试名在当前各包全部保留；其余入口/类型/限制已随后续架构演进 | 沿用最新主线的 strict/成本/转换与测试，不回退到旧 Gin/版本/迁移；原工作树保存在 s5 标签 |
| archive/reference-r05a-quotation-20261019 / 2c42b11 | 与已合并 eaba025 的 stable patch-id 同为 `91bcbf5d6f56316e23371e8a65c71305a4be3f49`，18 个非 Markdown blob 均在主线历史 | 完整报价补丁已吸收，保留后续 f770c56/f794dfa 等修复，原保存点转为标签 |
| archive/reference-r06-ledger-20261019 / 28fc3ec | 实际新增账本最终收费、当前占用、历史预留及 provider 包络分开显示；精确金额/未知/零/分页/角色回归 | 迁入当前前端，保留新任务/路由页面；文档冲突保留当前活动计划并追加原历史记录；用户本轮要求恢复此暂缓批次 |
| docs/fc002-background-freeze / 5506 工作树 | 分支头已合并；工作树中的路由/指标/选择器核心 blob 已精确存在于主线历史；其余源差异为删除文档引用、将 OpenAPI 移到 api/ 和旧记录 | 功能沿用已合并 R00 与后续修复；保留当前 docs/openapi.json、开发约束和文档，不重放旧删除/路径迁移；整个原状态保存在 5506-worktree 标签 |
| feat/reference-r04-account-tool / R04 工作树 | 原分支头 bdcb307 已在主线，实际新源码是 31 项未提交差异 | 从固定快照迁入 R04a，加真实命令消费回归、公开快照、来源通知及运行/迁移说明，集中验证后随本批提交 |
| fix/reference-background-null / bb5a006 | 提交已在主线，原工作树干净 | 继续保留 PR #39 的 JSON null 与 WS 终态边界修复，不重复应用 |

内容整合完成后，以最新、已验证文件树记录旧分支历史的解决结果。旧代码、文档删除或失效接口的解决选择以上表为准；不以只登记 merge 代替实际源码整合。完整 S5、R04 账号池/真实 Provider、收费后台/WS、持久重连和外部 Runtime 仍未完成。

## 本地恢复点

所有标签位于本地 `archive/pre-integration-20261019/` 命名空间，不推送用户的原始 WIP。可用 `git show <tag>` 查看，或 `git worktree add <新目录> <tag>` 恢复独立 checkout；原始工作树和个人忽略文件不被清空。

| 标签后缀 | 固定提交 |
|---|---|
| s5 | 60a7d257cc892394d1c5612772b17a50956a08a0 |
| quotation | 2c42b1108c284154c4999ff13360fdead6197918 |
| ledger-ui | 28fc3ec40bf1c55f28eb75ff3f45922c54962de1 |
| background-null | bb5a00630accd7127f87c2464841b4aad88633b2 |
| 5506-worktree | 992f95a8b773d41c75f3736474d91ab486f966a0 |
| r04-worktree | 8026ddacde6b9bcdf5e19202efb66a28d3127b2b |

## 验证记录

提交前的检查点逐项追加于此；本地通过、最终头 CI、合并及合并后 CI 分别记录。环境 Windows/PowerShell，Go 命令显式 `GOARCH=amd64`；所有授权/存储用例使用临时数据、假令牌与模拟 token/JWKS，真实 loopback 回调使用随机端口。

- 初轮定向 `go test -count=1`：core、upstreamidentity、protocol/openaiauth、compat/oauthloopback、foundation/upstreamhttp、store 和 cmd/gateway 全通过。
- `go test -count=1 ./cmd/gateway -run TestUpstreamAccountCommand` 通过：真实 callback → form exchange → 签名身份 → SQLite/AEAD → 单次刷新 → 不确定结果禁止复用 → 重新授权 → 停用；模拟 4 次 token POST、2 次 JWKS GET，无真实 Provider 请求。
- 公开签名导出比较：旧签名全部保持，只追加 3 个 raw HTTP 声明与 1 组候选 upstreamauth/v1；第一次导出因相对输出目录失败，改为绝对临时路径后通过，未因此放宽类型可见性检查。
- `node --test web/assets/api.test.mjs` 12 项通过。账本浏览器回归初次默认查找的 Chromium 版本不匹配；指定本机已有 Chromium 1248 后，桌面/手机、未知/零/waiver/max-int64、Key 筛选、失败重试与确认矩阵通过，无依赖下载。
- 原始源码快照与真实工作树/index 状态一致；临时校验脚本、日志、个人配置、密钥、数据库及二进制不提交。

### 集中本地验收

- `GOARCH=amd64 go build ./...`、`go vet ./...`、`go mod tidy` 与 go.mod/go.sum 无差异检查通过，无新依赖。
- `go test -json -count=1 ./...` 的 44 个有测试包通过，唯一失败是架构快照的 JSON 转义格式：导出的公开声明与记录语义完全一致，但 Python 序列化未保留 Go 的标准 HTML 转义。恢复 Go 生成器的原始字节后，`go test -json -count=1 ./internal/architecture` 全包复验通过；未放宽检查或修改公开声明。复用同一源码/基线/环境中其余包的结果，最终去重结果为 45 个测试包、1875 个测试/子用例 PASS，7 个预期 helper SKIP，39 个包无测试。
- `GOARCH=amd64 python -B tests/smoke/local.py --go go` 通过：实际主程序的旧 API/鉴权/控制台、strict 拒绝、结算/对账/强杀、密钥/持久 runtime、后台/SSE 恢复、用量与 shadow 行为均保持。
- 新命令消费者、12 项 Node API/金额用例与账本桌面/手机浏览器回归通过；受控命令只调用模拟 token/JWKS 和真实 loopback，无真实 OAuth/推理请求。
- 仅格式化本批 29 个 Go 文件。66 份 Markdown / 565 个本地链接锚点、交付脚本 Python AST、3 项变更 JS 语法和 `git diff --check` 通过。OpenAPI/go.mod/go.sum 不变；全部旧公开声明保留，原 S5 快照中的 30 个测试名在当前包均存在。
- 主线集成基线重新在线核对为 `290ba5d`，其合并后 Verify `39240068432` 全部成功，仅作为原基线证据。本批最终提交的自动 push/PR Linux 全量 race、冒烟及容器交付仍须独立通过后才合并；不手动 dispatch 或借用旧绿灯。
