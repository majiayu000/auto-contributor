# Contributor Skill vs Auto-Contributor 对比

## 实战数据

| 指标 | Contributor Skill (手动) | Auto-Contributor V1 |
|---|---|---|
| 尝试次数 | 7 | 63 |
| PR 创建成功 | 7 (100%) | 0 (0%) |
| 目标仓库 | FlowiseAI/Flowise | cli/cli, coder/coder 等 |
| 执行方式 | Claude Code 交互式 | 自动后台循环 |

## Skill 的 6 道防线（Auto-Contributor 缺失的）

### 防线 1: 上游重定向检查 (Phase 1.4)
- **Skill**: 检查维护者评论是否提到 "upstream", "other repo"
- **Auto**: ❌ 完全没有
- **影响**: 在错误的仓库修复，PR 被直接关闭

### 防线 2: 竞争 PR 检查 (Phase 1.2)
- **Skill**: fork 前检查一次，push 前再检查一次
- **Auto**: 只在发现阶段检查一次，push 时不再确认
- **影响**: 与他人撞车，PR 被标记 duplicate

### 防线 3: 读 CONTRIBUTING.md (Phase 3.3)
- **Skill**: 读贡献指南获取分支命名、commit 格式、CI 要求
- **Auto**: ❌ 不读
- **影响**: 不符合项目规范，PR 被要求修改或关闭

### 防线 4: 检测基准分支 (Phase 3.2)
- **Skill**: 查最近合并 PR 的 baseRefName 确定目标分支
- **Auto**: ❌ 硬编码 main
- **影响**: PR 目标分支错误

### 防线 5: Draft PR 策略 (Phase 2.2)
- **Skill**: 先创建 Draft PR，CI 通过后再转正式
- **Auto**: ❌ 直接创建正式 PR
- **影响**: CI 失败的正式 PR 给维护者留下坏印象

### 防线 6: 预沟通 (Phase 2.1)
- **Skill**: 在 issue 下留言确认修复方向
- **Auto**: ❌ 直接提 PR
- **影响**: 修复方向与维护者期望不符

## Anti-Patterns (Skill 总结的失败模式)

| Anti-Pattern | Skill 的预防措施 | Auto 是否具备 |
|---|---|---|
| 修在错误的层 | 上游检查 + 预沟通 | ❌ |
| PR 撞车 | 竞争检查 + 预沟通 | ⚠️ 只检查一次 |
| 过度工程 | 最小修改原则 | ⚠️ prompt 有提到但不强制 |
| CI 基础设施混淆 | 区分代码失败 vs 设施失败 | ❌ |
| 静默提交 | 预沟通模板 | ❌ |
| 目标分支错误 | 检测基准分支 | ❌ |

## 结论

Auto-Contributor 的 0% 成功率不是因为 Claude 不会写代码，而是**流程缺陷**导致的：
- 选了不该选的 issue
- 修了不该修的地方
- 没遵守项目规范
- 没做预沟通

V2 通过 Agent Pipeline 将 Skill 的 6 道防线嵌入流程，应能显著提升成功率。
