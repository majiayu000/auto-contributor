# Launch Readiness

This document records the trust boundaries and release checklist for
Auto-Contributor before broader public use.

## Current File-Backed Readiness

- License declaration is backed by `LICENSE`.
- Release notes are tracked in `CHANGELOG.md`.
- Issue and pull request templates define the minimum safety and verification
  evidence expected from future changes.
- The README documents operating limits, permissions, local state, and
  limitations.

## Trust Boundaries

Auto-Contributor performs high-trust actions:

- reads GitHub issues, pull requests, repository metadata, and CI state through
  the authenticated `gh` session;
- creates local branches, generated commits, and pull requests when `pipeline` or
  `loop` is run;
- signs commits with the configured Git identity for DCO compliance;
- stores local runtime state in `~/.auto-contributor/data.db`.

Credentials and tokens must stay outside the repository. Use `gh auth login`,
the Claude Code CLI login flow, environment variables, or the local operator
environment instead of committed configuration.

## Controlled Single-Issue Flow

Use this flow to produce launch proof before broadening scope:

```bash
gh auth status
go vet ./...
go build ./...
go test ./...
./auto-contributor discover-smart --topic golang --limit 3 --output issues.json
./auto-contributor pipeline --repo owner/repo --issue 123
git status --short
gh pr view --repo owner/repo --web
```

Record the selected repository, issue number, created pull request, and
verification output in the release notes before tagging a launch release.

## Limitations

- Maintainer acceptance cannot be inferred from a green local build alone.
- Branch protections, CI requirements, DCO checks, and human reviews remain the
  authority on whether a pull request can merge.
- Generated code and PR text require review before expanding beyond a single
  target repository or issue.
- Auto-Contributor is not a secrets manager and should not receive credentials
  through committed files.
- External repository behavior can change between discovery, implementation,
  and submission.

## Post-Merge Repository Metadata

These actions require repository-owner permissions and should happen after the
readiness PR merges:

- add GitHub topics: `automation`, `github`, `claude-code`, `pull-requests`,
  `go`;
- create an initial GitHub release from a reviewed main branch;
- attach the controlled single-issue proof or release notes to that release.
