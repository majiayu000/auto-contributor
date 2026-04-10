package main

import (
	"context"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/pkg/logger"
)

var log = logger.NewComponent("main")

var (
	cfg      *config.Config
	database *db.DB
	ghClient *github.Client
)

func main() {
	// Load .env file if it exists; absence is not an error.
	if err := godotenv.Load(); err != nil {
		log.Debug("no .env file, relying on environment variables")
	}

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

	// Loop command - continuous operation with parallel discovery + feedback
	loopCmd := &cobra.Command{
		Use:   "loop",
		Short: "Run continuously: --mode full (discover+feedback) or followup (feedback only)",
		RunE:  runLoop,
	}
	loopCmd.Flags().StringP("mode", "m", "", "Run mode: full (discover+feedback) or followup (feedback only)")

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

	// Feedback-loop command - periodically check open PRs for review feedback
	feedbackLoopCmd := &cobra.Command{
		Use:   "feedback-loop",
		Short: "Periodically check open PRs for review feedback and address it",
		RunE:  runFeedbackLoop,
	}
	feedbackLoopCmd.Flags().IntP("interval", "n", 30, "Minutes between feedback checks")
	feedbackLoopCmd.Flags().StringP("prompts", "p", "", "Path to prompts directory")

	rootCmd.AddCommand(discoverCmd, statsCmd, loopCmd, versionCmd, smartDiscoverCmd, pipelineCmd, pipelineAutoCmd, feedbackLoopCmd)

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

	// Configure logging
	if cfg.LogLevel != "" {
		logger.SetLevel(cfg.LogLevel)
	}
	if cfg.LogFile != "" {
		if err := logger.SetFile(cfg.LogFile); err != nil {
			log.Warn("failed to open log file", "path", cfg.LogFile, "error", err)
		} else {
			log.Info("logging to file", "path", cfg.LogFile)
		}
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
