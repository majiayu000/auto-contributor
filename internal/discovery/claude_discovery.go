package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

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
	FixType              string   `json:"fix_type"`      // bug, docs, feature, refactor
	Complexity           string   `json:"complexity"`    // low, medium, high
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

// ClaudeDiscoverer uses Claude to find and analyze issues
type ClaudeDiscoverer struct {
	timeout time.Duration
	log     *logger.ComponentLogger
}

// NewClaudeDiscoverer creates a new discoverer
func NewClaudeDiscoverer(timeout time.Duration) *ClaudeDiscoverer {
	return &ClaudeDiscoverer{
		timeout: timeout,
		log:     logger.NewComponent("discovery"),
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
	langs := req.Languages
	if len(langs) == 0 {
		langs = []string{"go"}
	}

	excludeRepos := ""
	if len(req.ExcludeRepos) > 0 {
		excludeRepos = fmt.Sprintf("\n排除这些仓库（不要搜索、不要推荐）: %s", strings.Join(req.ExcludeRepos, ", "))
	}

	prioritySection := "（无指定库，跳过此步，直接进入搜索。）"
	if len(req.PriorityRepos) > 0 {
		prioritySection = fmt.Sprintf("以下是指定的优先库，**先检查这些库的 issue**，然后再搜索新库：\n- %s\n\n对每个库先执行 issue 检查（第二步），有合适的就直接加入候选。", strings.Join(req.PriorityRepos, "\n- "))
	}

	minStars := req.MinStars
	if minStars <= 0 {
		minStars = 100
	}

	// Build language search commands for each language
	var langSearches strings.Builder
	for _, lang := range langs {
		fmt.Fprintf(&langSearches, "gh search repos \"%s\" --language=%s --stars=\">%d\" --sort=stars --json name,owner,stargazersCount,updatedAt --limit 15\n",
			req.Topic, lang, minStars)
	}

	// Build trending topic searches to catch high-star repos not matched by keyword (e.g. dify, open-webui)
	aiTopics := []string{"llm", "ai", "machine-learning", "deep-learning", "generative-ai"}
	var trendingSearches strings.Builder
	for _, topic := range aiTopics {
		for _, lang := range langs {
			fmt.Fprintf(&trendingSearches, "gh search repos --topic=%s --language=%s --stars=\">10000\" --sort=updated --json name,owner,stargazersCount,updatedAt --limit 10\n",
				topic, lang)
		}
	}

	labels := "good first issue,help wanted,bug"
	if len(req.Labels) > 0 {
		labels = strings.Join(req.Labels, ",")
	}

	return fmt.Sprintf(`你是一个GitHub贡献顾问。你的任务是找到真正适合贡献的高质量issue。

## 搜索策略

### 第零步：优先检查指定库
%s

### 第一步：搜索补充发现新库
使用ultrathink思考搜索策略。在已知库之外，额外搜索新的高星活跃项目。

执行以下搜索（覆盖多种语言）：

**关键词搜索：**
%s
**Topic trending 搜索（补充高星但名字不含关键词的库，如 dify/open-webui）：**
%s
%s

**重要：跳过已知库列表中已经检查过的库，只关注新发现的库。**

### 第二步：对每个库检查issue
对于每个找到的库，执行：
gh issue list --repo <owner/repo> --state open --label "%s" --json number,title,createdAt,assignees --limit 15

**跳过已有assignee的issue！**

### 第三步：验证issue没有关联的PR
对于每个候选issue，执行一次验证：
gh pr list --repo <owner/repo> --state all --search "<issue_number>" --limit 1

返回为空 = 没有关联PR。不要重复验证！

### 第四步：降级搜索（仅在高星库issue不足时）
如果stars>%d的库issue都已有PR，依次降级：
- stars %d..%d
- stars %d..%d
降级后重复第二、三步。**不要降到 stars < %d。**

## issue质量评估
对每个候选issue，深度分析：
1. issue是否定义清晰、可操作？
2. 修复范围是否明确？（修改哪些文件）
3. 预估复杂度？（low/medium/high）
4. 是否有阻塞因素？（需要外部服务、权限等）
5. 仓库是否活跃接受外部PR？（看最近merged的外部PR）

**优先推荐**：高星(>5000) + 定义清晰 + 复杂度low/medium + 无阻塞。

## 重要规则
1. **跳过已assign的issue**
2. **必须验证无关联PR**
3. **不要猜测**：无法确认就跳过
4. **每个仓库最多推荐2个issue**：避免集中在同一仓库
5. **至少找到%d个**确认没有PR且没有assignee的issue

## 输出格式
**严格要求：只输出JSON，不要任何其他文字、解释或markdown代码块！**
**第一个字符必须是 { ，最后一个字符必须是 }**

{
  "issues": [
    {
      "repo": "owner/repo",
      "issue_number": 123,
      "title": "Issue title",
      "url": "https://github.com/owner/repo/issues/123",
      "suitability_score": 0.85,
      "verified_no_pr": true,
      "has_assignee": false,
      "verification_method": "searched PRs with issue number, no results",
      "analysis": {
        "is_well_defined": true,
        "has_reproduction_steps": true,
        "is_self_contained": true,
        "fix_type": "bug",
        "complexity": "low",
        "estimated_files": 2,
        "blockers": [],
        "recommendation": "Clear bug with reproduction steps"
      },
      "repo_context": {
        "stars": 1500,
        "has_contributing": true,
        "has_claude_md": false,
        "test_framework": "pytest",
        "ci_system": "github-actions"
      }
    }
  ],
  "metadata": {
    "total_candidates": 50,
    "analyzed": 50,
    "issues_with_pr": 45,
    "selected": %d,
    "star_threshold_used": %d
  }
}

开始搜索！

## 关键约束
1. **禁止使用 Agent 工具** — 你必须自己直接执行所有搜索，不要委派给子 agent
2. **最终输出必须是且仅是一个 JSON 对象** — 第一个字符是 {，最后一个字符是 }
3. **不要输出任何解释、总结或 markdown** — 只输出 JSON
`, prioritySection, langSearches.String(), trendingSearches.String(), excludeRepos, labels,
		minStars, minStars/2, minStars, minStars/5, minStars/2, minStars/5,
		req.Limit, req.Limit, minStars)
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

	// Start progress logger
	startTime := time.Now()
	done := make(chan bool)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				elapsed := time.Since(startTime)
				d.log.Info("discovery still running...", "elapsed", elapsed.Round(time.Second).String())
			}
		}
	}()

	// Use CombinedOutput to capture stderr as well
	output, err := cmd.CombinedOutput()
	close(done) // Stop progress logger

	if err != nil {
		// Log stderr for debugging
		if len(output) > 0 {
			preview := string(output)
			if len(preview) > 1000 {
				preview = preview[:1000] + "..."
			}
			d.log.Error("claude error output", "output", preview)
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude timed out after %v", d.timeout)
		}
		return "", fmt.Errorf("claude command failed: %w", err)
	}

	d.log.Info("claude finished", "elapsed", time.Since(startTime).Round(time.Second).String(), "output_bytes", len(output))
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
