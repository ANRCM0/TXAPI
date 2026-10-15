# 开源 LLM 网关调研：New API、OmniRoute、sub2api 优点汇总

> 调研日期：2026-10-15
> 目的：为自研 api-gateway 项目提供设计参考和借鉴

---

## 一、项目概览

| 维度 | New API | OmniRoute |
|------|---------|-----------|
| **GitHub** | QuantumNous/new-api | diegosouzapw/OmniRoute |
| **Stars** | ~50k | ~76k |
| **语言** | Go (Gin + GORM) | TypeScript (Node.js) |
| **代码量** | ~162K 行 Go | ~157K 行 TS |
| **定位** | 企业级多租户 AI 网关 | 统一 AI 路由（358 供应商） |
| **许可证** | AGPL-3.0 | MIT |
| **数据库** | SQLite/MySQL/PostgreSQL/ClickHouse | SQLite/Redis |
| **前端** | React + Bun 完整控制台 | Next.js + React |

---

## 二、New API 核心优点

### 2.1 Adaptor 策略模式 + 注册表

**设计**：所有上游渠道实现统一的 `Adaptor` 接口，通过 `GetAdaptor(apiType)` 获取实例。

**优势**：
- 新增渠道只需实现接口 + 注册，主流程零改动
- 支持 40+ 上游渠道（OpenAI、Claude、Gemini、阿里、百度、火山、智谱等）
- 异步任务（TaskAdaptor）支持三段式计费：预估→提交结算→完成补差

**可借鉴**：用接口 + 注册表替代 switch-case，是构建可扩展渠道生态的典范。

---

### 2.2 预扣费 + 失败退款 + 可重放 Body

**设计**：转发前预扣额度，失败时 defer 退款，成功时结算差额。请求体用 `io.NopCloser` 包装支持重试时重复读取。

**优势**：
- 计费与重试协同，异常不吞额度
- `RefundFailedRequestBilling` 保证 panic 也能退款
- `NewReplayableBodyReader` 解决 body 只能读一次的问题

**可借鉴**：预扣→结算模式比事后扣费更安全，可重放 body 是网关必备。

---

### 2.3 协议转换注册表（relaykit）

**设计**：独立的协议转换库，用 `RequestConverterSpec` 声明 `From/To/Quality/Convert`，注册时强校验。

**优势**：
- 支持 OpenAI Chat/Responses、Claude Messages、Gemini 任意互转
- 转换质量分级：good / fair / discouraged
- 多步链式转换：A→B→C
- golden test 回归保障正确性
- 工具调用编解码（toolconv）处理跨协议工具定义

**可借鉴**：将协议转换抽成独立模块，带质量标注和回归测试，是网关核心竞争力的关键。

---

### 2.4 分级鉴权 + 审计内聚

**设计**：`authHelper(minRole)` 统一鉴权，`UserAuth/AdminAuth/RootAuth` 分级。

**优势**：
- 审计作为鉴权中间件的副作用，写接口自动挂审计，杜绝遗漏
- `TryUserAuth` 做可选鉴权，token 越权降级为匿名
- 请求 ID 贯穿日志与错误返回

**可借鉴**：鉴权中间件内聚审计，是安全设计的干净实现。

---

### 2.5 渠道三级索引缓存

**设计**：`group2model2channels map[group]map[model][]channelId` 三级索引 + RWMutex 保护。

**优势**：
- O(1) 路由匹配
- 内存缓存加速渠道选择
- 支持通配符模型匹配

**可借鉴**：路由匹配用内存索引而非每次查库，是高性能网关的必备优化。

---

### 2.6 主库/日志库分离

**设计**：`DB` / `LOG_DB` 分离，日志库可用 ClickHouse。

**优势**：
- 主库写入压力小
- 日志高写入场景用 ClickHouse 更合适
- 复合索引设计精细（`idx_created_at_id`、`idx_user_id_id`）

**可借鉴**：读写分离对高写入日志场景很实用。

---

### 2.7 安全细节

**设计**：
- LIKE 注入防护：`sanitizeLikePattern` 转义 `!`/`_`、限制 `%` 数量
- 日志分级脱敏：`FormatAdminLogs` / `FormatRootLogs` 按角色返回不同详细程度
- Token 脱敏：`MaskTokenKey`
- 状态码映射：非 200 时应用渠道级状态码映射

