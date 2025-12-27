package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// Client wraps the gh CLI for GitHub API calls
type Client struct {
	config *config.Config
}

// New creates a new GitHub client using gh CLI
func New(cfg *config.Config) *Client {
	return &Client{config: cfg}
}

// ghAPI executes a gh api command and returns the result
func (c *Client) ghAPI(ctx context.Context, endpoint string, args ...string) ([]byte, error) {
	cmdArgs := []string{"api", endpoint}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "gh", cmdArgs...)
	return cmd.Output()
}

// SearchResult represents GitHub search API response
type SearchResult struct {
	TotalCount int `json:"total_count"`
	Items      []struct {
		Number            int    `json:"number"`
		Title             string `json:"title"`
		Body              string `json:"body"`
		HTMLURL           string `json:"html_url"`
		PullRequest       *struct{} `json:"pull_request,omitempty"`
		Labels            []struct {
			Name string `json:"name"`
		} `json:"labels"`
		RepositoryURL     string `json:"repository_url"`
	} `json:"items"`
}

// SearchIssues searches for issues matching criteria
func (c *Client) SearchIssues(ctx context.Context, limit int) ([]*models.Issue, error) {
	query := c.buildSearchQuery()

	// Use gh search issues command (simpler)
	cmd := exec.CommandContext(ctx, "gh", "search", "issues",
		"--limit", fmt.Sprintf("%d", limit),
		"--json", "number,title,body,url,labels,repository",
		"--", query)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh search issues: %w", err)
	}

	var results []struct {
		Number     int    `json:"number"`
		Title      string `json:"title"`
		Body       string `json:"body"`
		URL        string `json:"url"`
		Labels     []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Repository struct {
			Name          string `json:"name"`
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
	}

	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("parse results: %w", err)
	}

	var issues []*models.Issue
	for _, r := range results {
		repo := r.Repository.NameWithOwner

		// Check if repo is excluded
		if c.isExcludedRepo(repo) {
			continue
		}

		// Check if issue already has a linked PR (skip if so)
		hasPR, err := c.HasExistingPR(ctx, repo, r.Number)
		if err == nil && hasPR {
			continue // Skip issues that already have PRs
		}

		// Get repo info
		repoInfo, err := c.GetRepoInfo(ctx, repo)
		if err != nil {
			continue
		}

		// Filter by stars
		if repoInfo.Stars < c.config.MinRepoStars {
			continue
		}

		// Collect labels
		var labels []string
		for _, label := range r.Labels {
			labels = append(labels, label.Name)
		}

		issue := &models.Issue{
			Repo:            repo,
			IssueNumber:     r.Number,
			Title:           r.Title,
			Body:            r.Body,
			Labels:          strings.Join(labels, ","),
			Language:        repoInfo.Language,
			DifficultyScore: c.estimateDifficulty(labels, repoInfo),
			Status:          models.IssueStatusDiscovered,
			DiscoveredAt:    time.Now(),
			UpdatedAt:       time.Now(),
		}

		issues = append(issues, issue)
	}

	return issues, nil
}

// buildSearchQuery constructs the GitHub search query
func (c *Client) buildSearchQuery() string {
	var parts []string

	// gh search uses freeform query, add key terms
	if len(c.config.IncludeLabels) > 0 {
		parts = append(parts, c.config.IncludeLabels[0]) // e.g., "good first issue"
	}

	// Add language filter
	if len(c.config.Languages) > 0 {
		parts = append(parts, fmt.Sprintf("language:%s", c.config.Languages[0]))
	}

	return strings.Join(parts, " ")
}

// RepoInfo holds repository metadata
type RepoInfo struct {
	Stars           int
	Language        string
	HasContributing bool
	HasClaudeMD     bool
	TestFramework   string
}

