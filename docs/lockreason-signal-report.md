# Signal Report: Lock Reason Classification Path

Issue source: harness finding `LOGIC-03`

## Root Cause

The finding is stale against `origin/main`: the current code already requests
`lockReason` from `gh pr view`, stores it on `github.PRInfo`, and classifies
`CLOSED` PRs with hostile lock reasons as `hostile_spam`.

The remaining risk is regression at two boundaries:

- `GetPRInfo` has no unit test proving `lockReason` survives JSON parsing.
- Hostile lock classification is case-sensitive, so any lowercase
  `"spam"`/`"off_topic"` value from a future caller would fall through to
  generic closure buckets.

## Resolution

- Add a focused parser helper test that proves `lockReason` is decoded into
  `PRInfo`.
- Normalize lock reasons during classification so casing differences do not
  suppress `hostile_spam`.

## Review Notes

- No GitHub issue was created because the defect report is already covered by
  the current codebase and the remaining work is a regression hardening change.

## Validation

- `gofmt -w .`: passed
- `go vet ./...`: passed
- `go build ./...`: passed
- `go test ./...`: passed
