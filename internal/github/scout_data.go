package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

const maxScoutFieldBytes = 8000

var assignmentCommentPattern = regexp.MustCompile(`(?i)not assigned|must be assigned|require.*assign`)

// ScoutData holds pre-collected data for the Scout agent to avoid tool calls.
type ScoutData struct {
	IssueComments       ScoutDataField
	CompetingPRs        ScoutDataField
	IssueTimeline       ScoutDataField
	MergedPRs           ScoutDataField
	RepoMeta            ScoutDataField
	Branches            ScoutDataField
	ContributingExcerpt ScoutDataField
	ClosedPRComments    ScoutDataField
	WorkflowNames       ScoutDataField
	RecentPRAuthors     ScoutDataField
}

// ScoutDataField separates confirmed empty data from fetch failures.
type ScoutDataField struct {
	Data    string
	Failure string
}

// CollectScoutData pre-fetches the data the Scout agent needs via gh CLI.
func (c *Client) CollectScoutData(ctx context.Context, repo string, issueNum int, issueTitle string) (*ScoutData, error) {
	d := &ScoutData{}
	var failures []error

	collect := func(name string, field *ScoutDataField, args ...string) {
		*field = collectScoutCommand(ctx, args...)
		if field.Failure != "" {
			failures = append(failures, fmt.Errorf("%s: %s", name, field.Failure))
		}
	}
	collectAllowNotFound := func(name string, field *ScoutDataField, args ...string) {
		*field = collectScoutCommandAllowNotFound(ctx, args...)
		if field.Failure != "" {
			failures = append(failures, fmt.Errorf("%s: %s", name, field.Failure))
		}
	}

	collect("Issue Comments", &d.IssueComments,
		"api",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, issueNum),
		"--jq", `[.[] | {author: .user.login, body: .body, created: .created_at}]`)

	d.CompetingPRs = collectCompetingPRs(ctx, repo, issueNum, issueTitle)
	if d.CompetingPRs.Failure != "" {
		failures = append(failures, fmt.Errorf("Open PRs Matching This Issue: %s", d.CompetingPRs.Failure))
	}

	collect("Issue Timeline", &d.IssueTimeline,
		"api",
		fmt.Sprintf("repos/%s/issues/%d/timeline", repo, issueNum),
		"--jq", `[.[] | select(.event == "cross-referenced" or .event == "referenced") | {event: .event, pr_url: .source.issue.pull_request.html_url, source_title: .source.issue.title}]`)

	collect("Recent Merged PRs", &d.MergedPRs,
		"pr", "list",
		"-R", repo,
		"--state", "merged",
		"--limit", "20",
		"--json", "number,author,mergedAt")

	collect("Repo Metadata", &d.RepoMeta,
		"api",
		fmt.Sprintf("repos/%s", repo),
		"--jq", `{default_branch: .default_branch, archived: .archived, disabled: .disabled}`)

	collect("Dev/Develop Branches", &d.Branches,
		"api",
		fmt.Sprintf("repos/%s/branches", repo),
		"--jq", `[.[] | select(.name | test("^(dev|develop|next|staging)$")) | .name]`)

	d.ContributingExcerpt = collectContributingExcerpt(ctx, repo)
	if d.ContributingExcerpt.Failure != "" {
		failures = append(failures, fmt.Errorf("CONTRIBUTING.md: %s", d.ContributingExcerpt.Failure))
	}

	d.ClosedPRComments = collectClosedPRAssignmentComments(ctx, repo)
	if d.ClosedPRComments.Failure != "" {
		failures = append(failures, fmt.Errorf("Closed PR Assignment Enforcement: %s", d.ClosedPRComments.Failure))
	}

	collectAllowNotFound("GitHub Workflow Files", &d.WorkflowNames,
		"api",
		fmt.Sprintf("repos/%s/contents/.github/workflows", repo),
		"--jq", `[.[].name]`)

	collect("Recent PR Authors", &d.RecentPRAuthors,
		"pr", "list",
		"-R", repo,
		"--state", "all",
		"--limit", "10",
		"--json", "number,author,state")

	if len(failures) > 0 {
		return d, errors.Join(failures...)
	}
	return d, nil
}

