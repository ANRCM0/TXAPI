# 上游账号授权工具

本批将保留工作树中的 R04a 源码整合为本地运维命令。支持 OpenAI 动态注册、重新授权、读取账号元数据、单次刷新和停用；账号尚不参与模型路由、推理、计费或自动刷新。`upstreamauth/v1` 是已登记的候选接口，尚未冻结。完整 R04、真实账号认证及收费均未因此验收。

实施范围及来源见 [R04a 决策](../../REFERENCE-R04A-IMPLEMENTATION.md)，本次各分支取舍和实际验证见 [整合记录](../history/branch-integration-20261019.md)。官方协议依据为 [Registration and sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)，2026-10-19 本批重新读取核对。受控回归使用真实 loopback 回调、签名 JWT、临时 SQLite 和模拟 token/JWKS 传输，不访问真实账号或推理服务。

## 配置与命令

沿用现有 `-config` 和 [凭据加密配置](secret-storage.md)。授权、重新授权与刷新要求实际 store 凭据加密就绪，缺少/错误密钥拒绝操作；元数据列表不暴露任何令牌。配置、密钥和数据库不提交。

```powershell
go build -o bin/gateway.exe ./cmd/gateway
./bin/gateway.exe -config config.yaml -upstream-account authorize -upstream-account-name "My account"
./bin/gateway.exe -config config.yaml -upstream-account list
```

授权命令先启动 `127.0.0.1` 上的随机端口回调监听，再向标准输出写入一行 `authorization_url`。在同一机器的浏览器手动打开该地址；回调完成后命令写入验证后的账号元数据并退出。授权窗口十分钟，命令总上限十一分钟；中断会关闭并等待监听器退出。不会自动启动浏览器或通过 URL 接收调用 Key。

后续操作使用列表返回的账号 ID 与精确十进制 revision；revision 是 JSON 字符串。以下值为占位符，不能直接执行成功：

```powershell
./bin/gateway.exe -config config.yaml -upstream-account refresh -upstream-account-id <account-id> -upstream-account-revision <revision>
./bin/gateway.exe -config config.yaml -upstream-account reauthorize -upstream-account-id <account-id> -upstream-account-revision <revision>
./bin/gateway.exe -config config.yaml -upstream-account disable -upstream-account-id <account-id> -upstream-account-revision <revision>
```

列表支持 `-upstream-account-limit`（1–100，默认 50）与 `-upstream-account-offset`（0–10000）。同一命令不能与管理员初始化、种子导入或旧 Key 认领组合。输出失败不自动重试财务或授权操作；先重新读取元数据确认结果。

## 身份与刷新边界

首次注册使用 `dynamic_agent_client`，持久化同一主机的随机 UUID；code exchange 与后续操作使用回调签发的 client ID。每次生成独立 state、nonce、S256 PKCE 和精确 loopback URI；重复参数、错误 state、账号/签发 client 切换、未验证或过期身份均拒绝。重新授权目前不发送 `id_token_hint`，可显示账号选择器，最终仍必须匹配已保存的已验证身份。

ID token 按官方 JWKS 验证 RS256 签名、issuer、audience、subject、时间、nonce 和存在时的 access-token hash。权限范围只来自 token 回复；有效身份不自动授予推理能力。token、nonce 及注册绑定通过现有 AEAD 加密，元数据与密文相互校验；读取和普通日志不输出令牌。

刷新采用 claim → 持久 started → 一次网络交换 → 条件提交。网络不在数据库事务内运行。停用或重新授权推进 revision/fence，旧回复不能覆盖；成功时原子替换令牌、到期与 scopes。只有完全有效成功回复省略 refresh token/ID token/scopes 时，才保留相应旧值。

只对已证明没有发出的尝试设置有界持久退避；部分 I/O、started 超时、成功回复未存下或其他可能发生令牌轮换的结果保持 uncertain，禁止重用旧 refresh token，需重新授权。明确拒绝标记 reauthorize。短时间重复刷新也可能被五秒冷却拒绝，应读取最新元数据，不能循环重试来绕过状态。

## 容量、存储与升级

最多 1000 个托管账号、4 个并发刷新领取，领取租约 90 秒；HTTP 调用限时且有正文/响应上限，无重定向、cookie jar 或部署级 endpoint 覆盖。不存在后台刷新 worker、账号池调度、服务端浏览器登录、device flow、token exchange grant 或秘密恢复日志。

统一迁移追加 schema v17，托管账号与静态上游 Key 分开存储，原账本与静态凭据流程保持。升级前停写并做一致备份；旧 schema16 二进制必须拒绝打开 v17 数据库。回退二进制需要恢复对应的停止写入备份，不删除账号或改写迁移历史。留存账号启动时必须有正确密钥，轮换沿用现有全部凭据所有者的原子启用流程。
