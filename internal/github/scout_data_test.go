package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/majiayu000/auto-contributor/internal/config"
)

func installFakeGH(t *testing.T, script string) {
	t.Helper()

	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "gh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh script: %v", err)
	}
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCollectScoutDataDistinguishesEmptyResultsFromFailures(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
case "$*" in
  "api repos/owner/repo/issues/7/comments"*) printf '%s' '[]'; exit 0 ;;
  "pr list -R owner/repo --state open --search 7 in:title,body"*) printf '%s' '[]'; exit 0 ;;
  "pr list -R owner/repo --state open --search panic in parser"*) printf '%s' '[]'; exit 0 ;;
  "api repos/owner/repo/issues/7/timeline"*) printf '%s' '[]'; exit 0 ;;
  "pr list -R owner/repo --state merged"*) printf '%s' '[{"number":1}]'; exit 0 ;;
  "api repos/owner/repo --jq"*) printf '%s' '{"default_branch":"main","archived":false,"disabled":false}'; exit 0 ;;
  "api repos/owner/repo/branches"*) printf '%s' '[]'; exit 0 ;;
  "api repos/owner/repo/contents/CONTRIBUTING.md"*) printf '%s\n' 'Not Found' >&2; exit 1 ;;
  "api repos/owner/repo/contents/.github/CONTRIBUTING.md"*) printf '%s\n' 'Not Found' >&2; exit 1 ;;
  "api repos/owner/repo/contents/docs/CONTRIBUTING.md"*) printf '%s\n' 'Not Found' >&2; exit 1 ;;
  "pr list -R owner/repo --state closed"*) printf '%s' '[]'; exit 0 ;;
  "api repos/owner/repo/contents/.github/workflows"*) printf '%s\n' 'Not Found' >&2; exit 1 ;;
  "pr list -R owner/repo --state all"*) printf '%s' '[]'; exit 0 ;;
  *) printf 'unexpected args: %s\n' "$*" >&2; exit 1 ;;
esac
`)

	client := New(&config.Config{})
	data, err := client.CollectScoutData(context.Background(), "owner/repo", 7, "panic in parser")
	if err != nil {
		t.Fatalf("CollectScoutData() error = %v", err)
	}

	formatted := data.Format()
	if !strings.Contains(formatted, "### Open PRs Matching This Issue\nNo data found.") {
		t.Fatalf("empty PR result was not rendered as verified empty:\n%s", formatted)
	}
	if strings.Contains(formatted, "UNKNOWN: fetch failed") {
		t.Fatalf("successful collection should not render UNKNOWN:\n%s", formatted)
	}
	if !strings.Contains(formatted, `"default_branch":"main"`) {
		t.Fatalf("repo metadata missing from formatted data:\n%s", formatted)
	}
}

func TestCollectScoutDataReturnsErrorAndMarksUnknownOnCriticalFetchFailure(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
case "$*" in
  "api repos/owner/repo/issues/7/comments"*) printf '%s' '[]'; exit 0 ;;
  "pr list -R owner/repo --state open --search 7 in:title,body"*) printf '%s\n' 'rate limit exceeded' >&2; exit 1 ;;
  "pr list -R owner/repo --state open --search panic in parser"*) printf '%s' '[]'; exit 0 ;;
  "api repos/owner/repo/issues/7/timeline"*) printf '%s' '[]'; exit 0 ;;
  "pr list -R owner/repo --state merged"*) printf '%s' '[]'; exit 0 ;;
  "api repos/owner/repo --jq"*) printf '%s' '{"default_branch":"main","archived":false,"disabled":false}'; exit 0 ;;
  "api repos/owner/repo/branches"*) printf '%s' '[]'; exit 0 ;;
  "api repos/owner/repo/contents/CONTRIBUTING.md"*) printf '%s\n' 'Not Found' >&2; exit 1 ;;
  "api repos/owner/repo/contents/.github/CONTRIBUTING.md"*) printf '%s\n' 'Not Found' >&2; exit 1 ;;
  "api repos/owner/repo/contents/docs/CONTRIBUTING.md"*) printf '%s\n' 'Not Found' >&2; exit 1 ;;
  "pr list -R owner/repo --state closed"*) printf '%s' '[]'; exit 0 ;;
  "api repos/owner/repo/contents/.github/workflows"*) printf '%s\n' 'Not Found' >&2; exit 1 ;;
  "pr list -R owner/repo --state all"*) printf '%s' '[]'; exit 0 ;;
  *) printf 'unexpected args: %s\n' "$*" >&2; exit 1 ;;
esac
`)

	client := New(&config.Config{})
	data, err := client.CollectScoutData(context.Background(), "owner/repo", 7, "panic in parser")
	if err == nil {
		t.Fatal("CollectScoutData() error = nil, want critical fetch failure")
	}
	if !strings.Contains(err.Error(), "Open PRs Matching This Issue") {
		t.Fatalf("error = %q, want competing PR context", err.Error())
	}

	formatted := data.Format()
	if !strings.Contains(formatted, "### Open PRs Matching This Issue\nUNKNOWN: fetch failed.") {
		t.Fatalf("failed PR query was not rendered as UNKNOWN:\n%s", formatted)
	}
	if strings.Contains(formatted, "### Open PRs Matching This Issue\nNo data found.") {
		t.Fatalf("failed PR query was rendered as no data:\n%s", formatted)
	}
}
