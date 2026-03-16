package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/internal/discovery"
	"github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/internal/pipeline"
	"github.com/majiayu000/auto-contributor/pkg/logger"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

var log = logger.NewComponent("main")

var (
	cfg      *config.Config
	database *db.DB
	ghClient *github.Client
)

func main() {
	// Load .env file if exists (ignore error if not found)
	_ = godotenv.Load()

	rootCmd := &cobra.Command{
		Use:   "auto-contributor",
		Short: "Automated GitHub contributor using AI agents",
		Long: `Auto-contributor automatically discovers GitHub issues,
uses a 5-stage AI agent pipeline (Scout→Analyst→Engineer⇄Reviewer→Submitter)
to create fixes, and submits pull requests.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip init for help commands
			if cmd.Name() == "help" || cmd.Name() == "version" {
				return nil
			}
			return initApp()
		},
	}

	// Discover command - find issues
	discoverCmd := &cobra.Command{
		Use:   "discover",
		Short: "Discover new issues from GitHub",
		RunE:  discoverIssues,
	}
	discoverCmd.Flags().IntP("limit", "l", 10, "Maximum issues to discover")

	// Stats command - show statistics
	statsCmd := &cobra.Command{
		Use:   "stats",
		Short: "Show statistics",
		RunE:  showStats,
	}
	statsCmd.Flags().IntP("days", "d", 7, "Number of days to show")

	// Loop command - continuous operation with smart discovery + V2 pipeline
	loopCmd := &cobra.Command{
		Use:   "loop",
		Short: "Run continuously: smart discovery → V2 pipeline (Scout→Analyst→Engineer⇄Reviewer→Submitter)",
		RunE:  runLoop,
	}
	loopCmd.Flags().IntP("interval", "n", 60, "Minutes between discovery cycles")
	loopCmd.Flags().StringP("topic", "t", "golang", "Topic to search (e.g., 'golang', 'ai', 'web')")
	loopCmd.Flags().StringP("depth", "d", "deep", "Analysis depth: quick, deep, ultrathink")
	loopCmd.Flags().StringP("prompts", "p", "", "Path to prompts directory")

	// Version command
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Show version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("auto-contributor v2.0.0 (Go)")
		},
	}

	// Smart discover command - uses AI for intelligent discovery
	smartDiscoverCmd := &cobra.Command{
		Use:   "discover-smart",
		Short: "Use AI to intelligently discover and analyze issues",
		RunE:  smartDiscover,
	}
	smartDiscoverCmd.Flags().StringP("topic", "t", "golang", "Topic to search (e.g., 'golang', 'ai', 'web')")
	smartDiscoverCmd.Flags().IntP("limit", "l", 10, "Maximum issues to return")
	smartDiscoverCmd.Flags().IntP("min-stars", "s", 50, "Minimum repo stars")
	smartDiscoverCmd.Flags().StringP("depth", "d", "deep", "Analysis depth: quick, deep, ultrathink")
	smartDiscoverCmd.Flags().StringP("output", "o", "", "Output file path (optional, defaults to stdout)")

	// Pipeline command - process a single issue through the 5-stage agent pipeline
	pipelineCmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Process an issue through the agent pipeline (Scout→Analyst→Engineer⇄Reviewer→Submitter)",
		RunE:  runPipeline,
	}
	pipelineCmd.Flags().StringP("repo", "r", "", "Repository (owner/repo)")
	pipelineCmd.Flags().IntP("issue", "i", 0, "Issue number")
	pipelineCmd.Flags().StringP("prompts", "p", "", "Path to prompts directory")

	// Pipeline-auto command - discover issues from specific repos and process them
	pipelineAutoCmd := &cobra.Command{
		Use:   "pipeline-auto",
		Short: "Auto-discover issues from specific repos and process through pipeline",
		RunE:  runPipelineAuto,
	}
	pipelineAutoCmd.Flags().StringSliceP("repos", "r", nil, "Target repositories (owner/repo), comma-separated")
	pipelineAutoCmd.Flags().IntP("limit", "l", 3, "Max issues to process")
	pipelineAutoCmd.Flags().StringP("prompts", "p", "", "Path to prompts directory")

	rootCmd.AddCommand(discoverCmd, statsCmd, loopCmd, versionCmd, smartDiscoverCmd, pipelineCmd, pipelineAutoCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func initApp() error {
	var err error

	// Load configuration
	cfg, err = config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Get username from gh CLI if not set
	if cfg.GitHubUsername == "" {
		ghClient = github.New(cfg)
		username, err := ghClient.GetUsername(context.Background())
		if err != nil {
			return fmt.Errorf("failed to get GitHub username from gh CLI: %w", err)
		}
		cfg.GitHubUsername = username
	}

	// Initialize database (PostgreSQL if DATABASE_URL is set, otherwise SQLite)
	database, err = db.NewWithURL(cfg.DatabaseURL, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("init database: %w", err)
	}

	if cfg.DatabaseURL != "" {
		log.Info("using PostgreSQL database")
	} else {
		log.Info("using SQLite database", "path", cfg.DatabasePath)
	}

	// Initialize GitHub client
	ghClient = github.New(cfg)

	return nil
}

func discoverIssues(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")

	fmt.Printf("Discovering up to %d issues...\n", limit)

	ctx := context.Background()
	issues, err := ghClient.SearchIssues(ctx, limit)
	if err != nil {
		return fmt.Errorf("search issues: %w", err)
	}

	fmt.Printf("Found %d issues:\n\n", len(issues))

	for _, issue := range issues {
		// Check blacklist before saving
		isBlacklisted, _ := database.IsBlacklisted(issue.Repo)
		if isBlacklisted {
			fmt.Printf("Skipping blacklisted repo: %s\n", issue.Repo)
			continue
		}

		// Save to database
		if err := database.CreateIssue(issue); err != nil {
			fmt.Printf("Warning: failed to save issue %s#%d: %v\n", issue.Repo, issue.IssueNumber, err)
			continue
		}

		fmt.Printf("  %s#%d: %s\n", issue.Repo, issue.IssueNumber, truncate(issue.Title, 60))
		fmt.Printf("  Language: %s, Difficulty: %.2f\n", issue.Language, issue.DifficultyScore)
	}

	return nil
}

func showStats(cmd *cobra.Command, args []string) error {
	days, _ := cmd.Flags().GetInt("days")

	stats, err := database.GetStats(days)
	if err != nil {
		return fmt.Errorf("get stats: %w", err)
	}

	fmt.Printf("Statistics (last %d days):\n", days)
	fmt.Println("─────────────────────────────")
	fmt.Printf("Total attempts:      %v\n", stats["total_attempts"])
	fmt.Printf("Successful attempts: %v\n", stats["successful_attempts"])
	fmt.Printf("Success rate:        %.1f%%\n", stats["success_rate"].(float64)*100)
	fmt.Printf("Avg duration:        %.1fs\n", stats["avg_duration_seconds"])

	return nil
}

// loopConfig holds configuration for the loop command
type loopConfig struct {
	topic      string
	depth      string
	promptsDir string
}

var loopCfg loopConfig

func runLoop(cmd *cobra.Command, args []string) error {
	interval, _ := cmd.Flags().GetInt("interval")
	loopCfg.topic, _ = cmd.Flags().GetString("topic")
	loopCfg.depth, _ = cmd.Flags().GetString("depth")
	loopCfg.promptsDir, _ = cmd.Flags().GetString("prompts")
	if loopCfg.promptsDir == "" {
		loopCfg.promptsDir = cfg.PromptsDir
	}

	fmt.Printf("Running in loop mode with smart discovery + V2 pipeline\n")
	fmt.Printf("  Interval: %d minutes\n", interval)
	fmt.Printf("  Topic: %s\n", loopCfg.topic)
	fmt.Printf("  Depth: %s\n", loopCfg.depth)
	fmt.Printf("  Prompts: %s\n", loopCfg.promptsDir)
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create V2 pipeline
	pipe, err := pipeline.New(cfg, database, ghClient, loopCfg.promptsDir)
	if err != nil {
		return fmt.Errorf("create pipeline: %w", err)
	}

	ticker := time.NewTicker(time.Duration(interval) * time.Minute)
	defer ticker.Stop()

	// Run immediately
	runCycle(ctx, pipe)

	for {
		select {
		case <-sigChan:
			fmt.Println("\nShutting down...")
			return nil
		case <-ticker.C:
			runCycle(ctx, pipe)
		}
	}
}

func runCycle(ctx context.Context, pipe *pipeline.Pipeline) {
	topic := loopCfg.topic
	if topic == "" {
		topic = "golang"
	}
	depth := loopCfg.depth
	if depth == "" {
		depth = "deep"
	}

	log.Info("starting discovery cycle", "topic", topic, "depth", depth)

	// Use Claude-powered smart discovery
	req := discovery.DiscoveryRequest{
		Topic:         topic,
		Languages:     cfg.Languages,
		MinStars:      cfg.MinRepoStars,
		Labels:        cfg.IncludeLabels,
		MaxAgeDays:    cfg.MaxIssueAgeDays,
		ExcludeRepos:  cfg.ExcludeRepos,
		Limit:         10,
		AnalysisDepth: depth,
	}

	discoverer := discovery.NewClaudeDiscoverer(24 * time.Hour)
	result, err := discoverer.Discover(ctx, req)
	if err != nil {
		log.Error("smart discovery failed", "error", err)
		return
	}

	log.Info("discovery complete",
		"issues_found", len(result.Issues),
		"total_candidates", result.Metadata.TotalCandidates,
	)

	// Process high-scoring issues through V2 pipeline
	var succeeded, failed, skipped int
	for _, issue := range result.Issues {
		if issue.SuitabilityScore < 0.4 {
			skipped++
			continue
		}

		// Double-check for existing PR
		hasPR, _ := ghClient.HasExistingPR(ctx, issue.Repo, issue.IssueNumber)
		if hasPR {
			skipped++
			continue
		}

		// Check blacklist
		isBlacklisted, _ := database.IsBlacklisted(issue.Repo)
		if isBlacklisted {
			skipped++
			continue
		}

		dbIssue := &models.Issue{
			Repo:            issue.Repo,
			IssueNumber:     issue.IssueNumber,
			Title:           issue.Title,
			Body:            issue.Analysis.Recommendation,
			Language:        cfg.Languages[0],
			DifficultyScore: issue.SuitabilityScore,
			Status:          models.IssueStatusDiscovered,
			DiscoveredAt:    time.Now(),
			UpdatedAt:       time.Now(),
		}

		if err := database.CreateIssue(dbIssue); err != nil {
			log.Warn("failed to save issue to DB",
				"repo", issue.Repo,
				"issue", issue.IssueNumber,
				"error", err,
			)
			continue
		}

		log.Info("processing issue",
			"repo", issue.Repo,
			"issue", issue.IssueNumber,
			"score", issue.SuitabilityScore,
		)

		// Process through V2 pipeline
		if err := pipe.ProcessIssue(ctx, dbIssue); err != nil {
			log.Error("pipeline failed",
				"repo", issue.Repo,
				"issue", issue.IssueNumber,
				"error", err,
			)
			failed++
		} else {
			succeeded++
		}
	}

	log.Info("cycle complete",
		"succeeded", succeeded,
		"failed", failed,
		"skipped", skipped,
	)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func runPipelineAuto(cmd *cobra.Command, args []string) error {
	repos, _ := cmd.Flags().GetStringSlice("repos")
	limit, _ := cmd.Flags().GetInt("limit")
	promptsDir, _ := cmd.Flags().GetString("prompts")
	if promptsDir == "" {
		promptsDir = cfg.PromptsDir
	}

	if len(repos) == 0 {
		return fmt.Errorf("--repos is required (e.g., -r owner/repo1,owner/repo2)")
	}

	fmt.Printf("Pipeline Auto: discovering up to %d issues from %v\n\n", limit, repos)

	ctx := context.Background()

	// Discover unassigned bug issues from target repos
	var candidates []*models.Issue
	for _, repo := range repos {
		issues, err := ghClient.GetUnassignedBugs(ctx, repo, limit)
		if err != nil {
			fmt.Printf("Warning: skip %s: %v\n", repo, err)
			continue
		}
		candidates = append(candidates, issues...)
	}

	if len(candidates) == 0 {
		return fmt.Errorf("no candidate issues found")
	}

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	fmt.Printf("Found %d candidates:\n", len(candidates))
	for _, c := range candidates {
		fmt.Printf("  %s#%d: %s\n", c.Repo, c.IssueNumber, c.Title)
	}
	fmt.Println()

	// Process each through the pipeline
	pipe, err := pipeline.New(cfg, database, ghClient, promptsDir)
	if err != nil {
		return fmt.Errorf("create pipeline: %w", err)
	}

	var succeeded, failed int
	for i, issue := range candidates {
		fmt.Printf("=== [%d/%d] %s#%d ===\n", i+1, len(candidates), issue.Repo, issue.IssueNumber)

		issue.Status = models.IssueStatusDiscovered
		database.CreateIssue(issue)

		if err := pipe.ProcessIssue(ctx, issue); err != nil {
			fmt.Printf("FAILED: %v\n\n", err)
			failed++
			continue
		}
		fmt.Printf("SUCCESS\n\n")
		succeeded++
	}

	fmt.Printf("Pipeline Auto complete: %d succeeded, %d failed out of %d\n", succeeded, failed, len(candidates))
	return nil
}

func runPipeline(cmd *cobra.Command, args []string) error {
	repo, _ := cmd.Flags().GetString("repo")
	issueNum, _ := cmd.Flags().GetInt("issue")

	if repo == "" || issueNum == 0 {
		return fmt.Errorf("both --repo and --issue are required")
	}

	// Resolve prompts directory
	promptsDir, _ := cmd.Flags().GetString("prompts")
	if promptsDir == "" {
		promptsDir = cfg.PromptsDir
	}

	fmt.Printf("Pipeline: %s#%d\n", repo, issueNum)
	fmt.Printf("Stages: Scout → Analyst → Engineer ⇄ Reviewer → Submitter\n\n")

	ctx := context.Background()

	// Fetch issue from GitHub
	issue, err := ghClient.GetIssue(ctx, repo, issueNum)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}

	repoInfo, err := ghClient.GetRepoInfo(ctx, repo)
	if err != nil {
		return fmt.Errorf("fetch repo info: %w", err)
	}

	issue.Language = repoInfo.Language
	issue.DifficultyScore = 0.5
	issue.Status = models.IssueStatusDiscovered

	// Save to database
	database.CreateIssue(issue)

	// Create and run pipeline
	pipe, err := pipeline.New(cfg, database, ghClient, promptsDir)
	if err != nil {
		return fmt.Errorf("create pipeline: %w", err)
	}

	if err := pipe.ProcessIssue(ctx, issue); err != nil {
		return fmt.Errorf("pipeline failed: %w", err)
	}

	return nil
}

func smartDiscover(cmd *cobra.Command, args []string) error {
	topic, _ := cmd.Flags().GetString("topic")
	limit, _ := cmd.Flags().GetInt("limit")
	minStars, _ := cmd.Flags().GetInt("min-stars")
	depth, _ := cmd.Flags().GetString("depth")
	outputPath, _ := cmd.Flags().GetString("output")

	fmt.Printf("Smart Discovery (Claude-powered)\n")
	fmt.Printf("   Topic: %s\n", topic)
	fmt.Printf("   Min Stars: %d\n", minStars)
	fmt.Printf("   Limit: %d\n", limit)
	fmt.Printf("   Depth: %s\n", depth)
	fmt.Println()

	// Create discovery request
	req := discovery.DiscoveryRequest{
		Topic:         topic,
		Languages:     cfg.Languages,
		MinStars:      minStars,
		Labels:        cfg.IncludeLabels,
		MaxAgeDays:    cfg.MaxIssueAgeDays,
		ExcludeRepos:  cfg.ExcludeRepos,
		Limit:         limit,
		AnalysisDepth: depth,
	}

	// Create discoverer - no practical timeout
	timeout := 24 * time.Hour
	discoverer := discovery.NewClaudeDiscoverer(timeout)

	fmt.Println("Running Claude discovery (this may take a few minutes)...")
	fmt.Println()

	ctx := context.Background()
	result, err := discoverer.Discover(ctx, req)
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}

	// Output results
	if outputPath != "" {
		data, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(outputPath, data, 0644); err != nil {
			return fmt.Errorf("failed to write output: %w", err)
		}
		fmt.Printf("Results saved to: %s\n", outputPath)
	}

	// Print summary
	fmt.Printf("Discovery Results\n")
	fmt.Printf("   Total candidates: %d\n", result.Metadata.TotalCandidates)
	fmt.Printf("   Analyzed: %d\n", result.Metadata.Analyzed)
	fmt.Printf("   Selected: %d\n", result.Metadata.Selected)
	fmt.Printf("   Time: %ds\n", result.Metadata.DiscoveryTimeSeconds)
	fmt.Println()

	// Print issues table
	fmt.Println("Discovered Issues:")
	fmt.Println("─────────────────────────────────────────────────────────────────────────")
	for i, issue := range result.Issues {
		score := "LOW"
		if issue.SuitabilityScore >= 0.8 {
			score = "HIGH"
		} else if issue.SuitabilityScore >= 0.6 {
			score = "MED"
		}

		fmt.Printf("%d. [%s %.2f] %s#%d\n", i+1, score, issue.SuitabilityScore, issue.Repo, issue.IssueNumber)
		fmt.Printf("   %s\n", truncate(issue.Title, 60))
		fmt.Printf("   %s\n", issue.URL)
		fmt.Printf("   %s | %s complexity | ~%d files\n",
			issue.Analysis.FixType, issue.Analysis.Complexity, issue.Analysis.EstimatedFiles)
		fmt.Printf("   %s\n", issue.Analysis.Recommendation)
		if len(issue.Analysis.Blockers) > 0 {
			fmt.Printf("   Blockers: %v\n", issue.Analysis.Blockers)
		}
		fmt.Println()
	}

	// Save high-scoring issues to database
	if database != nil && len(result.Issues) > 0 {
		fmt.Println("Saving high-scoring issues to database...")
		for _, issue := range result.Issues {
			if issue.SuitabilityScore >= 0.7 {
				isBlacklisted, _ := database.IsBlacklisted(issue.Repo)
				if isBlacklisted {
					fmt.Printf("   Skipping blacklisted repo: %s\n", issue.Repo)
					continue
				}

				dbIssue := &models.Issue{
					Repo:            issue.Repo,
					IssueNumber:     issue.IssueNumber,
					Title:           issue.Title,
					Body:            issue.Analysis.Recommendation,
					Language:        cfg.Languages[0],
					DifficultyScore: 1.0 - issue.SuitabilityScore,
					Status:          models.IssueStatusDiscovered,
					DiscoveredAt:    time.Now(),
					UpdatedAt:       time.Now(),
				}
				if err := database.CreateIssue(dbIssue); err != nil {
					fmt.Printf("   Failed to save %s#%d: %v\n", issue.Repo, issue.IssueNumber, err)
				} else {
					fmt.Printf("   Saved %s#%d\n", issue.Repo, issue.IssueNumber)
				}
			}
		}
	}

	return nil
}
