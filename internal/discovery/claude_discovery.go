package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DiscoveryRequest defines input for Claude-powered discovery
type DiscoveryRequest struct {
	Topic         string   `json:"topic"`          // e.g., "golang", "ai", or "repo:owner/name"
	Languages     []string `json:"languages"`      // e.g., ["go", "python"]
	MinStars      int      `json:"min_stars"`      // minimum repo stars
	Labels        []string `json:"labels"`         // e.g., ["good first issue"]
	MaxAgeDays    int      `json:"max_age_days"`   // max issue age
	ExcludeRepos  []string `json:"exclude_repos"`  // repos to skip
	Limit         int      `json:"limit"`          // max issues to return
	AnalysisDepth string   `json:"analysis_depth"` // "quick", "deep", "ultrathink"
}

// IssueAnalysis contains Claude's analysis of an issue
type IssueAnalysis struct {
	IsWellDefined       bool     `json:"is_well_defined"`
	HasReproductionSteps bool    `json:"has_reproduction_steps"`
	IsSelfContained     bool     `json:"is_self_contained"`
	FixType             string   `json:"fix_type"`      // bug, docs, feature, refactor
	Complexity          string   `json:"complexity"`    // low, medium, high
	EstimatedFiles      int      `json:"estimated_files"`
	Blockers            []string `json:"blockers"`
	Recommendation      string   `json:"recommendation"`
}

// RepoContext contains repository metadata
type RepoContext struct {
	Stars           int    `json:"stars"`
	HasContributing bool   `json:"has_contributing"`
	HasClaudeMD     bool   `json:"has_claude_md"`
	TestFramework   string `json:"test_framework"`
	CISystem        string `json:"ci_system"`
}

// DiscoveredIssue represents a discovered and analyzed issue
type DiscoveredIssue struct {
	Repo             string        `json:"repo"`
	IssueNumber      int           `json:"issue_number"`
	Title            string        `json:"title"`
	URL              string        `json:"url"`
	SuitabilityScore float64       `json:"suitability_score"`
	Analysis         IssueAnalysis `json:"analysis"`
	RepoContext      RepoContext   `json:"repo_context"`
}

// DiscoveryResult is the complete output of discovery
type DiscoveryResult struct {
	Issues   []DiscoveredIssue `json:"issues"`
	Metadata struct {
		TotalCandidates      int `json:"total_candidates"`
		Analyzed             int `json:"analyzed"`
		Selected             int `json:"selected"`
		DiscoveryTimeSeconds int `json:"discovery_time_seconds"`
	} `json:"metadata"`
}

// ClaudeDiscoverer uses Claude to find and analyze issues
type ClaudeDiscoverer struct {
	timeout time.Duration
}

// NewClaudeDiscoverer creates a new discoverer
func NewClaudeDiscoverer(timeout time.Duration) *ClaudeDiscoverer {
	return &ClaudeDiscoverer{timeout: timeout}
}

// Discover finds and analyzes issues using Claude
func (d *ClaudeDiscoverer) Discover(ctx context.Context, req DiscoveryRequest) (*DiscoveryResult, error) {
	startTime := time.Now()

	// Build the prompt for Claude
	prompt := d.buildDiscoveryPrompt(req)

	// Run Claude Code
	output, err := d.runClaude(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("claude discovery failed: %w", err)
	}

	// Parse the result
	result, err := d.parseDiscoveryResult(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse result: %w", err)
	}

	result.Metadata.DiscoveryTimeSeconds = int(time.Since(startTime).Seconds())
	return result, nil
}

