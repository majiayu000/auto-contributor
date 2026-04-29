package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/majiayu000/auto-contributor/internal/runtime"
	"github.com/majiayu000/auto-contributor/pkg/logger"
)

// DiscoveryRequest defines input for Claude-powered discovery
type DiscoveryRequest struct {
	Topic         string   `json:"topic"`          // e.g., "golang", "ai", or "repo:owner/name"
	Languages     []string `json:"languages"`      // e.g., ["go", "python"]
	MinStars      int      `json:"min_stars"`      // minimum repo stars
	Labels        []string `json:"labels"`         // e.g., ["good first issue"]
	MaxAgeDays    int      `json:"max_age_days"`   // max issue age
	ExcludeRepos  []string `json:"exclude_repos"`  // repos to skip
	PriorityRepos []string `json:"priority_repos"` // repos to check first
	Limit         int      `json:"limit"`          // max issues to return
	AnalysisDepth string   `json:"analysis_depth"` // "quick", "deep", "ultrathink"
}

// IssueAnalysis contains Claude's analysis of an issue
type IssueAnalysis struct {
	IsWellDefined        bool     `json:"is_well_defined"`
	HasReproductionSteps bool     `json:"has_reproduction_steps"`
	IsSelfContained      bool     `json:"is_self_contained"`
	FixType              string   `json:"fix_type"`   // bug, docs, feature, refactor
	Complexity           string   `json:"complexity"` // low, medium, high
	EstimatedFiles       int      `json:"estimated_files"`
	Blockers             []string `json:"blockers"`
	Recommendation       string   `json:"recommendation"`
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
	Repo               string        `json:"repo"`
	IssueNumber        int           `json:"issue_number"`
	Title              string        `json:"title"`
	URL                string        `json:"url"`
	SuitabilityScore   float64       `json:"suitability_score"`
	VerifiedNoPR       bool          `json:"verified_no_pr"`
	VerificationMethod string        `json:"verification_method"`
	Analysis           IssueAnalysis `json:"analysis"`
	RepoContext        RepoContext   `json:"repo_context"`
}

// DiscoveryResult is the complete output of discovery
type DiscoveryResult struct {
	Issues   []DiscoveredIssue `json:"issues"`
	Metadata DiscoveryMetadata `json:"metadata"`
}

// DiscoveryMetadata holds discovery stats (uses interface{} for flexible int/string parsing)
type DiscoveryMetadata struct {
	TotalCandidates      int `json:"total_candidates"`
	Analyzed             int `json:"analyzed"`
	IssuesWithPR         int `json:"issues_with_pr"`
	Selected             int `json:"selected"`
	StarThresholdUsed    int `json:"star_threshold_used"`
	DiscoveryTimeSeconds int `json:"discovery_time_seconds"`
}

