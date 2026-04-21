# Auto-Contributor V2 重设计方案

## 设计原则

1. **编排器只管调度，业务逻辑在 Agent Prompt 里** — 学 Symphony
2. **流程变更不需要改代码** — Policy-as-Markdown
3. **每个阶段由专门的 Agent 负责** — 分层 Agent 架构
4. **失败知识可积累** — 数据库记录失败原因，反馈给下一次尝试

## 架构总览

```
┌────────────────────────────────────────────────────────────────┐
│                    Orchestrator (Go)                           │
│  职责：调度、并发控制、状态管理、重试、Review-Rework 循环          │
│  不含任何贡献逻辑                                               │
└─────────────────────────┬──────────────────────────────────────┘
                          │ dispatches
                          ▼
┌────────────────────────────────────────────────────────────────┐
│                 Agent Pipeline (Prompt-Driven)                 │
│                                                                │
│  ┌──────────┐   ┌──────────┐   ┌──────────┐                  │
│  │ Scout    │──▶│ Analyst  │──▶│ Engineer │──┐               │
│  │ Agent    │   │ Agent    │   │ Agent    │  │               │
│  └──────────┘   └──────────┘   └──────────┘  │               │
│                                       ▲       ▼               │
│                                       │  ┌──────────┐         │
│                                       │  │ Reviewer │         │
│                                       │  │ Agent    │         │
│                                       │  └────┬─────┘         │
│                                       │       │               │
│                                  rework│  pass ▼               │
│                                       │  ┌──────────┐         │
│                                       └──│ Submitter│         │
│                                          │ Agent    │         │
│                                          └──────────┘         │
│                                                                │
│  每个 Agent = Claude Code 子进程 + 对应的 Prompt Template       │
└────────────────────────────────────────────────────────────────┘
```

## 状态机 (学 Symphony 的设计)

```
discovered → analyzing → engineering → reviewing → submitting → completed
                │              │            │            │
                ▼              ▼            │            ▼
            abandoned      failed     ┌────┘        pr_failed
                                      ▼
                                  rework (回到 engineering，携带 review 反馈)
                                      │
                                      ▼
                                  max_rework_exceeded → abandoned
```

**关键循环**：`engineering → reviewing → rework → engineering → reviewing → ...`

这是质量门禁。Engineer 写代码，Reviewer 审代码。不通过就带着反馈回去重做。
最多循环 `max_review_rounds` 次（默认 3），超过则 abandon。

## Agent 分层设计

### Agent 1: Scout (侦察兵)

**职责**：发现值得修复的 issue

**输入**：语言偏好、标签过滤、星级要求

**输出**：候选 issue 列表 (JSON)

**Prompt 模板** (`prompts/scout.md`):
```markdown
你是一个开源贡献侦察兵。搜索 GitHub 上适合自动修复的 issue。

要求：
- 使用 `gh` CLI 搜索 issue
- 必须检查每个 issue 是否已有竞争 PR
- 必须检查 issue 评论中维护者是否建议上游修复
- 必须检查 issue 是否有人声称正在处理

过滤条件：
- 语言：{{ languages }}
- 标签：{{ include_labels }}
- 最小星级：{{ min_stars }}

输出格式：JSON 数组，每项包含：
{
  "repo": "owner/name",
  "issue_number": 123,
  "title": "...",
  "difficulty": 0.3,
  "has_competing_pr": false,
  "upstream_redirect": false,
  "maintainer_direction": "fix in this repo by modifying X",
  "recommended": true
}

标记 FIX_COMPLETE 当搜索完成。
```

**关键**：上游检查和竞争 PR 检查在这里完成，不在 Go 代码中。

---

### Agent 2: Analyst (分析师)

**职责**：深度分析单个 issue，制定修复计划

**输入**：repo + issue_number

**输出**：修复计划 (JSON)

**Prompt 模板** (`prompts/analyst.md`):
```markdown
你是一个代码分析师。深度分析这个 issue 并制定修复计划。

Issue: {{ repo }}#{{ issue_number }}

步骤：
1. 读取 issue 正文和所有评论
2. 读取 CONTRIBUTING.md, .github/CONTRIBUTING.md
3. 检查最近合并的 PR 确定基准分支
4. 读取 CI 配置 (.github/workflows/)
5. 阅读相关代码，定位 root cause
6. 评估复杂度

输出格式 JSON：
{
  "can_fix": true/false,
  "reason": "为什么能/不能修",
  "base_branch": "main",
  "branch_naming": "fix/issue-123-desc",
  "commit_format": "conventional",
  "test_command": "go test ./...",
  "lint_command": "golangci-lint run",
  "dco_required": true,
  "fix_plan": "修改 X 文件的 Y 函数...",
  "files_to_change": ["path/to/file.go"],
  "complexity": "low"
}

如果判断不能修复，标记 FIX_INCOMPLETE 并说明原因。
标记 FIX_COMPLETE 当分析完成。
```

