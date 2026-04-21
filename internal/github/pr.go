package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// CIResult holds detailed CI check results.
type CIResult struct {
	Status       string   // "success", "failure", "pending", "unknown"
	FailedChecks []string // names of failed checks
	CodeFailures bool     // true if any non-metadata check failed
}

// PRReview represents a single review on a PR.
type PRReview struct {
	Author      string `json:"author"`
	State       string `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	Body        string `json:"body"`
	SubmittedAt string `json:"submittedAt"`
}

// PRReviewComment represents an inline review comment.
type PRReviewComment struct {
	ID        int64  `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	CreatedAt string `json:"createdAt"`
}

// PRInfo bundles state + reviews from a single gh call.
type PRInfo struct {
	State     string     `json:"state"`
	IsDraft   bool       `json:"isDraft"`
	Reviews   []PRReview `json:"reviews"`
	CreatedAt string     `json:"createdAt"` // RFC3339, authoritative GitHub PR open time
	MergedAt  string     `json:"mergedAt"`  // RFC3339, set when state=MERGED
	ClosedAt  string     `json:"closedAt"`  // RFC3339, set when state=CLOSED or MERGED
}

// GetPRInfo fetches state and reviews in one gh call to avoid rate limiting.
func (c *Client) GetPRInfo(ctx context.Context, repo string, prNum int) (*PRInfo, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view",
		fmt.Sprintf("%d", prNum),
		"-R", repo,
		"--json", "state,isDraft,reviews,createdAt,mergedAt,closedAt")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr view %s#%d: %s", repo, prNum, stderr.String())
	}

	// Parse with nested author object
	var raw struct {
		State     string `json:"state"`
		IsDraft   bool   `json:"isDraft"`
		CreatedAt string `json:"createdAt"`
		MergedAt  string `json:"mergedAt"`
		ClosedAt  string `json:"closedAt"`
		Reviews   []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			State       string `json:"state"`
			Body        string `json:"body"`
			SubmittedAt string `json:"submittedAt"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("parse PR info: %w", err)
	}

	info := &PRInfo{State: raw.State, IsDraft: raw.IsDraft, CreatedAt: raw.CreatedAt, MergedAt: raw.MergedAt, ClosedAt: raw.ClosedAt}
	for _, r := range raw.Reviews {
		info.Reviews = append(info.Reviews, PRReview{
			Author:      r.Author.Login,
			State:       r.State,
			Body:        r.Body,
			SubmittedAt: r.SubmittedAt,
		})
	}
	return info, nil
}

// GetPRStatus gets the CI status of a pull request
func (c *Client) GetPRStatus(ctx context.Context, repoFullName string, prNum int) (string, error) {
	r := c.GetCIResult(ctx, repoFullName, prNum)
	return r.Status, nil
}

// GetCIResult returns detailed CI check status with failure classification.
func (c *Client) GetCIResult(ctx context.Context, repoFullName string, prNum int) *CIResult {
	cmd := exec.CommandContext(ctx, "gh", "pr", "checks",
		fmt.Sprintf("%d", prNum),
		"--repo", repoFullName,
		"--json", "name,state")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// gh pr checks exits non-zero when any check fails, but still writes valid JSON to stdout.
	// Only treat it as unreadable when there is no output at all.
	output, err := cmd.Output()
	if err != nil && len(output) == 0 {
		// "no checks reported" means the repo has no CI configured — treat as success
		// so draft PRs in check-free repos can still be promoted.
		if noChecksConfigured(stderr.String()) {
			return &CIResult{Status: "success"}
		}
		return &CIResult{Status: "unknown"}
	}
	return parseChecksOutput(output)
}

// noChecksConfigured returns true when the gh CLI stderr indicates that the PR
// has no CI checks attached (as opposed to a genuine command error).
func noChecksConfigured(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "no checks reported")
}

// parseChecksOutput parses raw JSON from "gh pr checks --json name,state" into a CIResult.
// Returns Status="unknown" on any parse failure so callers can distinguish unreadable
// output from a genuine empty-checks (no CI) result.
func parseChecksOutput(data []byte) *CIResult {
	var checks []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &checks); err != nil {
		return &CIResult{Status: "unknown"}
	}

	result := &CIResult{}
	hasCodePending := false
	hasUnknownState := false
	for _, check := range checks {
		switch check.State {
		case "FAILURE", "ERROR", "ACTION_REQUIRED", "TIMED_OUT", "STARTUP_FAILURE", "CANCELLED":
			result.FailedChecks = append(result.FailedChecks, check.Name)
			if !isMetadataCheck(check.Name) {
				result.CodeFailures = true
			}
		case "PENDING", "QUEUED", "IN_PROGRESS", "REQUESTED", "WAITING", "EXPECTED":
			if !isMetadataCheck(check.Name) {
				hasCodePending = true
			}
		case "SUCCESS", "SKIPPED", "NEUTRAL", "STALE":
			// explicitly passing — no action needed
		default:
			hasUnknownState = true
		}
	}

	switch {
	case len(result.FailedChecks) > 0:
		result.Status = "failure"
	case hasCodePending || hasUnknownState:
		result.Status = "pending"
	default:
		result.Status = "success"
	}
	return result
}

// GetPRReviewComments fetches inline review comments for a PR.
func (c *Client) GetPRReviewComments(ctx context.Context, repo string, prNum int) ([]PRReviewComment, error) {
	output, err := c.ghAPI(ctx, fmt.Sprintf("repos/%s/pulls/%d/comments", repo, prNum))
	if err != nil {
		return nil, fmt.Errorf("get PR comments: %w", err)
	}

	var raw []struct {
		ID        int64  `json:"id"`
		Body      string `json:"body"`
		Path      string `json:"path"`
		Line      *int   `json:"line"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("parse comments: %w", err)
	}

	var comments []PRReviewComment
	for _, r := range raw {
		line := 0
		if r.Line != nil {
			line = *r.Line
		}
		comments = append(comments, PRReviewComment{
			ID:        r.ID,
			Author:    r.User.Login,
			Body:      r.Body,
			Path:      r.Path,
			Line:      line,
			CreatedAt: r.CreatedAt,
		})
	}
	return comments, nil
}

