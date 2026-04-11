package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// SearchResult represents GitHub search API response
type SearchResult struct {
	TotalCount int `json:"total_count"`
	Items      []struct {
		Number      int       `json:"number"`
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		PullRequest *struct{} `json:"pull_request,omitempty"`
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
		RepositoryURL string `json:"repository_url"`
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
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		URL    string `json:"url"`
		Labels []struct {
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

		// Check if issue already has a linked PR; surface lookup errors instead of silently skipping
		hasPR, err := c.HasExistingPR(ctx, repo, r.Number)
		if err != nil {
			return nil, fmt.Errorf("check existing PR for %s#%d: %w", repo, r.Number, err)
		}
		if hasPR {
			continue
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

// GetUnassignedBugs returns unassigned open bug issues with no competing PRs.
func (c *Client) GetUnassignedBugs(ctx context.Context, repoFullName string, limit int) ([]*models.Issue, error) {
	cmd := exec.CommandContext(ctx, "gh", "issue", "list",
		"-R", repoFullName,
		"--state", "open",
		"--label", "bug",
		"--limit", fmt.Sprintf("%d", limit*2), // fetch extra, we'll filter
		"--json", "number,title,body,labels,assignees")

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list %s: %w", repoFullName, err)
	}

	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("parse issues: %w", err)
	}

	repoInfo, _ := c.GetRepoInfo(ctx, repoFullName)
	lang := ""
	if repoInfo != nil {
		lang = repoInfo.Language
	}

	var issues []*models.Issue
	for _, r := range raw {
		if len(r.Assignees) > 0 {
			continue
		}

		// Skip if already has a competing PR; surface lookup errors instead of silently skipping
		hasPR, err := c.HasExistingPR(ctx, repoFullName, r.Number)
		if err != nil {
			return nil, fmt.Errorf("check existing PR for %s#%d: %w", repoFullName, r.Number, err)
		}
		if hasPR {
			continue
		}

		var labels []string
		for _, l := range r.Labels {
			labels = append(labels, l.Name)
		}

		issues = append(issues, &models.Issue{
			Repo:            repoFullName,
			IssueNumber:     r.Number,
			Title:           r.Title,
			Body:            r.Body,
			Labels:          strings.Join(labels, ","),
			Language:        lang,
			DifficultyScore: 0.5,
			Status:          models.IssueStatusDiscovered,
			DiscoveredAt:    time.Now(),
			UpdatedAt:       time.Now(),
		})

		if len(issues) >= limit {
			break
		}
	}

	return issues, nil
}
