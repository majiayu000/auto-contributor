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
	lang := "go"
	if len(req.Languages) > 0 {
		lang = req.Languages[0]
	}

	excludeRepos := ""
	if len(req.ExcludeRepos) > 0 {
		excludeRepos = fmt.Sprintf("\n排除这些仓库: %s", strings.Join(req.ExcludeRepos, ", "))
	}

	return fmt.Sprintf(`你是一个GitHub贡献顾问。你的任务是找到真正可以贡献的issue。

## 搜索策略

### 第一步：搜索热门库
使用ultrathink思考搜索策略，然后执行：

# 搜索AI相关的trending库
gh search repos "ai OR llm OR agent OR machine-learning" --language=%s --stars=">1000" --sort=updated --json name,owner,stargazersCount,updatedAt --limit 20

# 也搜索工具类库
gh search repos "cli OR tool OR framework" --language=%s --stars=">1000" --sort=updated --json name,owner,stargazersCount,updatedAt --limit 10
%s

### 第二步：对每个库检查issue
对于每个找到的库，执行：
gh issue list --repo <owner/repo> --state open --label "good first issue,help wanted,bug" --json number,title,createdAt --limit 15

### 第三步：验证issue没有关联的PR
对于每个候选issue，用**一次**API调用验证：

gh pr list --repo <owner/repo> --state all --search "<issue_number>" --limit 1

如果返回为空，说明没有PR关联。不要用多种方法重复验证！

### 第四步：递归降级搜索
如果高星库(>1000)的issue都有PR了，自动降级：

# 第一次降级: 500-1000 stars
gh search repos "ai OR llm OR agent" --language=%s --stars="500..1000" --sort=updated --limit 15

# 第二次降级: 100-500 stars
gh search repos "ai OR llm OR agent" --language=%s --stars="100..500" --sort=updated --limit 15

# 第三次降级: 50-100 stars
gh search repos "ai OR llm OR agent" --language=%s --stars="50..100" --sort=updated --limit 15

重复第二、三步，直到找到%d个确认没有PR的issue。

## 使用ultrathink分析
对每个候选issue，深度分析：
1. issue是否定义清晰？
2. 修复范围是否明确？（修改哪些文件）
3. 是否需要添加测试？
4. 预估复杂度？（low/medium/high）
5. 是否有阻塞因素？（需要外部服务、权限等）

## 重要规则
1. **必须验证**：每个issue都要确认没有关联PR
2. **不要猜测**：如果无法确认，跳过这个issue
3. **递归搜索**：如果高星库没有可用issue，自动降级到低星库
4. **最终结果**：至少找到%d个确认没有PR的issue

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
      "verification_method": "searched PRs with 'fixes #123', no results",
      "analysis": {
        "is_well_defined": true,
        "has_reproduction_steps": true,
        "is_self_contained": true,
        "fix_type": "bug",
        "complexity": "low",
        "estimated_files": 2,
        "blockers": [],
        "recommendation": "Clear bug with reproduction steps, good first contribution"
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
    "star_threshold_used": 1000
  }
}

开始搜索！记住：只输出JSON，不要任何解释文字！
`, lang, lang, excludeRepos, lang, lang, lang, req.Limit, req.Limit, req.Limit)
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
