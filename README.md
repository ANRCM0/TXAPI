# mini-gateway

一个极简的 LLM API 网关内核，用于学习「网关是怎么工作的」。

设计目标：**用最少的代码把完整转发链路讲清楚**。没有数据库、没有前端、
没有计费系统，只有转发本身。约 900 行 Go 代码。

## 它做了什么

```
客户端 ──Bearer sk-xxx──> 网关 ──渠道 key──> 上游 LLM
         (令牌鉴权)       (路由/转换/重试)    (OpenAI/Claude/...)
```

1. **令牌鉴权** —— 校验客户端令牌，支持按令牌限制可访问的模型
2. **渠道路由** —— 按模型名筛选渠道，按权重随机选择
3. **协议转换** —— 把客户端请求体转成上游格式（含模型名映射）
4. **失败重试** —— 429/5xx 时自动换渠道重试，跳过已失败的渠道
5. **流式转发** —— SSE 边收边发，保持打字机效果
6. **用量统计** —— 从响应中抽取 token 用量并记录

## 目录结构

```
mini-gateway/
├── main.go                          入口：加载配置 → 初始化 → 启动（极薄）
├── config.yaml                      生产配置示例
├── config.test.yaml                 测试配置（指向本地假上游）
├── config.retry.yaml                重试测试配置
├── internal/
│   ├── config/config.go             配置加载、默认值、启动期校验
│   ├── server/
│   │   ├── server.go                中间件链 + 路由注册
│   │   └── middleware/middleware.go Recovery / RequestID / CORS / Auth
│   └── relay/
│       ├── adaptor/adaptor.go       ⭐ Adaptor 接口（网关的扩展点）
│       ├── adaptor/openai/          OpenAI 兼容协议适配器
│       ├── meta/meta.go             单次转发的上下文
│       ├── model/model.go           内部数据结构
│       ├── registry.go              渠道池 + 适配器表
│       ├── relay.go                 ⭐ 转发主流程 + 重试
│       └── handler.go               HTTP 处理器
└── testtools/fakeupstream/          假上游，用于无 Key 测试
```

## 快速开始

```bash
# 1. 编译
export PATH=$PATH:/usr/local/go/bin
go build -o mini-gateway .

# 2. 改 config.yaml，填入真实的上游 key
vim config.yaml

# 3. 启动
./mini-gateway -config config.yaml

# 4. 调用
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Authorization: Bearer sk-test-123' \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"你好"}]}'
```

## 无 Key 测试（推荐先跑这个）

不需要任何真实 API Key，用假上游验证完整链路：

```bash
# 终端 1：启动假上游
go build -o /tmp/fakeupstream ./testtools/fakeupstream
/tmp/fakeupstream 127.0.0.1:9911

# 终端 2：启动网关（测试配置指向假上游）
go build -o /tmp/minigateway .
/tmp/minigateway -config config.test.yaml

# 终端 3：验证
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Authorization: Bearer sk-test-123' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

## 三个核心设计

### 1. Adaptor 接口 —— 网关的心脏

```go
type Adaptor interface {
    GetRequestURL(m *meta.Meta) (string, error)
    SetupRequestHeader(req *http.Request, m *meta.Meta) error
    ConvertRequest(body []byte, m *meta.Meta) ([]byte, error)
    DoResponse(c *gin.Context, resp *http.Response, m *meta.Meta) (*model.Usage, *model.ErrorWithStatusCode)
    GetChannelName() string
}
```

所有厂商差异被压进这一个接口。**新增厂商 = 新建一个包实现 5 个方法，
不改任何核心代码。**

### 2. 请求体透明透传

请求体只解析 `model` 和 `stream` 两个字段，其余**原样转发**给上游。

为什么不用结构体完整建模：LLM 请求参数在快速演进，用结构体反序列化
会静默丢掉未声明的字段，用户会遇到「某个参数在网关后面失效了」这种
极难排查的问题。

### 3. 重试换渠道的两个坑

```go
tried := make(map[int]bool)   // 坑1：记录已失败渠道，重试时跳过
```

```go
if !shouldRetry(err.StatusCode) { break }   // 坑2：400 换渠道也没用
```

- **不记录已失败渠道** → 一个坏渠道被反复选中，重试全浪费在它身上
- **不区分错误类型** → 参数错误(400)也去重试，白打一次上游

## 已验证的行为

| 场景 | 结果 |
|------|------|
| 非流式转发 | ✅ 正常返回，usage 记录 |
| 流式转发 (SSE) | ✅ 逐 chunk 转发，打字机效果保留 |
| model_mapping | ✅ 请求 `gpt-4o`，上游收到 `gpt-4o-2024-08-06` |
| 无令牌 / 错误令牌 | ✅ 401 |
| 受限令牌访问未授权模型 | ✅ 503 + 明确错误信息 |
| 模型列表按权限过滤 | ✅ 受限令牌只看到授权模型 |
| 429 重试换渠道 | ✅ bad-1(429) → bad-2(429) → good-3(成功) |

## 还没做（后续阶段）

- SQLite/PostgreSQL 持久化（渠道、令牌、日志）
- 管理界面
- 凭据池调度（冷却、黑名单、会话亲和）
- 计费与配额扣减
- Anthropic / Gemini 适配器
- 单二进制内嵌前端
