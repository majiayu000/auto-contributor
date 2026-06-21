# Changelog

All notable changes to Auto-Contributor are documented in this file.

## Unreleased

- Added launch-readiness documentation covering trust boundaries, proof checklist,
  limitations, and contribution templates.
- Added an MIT license file to match the README license declaration.
- Added issue and pull request templates for safer external review.

## 0.1.0 - Initial launch baseline

- Provides a Go CLI for discovering GitHub issues, running Claude Code on a
  bounded issue, signing commits, and opening pull requests through GitHub CLI.
- Includes CI coverage for `go vet ./...`, `go build ./...`, and
  `go test ./...`.