**可借鉴**：LIKE 注入防护和日志分级脱敏是安全基本功。

---

## 三、OmniRoute 核心优点

### 3.1 配额共享引擎（Fair-Share）

**设计**：纯函数 `decideFairShare`，按权重分配百分比，支持多维度（percent/tokens/usd/requests）× 多窗口（5h/weekly/monthly/hourly/daily）。

**优势**：
- **双模式 work-conserving**：generous（<50% 饱和阈值）允许借用未分配池，strict（>50%）严格按份额截断
- **三档策略**：hard（到份额即拦截）/ soft（超份额仅降优先级）/ burst（有全局余量就放行）
- **Fail-open on infra, fail-closed on corrupt data**：基础设施错误放行，损坏 policy 数据强制按最严格处理
- **Fail-safe 归一化**：未知 policy 强制当最严格的 hard 处理
- **饱和信号**：从上游真实利用率取信号（OAuth usage、rate-limit header），30s TTL 缓存 + in-flight 去重
- **可插拔存储**：SQLite / Redis 双 driver，Redis 不可用自动降级

**可借鉴**：配额引擎的"双向安全边界"设计——fail-open 保证可用性，fail-closed 保证安全性，是限流系统的最佳实践。

---

### 3.2 纯函数领域核心 + 依赖注入

**设计**：fairShare、pipeline、熔断器全部是纯函数，无隐藏状态。

**优势**：
- 可完整单测
- 可回放调试
- 零副作用，逻辑清晰

**可借鉴**：领域逻辑与副作用分离是测试友好架构的核心。

---

### 3.3 多因子乘积路由打分

**设计**：`capability × allocation × health × reliability × latency × preference × cost × quota × circuit`，每个因子 0..1 乘子。

**优势**：
- 任一因子为 0 直接淘汰
- 返回 `reasons[]` 说明为何选中，可解释
- 策略可配（priority/round-robin/weighted-random/least-used）

**可借鉴**：多因子打分模型让路由决策可解释、可调优。

---

### 3.4 弹性与容错

**设计**：
- **自适应熔断器**：closed→open→half_open，失败计数达阈值（默认 3）跳 open
- **失败分类**：10 种类型（authentication/rate_limit/quota_exhausted/timeout/5xx/network…），每种标注 `retryable`
- **优雅降级**：`withDegradation` 四档 full→reduced→minimal→default

**优势**：
- 熔断器纯函数式 `observeCircuit(current, event, opts) → next`
- 降级框架有同步/异步双版本和时钟偏移免疫
- 区分 `rate_limit` 与 `quota_exhausted`（后者不重试）
- 全局 registry 供仪表盘观测降级状态

**可借鉴**：失败分类驱动的 failover 策略，把"要不要换 provider"变成声明式策略。

---

### 3.5 滑动窗口计数器

**设计**：2 桶设计 `effective = prev×(1−elapsed/window) + curr`，per-key 内存 mutex 防读-改-写竞态。

**优势**：
- 比固定窗口更平滑
- 比全量记录更省内存
- SQLite UPSERT 本身原子

**可借鉴**：2 桶滑动窗口是单机限流的性能与精度平衡点。

---

### 3.6 安全内联进动态生成代码

**设计**：`cloudflareWorkerScript.ts` 动态生成 Worker 源码，内联 SSRF 防护（拒绝 RFC1918/loopback/内嵌凭证），用 `x-relay-auth` 密钥防泄露 URL 变开放代理。

**优势**：
- 路径经 `resolveRelayTarget()` 校验而非拼接（防路径改写绕过）
- 安全函数 `.toString()` 内联进模板，绑定字面量常量名防 minify 破坏

**可借鉴**：动态生成远程执行代码时的安全防护细节。

---

### 3.7 精确字面量类型契约

**设计**：`src/types/` 全部使用字面量联合类型，编译期即约束状态机。

**优势**：
- scope 层级 `admin ⊃ write ⊃ read`，未知 scope 永远不满足任何要求
- 错误载荷用精确字面量，非宽泛 string
- 管理令牌与推理 API key 明确区分

