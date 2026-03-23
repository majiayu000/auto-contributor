# Self-Learning System Spec

## Overview

Closed-loop learning: **collect events → label outcomes → synthesize rules → inject into agents**

## Implementation Phases

### Phase 0: Bootstrap Rules + Rule Loader (ship together with Phase 4 partial)
- Create `rules/` directory with seed YAML files
- Create `internal/rules/types.go` + `loader.go`
- Wire into Pipeline, inject into all 6 agent prompts
- Immediate value from manually-curated rules

### Phase 1: Pipeline Events Collection
- New `pipeline_events` table (SQLite + PostgreSQL)
- Record every agent invocation: stage, verdict, duration, output summary
- New `internal/db/events.go`

### Phase 2: Outcome Labeling
- When PR reaches terminal state, classify why (merged/rejected_scope/rejected_quality/etc.)
- Retroactively label all pipeline_events for that PR
- New `internal/pipeline/outcome.go`

### Phase 3: Rule Synthesizer
- Periodic job (every 24h) analyzes labeled events
- LLM clusters failure patterns → generates/updates/retires rules
- New `prompts/synthesizer.md` + `internal/pipeline/synthesizer.go`
- Confidence scoring with decay

## Rule Format

```yaml
id: skip-unresponsive-repos
stage: scout
severity: high
confidence: 0.95
source: manual
created_at: "2026-03-23"
evidence_count: 5
tags: [repo-quality, timeout]
condition: |
  Repo has open PRs with zero feedback for 14+ days.
body: |
  ## Skip Unresponsive Repos
  If this repo appears in the slow_repos list, output VERDICT: SKIP.
```

## Directory Structure

```
rules/
  scout/           # anti-patterns, repo quality filters
  analyst/         # branch targets, CLA detection
  engineer/        # code style, minimal changes
  reviewer/        # review depth per category
  submitter/       # PR templates, CLA checkboxes
  responder/       # feedback handling rules
  global/          # rate limits, etiquette
```

## Key Design Decisions

- Rules are YAML+Markdown files on disk, not DB rows (git-trackable)
- Manual rules (`source: manual`) never auto-retired
- Synthesized rules require `evidence_count >= 3`
- Confidence < 0.3 = dormant (loaded but not injected)
- Max 2000 chars of rules injected per stage
- Each stage gets its own rules + global rules
