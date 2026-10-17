# 输入边界档案

档案把可复核的接口约束落实为启动期和发送前检查。它不是 tokenizer，也不是所有厂商或模型的通用认证。输入边界与上游价格、用户售价分别管理。

## 执行门槛

2026-10-17 收口：strict 新尝试必须选择已登记的档案，并交给 `StartPreparedAttempt` 检查真实 URL/正文。缺档案返回 `provider_input_unproven`；配置和旧账本仍可读取，但非空 `evidence` 不再构成执行许可。无最终正文的 `StartCostedAttempt` 已弃用，不能发起新尝试。正文解析得到的输出字段与数值必须同成本核算使用的 `StrictRequest` 完全一致，防止少占预算而发送更大上限。

无执行的拒绝释放双预算且保留原因；已有未知 attempt 的请求继续 pending，不清除既有 provider 占用。新原因可通过原管理员账本查询，崩溃恢复保持该原因。当前没有 Anthropic/Gemini 或兼容代理的已登记档案，因此这些目标不能在 strict 下执行；legacy/disabled 不受此门槛影响。

## openai_embeddings_v1

来源核对日期：2026-10-17。

- [OpenAI Create embeddings](https://developers.openai.com/api/reference/resources/embeddings/methods/create)：整次请求输入最多 300,000 Token，单输入最多 8,192 Token；数组限制为 2,048。
- [OpenAI Embeddings guide](https://developers.openai.com/api/docs/guides/embeddings)：当前列出的三个模型系列及其接口用法。本档案只登记 `text-embedding-3-small`、`text-embedding-3-large`、`text-embedding-ada-002`，不按名称前缀接受未知模型。

实现约束：

1. 只能显式选择；协议为 `openai`，操作为 `embeddings`，base URL 精确为 `https://api.openai.com`。兼容代理、Azure、别名或新模型需要独立核对，不自动继承此档案。
2. `input_token_ceiling` 至少为 `300000`。不能把单输入的 8192 或批次中某一条输入的限制当作整请求上限。更大的配置值允许，但会增加保守占用。
3. 发送前核对最终 URL 精确为 `https://api.openai.com/v1/embeddings`，正文模型与固定契约一致，正文通过文本计价白名单。文本批次最多 2048 条；仍不接受客户端 token ID 数组或混合输入。
4. 无论正文多短，按配置的整个输入上限计算 provider 包络及售价覆盖。没有按字符估算实际 Token，也没有先按低估值执行再补占用。单输入是否超过 Token 限制仍由厂商检查；本地不宣称已经做了精确计数。
5. 开始 attempt 前检查真实执行输入；旧的无最终正文 `StartCostedAttempt` 入口无法开启此档案的 attempt。快照继续使用 format=2，并将档案名保存在 contract 中；已有无档案快照保持可读，不回填虚构证明。

保证边界：该来源支持接口约束，不认证运营填写的独立最高价格、币种、凭据实际费率或失败请求收费策略。`evidence` 仍需记录这些假设；请求失败/执行未知时继续保留占用。默认 HTTP 执行器已有禁止跟随重定向行为；自定义 client/transport 仍须遵守执行端口的信任约定。没有运行真实厂商请求，也不能承诺厂商违约时绝对无损。

未指定档案的旧成本契约只保留加载/历史语义，不再获准执行。下列 Chat 档案覆盖一个受约束组合；其他 Chat 模型及厂商仍是 S5-C 的待验收项，本页不改变 S5 阶段勾选。


## openai_chat_v1

来源核对日期：2026-10-17，使用 OpenAI Docs，不从参考项目的默认模型名/费率推断保证。

- [GPT-4o Mini](https://developers.openai.com/api/docs/models/gpt-4o-mini)：日期快照 `gpt-4o-mini-2024-07-18`，128000 上下文和 16384 最大输出；文档标准输入/缓存输入/输出分别为 USD 0.15/0.075/0.60 每百万 Token。
- [Chat Completions](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)：`max_completion_tokens` 为含可见输出和推理 Token 的生成总限制，不能与其他协议的旧 max_tokens 随意互换。
- [Fast mode](https://developers.openai.com/api/docs/guides/priority-processing)：项目级默认可改变未指定 service_tier 的请求等级，不能假定缺省永远采用标准费率；此档案要求客户端显式 `service_tier: "default"`，不由网关静默覆盖。

约束：仅官方根 URL、openai/chat、上述精确日期模型。配置输入上界至少 128000，配置最大输出至多 16384，输出字段必须是 max_completion_tokens。发送前再次核对 URL 精确为 `/v1/chat/completions`、正文模型、显式标准等级、输出限制与严格文本白名单。每次请求仍按完整配置输入包络占用，不把文字数估算当 Token 证明，不把缓存折扣从最坏占用扣除。tools、media、显式缓存/推理扩展和非标准服务等级继续拒绝。

这只是输入/输出档案，不是完整价格认证。上列 USD 来源方便复核，不会静默写入用户售价或将本系统货币单位自动视为 USD；独立 provider 最高费率、同渠道不同凭据、税/附加费和失败请求收费假设仍需运营核实。无档案契约现已拒绝执行，不能拿这条档案的证据替其他目标背书；S5-03 与完整 S5 保持未验收。