**可借鉴**：精确类型约束是 TypeScript 项目的安全网。

---

## 四、对比总结

### 设计哲学差异

| 维度 | New API | OmniRoute |
|------|---------|-----------|
| **核心抽象** | Adaptor 接口 + 注册表 | 纯函数领域引擎 + 策略模式 |
| **扩展方式** | 实现接口并注册 | 注入策略对象 + 纯函数组合 |
| **计费模型** | 预扣→结算→退款 | 公平份额 + 三档策略 |
| **路由模型** | 渠道选择（钉选>亲和>随机） | 多因子乘积打分 |
| **容错模型** | 分级重试 + 状态码映射 | 熔断器 + 失败分类 + 降级 |
| **安全模型** | 分级鉴权 + 审计内聚 | 双向安全边界 + 精确类型 |
| **存储模型** | 主库/日志库分离 | 可插拔 driver + 降级 |

### 各自独特优势

**New API 独有**：
1. 企业级多租户用户体系（OAuth/OIDC/Passkeys/2FA）
2. 完整 Web 管理控制台（7 语言）
3. 异步任务三段式计费
4. 支付集成
5. 主库/日志库分离（ClickHouse）

**OmniRoute 独有**：
1. 公平份额配额引擎（纯函数、双向安全边界）
2. 多因子乘积路由打分（可解释）
3. 自适应熔断器 + 失败分类 + 优雅降级
4. 358 个预置供应商
5. 饱和信号（从上游真实利用率取信号）

---

## 五、对自研 api-gateway 的借鉴建议

### P0：立即可做（内核增强）

| 借鉴点 | 来源 | 说明 |
|--------|------|------|
| **Fail-open 模式** | OmniRoute | 限流/计费出错时放行，不阻断正常流量 |
| **月度限额** | OmniRoute | token 加 MonthlyUsedUsd / MonthlyLimitUsd |
| **2 桶滑动窗口限流** | OmniRoute | 替换固定窗口，更平滑 |
| **预扣→结算→退款** | New API | 已有，确认 defer 兜底完整 |
| **可重放 Body** | New API | 重试时解决 body 只能读一次的问题 |

### P1：短期增强（1-2 周）

| 借鉴点 | 来源 | 说明 |
|--------|------|------|
| **失败分类 + 声明式重试策略** | OmniRoute | 区分可重试/不可重试错误 |
| **熔断器** | OmniRoute | 纯函数式，closed→open→half_open |
| **渠道三级索引缓存** | New API | group→model→channels 内存索引 |
| **LIKE 注入防护** | New API | 管理 API 的搜索接口 |
| **日志分级脱敏** | New API | 按角色返回不同详细程度 |
| **预置供应商模板** | OmniRoute | 渠道模板库，开箱即用 |

### P2：中期增强（1-2 月）

| 借鉴点 | 来源 | 说明 |
|--------|------|------|
| **多因子路由打分** | OmniRoute | 替换简单权重分流 |
| **协议转换质量分级** | New API | good/fair/discouraged |
| **协议转换 golden test** | New API | 保障转换正确性 |
| **优雅降级框架** | OmniRoute | withDegradation 模式 |
| **Web 管理界面** | New API | 内嵌 HTML 或独立前端 |

### P3：长期增强（3+ 月）

| 借鉴点 | 来源 | 说明 |
|--------|------|------|
| **多用户体系** | New API | 用户 + 令牌 + 权限组 |
| **主库/日志库分离** | New API | ClickHouse 日志 |
| **Redis 限流** | New API | 多节点共享限流 |
| **PostgreSQL 支持** | New API | 接口已留位置 |
| **水平扩展** | New API | 共享 DB + Redis |

---

## 六、代码阅读建议

### New API 阅读顺序

1. `relay/relay.go` — 转发入口，理解 RelayInfo 上下文
2. `middleware/distributor.go` — 渠道选择逻辑
3. `middleware/auth.go` — 分级鉴权
4. `relaykit/request_registry.go` — 协议转换注册表
5. `model/channel.go` — 渠道模型设计

### OmniRoute 阅读顺序