func (d *ClaudeDiscoverer) buildDiscoveryPrompt(req DiscoveryRequest) string {
	labelsStr := strings.Join(req.Labels, ", ")
	languagesStr := strings.Join(req.Languages, ", ")
	excludeStr := strings.Join(req.ExcludeRepos, ", ")

	analysisInstructions := ""
	switch req.AnalysisDepth {
	case "quick":
		analysisInstructions = "Do a quick assessment based on title and labels only."
	case "deep":
		analysisInstructions = "Read the full issue body and analyze thoroughly."
	case "ultrathink":
		analysisInstructions = `Use extended thinking to deeply analyze each issue:
- Read the full issue body and all comments
- Check if there's already a PR addressing this issue
- Look at the repo's CONTRIBUTING.md and code structure
- Evaluate if this can realistically be solved automatically
- Consider edge cases and potential complications`
	}

	return fmt.Sprintf(`You are an expert at finding GitHub issues suitable for automated solving.

## Task
Find and analyze GitHub issues that can be automatically fixed using Claude Code.

## Search Criteria
- Topic/Focus: %s
- Languages: %s
- Minimum Stars: %d
- Labels to look for: %s
- Maximum issue age: %d days
- Repos to exclude: %s
- Number of issues to return: %d

## Analysis Depth
%s

## Instructions

1. **Search Phase**: Use GitHub to search for issues matching the criteria:
   - Search with: gh search issues "label:\"good first issue\" language:go" --limit 50
   - Or browse trending repos in the topic area

2. **Filter Phase**: For each candidate issue, check:
   - Does it already have a linked PR? (skip if yes)
   - Is the repo actively maintained? (recent commits)
   - Is the issue clear and well-defined?

3. **Analysis Phase**: For promising issues, analyze deeply:
   - Read the full issue description
   - Check for reproduction steps or code examples
   - Evaluate complexity (lines of code, files affected)
   - Identify potential blockers (needs domain knowledge, external services, etc.)
   - Assess likelihood of successful automated fix

4. **Scoring**: Rate each issue 0.0-1.0 based on:
   - 0.9-1.0: Perfect for automation (clear bug, single file, has test)
   - 0.7-0.9: Good candidate (well-defined, low complexity)
   - 0.5-0.7: Possible but challenging (medium complexity)
   - 0.3-0.5: Difficult (high complexity or unclear)
   - 0.0-0.3: Not suitable (requires human judgment)

## Output Format

Return ONLY valid JSON in this exact format (no markdown, no explanation):

{
  "issues": [
    {
      "repo": "owner/repo",
      "issue_number": 123,
      "title": "Issue title here",
      "url": "https://github.com/owner/repo/issues/123",
      "suitability_score": 0.85,
      "analysis": {
        "is_well_defined": true,
        "has_reproduction_steps": true,
        "is_self_contained": true,
        "fix_type": "bug",
        "complexity": "low",
        "estimated_files": 1,
        "blockers": [],
        "recommendation": "Clear bug with stack trace, single file fix likely"
      },
      "repo_context": {
        "stars": 1234,
        "has_contributing": true,
        "has_claude_md": false,
        "test_framework": "go test",
        "ci_system": "GitHub Actions"
      }
    }
  ],
  "metadata": {
    "total_candidates": 50,
    "analyzed": 15,
    "selected": 10
  }
}

Begin discovery now. Search GitHub, analyze issues, and return the JSON result.
`, req.Topic, languagesStr, req.MinStars, labelsStr, req.MaxAgeDays, excludeStr, req.Limit, analysisInstructions)
}

func (d *ClaudeDiscoverer) runClaude(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"--dangerously-skip-permissions",
		"--output-format", "text",
	)

	cmd.Stdin = strings.NewReader(prompt)

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return string(output), nil
}

func (d *ClaudeDiscoverer) parseDiscoveryResult(output string) (*DiscoveryResult, error) {
	// Find JSON in output (Claude might include some text before/after)
	start := strings.Index(output, "{")
	end := strings.LastIndex(output, "}")

	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no valid JSON found in output")
	}

	jsonStr := output[start : end+1]

	var result DiscoveryResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w", err)
	}

	return &result, nil
}

// DiscoverAndSave discovers issues and saves them to a file
func (d *ClaudeDiscoverer) DiscoverAndSave(ctx context.Context, req DiscoveryRequest, outputPath string) error {
	result, err := d.Discover(ctx, req)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}

	return writeFile(outputPath, data)
}

func writeFile(path string, data []byte) error {
	cmd := exec.Command("tee", path)
	cmd.Stdin = strings.NewReader(string(data))
	return cmd.Run()
}
