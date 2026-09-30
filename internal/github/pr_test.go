package github

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestGetPRReviewCommentsReadsAllPages(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
case "$*" in
  "api repos/owner/repo/pulls/42/comments --paginate --slurp")
    printf '%s' '[[{"id":1,"user":{"login":"contributor"},"body":"fixed the incorrect logic","path":"main.go","line":null,"created_at":"2026-01-01T01:00:00Z"}],[],[{"id":2,"user":{"login":"maintainer"},"body":"Please close this PR; these changes are out of scope.","path":"later.go","line":12,"created_at":"2026-01-02T01:00:00Z"}]]' ;;
  *) printf '%s' '[{"id":1,"user":{"login":"contributor"}}]' ;;
esac
`)
	comments, err := (&Client{}).GetPRReviewComments(context.Background(), "owner/repo", 42)
	if err != nil {
		t.Fatal(err)
	}
	want := []PRReviewComment{
		{ID: 1, Author: "contributor", Body: "fixed the incorrect logic", Path: "main.go", CreatedAt: "2026-01-01T01:00:00Z"},
		{ID: 2, Author: "maintainer", Body: "Please close this PR; these changes are out of scope.", Path: "later.go", Line: 12, CreatedAt: "2026-01-02T01:00:00Z"},
	}
	if !reflect.DeepEqual(comments, want) {
		t.Fatalf("comments = %+v, want all ordered pages %+v", comments, want)
	}
}

func TestGetPRReviewCommentsPageErrorsReturnNoComments(t *testing.T) {
	for _, tc := range []struct {
		name, output, message string
		exitCode              string
	}{
		{"later command failure", `[[{"id":1}]]`, "get PR comments", "1"},
		{"later malformed page", `[[{"id":1}],invalid]`, "parse comments", "0"},
		{"later wrong shape", `[[{"id":1}],{"message":"unavailable"}]`, "parse comments", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeGH(t, `#!/bin/sh
printf '%s' "$GH_TEST_PAGES"
printf 'later page unavailable\n' >&2
exit "$GH_TEST_EXIT"
`)
			t.Setenv("GH_TEST_PAGES", tc.output)
			t.Setenv("GH_TEST_EXIT", tc.exitCode)
			comments, err := (&Client{}).GetPRReviewComments(context.Background(), "owner/repo", 42)
			if comments != nil || err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("comments = %+v, error = %v, want nil and %q error", comments, err, tc.message)
			}
			if tc.exitCode == "1" {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || !strings.Contains(err.Error(), "later page unavailable") {
					t.Fatalf("command error contract lost: %v", err)
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
