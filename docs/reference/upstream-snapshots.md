# S5–S7-A 上游参考快照

快照核对日期：2026-10-16；2026-10-17 仅澄清本地副本范围。本文记录当时的阅读版本、抽查代码和参考边界，不声明上游当前版本或安全状态；开发顺序与验收唯一入口为 [PLAN.md](../PLAN.md#后续开发路线)，不维护第二份执行计划。

## 本地目录与固定版本

历史阅读在原工作目录 `D:/my-project/TXAPI/.local/references/` 使用 `git clone --depth 1 --single-branch --no-tags` 准备了三个公开仓库，仅用于源码阅读。下表列建议相对目录与当时 SHA；副本被忽略，不随 Git clone 或新 worktree 分发，当前工作区可没有这些目录。未安装依赖、初始化子模块、运行上游程序、构建或测试，也未将其添加为 TXAPI 子模块或依赖。

| 项目 | 本地目录（相对 TXAPI 根目录） | 本次完整提交 SHA |
|---|---|---|
| [new-api](https://github.com/QuantumNous/new-api) | `.local/references/new-api` | `45fbb149f0c89ff48d805335f22419be392d0350` |
| [sub2api](https://github.com/Wei-Shaw/sub2api) | `.local/references/sub2api` | `f2e5b55f3e8069df1c02a9a27af537b5fdefa64e` |
| [claude-relay-service](https://github.com/Wei-Shaw/claude-relay-service) | `.local/references/claude-relay-service` | `cf95ecc5e0ba846faaf1b11de574367de89fd00c` |

三份工作树在快照核对时无修改，不代表每次文档整理重新检查。TXAPI 的 `.gitignore` 与 `.dockerignore` 已覆盖 `/.local/`，不会将参考库提交或带入镜像构建上下文。保留各自 `.git`、LICENSE 和 NOTICE，便于核对来源；浅克隆不是完整历史归档。

已有目录不自动拉取、覆盖或重置。更新参考前先检查远端 URL、当前 SHA 和工作树；如有改动先确认处理方式。后续采用其他版本时，在对应批次记录新的完整 SHA、安全修复差异和文件范围。不要把会移动的 main 链接作为最终复用证据。

## 已抽查的功能入口

### new-api：Responses 与逐轮 WebSocket

- [controller/responses_websocket.go](https://github.com/QuantumNous/new-api/blob/45fbb149f0c89ff48d805335f22419be392d0350/controller/responses_websocket.go)：每次 `response.create` 重新经过鉴权/限流，隔离请求上下文；参考行为，不照搬私有 Gin engine 或全局组装。
- [relay/responses_websocket.go](https://github.com/QuantumNous/new-api/blob/45fbb149f0c89ff48d805335f22419be392d0350/relay/responses_websocket.go)：连接与调用状态分离、目标绑定、控制事件、写截止时间、错误归属、关闭。
- [controller/responses_websocket_test.go](https://github.com/QuantumNous/new-api/blob/45fbb149f0c89ff48d805335f22419be392d0350/controller/responses_websocket_test.go)：撤销凭据后拒绝新轮次、连接复用、逐轮结算、断开与错误控制事件的测试思路。
- [relay/responses_websocket_test.go](https://github.com/QuantumNous/new-api/blob/45fbb149f0c89ff48d805335f22419be392d0350/relay/responses_websocket_test.go)：消息大小、忙写关闭、事件关联和原始计价参数的测试思路。
- [relay/responses_handler.go](https://github.com/QuantumNous/new-api/blob/45fbb149f0c89ff48d805335f22419be392d0350/relay/responses_handler.go)、[relaykit/README.md](https://github.com/QuantumNous/new-api/blob/45fbb149f0c89ff48d805335f22419be392d0350/relaykit/README.md)：Responses 操作、转换与计量分离。

适用 TXAPI：S6 与 S7-A01/A04。连接复用和会话亲和不构成跨进程会话持久化；该快照 WS 请求规范化会移除 `background`，不能用它证明已经具备可搬用的后台任务系统。

### sub2api：续链绑定、执行作用域与 WS 资源

- [backend/internal/service/openai_ws_state_store.go](https://github.com/Wei-Shaw/sub2api/blob/f2e5b55f3e8069df1c02a9a27af537b5fdefa64e/backend/internal/service/openai_ws_state_store.go)：response 到账号、owner、连接及会话状态的不同映射，TTL 和容量边界。部分映射只在进程内有效，不能原样作为 TXAPI 的持久会话主存储。
- [backend/internal/service/openai_ws_execution_scope.go](https://github.com/Wei-Shaw/sub2api/blob/f2e5b55f3e8069df1c02a9a27af537b5fdefa64e/backend/internal/service/openai_ws_execution_scope.go)：线程、窗口与请求种类区分，避免同一用户的不同工具/辅助请求互相抢占。
- [backend/internal/service/openai_ws_forwarder_ingress.go](https://github.com/Wei-Shaw/sub2api/blob/f2e5b55f3e8069df1c02a9a27af537b5fdefa64e/backend/internal/service/openai_ws_forwarder_ingress.go)、[openai_ws_pool.go](https://github.com/Wei-Shaw/sub2api/blob/f2e5b55f3e8069df1c02a9a27af537b5fdefa64e/backend/internal/service/openai_ws_pool.go)：接入与连接池阅读入口；采用前继续检查具体实现，不将文件存在视为正确性验收。
- [backend/internal/service/openai_previous_response_id.go](https://github.com/Wei-Shaw/sub2api/blob/f2e5b55f3e8069df1c02a9a27af537b5fdefa64e/backend/internal/service/openai_previous_response_id.go)：response/message ID 分类和诊断，不等同于 owner 权限校验。
- [backend/internal/handler/gateway_handler_responses.go](https://github.com/Wei-Shaw/sub2api/blob/f2e5b55f3e8069df1c02a9a27af537b5fdefa64e/backend/internal/handler/gateway_handler_responses.go)、[backend/internal/server/routes/gateway.go](https://github.com/Wei-Shaw/sub2api/blob/f2e5b55f3e8069df1c02a9a27af537b5fdefa64e/backend/internal/server/routes/gateway.go)：Responses 入口、失败处理和子路径保护。

适用 TXAPI：S7-A01/A02/A04/A05。本轮主要抽查上述状态存储、作用域、入口与路由；连接池等列为后续深入阅读点。不得一并引入账号订阅体系、Redis、Ent、整个依赖树，或将其他项目的失效降级规则替代 TXAPI 的严格预算保证。

### claude-relay-service：工具事件转换与断流处理

- [src/services/codexToOpenAI.js](https://github.com/Wei-Shaw/claude-relay-service/blob/cf95ecc5e0ba846faaf1b11de574367de89fd00c/src/services/codexToOpenAI.js)：工具调用标识、参数 delta/done、工具结果和重复输出处理；需要另验并行/交错工具事件，不能直接假设逐个调用的状态模型足够。
- [src/routes/openaiRoutes.js](https://github.com/Wei-Shaw/claude-relay-service/blob/cf95ecc5e0ba846faaf1b11de574367de89fd00c/src/routes/openaiRoutes.js)：Responses/compact 路径、SSE 与断开清理；不采用其中固定系统提示、私人账号端点或与 TXAPI 无关的客户端模拟逻辑。

适用 TXAPI：S6-B 与 S7-A01 的行为样本。不能把请求体出现 `background` 或文件名包含 Responses，当成已有持久任务、取消、事件重放和重启恢复实现。本轮未确认这三个项目中有可直接移植的完整 Conversations 与持久后台任务闭环，相关功能需要按 TXAPI 契约开发和验证。

## 官方协议核对点

核对日期同上；以下是外部协议依据，不是对 TXAPI 当前功能的描述。实施批次开始时重新核对官方 API schema、目标 SDK/客户端版本，并保存最小独立样本。

- [Conversation state](https://developers.openai.com/api/docs/guides/conversation-state)：Conversations 是可跨会话使用的持久对象，`previous_response_id` 用于续链；历史上下文不能因为本轮仅传增量就不计入输入成本。TXAPI 应明确自己与上游各自的状态来源。
- [Background mode](https://developers.openai.com/api/docs/guides/background)：后台任务可轮询和取消；流式恢复使用 `sequence_number` / `starting_after`，只有原先以流式创建的后台响应才可重新开流。当前文档允许后台 `store=false`，但仍会临时存储响应数据，不应宣称零保留。
- [WebSocket mode](https://developers.openai.com/api/docs/guides/websocket-mode)：Responses WS 用 `response.create` 和续链；`stream`、`background` 不是该传输的请求参数。连接缓存不同于持久历史，跨连接续链不能只依赖内存。命名流和取消/控制事件按具体协议版本验证，不套用语音 Realtime 的事件集合。

## 采用方式与许可

本次只下载、阅读和记录来源，没有复制实现到生产代码。

- new-api / RelayKit：核对快照内的 [LICENSE](https://github.com/QuantumNous/new-api/blob/45fbb149f0c89ff48d805335f22419be392d0350/LICENSE) 与 [NOTICE](https://github.com/QuantumNous/new-api/blob/45fbb149f0c89ff48d805335f22419be392d0350/NOTICE)，不能把受 AGPLv3 及附加声明约束的代码改名后标成 MIT 原创。
- sub2api：核对快照内的 [LICENSE](https://github.com/Wei-Shaw/sub2api/blob/f2e5b55f3e8069df1c02a9a27af537b5fdefa64e/LICENSE)、README 声明及具体文件来源；根 LICENSE 为 LGPLv3，不据此自动推断任何移植/静态链接方式都可保持现有发布策略。
- CRS：根 [LICENSE](https://github.com/Wei-Shaw/claude-relay-service/blob/cf95ecc5e0ba846faaf1b11de574367de89fd00c/LICENSE) 为 MIT；移植仍保留适用通知、核对第三方来源，并测试风险边界。

优先复用可描述的功能逻辑、状态机和失败场景，由 TXAPI 现有模块承接；确需复制、改写实现或引入依赖时，按 [协作约定](../COLLABORATION.md#后续批次与上游借鉴) 记录具体范围和许可处理。改变许可或依赖/部署承诺先单独评审，不通过混入源码规避确认。


## 2026-10-17 本轮采用记录

固定 SHA 不变。本轮读取原工作目录 `D:/my-project/TXAPI/.local/references/` 的 RelayKit README、sub2api execution_scope 和 CRS codexToOpenAI 源文件，参考能力拒绝、每次生成的独立作用域及工具事件关联。TXAPI 落点为 gateway/coordinator、protocol/operation、tools 行为回归与独立 protocol/responses；没有复制/改写上游实现，无新增依赖或许可变更。

外部协议另核对 [Anthropic streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)：工具起始数据位于 content_block，后续参数位于 delta.partial_json。测试使用独立最小样本覆盖真实字段、多个调用和交错分片，不沿用旧解析器的字段错误。OpenAI 原生 Responses 以原始请求/JSON/SSE 保留 function_call 等操作结构，不套 Chat DTO；该历史批次当时仅提供协议样本，不是生产路由或持久/后台/WS 功能。后续 A01 已独立交付并验收前台 HTTP/SSE，官方依据与范围见 [Responses 契约](responses-http.md)，不改变本页固定参考源码的阅读/许可记录。Gemini signed tool history 尚未无损建模，真实能力过滤保守拒绝该组合。