1. `src/domain/quotaCache.ts` — 配额缓存
2. `src/lib/quota/fairShare.ts` — 公平份额核心
3. `src/domain/degradation.ts` — 优雅降级
4. `src/lib/resilience/adaptiveCircuit.ts` — 熔断器
5. `src/domain/policyEngine.ts` — 策略引擎

---

## 七、总结

| 项目 | 一句话 |
|------|--------|
| **New API** | 企业级全功能网关，Adaptor 策略模式 + 协议转换注册表是核心亮点 |
| **OmniRoute** | 配额引擎的工程质量最高，纯函数 + 双向安全边界 + 多因子路由是最佳实践 |
| **共同点** | 都重视计费与重试协同、渠道缓存、协议转换、安全细节 |
| **差异点** | New API 偏全功能多租户，OmniRoute 偏配额严谨性和路由可解释性 |

---

## 四、sub2api 核心优点

### 4.1 流式安全的 Failover 循环

**设计**：一个 handler 内嵌状态机式 failover 循环，把「鉴权 → 校验 → 预扣 → 选号 → 抢占槽位 → 转发 → 记录用量」串成单请求流水线。

**优势**：
- **流式安全**：全程维护 `streamStarted` 标志，一旦开始写流，错误走 SSE 兜底而非改写状态码
- **客户端断开即停**：每轮 failover 先判 `failoverClientGone`，用户走了立刻标 499 停止重试
- **错误已回传时不重复兜底**：用 `c.Writer.Size()` 前后差判断是否已写过响应
- **用量不漏记**：错误分支只要 `result != nil` 也照常入账
- **可观测性内建**：逐段打点（auth / routing / upstream / response / TTFT），输出 `Server-Timing` 响应头
- **定价时刻冻结**：请求级 `pricingAt` 固定，保证调度看的价与扣费用的价一致

**可借鉴**：failover 循环 + 三态槽位结果（OK / Failed / ProfitVetoed）是干净的错误恢复骨架。

---

### 4.2 三段式选号器 + 批量预取

**设计**：`SelectAccountWithLoadAwareness` 分快路径和负载感知路径，核心是「过滤层 + 排序层 + 打散层」三段式。

**优势**：
- **粘性会话**：sessionHash → Redis 缓存命中账号
- **批量预取消除 N+1**：`withWindowCostPrefetch` / `withRPMPrefetch` 一次批量查
- **排除集驱动的 failover**：`excludedIDs` 作为选号输入，天然形成串行故障转移
- **调度快照 + outbox**：账号变更走 outbox 事件异步重建 Redis 快照
- **利润门（profit control）**：选号前置过滤 + 抢到槽位后终检，亏钱账号直接排除

**可借鉴**：三段式选号器每层都是纯函数，极易单测；context 作为"请求级预取缓存"消除 N+1。

---

### 4.3 探针式派生定价视图

**设计**：用真实计费函数做探针，用差商求单价。断点取自计费自身的规则输入，每段内只喂一种 token，单价 = `ΔActualCost / Δtoken`。

**优势**：
- **单一真相源**：单价不是重新实现规则，而是"问"计费函数
- 相邻同价段合并、浮点噪声截断、显式 $0 与"无价"区分
- 分时倍率锚点日选周一（避免周末恒为 1 的错误剔除）

**可借鉴**："要展示复杂规则的结果，就调那个规则本身"——用差分/探针推导派生视图。

---

### 4.4 异步池 + 溢出同步兜底

**设计**：基于 `alitto/pond` 的有界队列 + 固定 worker 异步执行器。

**优势**：
- **三级溢出策略**：sync（队列满时内联执行）/ sample（采样同步）/ drop（丢弃并计数）
- **自动扩缩容**：队列占用率 ≥70% 按步长 +32 扩容（上限 512），≤15% 才缩容
- **超时 + panic 隔离**：每个任务独立 5s context 超时，单任务 panic 不拖垮 worker
- **钱相关任务强制同步兜底**：含图片/视频/搜索/语音的请求走同步执行路径

**可借鉴**："异步池 + 溢出同步兜底"是写日志/记账类旁路任务的标准安全形态。

---

### 4.5 Lua 限流 + IP 统一解析

**设计**：Redis Lua 脚本实现固定窗口计数限流，INCR + PTTL 自愈 TTL。

