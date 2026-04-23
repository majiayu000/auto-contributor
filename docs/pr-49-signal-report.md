# Signal Report: PR 49 Reviewer Parse Failure Blocks

Issue: https://github.com/majiayu000/auto-contributor/issues/45
PR: https://github.com/majiayu000/auto-contributor/pull/49

## Root Cause

The original engineer-review loop treated reviewer `RunJSON` runtime or
JSON parse failures as implicit approval, allowing the pipeline to continue
toward submission without a valid reviewer result.

## Branch Diagnosis

- `internal/pipeline/agents.go` now records a failed reviewer event, marks the
  issue failed with `reviewer_failed`, and returns the reviewer error.
- Regression tests cover failed issue status for reviewer parse and runtime
  failures.
- Regression tests also assert that the failure is persisted to
  `pipeline_events` with `stage=reviewer`, `success=false`, `verdict=error`,
  and the original error message.

## Resolution

- Reviewer parse/runtime failures no longer become implicit approval.
- The engineer-review loop stops immediately on reviewer failures.
- The issue status and event log both preserve the reviewer failure signal.

## Review Comments

- PR review comments: none.
- PR submitted reviews: none.

## Validation

- `gofmt -w .`: passed
- `go vet ./...`: passed
- `go build ./...`: passed
- `go test ./...`: passed
- `cargo check && cargo test`: not applicable; repository has no `Cargo.toml`
