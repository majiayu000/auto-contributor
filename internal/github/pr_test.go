package github

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/majiayu000/auto-contributor/internal/config"
)

func TestGetPRIssueCommentsPagination(t *testing.T) {
	firstPage := `[{"id":1,"body":"Please sign the CLA.","created_at":"2026-09-29T00:00:00Z","user":{"login":"cla-assistant[bot]"}}]`
	for _, tc := range []struct {
		name  string
		pages string
		want  []IssueComment
	}{
		{"empty", `[[]]`, nil},
		{"single page", `[` + firstPage + `]`, []IssueComment{{ID: 1, Author: "cla-assistant[bot]", Body: "Please sign the CLA.", CreatedAt: "2026-09-29T00:00:00Z"}}},
		{"multiple pages", `[` + firstPage + `,[{"id":2,"body":"Maintainer feedback","created_at":"2026-09-30T00:00:00Z","user":{"login":"maintainer"}}],[{"id":3,"body":"All contributors have signed the CLA.","created_at":"2026-09-30T01:00:00Z","user":{"login":"cla-assistant[bot]"}}]]`, []IssueComment{
			{ID: 1, Author: "cla-assistant[bot]", Body: "Please sign the CLA.", CreatedAt: "2026-09-29T00:00:00Z"},
			{ID: 2, Author: "maintainer", Body: "Maintainer feedback", CreatedAt: "2026-09-30T00:00:00Z"},
			{ID: 3, Author: "cla-assistant[bot]", Body: "All contributors have signed the CLA.", CreatedAt: "2026-09-30T01:00:00Z"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeGH(t, `#!/bin/sh
case "$*" in
  "api repos/owner/repo/issues/42/comments") printf '%s' "$GH_TEST_FIRST_PAGE" ;;
  "api repos/owner/repo/issues/42/comments --paginate --slurp") printf '%s' "$GH_TEST_COMMENT_PAGES" ;;
  *) printf 'unexpected args: %s\n' "$*" >&2; exit 1 ;;
esac
`)
			t.Setenv("GH_TEST_FIRST_PAGE", firstPage)
			if tc.name == "empty" {
				t.Setenv("GH_TEST_FIRST_PAGE", "[]")
			}
			t.Setenv("GH_TEST_COMMENT_PAGES", tc.pages)
			comments, err := New(&config.Config{}).GetPRIssueComments(context.Background(), "owner/repo", 42)
			if err != nil {
				t.Fatalf("GetPRIssueComments: %v", err)
			}
			if !reflect.DeepEqual(comments, tc.want) {
				t.Fatalf("comments = %+v, want %+v", comments, tc.want)
			}
		})
	}
}

func TestGetPRIssueCommentsPaginationErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		output    string
		fail      string
		wantError string
	}{
		{"later page fetch failure", `[[{"id":1}]]`, "1", "get issue comments"},
		{"invalid later page", `[[{"id":1}],{"message":"unexpected response"}]`, "0", "parse issue comments"},
		{"truncated later page", `[[{"id":1}],[`, "0", "parse issue comments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeGH(t, `#!/bin/sh
printf '%s' "$GH_TEST_COMMENT_PAGES"
if [ "$GH_TEST_COMMENT_FAIL" = "1" ]; then
  printf 'later page fetch failed\n' >&2
  exit 1
fi
`)
			t.Setenv("GH_TEST_COMMENT_PAGES", tc.output)
			t.Setenv("GH_TEST_COMMENT_FAIL", tc.fail)
			comments, err := New(&config.Config{}).GetPRIssueComments(context.Background(), "owner/repo", 42)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || comments != nil {
				t.Fatalf("comments/error = %+v/%v, want nil/%q", comments, err, tc.wantError)
			}
			if tc.fail == "1" {
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) || !strings.Contains(err.Error(), "later page fetch failed") {
					t.Fatalf("fetch error lost CLI cause or stderr: %v", err)
				}
			}
		})
	}
}

func TestParsePRInfoOutput_PopulatesLockReason(t *testing.T) {
	data := []byte(`{
		"state":"CLOSED",
		"isDraft":false,
		"lockReason":"SPAM",
		"createdAt":"2026-04-01T00:00:00Z",
		"mergedAt":"",
		"closedAt":"2026-04-02T00:00:00Z",
		"reviews":[
			{
				"author":{"login":"reviewer1"},
				"state":"COMMENTED",
				"body":"needs work",
				"submittedAt":"2026-04-01T01:00:00Z"
			}
		]
	}`)

	info, err := parsePRInfoOutput(data)
	if err != nil {
		t.Fatalf("parsePRInfoOutput() error = %v", err)
	}
	if info.LockReason != "SPAM" {
		t.Fatalf("LockReason = %q, want %q", info.LockReason, "SPAM")
	}
	if len(info.Reviews) != 1 {
		t.Fatalf("len(Reviews) = %d, want 1", len(info.Reviews))
	}
	if info.Reviews[0].Author != "reviewer1" {
		t.Fatalf("Reviews[0].Author = %q, want %q", info.Reviews[0].Author, "reviewer1")
	}
}
