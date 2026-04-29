# Signal Report: PR 61 Feedback Workspaces Track PR Head Branches

Issue: https://github.com/majiayu000/auto-contributor/issues/60
PR: https://github.com/majiayu000/auto-contributor/pull/61

## Root Cause

GitHub-synced pull requests could be stored locally without a `branch_name`,
and the feedback / CI-fix paths only called `createWorkspace`, which creates a
directory but does not clone, fetch, or check out the PR head branch.

## Branch Diagnosis

- `internal/github/pr.go` keeps `gh search prs` on the supported JSON fields,
  then backfills each PR's `headRefName` through `gh pr view`.
- `internal/db/db.go` now updates `pull_requests.branch_name` when an existing
  tracked PR is re-synced with newly discovered branch data.
- `internal/pipeline/feedback.go` refreshes the tracked branch name from
  `GetPRInfo` before feedback or CI-fix handling continues.
- `internal/pipeline/pipeline.go` replaces empty-directory workspaces in the
  feedback paths with clone/fetch/checkout preparation for the tracked PR head
  branch.
- `internal/github/pr.go` retries `gh pr view` without `lockReason` when the
  installed GitHub CLI does not support that field, so feedback polling stays
  live on older `gh` builds.

## Resolution

- Open-PR sync persists head branch names instead of leaving GitHub-synced rows
  stranded with empty `branch_name`.
- Existing tracked PR rows are backfilled when later GitHub calls expose the
  head branch.
- Feedback responders and CI fixers now run from a checked-out PR branch rather
  than an empty or stale directory.
- Follow-up regression coverage now exercises `preparePRWorkspace` directly so
  the tracked-branch checkout path is verified in tests, not only by manual PR
  inspection.

## Review Comments

- PR review comments: none.
- PR submitted reviews: none.

## Validation

- `gofmt -w .`: passed
- `go vet ./...`: passed
- `go build ./...`: passed
- `go test ./...`: passed
- `cargo check && cargo test`: not applicable; repository has no `Cargo.toml`
