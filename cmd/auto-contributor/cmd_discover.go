package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/majiayu000/auto-contributor/internal/discovery"
	"github.com/majiayu000/auto-contributor/pkg/models"
	"github.com/spf13/cobra"
)

func discoverIssues(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")

	fmt.Printf("Discovering up to %d issues...\n", limit)

	ctx := cmd.Context()
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
		PriorityRepos: cfg.PriorityRepos,
		Limit:         limit,
		AnalysisDepth: depth,
	}

	// Create discoverer - no practical timeout
	timeout := 24 * time.Hour
	discoverer := discovery.NewClaudeDiscoverer(timeout)

	fmt.Println("Running Claude discovery (this may take a few minutes)...")
	fmt.Println()

	ctx := cmd.Context()
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