// IssueComment represents a comment on the PR's issue thread (not inline review comment).
type IssueComment struct {
	ID        int64
	Author    string
	Body      string
	CreatedAt string
}

// GetPRIssueComments fetches issue-level comments for a PR (used by bots like CLA assistant).
func (c *Client) GetPRIssueComments(ctx context.Context, repo string, prNum int) ([]IssueComment, error) {
	output, err := c.ghAPI(ctx, fmt.Sprintf("repos/%s/issues/%d/comments", repo, prNum))
	if err != nil {
		return nil, fmt.Errorf("get issue comments: %w", err)
	}

	var raw []struct {
		ID        int64  `json:"id"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("parse issue comments: %w", err)
	}

	var comments []IssueComment
	for _, r := range raw {
		comments = append(comments, IssueComment{
			ID:        r.ID,
			Author:    r.User.Login,
			Body:      r.Body,
			CreatedAt: r.CreatedAt,
		})
	}
	return comments, nil
}

// ReviewThread maps a review thread's GraphQL ID to its first comment's REST database ID.
type ReviewThread struct {
	ThreadID   string // GraphQL node ID (e.g., "PRRT_kwDO...")
	CommentID  int64  // REST API database ID of the first comment
	IsResolved bool
}

// GetReviewThreads fetches unresolved review thread IDs mapped to comment database IDs.
func (c *Client) GetReviewThreads(ctx context.Context, repo string, prNum int) ([]ReviewThread, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo format: %s", repo)
	}

	query := fmt.Sprintf(`query {
		repository(owner:"%s", name:"%s") {
			pullRequest(number:%d) {
				reviewThreads(first:50) {
					nodes {
						id
						isResolved
						comments(first:1) {
							nodes { databaseId }
						}
					}
				}
			}
		}
	}`, parts[0], parts[1], prNum)

	output, err := c.ghAPI(ctx, "graphql", "-f", fmt.Sprintf("query=%s", query))
	if err != nil {
		return nil, fmt.Errorf("get review threads: %w", err)
	}

	var resp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int64 `json:"databaseId"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, fmt.Errorf("parse review threads: %w", err)
	}

	var threads []ReviewThread
	for _, n := range resp.Data.Repository.PullRequest.ReviewThreads.Nodes {
		if len(n.Comments.Nodes) == 0 {
			continue
		}
		threads = append(threads, ReviewThread{
			ThreadID:   n.ID,
			CommentID:  n.Comments.Nodes[0].DatabaseID,
			IsResolved: n.IsResolved,
		})
	}
	return threads, nil
}