---

### Agent 3: Engineer (工程师)

**职责**：执行修复 + 写测试 + 确保通过

**输入**：repo（已 clone 到 workspace）+ 修复计划

**输出**：代码变更 + 测试通过

**Prompt 模板** (`prompts/engineer.md`):
```markdown
你是一个软件工程师。按照修复计划修复这个 issue。

Issue: {{ repo }}#{{ issue_number }}
修复计划：{{ fix_plan }}
基准分支：{{ base_branch }}

规则：
- 最小修改，不要重构周围代码
- 必须写对应的测试
- 必须运行 {{ test_command }} 确保所有测试通过
- 如果有 {{ lint_command }}，也要通过
- Commit 消息格式：{{ commit_format }}
- DCO sign-off：git commit -s
- 作者：{{ git_name }} <{{ git_email }}>
- 禁止 Co-Authored-By 或任何 AI 标记

完成后标记 TESTS_PASSED: true/false
标记 FIX_COMPLETE 当修复完成且测试通过。
```

---

### Agent 4: Reviewer (审查员)

**职责**：审查 Engineer 的代码变更，决定通过或打回 rework

**输入**：workspace（已有 commit）+ 修复计划 + issue 上下文

**输出**：审查结果 (JSON)

**Prompt 模板** (`prompts/reviewer.md`):
```markdown
你是一个严格的代码审查员。审查这个修复是否可以提交到开源项目。

Issue: {{ repo }}#{{ issue_number }}
修复计划：{{ fix_plan }}
Rework 轮次：{{ rework_round }} / {{ max_review_rounds }}
{% if previous_review %}
上一轮审查反馈：{{ previous_review }}
{% endif %}

审查清单（按优先级）：

1. **安全** — 有没有注入、XSS、硬编码密钥等安全问题？
2. **正确性** — 修复是否真的解决了 issue 描述的问题？root cause 是否正确？
3. **测试** — 有没有对应的测试？测试是否真的验证了修复？
4. **最小性** — 是否只改了必要的代码？有没有多余的重构、注释、格式化？
5. **项目规范** — commit 消息格式对吗？分支命名对吗？DCO sign-off 了吗？
6. **风格一致** — 代码风格是否跟项目现有代码一致？

步骤：
1. 运行 `git diff {{ base_branch }}..HEAD` 查看所有变更
2. 阅读每个变更文件的上下文
3. 运行 {{ test_command }} 确认测试通过
4. 如果有 {{ lint_command }}，也运行确认
5. 检查 `git log --oneline` 确认 commit 消息和 sign-off

输出格式 JSON：
{
  "verdict": "approve" | "rework",
  "confidence": 0.0-1.0,
  "issues_found": [
    {
      "severity": "critical" | "major" | "minor" | "nit",
      "file": "path/to/file",
      "line": 42,
      "description": "问题描述",
      "suggestion": "建议修改方式"
    }
  ],
  "rework_instructions": "如果 verdict=rework，给 Engineer 的具体修改指令",
  "summary": "一句话总结审查结论"
}

判断标准：
- 有 critical 或 major 问题 → 必须 rework
- 只有 minor/nit → 可以 approve（minor 由 Engineer 在下次顺手修）
- 测试不通过 → 必须 rework
- 多余的代码变更 → 必须 rework

标记 FIX_COMPLETE 当审查完成。
```

**关键设计**：
- Reviewer 和 Engineer 是**不同的 Agent 实例**，没有共享上下文，保证独立判断
- `previous_review` 字段让 Engineer 在 rework 时知道具体要改什么
- `rework_round` 让 Reviewer 知道这是第几轮，避免无限循环
- confidence 分数记录到数据库，用于后续分析审查质量

---

### Agent 5: Submitter (提交者)

**职责**：Fork + Push + 创建 PR + 在 issue 留言

**输入**：workspace（已有 commit）+ 修复计划