**优势**：
- **TTL 自愈**：显式处理 `ttl == -1`（键存在但无过期时间）
- **fail-open / fail-close 可配置**：Redis 挂了默认放行
- **IP 解析统一**：与审计日志、会话绑定、API Key IP ACL 同一套解析

**可借鉴**：Lua 脚本内做"计数 + TTL 修复 + 返回剩余时间"三合一；IP 解析必须"一个真相源"。

---

### 4.6 在途余额预留

**设计**：余额模式下按模型 + body 大小 + max_output_tokens 估算单请求预留额，登记到 Redis，扣费后释放。

**优势**：
- 防止并发请求在预检时看到同一份余额而集体透支
- `context.WithoutCancel` 保证客户端断开后预留仍能正确归还
- 无法定价时 fail-open（可配 fail-closed）

**可借鉴**：预付费/计量计费系统的并发安全设计。

---

### 4.7 上游连接池 + TLS 指纹

**设计**：基于 `utls` 复刻 Node.js 24.x 的 JA3/JA4 指纹，Transport 池化键把 (proxy, account, concurrency, profile) 都纳入。

**优势**：
- 逐字段复刻 17 个 cipher suite、14 个扩展的精确顺序
- 三种拨号器统一抽象（直连 / HTTP 代理 / SOCKS5 代理）
- 超时常量全部有"为什么"的注释
- `ForceAttemptHTTP2: false`（指纹只走 HTTP/1.1）

**可借鉴**：连接池化 + 超时设计 + 能力边界显式处理。

---

### 4.8 内置支付系统

**设计**：Provider 注册表 + 实例负载均衡，覆盖易支付、支付宝、微信、Stripe、Airwallex。

**优势**：
- 支付实例级负载均衡：查启用实例 → 批量查当日用量（含 PENDING）→ 过滤限额违规 → round-robin 选
- 密钥管理严谨：显式拒绝自动生成的 TOTP 密钥用于支付签名
- 完整的订单状态机（10+ 状态）

**可借鉴**：支付实例负载均衡 + PENDING 计入当日用量防并发打爆渠道额度。

---

### 4.9 全局工程亮点

1. **注释即设计文档**：几乎每个非平凡函数都有"为什么这么做 / 不这么做会怎样"的注释
2. **fail-open 是显式决策**：限流、在途预留、利润终检、审核入队，全部 fail-open 但全部可观测
3. **一切旁路任务都有界**：goroutine 池、队列上限、任务超时、panic recover
4. **流式感知贯穿全链路**：从 panic recover 到错误响应都维护 `streamStarted`
5. **测试规模**：45 万行测试代码，测试占比 ~40%

---

## 五、三项目对比总结

### 设计哲学差异

| 维度 | New API | OmniRoute | sub2api |
|------|---------|-----------|---------|
| **核心抽象** | Adaptor 接口 + 注册表 | 纯函数领域引擎 + 策略模式 | 状态机 failover + 三段式选号器 |
| **扩展方式** | 实现接口并注册 | 注入策略对象 + 纯函数组合 | 配置驱动 + outbox 事件 |
| **计费模型** | 预扣→结算→退款 | 公平份额 + 三档策略 | 在途预留 + 阶梯定价 + 利润门 |
| **路由模型** | 钉选 > 亲和 > 随机 | 多因子乘积打分 | 三段式过滤 + 打散 + LRU |
| **容错模型** | 分级重试 + 状态码映射 | 熔断器 + 失败分类 + 降级 | failover 循环 + 三态槽位 |
| **安全模型** | 分级鉴权 + 审计内聚 | 双向安全边界 + 精确类型 | 流式安全 + 客户端断开检测 |
| **存储模型** | 主库/日志库分离 | 可插拔 driver + 降级 | Redis 快照 + outbox 异步重建 |

### 各自独特优势

**New API 独有**：
1. 协议转换注册表（relaykit）带质量分级
2. 异步任务三段式计费
3. 主库/日志库分离（ClickHouse）
4. 完整 Web 管理控制台（7 语言）

**OmniRoute 独有**：
1. 公平份额配额引擎（纯函数、双向安全边界）
2. 多因子乘积路由打分（可解释）
3. 358 个预置供应商
4. 饱和信号（从上游真实利用率取信号）

