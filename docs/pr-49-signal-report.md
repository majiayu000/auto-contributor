# Signal Report: PR 49 Reviewer Parse Failure Blocks

Issue: https://github.com/majiayu000/auto-contributor/issues/45
PR: https://github.com/majiayu000/auto-contributor/pull/49

## Root Cause

The original engineer-review loop treated reviewer `RunJSON` runtime or
JSON parse failures as implicit approval, allowing the pipeline to continue
toward submission without a valid reviewer result.

## Current Branch Diagnosis

- `internal/pipeline/agents.go` now records a failed reviewer event, marks the
  issue failed with `reviewer_failed`, and returns the reviewer error.
- Existing regression tests cover failed issue status for reviewer parse and
  runtime failures.
- The remaining enforcement gap is that tests do not assert the failure is
  persisted to `pipeline_events`, even though issue #45 requires recording it.

## Fix Direction

Add deterministic regression assertions that reviewer parse and runtime
failures create failed reviewer events containing the original error message.

## Review Comments

- PR review comments: none.
- PR submitted reviews: none.
