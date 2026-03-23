package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/majiayu000/auto-contributor/internal/discovery"
	"github.com/majiayu000/auto-contributor/internal/pipeline"
	"github.com/majiayu000/auto-contributor/pkg/models"
	"github.com/spf13/cobra"
)

func runLoop(cmd *cobra.Command, args []string) error {
	// Resolve mode: flag > config > default
	mode, _ := cmd.Flags().GetString("mode")
	if mode == "" {
		mode = cfg.Mode
	}
	if mode == "" {
		mode = "full"
	}

	switch mode {
	case "full":
		fmt.Printf("Running in FULL mode (discovery + feedback)\n")
		fmt.Printf("  Discovery interval: %d min\n", cfg.DiscoveryInterval)
	case "followup":
		fmt.Printf("Running in FOLLOWUP mode (feedback only, no new issues)\n")
	default:
		return fmt.Errorf("unknown mode %q: use 'full' or 'followup'", mode)
	}
	fmt.Printf("  Feedback interval:  %d min\n", cfg.FeedbackInterval)
	fmt.Printf("  Prompts: %s\n", cfg.PromptsDir)
	fmt.Println()

	// Print PRs needing manual attention at startup
	if attentionPRs, err := database.GetNeedsAttentionPRs(); err == nil && len(attentionPRs) > 0 {
		fmt.Printf("⚠️  %d PR(s) require manual attention (e.g. CLA signing):\n", len(attentionPRs))
		for _, pr := range attentionPRs {
			fmt.Printf("   - %s\n", pr.PRURL)
		}
		fmt.Println()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	pipe, err := pipeline.New(cfg, database, ghClient, cfg.PromptsDir)
	if err != nil {
		return fmt.Errorf("create pipeline: %w", err)
	}

	// Goroutine 1: Issue discovery + pipeline (full mode only)
	if mode == "full" {
		go func() {
			log.Info("starting discovery goroutine")
			runDiscoveryCycle(ctx, pipe)

			ticker := time.NewTicker(time.Duration(cfg.DiscoveryInterval) * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					runDiscoveryCycle(ctx, pipe)
				}
			}
		}()
	}

	// Goroutine 2: PR feedback scanning (both modes)
	go func() {
		log.Info("starting feedback goroutine")
		checkOpenPRFeedback(ctx, pipe)

		ticker := time.NewTicker(time.Duration(cfg.FeedbackInterval) * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkOpenPRFeedback(ctx, pipe)
			}
		}
	}()

	<-sigChan
	fmt.Println("\nShutting down...")
	cancel()
	return nil
}