// GetRepoInfo fetches repository information using gh
func (c *Client) GetRepoInfo(ctx context.Context, repoFullName string) (*RepoInfo, error) {
	cmd := exec.CommandContext(ctx, "gh", "repo", "view", repoFullName,
		"--json", "stargazerCount,primaryLanguage")

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var result struct {
		StargazerCount  int `json:"stargazerCount"`
		PrimaryLanguage struct {
			Name string `json:"name"`
		} `json:"primaryLanguage"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return nil, err
	}

	info := &RepoInfo{
		Stars:    result.StargazerCount,
		Language: result.PrimaryLanguage.Name,
	}

	// Check for CONTRIBUTING.md
	checkCmd := exec.CommandContext(ctx, "gh", "api",
		fmt.Sprintf("repos/%s/contents/CONTRIBUTING.md", repoFullName),
		"--silent")
	info.HasContributing = checkCmd.Run() == nil

	// Check for CLAUDE.md
	checkCmd = exec.CommandContext(ctx, "gh", "api",
		fmt.Sprintf("repos/%s/contents/CLAUDE.md", repoFullName),
		"--silent")
	info.HasClaudeMD = checkCmd.Run() == nil

	return info, nil
}

// GetIssue fetches a single issue
func (c *Client) GetIssue(ctx context.Context, repoFullName string, issueNum int) (*models.Issue, error) {
	cmd := exec.CommandContext(ctx, "gh", "issue", "view",
		fmt.Sprintf("%d", issueNum),
		"--repo", repoFullName,
		"--json", "number,title,body,labels")

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var result struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return nil, err
	}

	var labels []string
	for _, l := range result.Labels {
		labels = append(labels, l.Name)
	}

	return &models.Issue{
		Repo:        repoFullName,
		IssueNumber: result.Number,
		Title:       result.Title,
		Body:        result.Body,
		Labels:      strings.Join(labels, ","),
	}, nil
}

// HasExistingPR checks if an issue already has a PR
func (c *Client) HasExistingPR(ctx context.Context, repoFullName string, issueNum int) (bool, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--repo", repoFullName,
		"--search", fmt.Sprintf("%d", issueNum),
		"--json", "number",
		"--limit", "1")

	output, err := cmd.Output()
	if err != nil {
		return false, err
	}

	var prs []struct {
		Number int `json:"number"`
	}
	json.Unmarshal(output, &prs)

	return len(prs) > 0, nil
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

// GetPRStatus gets the CI status of a pull request
func (c *Client) GetPRStatus(ctx context.Context, repoFullName string, prNum int) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "checks",
		fmt.Sprintf("%d", prNum),
		"--repo", repoFullName,
		"--json", "state")

	output, err := cmd.Output()
	if err != nil {
		return "unknown", nil
	}

	var checks []struct {
		State string `json:"state"`
	}
	json.Unmarshal(output, &checks)

	// Aggregate status
	for _, check := range checks {
		if check.State == "FAILURE" || check.State == "ERROR" {
			return "failure", nil
		}
		if check.State == "PENDING" {
			return "pending", nil
		}
	}

	return "success", nil
}

// ForkRepo forks a repository using gh
func (c *Client) ForkRepo(ctx context.Context, repoFullName string) error {
	var lastErr error

	// Retry up to 3 times for network issues
	for attempt := 1; attempt <= 3; attempt++ {
		cmd := exec.CommandContext(ctx, "gh", "repo", "fork", repoFullName, "--clone=false")
		output, err := cmd.CombinedOutput()
		outputStr := strings.TrimSpace(string(output))

		if err == nil {
			return nil
		}

		// Check if fork already exists (this is OK)
		if strings.Contains(outputStr, "already exists") ||
			strings.Contains(outputStr, "try again later") {
			return nil
		}

		lastErr = fmt.Errorf("%s - %w", outputStr, err)

		// Don't retry on permission/not found errors
		if strings.Contains(outputStr, "not found") ||
			strings.Contains(outputStr, "permission") ||
			strings.Contains(outputStr, "Could not resolve") {
			return lastErr
		}

		// Wait before retry for network issues
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}

	return lastErr
}

// GetUsername gets the authenticated username
func (c *Client) GetUsername(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".login")

	// Capture both stdout and stderr for better debugging
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("gh api user failed: %w, stderr: %s", err, stderr.String())
	}

	username := strings.TrimSpace(stdout.String())
	if username == "" {
		return "", fmt.Errorf("gh returned empty username, stderr: %s", stderr.String())
	}

	return username, nil
}

// isExcludedRepo checks if a repo is in the exclude list
func (c *Client) isExcludedRepo(repo string) bool {
	for _, excluded := range c.config.ExcludeRepos {
		if repo == excluded {
			return true
		}
	}
	return false
}

// GetContributingGuide fetches CONTRIBUTING.md content from a repo
func (c *Client) GetContributingGuide(ctx context.Context, repoFullName string) (string, error) {
	// Try common locations for contribution guide
	paths := []string{
		"CONTRIBUTING.md",
		".github/CONTRIBUTING.md",
		"docs/CONTRIBUTING.md",
	}

	for _, path := range paths {
		cmd := exec.CommandContext(ctx, "gh", "api",
			fmt.Sprintf("repos/%s/contents/%s", repoFullName, path),
			"--jq", ".content")

		output, err := cmd.Output()
		if err != nil {
			continue
		}

		// Content is base64 encoded
		content := strings.TrimSpace(string(output))
		if content == "" || content == "null" {
			continue
		}

		// Decode base64
		decoded, err := decodeBase64(content)
		if err != nil {
			continue
		}

		// Truncate if too long (keep first 4000 chars)
		if len(decoded) > 4000 {
			decoded = decoded[:4000] + "\n... (truncated)"
		}

		return decoded, nil
	}

	return "", fmt.Errorf("no CONTRIBUTING.md found")
}

// decodeBase64 decodes a base64 string
func decodeBase64(s string) (string, error) {
	// GitHub API returns base64 with possible line breaks
	s = strings.ReplaceAll(s, "\\n", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\"", "")

	// Use encoding/base64 to decode
	decoded, err := base64Decode(s)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// base64Decode decodes base64 string using standard library
func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// estimateDifficulty provides initial difficulty estimate
func (c *Client) estimateDifficulty(labels []string, repo *RepoInfo) float64 {
	score := 0.5

	for _, label := range labels {
		name := strings.ToLower(label)
		if strings.Contains(name, "good first") || strings.Contains(name, "beginner") {
			score -= 0.2
		}
		if strings.Contains(name, "complex") || strings.Contains(name, "difficult") {
			score += 0.2
		}
	}

	if repo.Stars > 10000 {
		score += 0.1
	}

	if repo.HasClaudeMD {
		score -= 0.15
	}

	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}

	return score
}
