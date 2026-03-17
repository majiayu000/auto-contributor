package main

import (
	"context"
	"fmt"

	"github.com/majiayu000/auto-contributor/internal/pipeline"
	"github.com/majiayu000/auto-contributor/pkg/models"
	"github.com/spf13/cobra"
)

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