**输出**：PR URL

**Prompt 模板** (`prompts/submitter.md`):
```markdown
你是一个 PR 提交专家。将修复提交到上游仓库。

Issue: {{ repo }}#{{ issue_number }}
分支：{{ branch_name }}

步骤：
1. 再次检查是否有竞争 PR（最后一道防线）
2. Fork 仓库到 {{ github_username }}
3. Push 分支到 fork
4. 创建 Draft PR（不是正式 PR）
5. 在 issue 下留言说明你的修复方向（礼貌、简洁）

PR 模板：
## Summary
<1-2 句描述>

Fixes #{{ issue_number }}

## Changes
- <变更点>

## Test plan
- <测试方案>

如果发现已有竞争 PR，标记 FIX_INCOMPLETE 说明原因。
标记 FIX_COMPLETE 当 PR 创建成功。
```

---

## Orchestrator 设计 (Go)

Orchestrator 变得非常薄，只负责调度 + Review-Rework 循环控制：

```go
type Orchestrator struct {
    db        *db.Database
    config    *Config
    prompts   *PromptStore     // 加载 prompts/*.md
    workspace *WorkspaceManager
}

// 主循环
func (o *Orchestrator) Run() {
    for {
        // 1. Scout 阶段：发现 issues
        issues := o.runAgent("scout", scoutContext)
        o.db.SaveDiscoveredIssues(issues)

        // 2. 对每个 pending issue
        for _, issue := range o.db.GetPendingIssues() {
            o.processIssue(issue)
        }

        time.Sleep(config.PollInterval)
    }
}

func (o *Orchestrator) processIssue(issue *Issue) {
    workspace := o.workspace.Create(issue)

    // 阶段 1: Analyst 分析
    o.db.UpdateStatus(issue, "analyzing")
    plan := o.runAgent("analyst", analystCtx(issue, workspace))
    if !plan.CanFix {
        o.db.MarkAbandoned(issue, plan.Reason)
        return
    }

    // 阶段 2: Engineer-Reviewer 循环
    var lastReview *ReviewResult
    for round := 1; round <= o.config.MaxReviewRounds; round++ {

        // Engineer 修复（首次或 rework）
        o.db.UpdateStatus(issue, "engineering")
        engineerCtx := engineerCtx(issue, workspace, plan)
        if lastReview != nil {
            // Rework: 携带上一轮审查反馈
            engineerCtx["rework_round"] = round
            engineerCtx["rework_instructions"] = lastReview.ReworkInstructions
            engineerCtx["issues_found"] = lastReview.IssuesFound
        }
        result := o.runAgent("engineer", engineerCtx)
        if !result.Success {
            o.db.RecordAttempt(issue, result)
            return
        }

        // Reviewer 审查
        o.db.UpdateStatus(issue, "reviewing")
        reviewCtx := reviewCtx(issue, workspace, plan, round, lastReview)
        review := o.runAgent("reviewer", reviewCtx)
        o.db.RecordReview(issue, review, round)

        if review.Verdict == "approve" {
            // 通过审查 → 进入提交阶段
            break
        }

        // 未通过 → rework
        lastReview = &review
        o.db.UpdateStatus(issue, "rework")
        log.Info("Review round %d: rework required - %s", round, review.Summary)

        if round == o.config.MaxReviewRounds {
            o.db.MarkAbandoned(issue, "max review rounds exceeded")
            return
        }
    }

    // 阶段 3: Submitter 提交
    o.db.UpdateStatus(issue, "submitting")
    pr := o.runAgent("submitter", submitterCtx(issue, workspace, plan))
    o.db.SavePR(issue, pr)
    o.db.UpdateStatus(issue, "completed")
}

// runAgent: 唯一跟 Claude 交互的地方
func (o *Orchestrator) runAgent(name string, ctx map[string]any) AgentResult {
    prompt := o.prompts.Render(name, ctx)
    return o.executor.Run(prompt, workspace)
}
```

**Review-Rework 循环的关键**：
- Orchestrator 只控制循环次数和状态转换
- 审查标准在 `prompts/reviewer.md` 中，不在 Go 代码里
- Rework 指令通过模板变量 `{{ rework_instructions }}` 传给 Engineer
- 每轮 review 结果记录到数据库，可分析审查质量趋势

## 配置文件设计 (config.yaml)

