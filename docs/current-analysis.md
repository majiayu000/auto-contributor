# Auto-Contributor 现状分析

## 当前架构

```
Discovery (Claude智能发现) → Worker Pool → Claude CLI 子进程修复 → Push → PR
```

## 运行数据

- 63 次尝试，0 个 PR 成功创建
- 49 个 issue（25 discovered, 23 abandoned, 1 processing）
- 涉及仓库：cli/cli, coder/coder, docker/cagent, flyteorg/flyte 等

## 关键缺失（对比 Contributor Skill 的实战经验）

### P0 缺失
1. **没有上游重定向检查** — 很多 issue 维护者想在上游修，不检查就浪费全部工作
2. **没有竞争 PR 二次确认** — 在 push 前需要再次检查，避免撞车

### P1 缺失
3. **没有 CONTRIBUTING.md 解析** — 分支命名、commit 格式、CI 要求全靠猜
4. **基准分支硬编码 main** — 有些项目用 dev/develop

### P2 缺失
5. **没有 Draft PR 策略** — CI 失败的正式 PR 给维护者留坏印象
6. **没有预沟通机制** — 直接提 PR，被拒概率高

## 根本问题

当前架构把所有逻辑 hardcode 在 Go 代码中：
- 发现逻辑 hardcode 在 `internal/discovery/`
- 修复逻辑 hardcode 在 `internal/claude/executor.go` 的 prompt 中
- PR 创建逻辑 hardcode 在 `internal/github/client.go`
- 重试策略 hardcode 在 `internal/worker/pool.go`

任何流程调整都需要改 Go 代码、重新编译、重新部署。
