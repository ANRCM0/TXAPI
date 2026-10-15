# 参与 TXAPI 开发

开始前阅读 [协作框架与团队边界](docs/COLLABORATION.md)、[架构](docs/ARCHITECTURE.md) 和 [开发命令/升级恢复](docs/DEVELOPMENT.md)。业务阶段以 [PLAN](docs/PLAN.md) 为准。

- 从最新 master 为单一功能开分支，先在任务/PR 说明负责目录、依赖接口和验收行为。
- 前后端基于 [OpenAPI](docs/openapi.json) 联调；更改路径/鉴权/字段时同 PR 更新契约、测试和消费者。API Key 明文仅创建时显示。
- core/protocol 不访问数据库/权限/Gin；模块依赖检查会阻止反向引用。新厂商在自己的 adaptor 目录实现，由集成团队接线。
- 数据迁移由 store 注册表统一分配版本，附旧库输入、失败回滚、幂等和恢复限制。不要更改已发布步骤。
- S3 准入实现通过 gateway.Admission 接入，不在 handler 重写授权或无限排队；所有占用必须在失败/取消/panic 时释放。
- 共享文件（main、注册点、数据库模型、迁移版本、CI）由集成负责人协调；真实 CODEOWNERS/分支保护由维护者配置。
- 提交前执行 gofmt、git diff --check、构建/vet、对应行为回归；合并前 Linux 全量 race 与 make smoke 必须通过。不要提交 config.yaml、密钥、数据库、二进制或本地分析文件。

PR 描述采用仓库模板，给出复现/验收命令和兼容影响。性能数据需要环境、payload、并发和分位数，不给无基线的提升百分比。