func runDiscoveryCycle(ctx context.Context, pipe *pipeline.Pipeline) {
	topic := cfg.Topic
	depth := cfg.AnalysisDepth

	log.Info("starting discovery cycle", "topic", topic, "depth", depth)

	// Query slow-response repos (open PRs with no feedback for 7+ days)
	slowRepos, err := database.GetSlowRepos()
	if err != nil {
		log.Warn("failed to query slow repos", "error", err)
	}
	if len(slowRepos) > 0 {
		log.Info("deprioritizing slow repos", "count", len(slowRepos), "repos", slowRepos)
	}

	// Merge exclude lists: config + slow repos
	excludeRepos := append([]string{}, cfg.ExcludeRepos...)
	excludeRepos = append(excludeRepos, slowRepos...)

	// Use Claude-powered smart discovery
	req := discovery.DiscoveryRequest{
		Topic:         topic,
		Languages:     cfg.Languages,
		MinStars:      cfg.MinRepoStars,
		Labels:        cfg.IncludeLabels,
		MaxAgeDays:    cfg.MaxIssueAgeDays,
		ExcludeRepos:  excludeRepos,
		PriorityRepos: cfg.PriorityRepos,
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

	// Process high-scoring issues through V2 pipeline (concurrent worker pool)
	maxWorkers := cfg.MaxConcurrentPipelines
	if maxWorkers <= 0 {
		maxWorkers = 3
	}

	var (
		mu        sync.Mutex
		succeeded int
		failed    int
		skipped   int
		wg        sync.WaitGroup
		sem       = make(chan struct{}, maxWorkers)
	)

	for _, issue := range result.Issues {
		if issue.SuitabilityScore < 0.4 {
			mu.Lock()
			skipped++
			mu.Unlock()
			continue
		}

		// Double-check for existing PR
		hasPR, _ := ghClient.HasExistingPR(ctx, issue.Repo, issue.IssueNumber)
		if hasPR {
			mu.Lock()
			skipped++
			mu.Unlock()
			continue
		}

		// Check blacklist
		isBlacklisted, _ := database.IsBlacklisted(issue.Repo)
		if isBlacklisted {
			mu.Lock()
			skipped++
			mu.Unlock()
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

		wg.Add(1)
		sem <- struct{}{}
		go func(iss *models.Issue) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := pipe.ProcessIssue(ctx, iss); err != nil {
				log.Error("pipeline failed",
					"repo", iss.Repo,
					"issue", iss.IssueNumber,
					"error", err,
				)
				mu.Lock()
				failed++
				mu.Unlock()
			} else {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}(dbIssue)
	}

	wg.Wait()

	log.Info("discovery cycle complete",
		"succeeded", succeeded,
		"failed", failed,
		"skipped", skipped,
	)
}

func runFeedbackLoop(cmd *cobra.Command, args []string) error {
	interval, _ := cmd.Flags().GetInt("interval")
	promptsDir, _ := cmd.Flags().GetString("prompts")
	if promptsDir == "" {
		promptsDir = cfg.PromptsDir
	}

	fmt.Printf("Feedback loop: checking open PRs every %d minutes\n", interval)
	fmt.Printf("  Prompts: %s\n\n", promptsDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	pipe, err := pipeline.New(cfg, database, ghClient, promptsDir)
	if err != nil {
		return fmt.Errorf("create pipeline: %w", err)
	}

	ticker := time.NewTicker(time.Duration(interval) * time.Minute)
	defer ticker.Stop()

	// Run immediately
	checkOpenPRFeedback(ctx, pipe)

	for {
		select {
		case <-sigChan:
			fmt.Println("\nShutting down...")
			return nil
		case <-ticker.C:
			checkOpenPRFeedback(ctx, pipe)
		}
	}
}

func checkOpenPRFeedback(ctx context.Context, pipe *pipeline.Pipeline) {
	// Sync open PRs from GitHub into local DB
	log.Info("scanning GitHub for open PRs...")
	ghPRs, err := ghClient.ListUserOpenPRs(ctx)
	if err != nil {
		log.Error("failed to list open PRs from GitHub", "error", err)
	} else {
		username := cfg.GitHubUsername
		var synced int
		for _, gpr := range ghPRs {
			if gpr.Repo == "" {
				continue
			}
			// Skip PRs on own repos — only track upstream contributions
			owner := gpr.Repo
			if idx := strings.Index(owner, "/"); idx > 0 {
				owner = owner[:idx]
			}
			if strings.EqualFold(owner, username) {
				continue
			}
			// Skip blacklisted repos
			if bl, _ := database.IsBlacklisted(gpr.Repo); bl {
				continue
			}
			if _, err := database.EnsurePRWithIssue(
				gpr.Repo, gpr.Number, gpr.URL, gpr.BranchName, gpr.Title, gpr.Body,
			); err != nil {
				log.Warn("failed to sync PR to DB", "pr", gpr.URL, "error", err)
			} else {
				synced++
			}
		}
		log.Info("synced upstream PRs from GitHub", "total", len(ghPRs), "upstream", synced)
	}

	// Now process all open PRs from DB (includes both pipeline-created and GitHub-synced)
	prs, err := database.GetOpenPRs()
	if err != nil {
		log.Error("failed to get open PRs", "error", err)
		return
	}
	if len(prs) == 0 {
		log.Info("no open PRs to check")
		return
	}

	log.Info("checking feedback on open PRs", "count", len(prs))
	username := cfg.GitHubUsername
	for _, pr := range prs {
		repo := repoFromURL(pr.PRURL)
		if repo == "" {
			continue
		}
		// Skip PRs on own repos
		owner := repo
		if idx := strings.Index(owner, "/"); idx > 0 {
			owner = owner[:idx]
		}
		if strings.EqualFold(owner, username) {
			continue
		}
		// Skip blacklisted repos
		if bl, _ := database.IsBlacklisted(repo); bl {
			log.Info("skipping blacklisted repo", "pr", pr.PRURL)
			continue
		}
		if err := pipe.ProcessPR(ctx, pr); err != nil {
			log.Error("feedback processing failed", "pr", pr.PRURL, "error", err)
		}
	}
}

// repoFromURL extracts "owner/repo" from a GitHub PR URL.
func repoFromURL(prURL string) string {
	const prefix = "github.com/"
	idx := strings.Index(prURL, prefix)
	if idx < 0 {
		return ""
	}
	rest := prURL[idx+len(prefix):]
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
