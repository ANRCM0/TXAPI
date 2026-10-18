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

已验收并合并：功能头 `ab0fe952abc3ff3a8dc4776a9ffb78c9e1e5bafa`，PR #24，合并后 `master/231af3a411246b96c0f4c3a948ea5cad35d868df`。功能头与合并树一致。

| 精确来源 | 自动运行 | 结果 |
|---|---|---|
| 功能头 push | `39140219794` | check/delivery SUCCESS |
| PR #24 | `39140241563` | check/delivery SUCCESS |
| 合并后 master/231af3a | `39140726076` | check/delivery SUCCESS |

均含现有 Linux 全量 race、主程序恢复冒烟和容器安装/升级/恢复；没有手动重复触发 workflow。未制作 release 或发布新标签。

## R01：执行事实与单次协议交付

具体新增签名及范围由 `REFERENCE-R01-CHANGE-REQUEST.md` 定义，用户已于 2026-10-18 明确回复“批准 R01 具体变更”。
实施内容限追加 execution/v2、L1 decoder 桥、原生 Responses 真实消费者与独立 L3 样例；保留 v1 与 L0 签名。
已验收并合并：功能头 `a66a0706034c3bb86d60742931b7fcc8182e7aef`，PR #25，合并后 `master/5938c055ab69097adeab460919b4e5c21410939c`。功能头基于 `master/231af3a`，新增签名快照只增加 execution/v2 的 16 个条目，原有所有冻结条目逐项相同；L0 与 v1 签名不变。

真实 Responses HTTP/SSE 消费者通过显式 v2 decoder 能力使用新桥；独立 L3 facts 样例通过真实 SQLite 身份、当前 Key 撤销、真实 gateway 与假上游证明接入。用量完整性、事实来源、原始表示和宿主派发/交付水位分别保存。

兼容细节：v1 partial usage 仍投影为 nil；新 v2 路径按已批准合同校验每个已知计数，所以负数的 partial usage 会拒绝。合法部分用量与缺失用量继续可交付，不补零、不开放新的 strict 组合。

首轮集中 vet 发现新增测试有跨包未命名字段字面量，已修为命名字段。代码复核补充了传输 panic 的保守 Dispatching 记录与回归；SSE 部分写/Flush 失败保留 Native 终态，但 ProtocolComplete 仍由原 core 的 End 验证决定，不伪造客户端已消费或协议已完整收尾。

### R01 最终本地来源与检查

基线：已验收 `master/231af3a411246b96c0f4c3a948ea5cad35d868df`。环境同 R00：Windows/PowerShell，Go 1.26.3，GOARCH=amd64、CGO_ENABLED=0。
13 个代码/架构来源文件的归一化 LF 清单摘要为 `37a5acd84904bf88799c8e2ed97709eaa8926f097946c34b9f308665cfaf93dd`；清单和原始日志在仓库外 `D:\my-project\TXAPI-references\validation-r01`。

| 实际命令/检查 | 最终结果 |
|---|---|
| `go build ./...`、`go vet ./...` | PASS |
| `go test -json -count=1 ./...` | 1260 项测试/子用例、31 个测试包 PASS，6 个预期 helper SKIP；另 31 包无测试 |
| `go mod tidy` + go.mod/go.sum 无差异 | PASS |
| `go test -count=1 ./internal/architecture` | PASS；包含依赖、公开类型及原签名保持检查 |
| `gofmt -l` 本批 11 个 Go 文件、`git diff --check` | PASS；只格式化本次修改文件 |
| `python -B tests/smoke/local.py --go go` | 最终代码 PASS，包含真实 main 的 HTTP/SSE、strict/密钥/恢复、后台 SSE 强杀与续传 |
| 前端语法与 API 回归 | 复用 R00/ab0fe95 的五脚本语法与 10/10 API 测试；相应脚本、HTTP 路由、前端接口和 v1 输出签名逐字未变，真实主程序冒烟另已复验 |
| 新增方案/变更请求/记录本地链接 | 全部目标存在 |

`go test ./internal/architecture -run TestVersionedPublicSignaturesHideImplementationTypes -record-foundation-api <仓库外候选文件>` 只用于导出候选快照；先逐项确认所有旧公开声明不变，仅新增批准的 execution/v2 条目后才复制进记录。初次架构检查发现版本路径白名单未登记 execution/v2，已仅追加该已批准入口，原越层/类型检查保持。

本地未运行 Linux race、容器 delivery 或真实 Provider。R01 功能头 push `39141714771`、PR `39141745597`、合并后 master/5938c05 的 `39142273005` 均 check/delivery SUCCESS，覆盖 Linux 全量 race 与容器交付；真实 Provider 仍未运行。

## R02/R03a：运行代次与 WS 机械执行

R03a 范围及修订签名已获用户批准。候选实现已接入本地生产包：L0 `internal/core/websocket` 只处理有界读写/取消/部分写，L1 gateway 映射 execution/v2，L2 transport 仍保留连接容量/消息/期限边界；生产 OpenAI WS 路由尚未注册。RuntimeLeaseSet 已覆盖发布、获取、退休和并发释放，真实 WS 产品消费者仍是下一步 R03b。

R03a 本地最终检查（基线 `master/5938c055ab69097adeab460919b4e5c21410939c`，Windows/PowerShell，Go 1.26.3，GOARCH=amd64）：`go build ./...`、`go vet ./...`、`go mod tidy`、`go test -count=1 ./...`、架构/公开签名检查、`git diff --check`、真实主程序恢复冒烟均通过；1267 项测试/子用例 PASS，6 个预期 helper SKIP，32 个测试包 PASS。新增代码清单摘要为 `cfda046bea954af44345032bde8e5b17131bff4d99f25f240384e873a57a42c6`，原始日志和清单在 `D:\my-project\TXAPI-references\validation-r03a`。本地未运行 Linux race、容器 delivery 或真实 Provider。

R03a 新增执行快照只增加 execution/v2 WS 成员，并登记 L0 `core/websocket`；既有 v1/v2 HTTP 事实、L0 HTTP、OpenAPI、schema 和计费行为保持。一次架构快照检查发现新 L0 包缺显式 Version，已补登记并重新通过；一次 lease 溢出测试误把无引用 current 当作退休占用，已改为同时持有两代 lease 后验证拒绝。

## 后续批次

R02 资源代次、R03 原生 WS、R04 账号授权、R05 计量/收费后台、R06 管理产品、R07 外部 Runtime 仍按方案依赖逐批推进。
OAuth 真实账号证据、Provider 成本证明及外部扩展的独立发布需求各有进入条件；模拟通过不替代真实验收。
