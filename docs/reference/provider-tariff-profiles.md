# 上游费率档案与凭据绑定

核对日期：2026-10-17。此档案与[输入边界档案](input-bound-profiles.md)分开：输入档案约束允许计量的请求，费率档案约束独立 provider 包络的币种、最低保守单价和适用凭据。它不是用户售价、自动汇率换算或完整账单保证。

## openai_standard_usd_2026_10_17

本批实际读取 [OpenAI Docs Pricing](https://developers.openai.com/api/docs/pricing.md) 的 Standard 表，而不是 Batch、Flex、Fast 或微调表。只登记现有输入档案允许的官方目标；Chat 请求继续要求显式 `service_tier: "default"`，embeddings 不增加不存在的服务等级参数。

| 目标模型 | 输入下限，USD / 1K Token | 输出下限，USD / 1K Token |
|---|---|---|
| `gpt-4o-mini-2024-07-18` | `0.00015` | `0.0006` |
| `text-embedding-3-small` | `0.00002` | `0`（无输出 Token 计费） |
| `text-embedding-3-large` | `0.00013` | `0`（无输出 Token 计费） |
| `text-embedding-ada-002` | `0.0001` | `0`（无输出 Token 计费） |

原表为每百万 Token，表内数值已除以 1000。GPT-4o Mini 的日期模型沿用[已核对模型来源](input-bound-profiles.md#openai_chat_v1)，不按名称前缀接受其他模型。缓存输入折扣不能用来降低最坏情况输入单价；更高的显式包络价允许，仍使用定点整数与向上舍入计算。低于下限、未知档案/币种或输入/费率目标不匹配时拒绝启动，不能用“自定义折扣”跳过此门槛。

配置要求：`tariff_profile` 为上列档案名，`currency: USD`，且必须有 `credential_bindings`。当前唯一可执行费率档案是 USD，因此 `max_provider_request_cost` 与新 attempt 的 provider 金额都是 USD、精度 `1e-9`；用户 `max_request_cost`/售价沿用独立账户计价单位，不进行二者之间的汇率换算或大小比较。旧快照仍按原记录读取，不能回填或展示为已经认证的 USD 历史成本。

## 凭据与最终请求头

`credential_bindings` 是凭据 ID 到完整上游 Key 的小写 SHA-256 摘要的映射；不放真实 Key、不用页面掩码或调用方 Key。ID 使用正十进制字符串（不带前导零）。本地生成摘要时可用 Python 的隐藏输入，不把 Key 放进命令参数或 shell 历史：

```bash
python -c "import getpass,hashlib; print(hashlib.sha256(getpass.getpass('Upstream key: ').encode()).hexdigest())"
```

将摘要与管理员查询到的凭据 ID 填入被 Git 忽略的运行配置，格式见[配置样例](../../configs/gateway.example.yaml)。不要把真实配置或摘要清单提交到仓库。摘要不是上游 Key 的加密存储，也不能证明其所属账户的价格已经得到厂商认证。

每次尝试检查实际准备发送的 `Authorization: Bearer <key>` 与选中凭据 ID：两者都须匹配已固定的绑定。检查最终 Header 而非仅检查 Meta，防止适配器改用另一把 Key；缺鉴权、多个鉴权值、大小写重复键、额外项目/组织选择头及未登记头均拒绝。当前允许的准备头只有 Authorization、Content-Type、Accept；executor 后续的标准传输头不属于用户选择上游计费账户的入口。可信自定义 transport/client 仍须遵守执行端口约定，不能在校验后替换目标、鉴权或正文。

新增凭据、原 ID 换 Key、环境覆盖 Key 后，旧证明不自动继承；必须重新核实该凭据的费率适用范围、更新绑定并重启加载。修改权重或健康状态不改变 Key 摘要，不要求因健康冷却重复认证。启动编译复制绑定 map；调用方之后修改原配置不能热改本次策略。快照继续使用 format=2，将档案、币种和摘要绑定纳入同一 SHA-256；只保存摘要、不保存鉴权头或真实 Key，现有管理员快照展开即可查询。

## 拒绝、恢复与保证边界

旧契约可继续加载，但无费率档案的新尝试返回 503 `provider_tariff_unproven`；凭据或实际 Header 不匹配返回 503 `provider_credential_unproven`。两者均先写既有 reservation reason，再停止重试，不发网络、不新增 durable attempt、不反馈健康。未执行请求释放双 hold；已有未知执行的请求继续保留完整 hold/provider 包络及 `execution_or_usage_unknown:<原因>`，恢复不改写原快照或重新计价。

本档案只将**本次核对的标准模型 Token 费率**变成可执行下限检查，不是“任意配置值都可信”，也不是厂商最高发票金额的证明。运营仍须逐凭据核实适用账户/合同，是否存在额外附加费；未覆盖的地域/FedRAMP、其他服务等级、第三方代理和新计费维度不能继承此档案。税、充值/支付费用及汇兑不纳入模型 Token 包络，不应混称为“全部成本”。

本批来源没有建立所有失败/拒绝请求的可计费 Token 硬上限，也没有验证真实凭据账单；继续保留未知执行成本、不因 HTTP 错误假设免费。带日期的费率档案是人工核对快照，不自动在线刷新或保证未来不涨价；部署方必须跟踪提供商变更，变化后停止使用过时许可并更新档案/配置。因此 S5-03 和完整 S5-C 仍未验收，不将 fake provider 回归或 CI 通过当成真实厂商计价认证。