// Format returns the pre-collected data as a prompt section.
func (d *ScoutData) Format() string {
	var sb strings.Builder
	sb.WriteString("## Pre-collected Data (DO NOT re-run these commands)\n\n")
	sb.WriteString("Successful empty sections mean the query returned no matching data. Sections marked UNKNOWN are fetch failures and must not be treated as absence of risk.\n\n")

	section := func(title string, field ScoutDataField, emptyMessage string) {
		if field.Failure != "" {
			sb.WriteString(fmt.Sprintf("### %s\nUNKNOWN: fetch failed. Do not treat this as verified absence.\nError: %s\n\n", title, field.Failure))
			return
		}
		if isEmptyScoutData(field.Data) {
			sb.WriteString(fmt.Sprintf("### %s\n%s\n\n", title, emptyMessage))
			return
		}
		sb.WriteString(fmt.Sprintf("### %s\n```json\n%s\n```\n\n", title, field.Data))
	}

	section("Issue Comments", d.IssueComments, "No data found.")
	section("Open PRs Matching This Issue", d.CompetingPRs, "No data found.")
	section("Issue Timeline (Cross-references)", d.IssueTimeline, "No data found.")
	section("Recent Merged PRs (last 20)", d.MergedPRs, "No data found.")
	section("Repo Metadata", d.RepoMeta, "No data found.")
	section("Dev/Develop Branches", d.Branches, "No data found.")
	section("CONTRIBUTING.md", d.ContributingExcerpt, "No assignment-related content found.")
	section("Closed PR Assignment Enforcement", d.ClosedPRComments, "No data found.")
	section("GitHub Workflow Files", d.WorkflowNames, "No data found.")
	section("Recent PR Authors", d.RecentPRAuthors, "No data found.")

	return sb.String()
}

func collectCompetingPRs(ctx context.Context, repo string, issueNum int, issueTitle string) ScoutDataField {
	byNumber := collectScoutCommand(ctx,
		"pr", "list",
		"-R", repo,
		"--state", "open",
		"--search", fmt.Sprintf("%d in:title,body", issueNum),
		"--json", "number,title,author,updatedAt,url",
		"--limit", "10")

	byTitle := collectScoutCommand(ctx,
		"pr", "list",
		"-R", repo,
		"--state", "open",
		"--search", issueTitle,
		"--json", "number,title,author,updatedAt,url",
		"--limit", "5")

	var failures []string
	if byNumber.Failure != "" {
		failures = append(failures, "by issue number: "+byNumber.Failure)
	}
	if byTitle.Failure != "" {
		failures = append(failures, "by title: "+byTitle.Failure)
	}
	if len(failures) > 0 {
		return ScoutDataField{Failure: strings.Join(failures, "; ")}
	}

	data := byNumber.Data
	if !isEmptyScoutData(byTitle.Data) {
		if data != "" {
			data += "\n\nSearch by title:\n"
		}
		data += byTitle.Data
	}
	return ScoutDataField{Data: truncateScoutData(data)}
}

func collectContributingExcerpt(ctx context.Context, repo string) ScoutDataField {
	paths := []string{
		"CONTRIBUTING.md",
		".github/CONTRIBUTING.md",
		"docs/CONTRIBUTING.md",
	}
	var failures []string
	for _, path := range paths {
		content, err := runGHRaw(ctx, "api", fmt.Sprintf("repos/%s/contents/%s", repo, path), "--jq", ".content")
		if err != nil {
			if isNotFoundError(err) {
				continue
			}
			failures = append(failures, fmt.Sprintf("%s: %s", path, err.Error()))
			continue
		}
		if isEmptyScoutData(content) {
			continue
		}

		decoded, err := decodeScoutBase64(content)
		if err != nil {
			return ScoutDataField{Failure: fmt.Sprintf("%s: decode content: %s", path, err.Error())}
		}
		return ScoutDataField{Data: extractAssignmentLines(decoded)}
	}

	if len(failures) > 0 {
		return ScoutDataField{Failure: strings.Join(failures, "; ")}
	}
	return ScoutDataField{}
}