```yaml
# 全局配置
github:
  username: majiayu000
  email: user@example.com

# Agent 配置
agents:
  scout:
    prompt: prompts/scout.md
    timeout: 10m
    max_retries: 2
  analyst:
    prompt: prompts/analyst.md
    timeout: 5m
    max_retries: 1
  engineer:
    prompt: prompts/engineer.md
    timeout: 30m
    max_retries: 3
  reviewer:
    prompt: prompts/reviewer.md
    timeout: 10m
    max_review_rounds: 3     # Engineer-Reviewer 最大循环次数
  submitter:
    prompt: prompts/submitter.md
    timeout: 5m
    max_retries: 2

# 发现配置
discovery:
  languages: [go, python, typescript, rust]
  include_labels: [bug, "good first issue", "help wanted"]
  min_stars: 50
  max_issue_age_days: 30
  poll_interval: 60m

# 并发
concurrency:
  max_workers: 2
  max_concurrent_agents: 1

# 工作区
workspace:
  root: ~/.auto-contributor/workspace
  hooks:
    after_create: "git clone --depth 1 ..."
    before_run: ""
    after_run: ""

# 数据库
database:
  path: ~/.auto-contributor/data.db
  # url: postgres://...  # 可选

# Web UI
web:
  enabled: true
  port: 8080
```

## 与当前架构的对比

| 维度 | 当前 V1 | 新 V2 |
|---|---|---|
| 修复逻辑 | Go 代码 hardcode | Prompt 模板 |
| 阶段划分 | 单一 Claude 调用 | 5 个专职 Agent + Review 循环 |
| 上游检查 | ❌ | Scout Agent prompt 中 |
| CONTRIBUTING.md | ❌ | Analyst Agent prompt 中 |
| 竞争 PR 检查 | 1 次 | Scout + Submitter 各 1 次 |
| Draft PR | ❌ | Submitter Agent prompt 中 |
| 预沟通 | ❌ | Submitter Agent prompt 中 |
| 基准分支 | hardcode main | Analyst Agent 自动检测 |
| 修改流程 | 改 Go → 编译 → 部署 | 改 Prompt → 立即生效 |
| 代码审查 | ❌ | Reviewer Agent + Rework 循环 |
| Orchestrator | 500+ 行业务逻辑 | ~200 行纯调度 + 循环控制 |

## 目录结构

```
auto-contributor/
├── cmd/
│   └── auto-contributor/
│       └── main.go              # CLI 入口
├── internal/
│   ├── orchestrator/
│   │   └── orchestrator.go      # 薄调度层 (~200 行)
│   ├── agent/
│   │   └── runner.go            # Claude 子进程管理
│   ├── workspace/
│   │   └── workspace.go         # 工作区管理
│   ├── prompt/
│   │   └── store.go             # 模板加载 + 渲染
│   ├── config/
│   │   └── config.go            # 配置解析
│   ├── db/
│   │   └── db.go                # 数据库层（保留）
│   ├── github/
│   │   └── client.go            # 只保留最基础的 API
│   └── web/
│       └── server.go            # Dashboard（保留）
├── prompts/                     # Agent Prompt 模板
│   ├── scout.md
│   ├── analyst.md
│   ├── engineer.md
│   ├── reviewer.md
│   └── submitter.md
├── pkg/
│   ├── models/
│   │   └── models.go
│   └── logger/
│       └── logger.go
├── config.yaml                  # 运行配置
├── docs/
│   ├── current-analysis.md
│   ├── symphony-patterns.md
│   └── redesign-proposal.md     # 本文档
├── go.mod
└── go.sum
```

## 实施路径

### Phase 1: Prompt 模板系统
- 创建 `prompts/` 目录和 4 个 Agent 模板
- 实现 `internal/prompt/store.go` 模板加载和渲染
- Agent Runner 从模板读取 prompt 而不是 hardcode

### Phase 2: Agent Pipeline
- 重构 Orchestrator 为 4 阶段 pipeline
- 每个阶段对应一个 Agent 调用
- 阶段之间通过 JSON 传递上下文

### Phase 3: 结果解析
- 统一 Agent 输出格式（JSON + 标记）
- 结构化记录每个 Agent 的输出到数据库

### Phase 4: 清理
- 删除 `internal/discovery/` (被 Scout Agent 替代)
- 精简 `internal/claude/executor.go` (只保留子进程管理)
- 精简 `internal/github/client.go` (Agent 直接用 gh CLI)