**sub2api 独有**：
1. 流式安全 failover 循环（streamStarted 贯穿全链路）
2. 三段式选号器 + 批量预取 + outbox 快照
3. 探针式派生定价视图（单一真相源）
4. 异步池 + 溢出同步兜底 + 自动扩缩容
5. 在途余额预留（并发安全）
6. 内置支付系统 + 实例负载均衡
7. TLS 指纹模拟 + 连接池化
8. 利润门（商业护栏）

---

## 六、并发限制对比

### New API 并发限制

| 维度 | 默认值 | 环境变量 | 说明 |
|------|--------|----------|------|
| 全局并发 | 128 | `TASK_ARTIFACT_GLOBAL_CONCURRENCY` | 所有 IP 加起来最多 128 个并发任务 |
| IP 并发 | 64 | `TASK_ARTIFACT_IP_CONCURRENCY` | 单个 IP 最多 64 个并发任务 |
| 对象并发 | 16 | `TASK_ARTIFACT_OBJECT_CONCURRENCY` | 单个对象最多 16 个并发访问 |
| 渠道测试并发 | 1-32 | 可配置 | 同时测试多少个渠道 |
| 价格同步并发 | 8 | 硬编码 | 同时拉取价格信息的并发数 |

**实现**：内存计数器 + 中间件，三层并发限制（全局/IP/对象）。

### OmniRoute 并发限制

| 维度 | 默认值 | 说明 |
|------|--------|------|
| 弹性设置 concurrentRequests | 可配置 | 请求队列的并发数 |
| 供应商 maxConcurrent | 可配置 | 每个供应商最大并发连接数 |
| 服务发现 maxConcurrentScans | 3 | 同时扫描的供应商数量 |
| WebSocket 代理 | 50 | 每个服务最多 50 个并发 WebSocket 连接 |

**实现**：数据库字段 + 设置归一化，供应商级 + 服务级。

### sub2api 并发限制

| 维度 | 默认值 | 说明 |
|------|--------|------|
| 用户并发槽位 | 可配置 | `acquireResponsesUserSlot`，流式请求发 ping 保活 |
| 账号并发槽位 | 可配置 | 选号时抢占，含利润终检否决 |
| 在途余额预留 | 按模型+body 估算 | 防止并发请求集体透支 |
| 异步池 worker | 自动扩缩容 | 队列占用率 ≥70% 扩容，≤15% 缩容，上限 512 |
| 上游连接池 | 240 idle / 240 max | `MaxIdleConns` / `MaxConnsPerHost` |

**实现**：Redis 槽位 + 在途预留 + 异步池 + 连接池，多层并发控制。

### 对比总结

| 维度 | New API | OmniRoute | sub2api |
|------|---------|-----------|---------|
| **限制层级** | 全局 + IP + 对象（三层） | 供应商级 + 服务级 | 用户级 + 账号级 + 连接级 |
| **适用场景** | 任务制品（图片/视频生成） | 请求转发 + 供应商连接 | 请求转发 + 计费 + 连接池 |
| **实现方式** | 内存计数器 + 中间件 | 数据库字段 + 设置归一化 | Redis 槽位 + 在途预留 + 异步池 |
| **独特设计** | 三层并发限制 | 供应商级 maxConcurrent | 在途余额预留 + 自动扩缩容 |

---

## 七、对自研 api-gateway 的借鉴建议（更新版）

### P0：立即可做（内核增强）

| 借鉴点 | 来源 | 说明 |
|--------|------|------|
| **Fail-open 模式** | OmniRoute + sub2api | 限流/计费出错时放行，不阻断正常流量 |
| **月度限额** | OmniRoute | token 加 MonthlyUsedUsd / MonthlyLimitUsd |
| **2 桶滑动窗口限流** | OmniRoute | 替换固定窗口，更平滑 |
| **预扣→结算→退款** | New API | 已有，确认 defer 兜底完整 |
| **可重放 Body** | New API | 重试时解决 body 只能读一次的问题 |
| **流式安全错误处理** | sub2api | `streamStarted` 标志贯穿全链路 |
| **客户端断开即停** | sub2api | failover 每轮先判客户端是否还在 |