func collectClosedPRAssignmentComments(ctx context.Context, repo string) ScoutDataField {
	data, err := runGHRaw(ctx,
		"pr", "list",
		"-R", repo,
		"--state", "closed",
		"--limit", "5",
		"--json", "number,comments")
	if err != nil {
		return ScoutDataField{Failure: err.Error()}
	}

	filtered, err := extractClosedPRAssignmentComments(data)
	if err != nil {
		return ScoutDataField{Failure: err.Error()}
	}
	return ScoutDataField{Data: filtered}
}

func extractClosedPRAssignmentComments(data string) (string, error) {
	if isEmptyScoutData(data) {
		return "[]", nil
	}

	var prs []struct {
		Comments json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal([]byte(data), &prs); err != nil {
		return "", fmt.Errorf("parse closed PR comments: %w", err)
	}

	matches := make([]struct {
		Body string `json:"body"`
	}, 0)
	for _, pr := range prs {
		comments, err := parseScoutPRComments(pr.Comments)
		if err != nil {
			return "", err
		}
		for _, comment := range comments {
			if !assignmentCommentPattern.MatchString(comment.Body) {
				continue
			}
			body := comment.Body
			if len(body) > 200 {
				body = body[:200]
			}
			matches = append(matches, struct {
				Body string `json:"body"`
			}{Body: body})
		}
	}

	output, err := json.Marshal(matches)
	if err != nil {
		return "", fmt.Errorf("encode closed PR comments: %w", err)
	}
	return truncateScoutData(string(output)), nil
}

type scoutPRComment struct {
	Body string `json:"body"`
}

func parseScoutPRComments(raw json.RawMessage) ([]scoutPRComment, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var comments []scoutPRComment
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &comments); err != nil {
			return nil, fmt.Errorf("parse comments array: %w", err)
		}
		return comments, nil
	}

	var connection struct {
		Nodes []scoutPRComment `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &connection); err != nil {
		return nil, fmt.Errorf("parse comments connection: %w", err)
	}
	return connection.Nodes, nil
}

func extractAssignmentLines(content string) string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "assign") ||
			strings.Contains(lower, "claim") ||
			(strings.Contains(lower, "before") && strings.Contains(lower, "pr")) {
			lines = append(lines, line)
		}
	}
	return truncateScoutData(strings.Join(lines, "\n"))
}

func collectScoutCommand(ctx context.Context, args ...string) ScoutDataField {
	data, err := runGH(ctx, args...)
	if err != nil {
		return ScoutDataField{Failure: err.Error()}
	}
	return ScoutDataField{Data: data}
}

func collectScoutCommandAllowNotFound(ctx context.Context, args ...string) ScoutDataField {
	data, err := runGH(ctx, args...)
	if err != nil {
		if isNotFoundError(err) {
			return ScoutDataField{}
		}
		return ScoutDataField{Failure: err.Error()}
	}
	return ScoutDataField{Data: data}
}

func runGH(ctx context.Context, args ...string) (string, error) {
	data, err := runGHRaw(ctx, args...)
	if err != nil {
		return "", err
	}
	return truncateScoutData(data), nil
}

func runGHRaw(ctx context.Context, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func decodeScoutBase64(content string) (string, error) {
	clean := strings.ReplaceAll(content, "\\n", "")
	clean = strings.ReplaceAll(clean, "\n", "")
	clean = strings.ReplaceAll(clean, "\"", "")
	decoded, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func isEmptyScoutData(data string) bool {
	trimmed := strings.TrimSpace(data)
	return trimmed == "" || trimmed == "[]" || trimmed == "null"
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "http 404")
}

func truncateScoutData(data string) string {
	if len(data) <= maxScoutFieldBytes {
		return data
	}
	return data[:maxScoutFieldBytes] + "\n... (truncated)"
}
