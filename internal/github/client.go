package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v60/github"
	"golang.org/x/oauth2"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// Client wraps the GitHub API client
type Client struct {
	client *github.Client
	config *config.Config
}

// New creates a new GitHub client
func New(cfg *config.Config) *Client {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: cfg.GitHubToken},
	)
	tc := oauth2.NewClient(ctx, ts)

	return &Client{
		client: github.NewClient(tc),
		config: cfg,
	}
}

// SearchIssues searches for issues matching criteria
func (c *Client) SearchIssues(ctx context.Context, limit int) ([]*models.Issue, error) {
	var allIssues []*models.Issue

	// Build search query
	query := c.buildSearchQuery()

	opts := &github.SearchOptions{
		Sort:  "created",
		Order: "desc",
		ListOptions: github.ListOptions{
			PerPage: min(limit, 100),
		},
	}

	result, _, err := c.client.Search.Issues(ctx, query, opts)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}

	for _, issue := range result.Issues {
		if len(allIssues) >= limit {
			break
		}

		// Skip pull requests (GitHub API returns them in issue search)
		if issue.PullRequestLinks != nil {
			continue
		}

		// Parse repo from URL
		repo := c.extractRepoFromURL(issue.GetHTMLURL())
		if repo == "" {
			continue
		}

		// Check if repo is excluded
		if c.isExcludedRepo(repo) {
			continue
		}

		// Get repo details for additional filtering
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
		for _, label := range issue.Labels {
			labels = append(labels, label.GetName())
		}

		modelIssue := &models.Issue{
			Repo:            repo,
			IssueNumber:     issue.GetNumber(),
			Title:           issue.GetTitle(),
			Body:            issue.GetBody(),
			Labels:          strings.Join(labels, ","),
			Language:        repoInfo.Language,
			DifficultyScore: c.estimateDifficulty(issue, repoInfo),
			Status:          models.IssueStatusDiscovered,
			DiscoveredAt:    time.Now(),
			UpdatedAt:       time.Now(),
		}

		allIssues = append(allIssues, modelIssue)
	}

	return allIssues, nil
}

// buildSearchQuery constructs the GitHub search query
func (c *Client) buildSearchQuery() string {
	var parts []string

	parts = append(parts, "is:issue", "is:open", "no:assignee")

	// Add label filters
	if len(c.config.IncludeLabels) > 0 {
		labelParts := make([]string, len(c.config.IncludeLabels))
		for i, label := range c.config.IncludeLabels {
			labelParts[i] = fmt.Sprintf("label:\"%s\"", label)
		}
		// Use OR for labels (any matching label)
		parts = append(parts, "("+strings.Join(labelParts, " OR ")+")")
	}

	// Add language filters
	if len(c.config.Languages) > 0 {
		langParts := make([]string, len(c.config.Languages))
		for i, lang := range c.config.Languages {
			langParts[i] = fmt.Sprintf("language:%s", lang)
		}
		parts = append(parts, "("+strings.Join(langParts, " OR ")+")")
	}

	// Add date filter
	if c.config.MaxIssueAgeDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -c.config.MaxIssueAgeDays).Format("2006-01-02")
		parts = append(parts, fmt.Sprintf("created:>=%s", cutoff))
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

// GetRepoInfo fetches repository information
func (c *Client) GetRepoInfo(ctx context.Context, repoFullName string) (*RepoInfo, error) {
	parts := strings.Split(repoFullName, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo name: %s", repoFullName)
	}

	repo, _, err := c.client.Repositories.Get(ctx, parts[0], parts[1])
	if err != nil {
		return nil, err
	}

	info := &RepoInfo{
		Stars:    repo.GetStargazersCount(),
		Language: repo.GetLanguage(),
	}

	// Check for CONTRIBUTING.md
	_, _, _, err = c.client.Repositories.GetContents(ctx, parts[0], parts[1], "CONTRIBUTING.md", nil)
	info.HasContributing = err == nil

	// Check for CLAUDE.md
	_, _, _, err = c.client.Repositories.GetContents(ctx, parts[0], parts[1], "CLAUDE.md", nil)
	info.HasClaudeMD = err == nil

	return info, nil
}

