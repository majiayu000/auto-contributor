package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/internal/web"
	"github.com/majiayu000/auto-contributor/internal/worker"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

var (
	cfg      *config.Config
	database *db.DB
	ghClient *github.Client
	pool     *worker.Pool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "auto-contributor",
		Short: "Automated GitHub contributor using Claude Code",
		Long: `Auto-contributor automatically discovers GitHub issues,
uses Claude Code to create fixes, and submits pull requests.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip init for help commands
			if cmd.Name() == "help" || cmd.Name() == "version" {
				return nil
			}
			return initApp()
		},
	}

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

	// Loop command - continuous operation
	loopCmd := &cobra.Command{
		Use:   "loop",
		Short: "Run continuously with intervals",
		RunE:  runLoop,
	}
	loopCmd.Flags().IntP("interval", "n", 10, "Minutes between runs")

	// Version command
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Show version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("auto-contributor v1.0.0 (Go)")
		},
	}

	rootCmd.AddCommand(runCmd, discoverCmd, solveCmd, statsCmd, statusCmd, loopCmd, versionCmd)

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

	// Validate required config
	if cfg.GitHubToken == "" {
		return fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}
	if cfg.GitHubUsername == "" {
		return fmt.Errorf("GITHUB_USERNAME environment variable is required")
	}

	// Initialize database
	database, err = db.New(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("init database: %w", err)
	}

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
	ghIssue, err := ghClient.GetIssue(ctx, repo, issueNum)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}

	// Get repo info
	repoInfo, err := ghClient.GetRepoInfo(ctx, repo)
	if err != nil {
		return fmt.Errorf("fetch repo info: %w", err)
	}

	// Create model issue
	issue := &models.Issue{
		Repo:            repo,
		IssueNumber:     issueNum,
		Title:           ghIssue.GetTitle(),
		Body:            ghIssue.GetBody(),
		Language:        repoInfo.Language,
		DifficultyScore: 0.5,
		Status:          models.IssueStatusDiscovered,
	}

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

func runLoop(cmd *cobra.Command, args []string) error {
	interval, _ := cmd.Flags().GetInt("interval")

	fmt.Printf("Running in loop mode (interval: %d minutes)\n", interval)

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
	fmt.Printf("[%s] Starting cycle...\n", time.Now().Format("15:04:05"))

	// Discover new issues
	issues, err := ghClient.SearchIssues(ctx, 5)
	if err != nil {
		fmt.Printf("Error discovering issues: %v\n", err)
		return
	}

	// Submit issues to queue
	for _, issue := range issues {
		database.CreateIssue(issue)
		if err := pool.Submit(issue); err != nil {
			fmt.Printf("Queue full, skipping %s#%d\n", issue.Repo, issue.IssueNumber)
		}
	}

	fmt.Printf("Submitted %d issues to queue\n", len(issues))
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
				fmt.Printf("✓ [Worker %d] %s#%d - PR: %s\n",
					result.WorkerID, result.Issue.Repo, result.Issue.IssueNumber, result.PRURL)
			} else {
				fmt.Printf("✗ [Worker %d] %s#%d - Error: %v\n",
					result.WorkerID, result.Issue.Repo, result.Issue.IssueNumber, result.Error)
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