### P1：短期增强（1-2 周）

| 借鉴点 | 来源 | 说明 |
|--------|------|------|
| **失败分类 + 声明式重试策略** | OmniRoute | 区分可重试/不可重试错误 |
| **熔断器** | OmniRoute | 纯函数式，closed→open→half_open |
| **渠道三级索引缓存** | New API | group→model→channels 内存索引 |
| **LIKE 注入防护** | New API | 管理 API 的搜索接口 |
| **日志分级脱敏** | New API | 按角色返回不同详细程度 |
| **预置供应商模板** | OmniRoute | 渠道模板库，开箱即用 |
| **在途余额预留** | sub2api | 防止并发请求集体透支 |
| **异步池 + 溢出同步兜底** | sub2api | 记账/日志旁路任务的标准安全形态 |

### P2：中期增强（1-2 月）

| 借鉴点 | 来源 | 说明 |
|--------|------|------|
| **多因子路由打分** | OmniRoute | 替换简单权重分流 |
| **协议转换质量分级** | New API | good/fair/discouraged |
| **协议转换 golden test** | New API | 保障转换正确性 |
| **优雅降级框架** | OmniRoute | withDegradation 模式 |
| **Web 管理界面** | New API | 内嵌 HTML 或独立前端 |
| **三段式选号器** | sub2api | 过滤 + 排序 + 打散 |
| **批量预取消除 N+1** | sub2api | context 作为请求级预取缓存 |
| **探针式派生定价视图** | sub2api | 用真实计费函数做探针 |

### P3：长期增强（3+ 月）

| 借鉴点 | 来源 | 说明 |
|--------|------|------|
| **多用户体系** | New API | 用户 + 令牌 + 权限组 |
| **主库/日志库分离** | New API | ClickHouse 日志 |
| **Redis 限流** | New API + sub2api | 多节点共享限流 |
| **PostgreSQL 支持** | New API | 接口已留位置 |
| **水平扩展** | New API | 共享 DB + Redis |
| **outbox + 快照** | sub2api | DB 为准、缓存异步追赶 |
| **支付系统** | sub2api | 实例负载均衡 + 订单状态机 |
| **连接池化 + 超时** | sub2api | Transport 池 + 超时常量 |

---

## 八、代码阅读建议（更新版）

### New API 阅读顺序
1. `relay/relay.go` — 转发入口，理解 RelayInfo 上下文
2. `middleware/distributor.go` — 渠道选择逻辑
3. `middleware/auth.go` — 分级鉴权
4. `relaykit/request_registry.go` — 协议转换注册表
5. `model/channel.go` — 渠道模型设计

### OmniRoute 阅读顺序
1. `src/domain/quotaCache.ts` — 配额缓存
2. `src/lib/quota/fairShare.ts` — 公平份额核心
3. `src/domain/degradation.ts` — 优雅降级
4. `src/lib/resilience/adaptiveCircuit.ts` — 熔断器
5. `src/domain/policyEngine.ts` — 策略引擎

### sub2api 阅读顺序
1. `backend/internal/handler/openai_chat_completions.go` — 核心转发 failover 循环
2. `backend/internal/service/gateway_scheduling.go` — 三段式选号器
3. `backend/internal/service/billing_context_schedule.go` — 探针式定价
4. `backend/internal/service/usage_record_worker_pool.go` — 异步池
5. `backend/internal/middleware/rate_limiter.go` — Lua 限流
6. `backend/internal/pkg/tlsfingerprint/` — TLS 指纹（仅学习架构，注意合规）

---

## 九、总结（更新版）

| 项目 | 一句话 |
|------|--------|
| **New API** | 企业级全功能网关，Adaptor 策略模式 + 协议转换注册表是核心亮点 |
| **OmniRoute** | 配额引擎的工程质量最高，纯函数 + 双向安全边界 + 多因子路由是最佳实践 |
| **sub2api** | 流式安全 failover + 段式选号器 + 异步池 + 支付系统，工程化程度最高（692K 行） |
| **共同点** | 都重视计费与重试协同、渠道缓存、协议转换、安全细节 |
| **差异点** | New API 偏全功能多租户，OmniRoute 偏配额严谨性，sub2api 偏流式安全和商业闭环 |