// GetIssue fetches a single issue
func (c *Client) GetIssue(ctx context.Context, repoFullName string, issueNum int) (*github.Issue, error) {
	parts := strings.Split(repoFullName, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo name: %s", repoFullName)
	}

	issue, _, err := c.client.Issues.Get(ctx, parts[0], parts[1], issueNum)
	return issue, err
}

// HasExistingPR checks if an issue already has a PR
func (c *Client) HasExistingPR(ctx context.Context, repoFullName string, issueNum int) (bool, error) {
	parts := strings.Split(repoFullName, "/")
	if len(parts) != 2 {
		return false, fmt.Errorf("invalid repo name: %s", repoFullName)
	}

	// Search for PRs mentioning this issue
	query := fmt.Sprintf("repo:%s is:pr %d in:body", repoFullName, issueNum)
	result, _, err := c.client.Search.Issues(ctx, query, nil)
	if err != nil {
		return false, err
	}

	return result.GetTotal() > 0, nil
}

// CreatePullRequest creates a new pull request
func (c *Client) CreatePullRequest(ctx context.Context, repoFullName, title, body, head, base string) (*github.PullRequest, error) {
	parts := strings.Split(repoFullName, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo name: %s", repoFullName)
	}

	newPR := &github.NewPullRequest{
		Title: github.String(title),
		Head:  github.String(head),
		Base:  github.String(base),
		Body:  github.String(body),
	}

	pr, _, err := c.client.PullRequests.Create(ctx, parts[0], parts[1], newPR)
	return pr, err
}

// GetPRStatus gets the CI status of a pull request
func (c *Client) GetPRStatus(ctx context.Context, repoFullName string, prNum int) (string, error) {
	parts := strings.Split(repoFullName, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid repo name: %s", repoFullName)
	}

	pr, _, err := c.client.PullRequests.Get(ctx, parts[0], parts[1], prNum)
	if err != nil {
		return "", err
	}

	// Get combined status
	status, _, err := c.client.Repositories.GetCombinedStatus(ctx, parts[0], parts[1], pr.GetHead().GetSHA(), nil)
	if err != nil {
		return "unknown", nil
	}

	return status.GetState(), nil
}

// ForkRepo forks a repository
func (c *Client) ForkRepo(ctx context.Context, repoFullName string) (*github.Repository, error) {
	parts := strings.Split(repoFullName, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo name: %s", repoFullName)
	}

	fork, _, err := c.client.Repositories.CreateFork(ctx, parts[0], parts[1], nil)
	if err != nil {
		// Fork might already exist
		if strings.Contains(err.Error(), "try again later") {
			// Fork is being created, wait a bit
			time.Sleep(5 * time.Second)
			repo, _, getErr := c.client.Repositories.Get(ctx, c.config.GitHubUsername, parts[1])
			return repo, getErr
		}
	}

	return fork, err
}

// extractRepoFromURL extracts owner/repo from GitHub URL
func (c *Client) extractRepoFromURL(url string) string {
	// URL format: https://github.com/owner/repo/issues/123
	url = strings.TrimPrefix(url, "https://github.com/")
	parts := strings.Split(url, "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return ""
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

// estimateDifficulty provides initial difficulty estimate
func (c *Client) estimateDifficulty(issue *github.Issue, repo *RepoInfo) float64 {
	score := 0.5

	// Lower difficulty for good first issues
	for _, label := range issue.Labels {
		name := strings.ToLower(label.GetName())
		if strings.Contains(name, "good first") || strings.Contains(name, "beginner") {
			score -= 0.2
		}
		if strings.Contains(name, "complex") || strings.Contains(name, "difficult") {
			score += 0.2
		}
	}

	// Higher difficulty for large repos
	if repo.Stars > 10000 {
		score += 0.1
	}

	// Lower difficulty if repo has CLAUDE.md
	if repo.HasClaudeMD {
		score -= 0.15
	}

	// Normalize to 0-1 range
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}

	return score
}
