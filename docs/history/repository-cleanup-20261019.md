# 2026-10-19 本地分支、远端与文档整理

## 批次边界

按用户要求整理分支和说明文档。所属范围为仓库维护、README、活动计划、开发/协作/架构说明、状态索引与 history；依赖主线 bdcb307 中已验收的公开接口和契约，接口版本不变；不触碰地基实现；不新增或修改 API、schema、配置或 CI。纯文档验证差异、链接/锚点、规则和源码状态一致性。其他工作树的源码及未提交文档不被本批编辑。

## 已执行的 Git 整理

- 在线核对远端 master 为 `bdcb307e18b029119f56f2cd4c8989318e60e51d`，对应 Verify `39228308923` check/delivery 成功；远端没有打开的 PR。
- `git fetch --prune origin` 清除已被服务器删除的远端跟踪引用。删除前用 `git merge-base --is-ancestor` 确认 3 个旧远端功能分支已在主线内；使用逐引用 SHA lease 删除 `feat/s2-access`、`feat/s3-admission`、`feat/s5-pricing`，若远端移动则拒绝覆盖。
- 将原主目录 `feat/s5-pricing/746f673` 的 39 个修改/未跟踪项保存为本地归档提交 `60a7d25`，分支 `archive/s5-pricing-wip-20261019`。这是 WIP 保存点，不是新功能验收或待合入主线的提交。
- 主目录切回 master，以 `git merge --ff-only origin/master` 从 `5f90a20` 同步到 `bdcb307`。文档修改在独立 `docs/repository-status-20261019` 分支完成。
- 用祖先关系筛选并通过 `git branch -d` 删除 42 个已合并且未被工作树占用的本地分支；已合并源码继续由 master 历史保留。
- 两个非祖先 WIP 原样改名保留：`archive/reference-r05a-quotation-20261019` / `2c42b11` 与 `archive/reference-r06-ledger-20261019` / `28fc3ec`。本地归档不推送，不把旧改动重复合并。
- 移除上述两个归档分支以及两个保留旧工作树分支的失效/误导 upstream 设置；不改变其提交或工作树文件。

本地归档可用 `git show <归档分支>` 查看，用 `git worktree add <新目录> <归档分支>` 在独立目录恢复。忽略的个人配置、数据库、二进制及 `.local` 留在原处，不进入归档提交或文档推送。

## 保留工作树

| 工作树 | 保留原因 |
|---|---|
| 主目录 TXAPI | 已同步主线，本批只提交文档 |
| Codex 5506 / docs/fc002-background-freeze | 有既存代码修改及大量文档删除，完整保留，不视为可删除工作树 |
| Codex reference-adoption / feat/reference-r04-account-tool | R04a 账号授权工具的活跃未提交实现；本批不编辑、不代提交 |
| TXAPI-references/worktrees/background-null | 干净且关联既有工作；保留已合并分支和目录，不擅自解除其他聊天的工作树 |

## 文档整理与证据范围

将整理前 bdcb307 的完整 PLAN 另存为 [历史快照](plan-checkpoint-20261019.md)，保留全部勾选、命令、失败/未运行项和 CI；仅调整相对链接。新的 [PLAN](../PLAN.md) 收口为当前状态、后续进入条件与原完整验收要求，不把子批通过扩大为 S5、A03–A06 或 R00–R07 完成。

README、开发/协作/架构摘要与能力状态索引对齐 OpenAPI 0.11.0 / 111 操作和主线 schema v16，标明已验收 disabled 后台/WS 子集、报价/用量/个人账本读取及尚未交付的收费、真实 Provider、账号和 Runtime 范围。旧阶段版本仅作为具名历史基线出现。

执行环境 Windows/PowerShell，基线 bdcb307。实际运行 `git diff --check` 与本地临时校验器 `python .local/validation/check_cleanup_docs.py`，61 份 Markdown / 532 个本地链接锚点全部通过；归档与原 PLAN 在相对链接调整以外完全一致。活动计划保留 58 条原功能清单且状态不变，6 条已冻结 FND 勾选完整保留在归档；所有未完成清单均未丢失、未新增完成勾选。OpenAPI 0.11.0 / 111 操作、schema v16 与源码一致，变更仅 10 份 Markdown。

本批不重复运行 Go/浏览器功能测试，不手动 dispatch CI；push/PR 的自动完整 workflow 保持现状。最终头、PR、合并与合并后 CI 以对应自动运行结果单独核对，原主线通过不充当本次文本的 CI 证据。
