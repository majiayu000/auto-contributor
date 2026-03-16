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
	"github.com/majiayu000/auto-contributor/internal/web"
	"github.com/majiayu000/auto-contributor/internal/worker"
	"github.com/majiayu000/auto-contributor/pkg/logger"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

var log = logger.NewComponent("main")

var (
	cfg       *config.Config
	database  *db.DB
	ghClient  *github.Client
	pool      *worker.Pool
	webServer *web.Server
)

func main() {
	// Load .env file if exists (ignore error if not found)
	_ = godotenv.Load()

	rootCmd := &cobra.Command{
		Use:   "auto-contributor",
		Short: "Automated GitHub contributor using AI (Claude or Codex)",
		Long: `Auto-contributor automatically discovers GitHub issues,
uses AI (Claude or Codex) to create fixes, and submits pull requests.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip init for help commands
			if cmd.Name() == "help" || cmd.Name() == "version" {
				return nil
			}
			return initApp(cmd)
		},
	}

	// Global flags
	rootCmd.PersistentFlags().StringP("executor", "e", "claude", "AI executor to use: claude or codex")

	// Run command - main loop
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Start the auto-contributor service",
		RunE:  runService,
	}

	// Discover command - find issues
	discoverCmd := &cobra.Command{
		Use:   "discover",
		Short: "Discover new issues from GitHub",
		RunE:  discoverIssues,
	}
	discoverCmd.Flags().IntP("limit", "l", 10, "Maximum issues to discover")

	// Solve command - solve a single issue
	solveCmd := &cobra.Command{
		Use:   "solve",
		Short: "Solve a single issue",
		RunE:  solveSingle,
	}
	solveCmd.Flags().StringP("repo", "r", "", "Repository (owner/repo)")
	solveCmd.Flags().IntP("issue", "i", 0, "Issue number")

	// Stats command - show statistics
	statsCmd := &cobra.Command{
		Use:   "stats",
		Short: "Show statistics",
		RunE:  showStats,
	}
	statsCmd.Flags().IntP("days", "d", 7, "Number of days to show")

	// Status command - show worker status
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show current worker status",
		RunE:  showStatus,
	}

	// Loop command - continuous operation with smart discovery
	loopCmd := &cobra.Command{
		Use:   "loop",
		Short: "Run continuously with AI smart discovery",
		RunE:  runLoop,
	}
	loopCmd.Flags().IntP("interval", "n", 60, "Minutes between discovery cycles (default 60 for ~1 issue/hour)")
	loopCmd.Flags().StringP("topic", "t", "golang", "Topic to search (e.g., 'golang', 'ai', 'web')")
	loopCmd.Flags().StringP("depth", "d", "deep", "Analysis depth: quick, deep, ultrathink")

	// Version command
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Show version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("auto-contributor v1.0.0 (Go)")
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

	// Pipeline V2 command - process a single issue through the 5-stage agent pipeline
	pipelineCmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Process an issue through the V2 agent pipeline (Scout→Analyst→Engineer⇄Reviewer→Submitter)",
		RunE:  runPipeline,
	}
	pipelineCmd.Flags().StringP("repo", "r", "", "Repository (owner/repo)")
	pipelineCmd.Flags().IntP("issue", "i", 0, "Issue number")
	pipelineCmd.Flags().StringP("prompts", "p", "", "Path to prompts directory (default: auto-detect)")

	// Pipeline-auto command - discover issues and process them through V2 pipeline
	pipelineAutoCmd := &cobra.Command{
		Use:   "pipeline-auto",
		Short: "Auto-discover issues and process through V2 pipeline",
		RunE:  runPipelineAuto,
	}
	pipelineAutoCmd.Flags().StringSliceP("repos", "r", nil, "Target repositories (owner/repo), comma-separated")
	pipelineAutoCmd.Flags().IntP("limit", "l", 3, "Max issues to process")
	pipelineAutoCmd.Flags().StringP("prompts", "p", "", "Path to prompts directory")

	rootCmd.AddCommand(runCmd, discoverCmd, solveCmd, statsCmd, statusCmd, loopCmd, versionCmd, smartDiscoverCmd, pipelineCmd, pipelineAutoCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func initApp(cmd *cobra.Command) error {
	var err error

	// Load configuration
	cfg, err = config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Override executor type from command line flag
	if cmd != nil {
		executorType, _ := cmd.Flags().GetString("executor")
		if executorType != "" {
			cfg.ExecutorType = executorType
		}
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

	log.Info("using executor", "type", cfg.ExecutorType)

	// Initialize GitHub client
	ghClient = github.New(cfg)

	// Initialize worker pool
	pool = worker.NewPool(cfg, database, ghClient)

	return nil
}

func runService(cmd *cobra.Command, args []string) error {
	fmt.Println("Starting auto-contributor service...")
	fmt.Printf("Workers: %d, Queue size: %d\n", cfg.WorkerCount, cfg.WorkerQueueSize)

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start worker pool
	pool.Start()
	defer pool.Stop()

	// Start web server if enabled
	if cfg.WebEnabled {
		webServer := web.New(cfg, database, pool)
		go webServer.Start()
		fmt.Printf("Web dashboard: http://localhost:%d\n", cfg.WebPort)
	}

	// Start result handler
	go handleResults(ctx)

	// Start issue discovery loop
	go discoveryLoop(ctx)

	// Wait for shutdown signal
	<-sigChan
	fmt.Println("\nShutting down...")

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

		fmt.Printf("• %s#%d: %s\n", issue.Repo, issue.IssueNumber, truncate(issue.Title, 60))
		fmt.Printf("  Language: %s, Difficulty: %.2f\n", issue.Language, issue.DifficultyScore)
	}

	return nil
}

func solveSingle(cmd *cobra.Command, args []string) error {
	repo, _ := cmd.Flags().GetString("repo")
	issueNum, _ := cmd.Flags().GetInt("issue")

	if repo == "" || issueNum == 0 {
		return fmt.Errorf("both --repo and --issue are required")
	}

	fmt.Printf("Solving %s#%d...\n", repo, issueNum)

	ctx := context.Background()

	// Fetch issue from GitHub
	issue, err := ghClient.GetIssue(ctx, repo, issueNum)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}

	// Get repo info for language
	repoInfo, err := ghClient.GetRepoInfo(ctx, repo)
	if err != nil {
		return fmt.Errorf("fetch repo info: %w", err)
	}

	issue.Language = repoInfo.Language
	issue.DifficultyScore = 0.5
	issue.Status = models.IssueStatusDiscovered

	// Save to database
	database.CreateIssue(issue)

	// Start pool with single worker
	cfg.WorkerCount = 1
	pool = worker.NewPool(cfg, database, ghClient)

	// Set status callback
	pool.SetStatusCallback(func(workerID int, status *worker.WorkerStatus) {
		fmt.Printf("[Worker %d] %s: %s\n", workerID, status.Phase, status.LastOutput)
	})

	pool.Start()

	// Submit issue
	pool.Submit(issue)

	// Wait for result
	result := <-pool.Results()

	pool.Stop()

	if result.Success {
		fmt.Printf("\n✓ Success! PR created: %s\n", result.PRURL)
	} else {
		fmt.Printf("\n✗ Failed: %v\n", result.Error)
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

func showStatus(cmd *cobra.Command, args []string) error {
	if pool == nil {
		fmt.Println("Service not running. Use 'run' to start.")
		return nil
	}

	stats := pool.GetSystemStats()

	fmt.Println("Worker Status:")
	fmt.Println("─────────────────────────────")
	fmt.Printf("Active workers: %d/%d\n", stats.ActiveWorkers, stats.ActiveWorkers+stats.IdleWorkers)
	fmt.Printf("Queue size:     %d\n", stats.QueueSize)
	fmt.Println()

	for _, w := range stats.Workers {
		status := "●"
		if w.Status == "idle" {
			status = "○"
		}
		fmt.Printf("%s Worker %d: %s", status, w.ID, w.Phase)
		if w.CurrentIssue != nil {
			fmt.Printf(" - %s#%d", w.CurrentIssue.Repo, w.CurrentIssue.IssueNumber)
		}
		fmt.Printf(" (completed: %d, failed: %d)\n", w.TasksCompleted, w.TasksFailed)
	}

	return nil
}

// loopConfig holds configuration for the loop command
type loopConfig struct {
	topic string
	depth string
}

var loopCfg loopConfig

func runLoop(cmd *cobra.Command, args []string) error {
	interval, _ := cmd.Flags().GetInt("interval")
	loopCfg.topic, _ = cmd.Flags().GetString("topic")
	loopCfg.depth, _ = cmd.Flags().GetString("depth")

	fmt.Printf("Running in loop mode with Claude smart discovery\n")
	fmt.Printf("  Interval: %d minutes\n", interval)
	fmt.Printf("  Topic: %s\n", loopCfg.topic)
	fmt.Printf("  Depth: %s\n", loopCfg.depth)
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start worker pool
	pool.Start()
	defer pool.Stop()

	// Start web server if enabled
	if cfg.WebEnabled {
		webServer = web.New(cfg, database, pool)
		go webServer.Start()
		fmt.Printf("Web dashboard: http://localhost:%d\n", cfg.WebPort)
	}

	// Start result handler
	go handleResults(ctx)

	ticker := time.NewTicker(time.Duration(interval) * time.Minute)
	defer ticker.Stop()

	// Run immediately
	runCycle(ctx)

	for {
		select {
		case <-sigChan:
			fmt.Println("\nShutting down...")
			return nil
		case <-ticker.C:
			runCycle(ctx)
		}
	}
}

func runCycle(ctx context.Context) {
	topic := loopCfg.topic
	if topic == "" {
		topic = "golang"
	}
	depth := loopCfg.depth
	if depth == "" {
		depth = "deep"
	}

	log.Info("starting discovery cycle", "topic", topic, "depth", depth)

	// Update discovery status
	if webServer != nil {
		webServer.UpdateDiscoveryStatus("searching", topic, "Searching GitHub for issues...", 0)
	}

	// Use Claude-powered smart discovery instead of dumb GitHub search
	req := discovery.DiscoveryRequest{
		Topic:         topic,
		Languages:     cfg.Languages,
		MinStars:      cfg.MinRepoStars,
		Labels:        cfg.IncludeLabels,
		MaxAgeDays:    cfg.MaxIssueAgeDays,
		ExcludeRepos:  cfg.ExcludeRepos,
		Limit:         10, // Find more issues, store all in DB for workers
		AnalysisDepth: depth,
	}

	// Update status to analyzing
	if webServer != nil {
		webServer.UpdateDiscoveryStatus("analyzing", topic, "Claude is analyzing issues...", 0)
	}

	discoverer := discovery.NewClaudeDiscoverer(24 * time.Hour) // No practical timeout - let Claude work
	result, err := discoverer.Discover(ctx, req)
	if err != nil {
		log.Error("smart discovery failed", "error", err)
		if webServer != nil {
			webServer.UpdateDiscoveryStatus("idle", "", "", 0)
		}
		return
	}

	log.Info("discovery complete",
		"issues_found", len(result.Issues),
		"total_candidates", result.Metadata.TotalCandidates,
	)

	// Update status to complete
	if webServer != nil {
		webServer.UpdateDiscoveryStatus("complete", topic,
			fmt.Sprintf("Found %d issues from %d candidates", len(result.Issues), result.Metadata.TotalCandidates),
			len(result.Issues))
	}

	// Submit high-scoring issues to queue
	submitted := 0
	for _, issue := range result.Issues {
		// Only submit issues with score >= 0.4 (lowered from 0.6)
		if issue.SuitabilityScore < 0.4 {
			log.Debug("skipping low score issue",
				"repo", issue.Repo,
				"issue", issue.IssueNumber,
				"score", issue.SuitabilityScore,
			)
			continue
		}

		// Double-check for existing PR before submitting to queue
		hasPR, _ := ghClient.HasExistingPR(ctx, issue.Repo, issue.IssueNumber)
		if hasPR {
			log.Debug("skipping issue with existing PR",
				"repo", issue.Repo,
				"issue", issue.IssueNumber,
			)
			continue
		}

		// Check blacklist before saving
		isBlacklisted, _ := database.IsBlacklisted(issue.Repo)
		if isBlacklisted {
			log.Debug("skipping blacklisted repo",
				"repo", issue.Repo,
				"issue", issue.IssueNumber,
			)
			continue
		}

		dbIssue := &models.Issue{
			Repo:            issue.Repo,
			IssueNumber:     issue.IssueNumber,
			Title:           issue.Title,
			Body:            issue.Analysis.Recommendation,
			Language:        cfg.Languages[0],
			DifficultyScore: issue.SuitabilityScore, // Higher score = higher priority
			Status:          models.IssueStatusPending, // Workers will pick this up from DB
			DiscoveredAt:    time.Now(),
			UpdatedAt:       time.Now(),
		}

		// Save to DB - workers will automatically pick up pending issues
		if err := database.CreateIssue(dbIssue); err != nil {
			log.Warn("failed to save issue to DB",
				"repo", issue.Repo,
				"issue", issue.IssueNumber,
				"error", err,
			)
		} else {
			log.Info("saved issue to DB",
				"repo", issue.Repo,
				"issue", issue.IssueNumber,
				"score", issue.SuitabilityScore,
				"fix_type", issue.Analysis.FixType,
			)
			submitted++
		}
	}

	log.Info("cycle complete", "submitted", submitted)

	// Set discovery to idle after cycle completes
	if webServer != nil {
		webServer.UpdateDiscoveryStatus("idle", "", "", 0)
	}
}

func handleResults(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case result, ok := <-pool.Results():
			if !ok {
				return
			}
			if result.Success {
				log.Info("PR created successfully",
					"worker_id", result.WorkerID,
					"repo", result.Issue.Repo,
					"issue", result.Issue.IssueNumber,
					"pr_url", result.PRURL,
				)
			} else {
				log.Error("worker failed",
					"worker_id", result.WorkerID,
					"repo", result.Issue.Repo,
					"issue", result.Issue.IssueNumber,
					"error", result.Error,
				)
			}
		}
	}
}

func discoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(cfg.IssueCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Get pending issues from database
			issues, err := database.GetIssuesByStatus(models.IssueStatusDiscovered, 10)
			if err != nil {
				continue
			}

			for _, issue := range issues {
				pool.Submit(issue)
			}
		}
	}
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

	fmt.Printf("V2 Pipeline: %s#%d\n", repo, issueNum)
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

	fmt.Printf("🔍 Smart Discovery (Claude-powered)\n")
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

	fmt.Println("⏳ Running Claude discovery (this may take a few minutes)...")
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
		fmt.Printf("✅ Results saved to: %s\n", outputPath)
	}

	// Print summary
	fmt.Printf("📊 Discovery Results\n")
	fmt.Printf("   Total candidates: %d\n", result.Metadata.TotalCandidates)
	fmt.Printf("   Analyzed: %d\n", result.Metadata.Analyzed)
	fmt.Printf("   Selected: %d\n", result.Metadata.Selected)
	fmt.Printf("   Time: %ds\n", result.Metadata.DiscoveryTimeSeconds)
	fmt.Println()

	// Print issues table
	fmt.Println("🎯 Discovered Issues:")
	fmt.Println("─────────────────────────────────────────────────────────────────────────")
	for i, issue := range result.Issues {
		scoreEmoji := "🔴"
		if issue.SuitabilityScore >= 0.8 {
			scoreEmoji = "🟢"
		} else if issue.SuitabilityScore >= 0.6 {
			scoreEmoji = "🟡"
		}

		fmt.Printf("%d. %s [%.2f] %s#%d\n", i+1, scoreEmoji, issue.SuitabilityScore, issue.Repo, issue.IssueNumber)
		fmt.Printf("   📝 %s\n", truncate(issue.Title, 60))
		fmt.Printf("   🔗 %s\n", issue.URL)
		fmt.Printf("   📋 %s | %s complexity | ~%d files\n",
			issue.Analysis.FixType, issue.Analysis.Complexity, issue.Analysis.EstimatedFiles)
		fmt.Printf("   💡 %s\n", issue.Analysis.Recommendation)
		if len(issue.Analysis.Blockers) > 0 {
			fmt.Printf("   ⚠️  Blockers: %v\n", issue.Analysis.Blockers)
		}
		fmt.Println()
	}

	// Optionally save to database for later processing
	if database != nil && len(result.Issues) > 0 {
		fmt.Println("💾 Saving high-scoring issues to database...")
		for _, issue := range result.Issues {
			if issue.SuitabilityScore >= 0.7 {
				// Check blacklist before saving
				isBlacklisted, _ := database.IsBlacklisted(issue.Repo)
				if isBlacklisted {
					fmt.Printf("   ⚫ Skipping blacklisted repo: %s\n", issue.Repo)
					continue
				}

				dbIssue := &models.Issue{
					Repo:            issue.Repo,
					IssueNumber:     issue.IssueNumber,
					Title:           issue.Title,
					Body:            issue.Analysis.Recommendation,
					Language:        cfg.Languages[0],
					DifficultyScore: 1.0 - issue.SuitabilityScore, // invert for difficulty
					Status:          models.IssueStatusDiscovered,
					DiscoveredAt:    time.Now(),
					UpdatedAt:       time.Now(),
				}
				if err := database.CreateIssue(dbIssue); err != nil {
					fmt.Printf("   ⚠️  Failed to save %s#%d: %v\n", issue.Repo, issue.IssueNumber, err)
				} else {
					fmt.Printf("   ✅ Saved %s#%d\n", issue.Repo, issue.IssueNumber)
				}
			}
		}
	}

	return nil
}