// ResolveReviewThread marks a review thread as resolved via GraphQL.
func (c *Client) ResolveReviewThread(ctx context.Context, threadID string) error {
	query := fmt.Sprintf(`mutation {
		resolveReviewThread(input:{threadId:"%s"}) {
			thread { isResolved }
		}
	}`, threadID)

	_, err := c.ghAPI(ctx, "graphql", "-f", fmt.Sprintf("query=%s", query))
	if err != nil {
		return fmt.Errorf("resolve thread %s: %w", threadID, err)
	}
	return nil
}

// ReplyToPRComment posts a reply to a PR review comment.
func (c *Client) ReplyToPRComment(ctx context.Context, repo string, prNum int, commentID int64, body string) error {
	_, err := c.ghAPI(ctx,
		fmt.Sprintf("repos/%s/pulls/%d/comments/%d/replies", repo, prNum, commentID),
		"-f", fmt.Sprintf("body=%s", body))
	if err != nil {
		return fmt.Errorf("reply to comment %d: %w", commentID, err)
	}
	return nil
}

// CreatePullRequest creates a new pull request using gh
func (c *Client) CreatePullRequest(ctx context.Context, repoFullName, title, body, head, base string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "create",
		"--repo", repoFullName,
		"--title", title,
		"--body", body,
		"--head", head,
		"--base", base)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Include the actual error message from gh CLI
		return "", 0, fmt.Errorf("create PR: %s - %w", strings.TrimSpace(string(output)), err)
	}

	// Output is the PR URL
	prURL := strings.TrimSpace(string(output))

	// Extract PR number from URL
	parts := strings.Split(prURL, "/")
	prNum := 0
	if len(parts) > 0 {
		fmt.Sscanf(parts[len(parts)-1], "%d", &prNum)
	}

	return prURL, prNum, nil
}

// MarkPRReady converts a draft PR to ready for review.
func (c *Client) MarkPRReady(ctx context.Context, repo string, prNum int) error {
	cmd := exec.CommandContext(ctx, "gh", "pr", "ready",
		fmt.Sprintf("%d", prNum),
		"-R", repo)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr ready %s#%d: %s - %w", repo, prNum, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// GitHubPR represents an open PR discovered from GitHub.
type GitHubPR struct {
	Repo       string `json:"repo"` // owner/repo
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	URL        string `json:"url"`
	BranchName string `json:"headRefName"`
	State      string `json:"state"`
}

// ListUserOpenPRs returns all open PRs authored by the configured user.
func (c *Client) ListUserOpenPRs(ctx context.Context) ([]GitHubPR, error) {
	username := c.config.GitHubUsername
	if username == "" {
		var err error
		username, err = c.GetUsername(ctx)
		if err != nil {
			return nil, fmt.Errorf("get username: %w", err)
		}
	}

	cmd := exec.CommandContext(ctx, "gh", "search", "prs",
		"--author", username,
		"--state", "open",
		"--limit", "50",
		"--json", "repository,number,title,body,url",
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("search open PRs: %w", err)
	}

	var raw []struct {
		Repository struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("parse PR list: %w", err)
	}

	var prs []GitHubPR
	for _, r := range raw {
		prs = append(prs, GitHubPR{
			Repo:   r.Repository.NameWithOwner,
			Number: r.Number,
			Title:  r.Title,
			Body:   r.Body,
			URL:    r.URL,
		})
	}
	return prs, nil
}

// ClosePR closes a pull request with an optional comment.
func (c *Client) ClosePR(ctx context.Context, repo string, prNum int, comment string) error {
	if comment != "" {
		cmd := exec.CommandContext(ctx, "gh", "pr", "close",
			fmt.Sprintf("%d", prNum),
			"-R", repo,
			"-c", comment)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("gh pr close %s#%d: %s - %w", repo, prNum, strings.TrimSpace(string(output)), err)
		}
		return nil
	}
	cmd := exec.CommandContext(ctx, "gh", "pr", "close",
		fmt.Sprintf("%d", prNum),
		"-R", repo)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr close %s#%d: %s - %w", repo, prNum, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// isMetadataCheck returns true for CI checks that validate PR metadata
// (title, description, labels, DCO) rather than code correctness.
func isMetadataCheck(name string) bool {
	lower := strings.ToLower(name)
	metadataPatterns := []string{
		"description", "title", "label", "dco",
		"conventional commit", "semantic", "changelog",
		"deploy/", "netlify", "vercel", "pages changed", "redirect", "header rules",
		"branch stack", "wip", "cla", "license", "stale", "codecov",
	}
	for _, p := range metadataPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
