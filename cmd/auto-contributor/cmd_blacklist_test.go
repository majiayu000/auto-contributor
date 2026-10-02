package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/internal/pipeline"
	"github.com/majiayu000/auto-contributor/pkg/logger"
	"github.com/majiayu000/auto-contributor/pkg/models"
	"github.com/spf13/cobra"
)

func setupBlacklistTest(t *testing.T, state string) (*bytes.Buffer, string) {
	t.Helper()
	oldCfg, oldDB, oldGH := cfg, database, ghClient
	oldOutput := logger.GetLogger().Out
	t.Cleanup(func() {
		cfg, database, ghClient = oldCfg, oldDB, oldGH
		logger.GetLogger().SetOutput(oldOutput)
	})

	var err error
	database, err = db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create test DB: %v", err)
	}
	testDB := database
	t.Cleanup(func() { _ = testDB.Close() })
	cfg = &config.Config{GitHubUsername: "contributor", RulesDir: t.TempDir()}
	ghClient = github.New(cfg)
	logs := &bytes.Buffer{}
	logger.GetLogger().SetOutput(logs)

	switch state {
	case "blacklisted":
		if err := database.AddToBlacklist("upstream/project", "test ban"); err != nil {
			t.Fatalf("blacklist test repo: %v", err)
		}
	case "unavailable":
		if _, err := database.Exec("DROP TABLE blacklist"); err != nil {
			t.Fatalf("drop blacklist table: %v", err)
		}
	}

	dir := t.TempDir()
	ghLog := filepath.Join(dir, "gh.log")
	t.Setenv("BLACKLIST_TEST_GH_LOG", ghLog)
	t.Setenv("BLACKLIST_TEST_PRS", "[]")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BLACKLIST_TEST_GH_LOG"
case "$1 $2" in
  "search issues") printf '%s' '[{"number":7,"title":"test issue","repository":{"nameWithOwner":"upstream/project"}}]' ;;
  "pr list") printf '%s' '[]' ;;
  "repo view") printf '%s' '{"stargazerCount":1000,"primaryLanguage":{"name":"Go"},"defaultBranchRef":{"name":"main"}}' ;;
  "api repos/upstream/project/contents/"*) exit 1 ;;
  "search prs") printf '%s' "$BLACKLIST_TEST_PRS" ;;
  "pr view") printf '%s' '{"state":"OPEN","headRefName":"fix/test"}' ;;
  *) printf 'unexpected gh args: %s\n' "$*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logs, ghLog
}

func assertBlacklistFailureLogged(t *testing.T, state string, logs *bytes.Buffer) {
	t.Helper()
	if state == "unavailable" {
		for _, want := range []string{"blacklist check failed", "upstream/project", "no such table: blacklist"} {
			if !strings.Contains(logs.String(), want) {
				t.Errorf("logs missing %q: %s", want, logs)
			}
		}
	}
}

func TestDiscoverIssuesBlacklist(t *testing.T) {
	for _, state := range []string{"allowed", "blacklisted", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			logs, _ := setupBlacklistTest(t, state)
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			cmd.Flags().Int("limit", 1, "")
			if err := discoverIssues(cmd, nil); err != nil {
				t.Fatalf("discover issues: %v", err)
			}
			var count int
			if err := database.QueryRow("SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
				t.Fatalf("count issues: %v", err)
			}
			want := 0
			if state == "allowed" {
				want = 1
			}
			if count != want {
				t.Errorf("saved issues = %d, want %d", count, want)
			}
			assertBlacklistFailureLogged(t, state, logs)
		})
	}
}

func TestSyncOpenPRsBlacklist(t *testing.T) {
	for _, state := range []string{"allowed", "blacklisted", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			logs, _ := setupBlacklistTest(t, state)
			t.Setenv("BLACKLIST_TEST_PRS", `[{"repository":{"nameWithOwner":"upstream/project"},"number":42,"title":"test PR","url":"https://github.com/upstream/project/pull/42","headRefName":"fix/test"}]`)
			pipe, err := pipeline.New(cfg, database, ghClient, t.TempDir())
			if err != nil {
				t.Fatalf("create pipeline: %v", err)
			}
			checkOpenPRFeedback(context.Background(), pipe)
			for _, table := range []string{"pull_requests", "issues"} {
				var count int
				if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
					t.Fatalf("count %s: %v", table, err)
				}
				want := 0
				if state == "allowed" {
					want = 1
				}
				if count != want {
					t.Errorf("synced %s = %d, want %d", table, count, want)
				}
			}
			assertBlacklistFailureLogged(t, state, logs)
		})
	}
}

func TestOpenPRFeedbackBlacklist(t *testing.T) {
	for _, state := range []string{"allowed", "blacklisted", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			logs, ghLog := setupBlacklistTest(t, state)
			pr, err := database.EnsurePRWithIssue("upstream/project", 42, "https://github.com/upstream/project/pull/42", "fix/test", "test PR", "")
			if err != nil {
				t.Fatalf("seed tracked PR: %v", err)
			}
			// This status fetches PR state without dispatching an agent.
			if err := database.UpdatePRStatus(pr.ID, models.PRStatusNeedsAttention); err != nil {
				t.Fatalf("update tracked PR: %v", err)
			}
			pipe, err := pipeline.New(cfg, database, ghClient, t.TempDir())
			if err != nil {
				t.Fatalf("create pipeline: %v", err)
			}
			checkOpenPRFeedback(context.Background(), pipe)
			calls, err := os.ReadFile(ghLog)
			if err != nil {
				t.Fatalf("read gh calls: %v", err)
			}
			processed := strings.Contains(string(calls), "pr view 42 -R upstream/project")
			if processed != (state == "allowed") {
				t.Errorf("feedback processed = %v, want %v; calls:\n%s", processed, state == "allowed", calls)
			}
			assertBlacklistFailureLogged(t, state, logs)
		})
	}
}
