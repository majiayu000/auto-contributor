package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/majiayu000/auto-contributor/internal/discovery"
	"github.com/majiayu000/auto-contributor/internal/pipeline"
	"github.com/majiayu000/auto-contributor/internal/runtime"
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

	// Issue queue: discovery pushes issues, workers pull and process
	issueCh := make(chan *models.Issue, 50)

	// Goroutine 1a: Discovery — finds issues and pushes to queue
	if mode == "full" {
		go func() {
			log.Info("starting discovery goroutine")
			runDiscovery(ctx, issueCh)

			ticker := time.NewTicker(time.Duration(cfg.DiscoveryInterval) * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					runDiscovery(ctx, issueCh)
				}
			}
		}()
	}

	// Goroutine 1b: Worker pool — pulls issues from queue and processes them
	if mode == "full" {
		go func() {
			maxWorkers := cfg.MaxConcurrentPipelines
			if maxWorkers <= 0 {
				maxWorkers = 5
			}
			sem := make(chan struct{}, maxWorkers)
			log.Info("starting pipeline workers", "count", maxWorkers)

			for issue := range issueCh {
				select {
				case <-ctx.Done():
					return
				case sem <- struct{}{}:
				}

				go func(iss *models.Issue) {
					defer func() { <-sem }()
					if err := pipe.ProcessIssue(ctx, iss); err != nil {
						log.Error("pipeline failed",
							"repo", iss.Repo,
							"issue", iss.IssueNumber,
							"error", err,
						)
					}
				}(issue)
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

	// Goroutine 3: Rule synthesis (periodic self-learning)
	go func() {
		interval := cfg.SynthesisInterval
		if interval <= 0 {
			interval = 24
		}
		// Wait before first run to let events accumulate
		time.Sleep(1 * time.Hour)
		log.Info("starting synthesis goroutine")
		if err := pipe.RunSynthesis(ctx); err != nil {
			log.Warn("synthesis cycle failed", "error", err)
		}

		ticker := time.NewTicker(time.Duration(interval) * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := pipe.RunSynthesis(ctx); err != nil {
					log.Warn("synthesis cycle failed", "error", err)
				}
			}
		}
	}()

	<-sigChan
	fmt.Println("\nShutting down...")
	cancel()
	return nil
}

// runDiscovery finds issues and pushes them to the channel for workers to process.
// It returns immediately after queuing — does NOT wait for pipeline completion.
func runDiscovery(ctx context.Context, issueCh chan<- *models.Issue) {
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

	rt, err := runtime.New(cfg.RuntimeType, cfg.RuntimePath)
	if err != nil {
		log.Error("create runtime for discovery", "error", err)
		return
	}
	discoverer := discovery.NewClaudeDiscoverer(rt, cfg.ClaudeTimeout, cfg.ClaudeMaxRetries)
	result, err := discoverer.Discover(ctx, req)
	if err != nil {
		switch {
		case runtime.IsClaudeQuotaBillingError(err):
			log.Error("claude discovery stopped: quota or billing exhausted", "error", err)
		case runtime.IsClaudeThrottleError(err):
			log.Warn("claude discovery stopped after transient throttling retries exhausted", "error", err)
		default:
			log.Error("smart discovery failed", "error", err)
		}
		return
	}

	log.Info("discovery complete",
		"issues_found", len(result.Issues),
		"total_candidates", result.Metadata.TotalCandidates,
	)

	filter := discovery.NewFilter(discovery.FilterConfig{
		MinSuitability: minSuitabilityForLoop,
		MinStars:       cfg.MinRepoStars,
		Languages:      cfg.Languages,
		ExcludeRepos:   excludeRepos,
	}, ghClient, database)

	filterResults := filter.Apply(ctx, result.Issues)
	stats := discovery.Summarize(filterResults)

	var queued int
	for _, fr := range filterResults {
		if !fr.Accepted() {
			log.Debug("issue skipped",
				"repo", fr.Issue.Repo,
				"issue", fr.Issue.IssueNumber,
				"reason", string(fr.Reason),
				"detail", fr.Detail,
			)
			continue
		}

		issue := fr.Issue
		dbIssue := &models.Issue{
			Repo:            issue.Repo,
			IssueNumber:     issue.IssueNumber,
			Title:           issue.Title,
			Body:            issue.Analysis.Recommendation,
			Language:        primaryLanguage(cfg.Languages, issue.RepoContext.Language),
			DifficultyScore: discovery.SuitabilityToDifficulty(issue.SuitabilityScore),
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

		log.Info("queued issue",
			"repo", issue.Repo,
			"issue", issue.IssueNumber,
			"score", issue.SuitabilityScore,
		)

		issueCh <- dbIssue
		queued++
	}

	logSkipBreakdown("discovery cycle complete", queued, stats)
}

// minSuitabilityForLoop is the minimum SuitabilityScore (0..1) we are
// willing to enqueue from the discovery loop. Lifted verbatim from the
// previous inline 0.4 magic number so behavior is unchanged; the named
// constant lets the threshold be tuned without code spelunking.
const minSuitabilityForLoop = 0.4

// primaryLanguage picks a sensible value for models.Issue.Language.
// Prefers the repo-reported language when available; otherwise falls
// back to the first configured allowlist entry, matching prior behavior.
// Returns empty string when no information is available rather than
// indexing into a nil slice (which would panic).
func primaryLanguage(allowed []string, fromRepo string) string {
	if fromRepo != "" {
		return fromRepo
	}
	if len(allowed) > 0 {
		return allowed[0]
	}
	return ""
}

// logSkipBreakdown emits a single structured line summarising one
// discovery cycle. Skip reasons appear as `skip_<reason>` keys with the
// raw count as value, so log analyzers can chart them over time.
func logSkipBreakdown(msg string, queued int, stats discovery.FilterStats) {
	fields := []interface{}{
		"queued", queued,
		"skipped", stats.Skipped,
	}
	for _, reason := range stats.SkipReasons() {
		fields = append(fields, "skip_"+string(reason), stats.Counts[reason])
	}
	log.Info(msg, fields...)
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