// UnmarshalJSON handles both int and string values for numeric fields
func (m *DiscoveryMetadata) UnmarshalJSON(data []byte) error {
	// Use a flexible struct to parse
	var raw struct {
		TotalCandidates      interface{} `json:"total_candidates"`
		Analyzed             interface{} `json:"analyzed"`
		IssuesWithPR         interface{} `json:"issues_with_pr"`
		Selected             interface{} `json:"selected"`
		StarThresholdUsed    interface{} `json:"star_threshold_used"`
		DiscoveryTimeSeconds interface{} `json:"discovery_time_seconds"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	m.TotalCandidates = toInt(raw.TotalCandidates)
	m.Analyzed = toInt(raw.Analyzed)
	m.IssuesWithPR = toInt(raw.IssuesWithPR)
	m.Selected = toInt(raw.Selected)
	m.StarThresholdUsed = toInt(raw.StarThresholdUsed)
	m.DiscoveryTimeSeconds = toInt(raw.DiscoveryTimeSeconds)

	return nil
}

// toInt converts interface{} to int, handling both float64 (JSON default) and string
func toInt(v interface{}) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case string:
		i, _ := strconv.Atoi(val)
		return i
	default:
		return 0
	}
}

// ClaudeDiscoverer uses an agent runtime to find and analyze issues
type ClaudeDiscoverer struct {
	rt         runtime.Runtime
	timeout    time.Duration
	retryCount int
	sleep      func(context.Context, time.Duration) error
	log        *logger.ComponentLogger
}

// NewClaudeDiscoverer creates a new discoverer with the given runtime
func NewClaudeDiscoverer(rt runtime.Runtime, timeout time.Duration, retryCount int) *ClaudeDiscoverer {
	if retryCount < 0 {
		retryCount = 0
	}
	return &ClaudeDiscoverer{
		rt:         rt,
		timeout:    timeout,
		retryCount: retryCount,
		sleep:      sleepWithContext,
		log:        logger.NewComponent("discovery"),
	}
}

// Discover finds and analyzes issues using Claude
func (d *ClaudeDiscoverer) Discover(ctx context.Context, req DiscoveryRequest) (*DiscoveryResult, error) {
	startTime := time.Now()

	d.log.Info("starting claude discovery",
		"topic", req.Topic,
		"min_stars", req.MinStars,
		"limit", req.Limit,
		"depth", req.AnalysisDepth,
	)

	// Build the prompt for Claude
	prompt := d.buildDiscoveryPrompt(req)
	d.log.Debug("prompt built", "length", len(prompt))

	// Run Claude Code
	d.log.Info("calling claude code", "timeout", d.timeout.String())
	output, err := d.runClaude(ctx, prompt)
	if err != nil {
		d.log.Error("claude discovery failed", "error", err)
		return nil, fmt.Errorf("claude discovery failed: %w", err)
	}

	elapsed := time.Since(startTime)
	d.log.Info("claude returned", "output_length", len(output), "elapsed", elapsed.String())

	// Parse the result
	result, err := d.parseDiscoveryResult(output)
	if err != nil {
		// Log first 500 chars of output for debugging
		preview := output
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		d.log.Error("parse error", "error", err, "output_preview", preview)
		return nil, fmt.Errorf("failed to parse result: %w", err)
	}

	result.Metadata.DiscoveryTimeSeconds = int(elapsed.Seconds())
	d.log.Info("discovery complete",
		"issues_found", len(result.Issues),
		"issues_with_pr", result.Metadata.IssuesWithPR,
		"star_threshold", result.Metadata.StarThresholdUsed,
		"duration_seconds", result.Metadata.DiscoveryTimeSeconds,
	)
	return result, nil
}

func (d *ClaudeDiscoverer) buildDiscoveryPrompt(req DiscoveryRequest) string {
	excludeSection := ""
	if len(req.ExcludeRepos) > 0 {
		excludeSection = fmt.Sprintf("\n\n这些库不要推荐（黑名单或已有 PR 在跑）:\n%s", strings.Join(req.ExcludeRepos, ", "))
	}

	langList := strings.Join(req.Languages, ", ")
	if langList == "" {
		langList = "go, python, typescript, javascript, rust"
	}

	return fmt.Sprintf(`帮我看下 GitHub 的 trending 库或者 AI 相关的库，维护频繁的，有哪些 issue 没有 PR 的，可以提交 PR 的。
我想做一些贡献，你可以搜索一下，使用 ultrathink 对这些库做一个排行，出一个列表给我。
如果这些知名库都找不到合适的，就自己想办法找别的库，一点点降低标准。

语言范围: %s%s

最终只输出 JSON，第一个字符是 {，最后一个字符是 }，不要任何其他文字。

JSON 格式:
{
  "issues": [
    {
      "repo": "owner/repo",
      "issue_number": 123,
      "title": "Issue title",
      "url": "https://github.com/owner/repo/issues/123",
      "suitability_score": 0.85,
      "verified_no_pr": true,
      "analysis": {
        "fix_type": "bug",
        "complexity": "low",
        "estimated_files": 2,
        "recommendation": "Clear bug with reproduction steps"
      },
      "repo_context": {
        "stars": 50000
      }
    }
  ],
  "metadata": {
    "total_candidates": 50,
    "issues_with_pr": 45,
    "selected": 12
  }
}
`, langList, excludeSection)
}

func (d *ClaudeDiscoverer) runClaude(ctx context.Context, prompt string) (string, error) {
	startTime := time.Now()

	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, d.timeout)
		attemptNumber := attempt + 1

		done := make(chan bool)
		go func(attemptNumber int) {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					elapsed := time.Since(startTime)
					d.log.Info("discovery still running...", "elapsed", elapsed.Round(time.Second).String(), "attempt", attemptNumber)
				}
			}
		}(attemptNumber)

		output, err := d.rt.ExecuteStdin(attemptCtx, prompt)
		close(done)
		cancel()

		if err == nil {
			d.log.Info("discovery finished",
				"elapsed", time.Since(startTime).Round(time.Second).String(),
				"output_bytes", len(output),
				"attempts", attemptNumber,
			)
			return output, nil
		}

		if !runtime.IsClaudeThrottleError(err) {
			d.log.Error("discovery agent failed", "error", err, "attempt", attemptNumber)
			return "", fmt.Errorf("discovery agent failed: %w", err)
		}
		if attempt >= d.retryCount {
			d.log.Warn("discovery throttle retries exhausted",
				"attempts", attemptNumber,
				"retries", d.retryCount,
				"error", err,
			)
			return "", fmt.Errorf("discovery agent throttled after %d retries: %w", d.retryCount, err)
		}

		backoff := discoveryRetryBackoff(attempt)
		d.log.Warn("discovery throttled, retrying",
			"attempt", attemptNumber,
			"retry_in", backoff.String(),
			"error", err,
		)

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("discovery retry canceled: %w", ctx.Err())
		default:
		}

		if err := d.sleep(ctx, backoff); err != nil {
			return "", fmt.Errorf("discovery retry canceled: %w", err)
		}
	}
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
	return os.WriteFile(path, data, 0644)
}

func discoveryRetryBackoff(attempt int) time.Duration {
	backoff := 5 * time.Second
	for i := 0; i < attempt; i++ {
		backoff *= 2
		if backoff >= 20*time.Second {
			return 20 * time.Second
		}
	}
	return backoff
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
