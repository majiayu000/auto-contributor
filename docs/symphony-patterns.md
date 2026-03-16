# Symphony 架构模式参考

## 核心理念

> Symphony handles orchestration; agents handle business logic.

Symphony 的精髓：**编排器只管调度，业务逻辑全在 Agent 的 Workflow prompt 里**。

## 关键设计模式

### 1. Policy-as-Code (WORKFLOW.md)

所有业务逻辑都在一个 Markdown 文件里，不在代码中：

```yaml
---
tracker:
  kind: linear
  project_slug: "my-project"
hooks:
  after_create: "git clone ... ."
agent:
  max_concurrent_agents: 5
---

You are working on {{ issue.identifier }}: {{ issue.title }}
{{ issue.description }}

Implement the solution and run tests.
```

**好处**：修改流程不需要改代码、编译、部署，改 Markdown 就行。

### 2. Workspace 隔离

每个 issue 有独立的工作目录，互不干扰：
```
~/.config/symphony-workspaces/
├── MT-32/    # Issue MT-32 的工作区
├── MT-45/    # Issue MT-45 的工作区
└── MT-50/    # Issue MT-50 的工作区
```

### 3. Hook 生命周期

```
create_workspace → after_create hook (git clone)
                 → before_run hook (lint/type-check)
                 → Agent 执行
                 → after_run hook (coverage)
                 → before_remove hook (cleanup)
```

### 4. Tracker 抽象

不绑定具体平台，通过 Behavior/Interface 抽象：
```
Tracker interface:
  fetch_candidate_issues()
  fetch_issue_states_by_ids()
  create_comment()
  update_issue_state()
```

Linear 是一个实现，可以换成 GitHub Issues、Jira 等。

### 5. Dynamic Tools (Agent ↔ Orchestrator 通信)

Agent 可以调用编排器提供的工具：
```
Agent → linear_graphql(query, variables) → Orchestrator → Linear API → Response
```

### 6. 状态调和 (Reconciliation)

不信任内存状态，定期从 Tracker 拉取真实状态：
- 如果 issue 被手动关闭 → 终止正在运行的 agent
- 如果 issue 被重新分配 → 终止 agent
- 如果 issue 状态变化 → 更新内部状态

### 7. 重试 + 续航

- 失败重试：指数退避，最大 5 分钟
- 正常完成但 issue 仍活跃 → 自动续航（continuation），最多 max_turns 轮
