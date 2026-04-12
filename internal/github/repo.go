package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// RepoInfo holds repository metadata
type RepoInfo struct {
	Stars           int
	Language        string
	DefaultBranch   string
	HasContributing bool
	HasClaudeMD     bool
	TestFramework   string
}

// GetRepoInfo fetches repository information using gh
func (c *Client) GetRepoInfo(ctx context.Context, repoFullName string) (*RepoInfo, error) {
	cmd := exec.CommandContext(ctx, "gh", "repo", "view", repoFullName,
		"--json", "stargazerCount,primaryLanguage,defaultBranchRef")

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var result struct {
		StargazerCount  int `json:"stargazerCount"`
		PrimaryLanguage struct {
			Name string `json:"name"`
		} `json:"primaryLanguage"`
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return nil, err
	}

	defaultBranch := result.DefaultBranchRef.Name
	if defaultBranch == "" {
		defaultBranch = "main" // fallback
	}

	info := &RepoInfo{
		Stars:         result.StargazerCount,
		Language:      result.PrimaryLanguage.Name,
		DefaultBranch: defaultBranch,
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
	if err := json.Unmarshal(output, &prs); err != nil {
		return false, fmt.Errorf("failed to parse pr list output: %w", err)
	}

	return len(prs) > 0, nil
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

// CloneRepo clones a repo into destDir using gh CLI with retry on network errors.
func (c *Client) CloneRepo(ctx context.Context, repoFullName, destDir string) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		cmd := exec.CommandContext(ctx, "gh", "repo", "clone", repoFullName, destDir, "--", "--depth", "1")
		output, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("clone %s: %s - %w", repoFullName, strings.TrimSpace(string(output)), err)

		outputStr := string(output)
		// Don't retry on permission/not-found errors
		if strings.Contains(outputStr, "not found") ||
			strings.Contains(outputStr, "permission") ||
			strings.Contains(outputStr, "Could not resolve") {
			return lastErr
		}

		// Clean up failed clone dir before retry
		os.RemoveAll(destDir)

		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 5 * time.Second)
		}
	}
	return lastErr
}

// SetupForkRemote adds a "fork" remote pointing to the user's fork.
func (c *Client) SetupForkRemote(ctx context.Context, repoDir, repoFullName string) error {
	// Extract repo name (owner/repo -> repo)
	parts := strings.Split(repoFullName, "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid repo name: %s", repoFullName)
	}
	forkURL := fmt.Sprintf("https://github.com/%s/%s.git", c.config.GitHubUsername, parts[1])

	cmd := exec.CommandContext(ctx, "git", "remote", "add", "fork", forkURL)
	cmd.Dir = repoDir
	// Ignore error if remote already exists
	cmd.CombinedOutput()
	return nil
}

// CommentOnIssue posts a comment on a GitHub issue (pre-communication).
func (c *Client) CommentOnIssue(ctx context.Context, repoFullName string, issueNum int, body string) error {
	cmd := exec.CommandContext(ctx, "gh", "issue", "comment",
		fmt.Sprintf("%d", issueNum),
		"-R", repoFullName,
		"--body", body)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("comment on %s#%d: %s - %w", repoFullName, issueNum, strings.TrimSpace(string(output)), err)
	}
	return nil
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
