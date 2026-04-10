package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

// --- Scout ---

func (p *Pipeline) runScout(ctx context.Context, issue *models.Issue) (*ScoutResult, error) {
	tmplCtx := map[string]any{
		"Repo":        issue.Repo,
		"IssueNumber": issue.IssueNumber,
		"IssueTitle":  issue.Title,
		"IssueBody":   issue.Body,
		"IssueLabels": issue.Labels,
		"Rules":       p.ruleLoader.FormatForPrompt("scout"),
	}

	// Inject repo_structure lessons so scout can detect wrong-repo patterns
	if lessons, err := p.db.GetRecentLessons(10); err == nil && len(lessons) > 0 {
		tmplCtx["PastLessons"] = formatLessonsForPrompt(lessons)
	}

	start := time.Now()
	var result ScoutResult
	if _, err := p.runner.RunJSON(ctx, "scout", p.cfg.WorkspaceDir, tmplCtx, &result); err != nil {
		p.recordEvent(issue, nil, "scout", 1, start, "", false, "", err.Error())
		return nil, err
	}
	summary, _ := json.Marshal(map[string]any{"verdict": result.Verdict, "difficulty": result.Difficulty, "competing_pr": result.HasCompetingPR})
	p.recordEvent(issue, nil, "scout", 1, start, result.Verdict, result.Verdict == "PROCEED", string(summary), "")
	return &result, nil
}

// --- Analyst ---

func (p *Pipeline) runAnalyst(ctx context.Context, issue *models.Issue, workspace string, scout *ScoutResult) (*AnalystResult, error) {
	scoutJSON, _ := json.MarshalIndent(scout, "", "  ")

	tmplCtx := map[string]any{
		"Repo":        issue.Repo,
		"IssueNumber": issue.IssueNumber,
		"IssueTitle":  issue.Title,
		"IssueBody":   issue.Body,
		"ScoutResult": string(scoutJSON),
		"Rules":       p.ruleLoader.FormatForPrompt("analyst"),
	}

	start := time.Now()
	var result AnalystResult
	if _, err := p.runner.RunJSON(ctx, "analyst", workspace, tmplCtx, &result); err != nil {
		p.recordEvent(issue, nil, "analyst", 1, start, "", false, "", err.Error())
		return nil, err
	}

	if result.BaseBranch == "" {
		result.BaseBranch = "main"
	}

	verdict := "can_fix"
	if !result.CanFix {
		verdict = "cannot_fix"
	}
	p.recordEvent(issue, nil, "analyst", 1, start, verdict, result.CanFix, fmt.Sprintf("base_branch=%s files=%d", result.BaseBranch, len(result.FixPlan.FilesToModify)), "")
	return &result, nil
}

// --- Engineer context ---

func (p *Pipeline) buildEngineerCtx(issue *models.Issue, analyst *AnalystResult, lastReview *CodeReviewResult, round int) map[string]any {
	planJSON, _ := json.MarshalIndent(analyst.FixPlan, "", "  ")

	ctx := map[string]any{
		"Repo":         issue.Repo,
		"IssueNumber":  issue.IssueNumber,
		"IssueTitle":   issue.Title,
		"IssueBody":    issue.Body,
		"AnalystPlan":  string(planJSON),
		"BaseBranch":   analyst.BaseBranch,
		"CommitFormat": analyst.CommitFormat,
		"BranchName":   analyst.BranchName,
		"CICommands":   analyst.CICommands,
		"IsRework":     lastReview != nil,
	}

	if lastReview != nil {
		ctx["ReworkRound"] = round
		ctx["ReworkInstructions"] = lastReview.ReworkInstructions
		ctx["IssuesFound"] = lastReview.IssuesFound
	}

	// Inject lessons from past reviews
	lessons, err := p.db.GetRecentLessons(10)
	if err == nil && len(lessons) > 0 {
		ctx["PastLessons"] = formatLessonsForPrompt(lessons)
	}

	ctx["Rules"] = p.ruleLoader.FormatForPrompt("engineer")
	return ctx
}

// --- Reviewer context ---

func (p *Pipeline) buildReviewerCtx(issue *models.Issue, analyst *AnalystResult, round int, lastReview *CodeReviewResult) map[string]any {
	planJSON, _ := json.MarshalIndent(analyst.FixPlan, "", "  ")

	ctx := map[string]any{
		"Repo":        issue.Repo,
		"IssueNumber": issue.IssueNumber,
		"IssueTitle":  issue.Title,
		"IssueBody":   issue.Body,
		"AnalystPlan": string(planJSON),
		"BaseBranch":  analyst.BaseBranch,
		"CICommands":  analyst.CICommands,
		"ReviewRound": round,
		"MaxRounds":   p.maxReview,
	}

	if lastReview != nil {
		prevJSON, _ := json.MarshalIndent(lastReview, "", "  ")
		ctx["PreviousReview"] = string(prevJSON)
	}

	ctx["Rules"] = p.ruleLoader.FormatForPrompt("reviewer")
	return ctx
}

// --- Submitter ---

func (p *Pipeline) runSubmitter(ctx context.Context, issue *models.Issue, workspace string, analyst *AnalystResult) (*SubmitResult, error) {
	planJSON, _ := json.MarshalIndent(analyst.FixPlan, "", "  ")

	tmplCtx := map[string]any{
		"Repo":           issue.Repo,
		"IssueNumber":    issue.IssueNumber,
		"IssueTitle":     issue.Title,
		"IssueBody":      issue.Body,
		"AnalystPlan":    string(planJSON),
		"BranchName":     analyst.BranchName,
		"BaseBranch":     analyst.BaseBranch,
		"CICommands":     analyst.CICommands,
		"PRTitle":        issue.Title,
		"ChangesSummary": analyst.FixPlan.Description,
		"TestPlan":       analyst.FixPlan.TestStrategy,
		"Rules":          p.ruleLoader.FormatForPrompt("submitter"),
	}

	start := time.Now()
	var result SubmitResult
	raw, err := p.runner.RunJSON(ctx, "submitter", workspace, tmplCtx, &result)
	if err != nil {
		log.WithFields(Fields{
			"repo":        issue.Repo,
			"issue":       issue.IssueNumber,
			"output_len":  len(raw),
			"output_tail": truncate(raw, 500),
		}).Warn("submitter failed to produce valid JSON")
		p.recordEvent(issue, nil, "submitter", 1, start, "", false, "", err.Error())
		return nil, err
	}
	p.recordEvent(issue, nil, "submitter", 1, start, result.Status, result.Status == "submitted", fmt.Sprintf("pr=%s", result.PRURL), "")
	return &result, nil
}

// --- Critic ---

func (p *Pipeline) runCritic(ctx context.Context, issue *models.Issue, workspace string, analyst *AnalystResult, round int) (*CriticResult, error) {
	planJSON, _ := json.MarshalIndent(analyst.FixPlan, "", "  ")

	tmplCtx := map[string]any{
		"Repo":        issue.Repo,
		"IssueNumber": issue.IssueNumber,
		"IssueTitle":  issue.Title,
		"IssueBody":   issue.Body,
		"AnalystPlan": string(planJSON),
		"BaseBranch":  analyst.BaseBranch,
		"CICommands":  analyst.CICommands,
		"CriticRound": round,
		"MaxRounds":   p.maxCriticRounds,
		"Rules":       p.ruleLoader.FormatForPrompt("critic"),
	}

	start := time.Now()
	var result CriticResult
	if _, err := p.runner.RunJSON(ctx, "critic", workspace, tmplCtx, &result); err != nil {
		p.recordEvent(issue, nil, "critic", round, start, "", false, "", err.Error())
		return nil, err
	}

	// Normalise so comparisons are case- and whitespace-insensitive.
	result.Verdict = strings.TrimSpace(strings.ToLower(result.Verdict))
	result.Severity = strings.TrimSpace(strings.ToLower(result.Severity))

	summary, _ := json.Marshal(map[string]any{
		"verdict":  result.Verdict,
		"severity": result.Severity,
		"findings": len(result.Findings),
	})
	p.recordEvent(issue, nil, "critic", round, start, result.Verdict, result.Verdict == "approve", string(summary), "")
	return &result, nil
}

// criticLoop runs the critic gate between engineerReviewLoop and runSubmitter.
// It simulates an external maintainer perspective. A maxCriticRounds of 0 skips
// the critic entirely.
func (p *Pipeline) criticLoop(ctx context.Context, issue *models.Issue, workspace string, analyst *AnalystResult) error {
	// Gracefully skip if critic prompt is absent (e.g. stale bundle without critic.md).
	if p.maxCriticRounds > 0 && !p.prompts.Has("critic") {
		log.Warn("critic prompt template not found; skipping critic gate")
		return nil
	}
	for round := 1; round <= p.maxCriticRounds; round++ {
		if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusReviewing, ""); err != nil {
			log.WithError(err).Warn("update status to reviewing (critic)")
		}

		criticResult, err := p.runCritic(ctx, issue, workspace, analyst, round)
		if err != nil {
			p.markFailed(issue, "critic_failed", err.Error())
			return err
		}

		log.WithFields(Fields{
			"repo":     issue.Repo,
			"issue":    issue.IssueNumber,
			"round":    round,
			"verdict":  criticResult.Verdict,
			"severity": criticResult.Severity,
			"summary":  criticResult.Summary,
		}).Info("critic evaluation completed")

		if criticResult.Verdict == "approve" {
			return nil
		}

		// Safety: if the LLM returned "reject" with an unrecognised severity
		// (e.g. a typo like "sevree" or an omitted field), treat it as "severe"
		// so that malformed output cannot silently bypass the critic gate.
		if criticResult.Verdict == "reject" {
			switch criticResult.Severity {
			case "minor", "moderate", "severe":
				// recognised — keep as-is
			default:
				log.WithFields(Fields{
					"repo":     issue.Repo,
					"issue":    issue.IssueNumber,
					"round":    round,
					"severity": criticResult.Severity,
				}).Warn("critic rejected with unrecognised severity; treating as severe to prevent silent bypass")
				criticResult.Severity = "severe"
			}
		}

		// Non-severe (minor/moderate) rejection: non-blocking per critic.md definition.
		// These findings are informational; proceed to the submitter.
		if criticResult.Severity != "severe" {
			log.WithFields(Fields{
				"repo":     issue.Repo,
				"issue":    issue.IssueNumber,
				"round":    round,
				"severity": criticResult.Severity,
				"summary":  criticResult.Summary,
			}).Info("critic raised non-blocking findings, proceeding to submit")
			return nil
		}

		// Severe rejection: skip rework on the final allowed round — no subsequent
		// critic pass will evaluate it and the loop immediately marks abandoned,
		// so running the engineer here only wastes time and risks a misleading
		// "engineer_failed_critic_rework" failure state.
		if round >= p.maxCriticRounds {
			log.WithFields(Fields{
				"repo":  issue.Repo,
				"issue": issue.IssueNumber,
				"round": round,
			}).Info("severe critic rejection on final round; skipping rework (max rounds reached)")
			break
		}

		// Severe rejection: single Engineer rework pass (no Reviewer re-run)
		if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusEngineering, ""); err != nil {
			log.WithError(err).Warn("update status to engineering (critic rework)")
		}

		criticRework := &CodeReviewResult{
			ReworkInstructions: criticResult.ReworkInstructions,
		}
		engCtx := p.buildEngineerCtx(issue, analyst, criticRework, round)
		engStart := time.Now()
		raw, err := p.runner.Run(ctx, "engineer", workspace, engCtx)
		if err != nil {
			p.recordEvent(issue, nil, "engineer", round, engStart, "", false, "", err.Error())
			p.markFailed(issue, "engineer_failed_critic_rework", err.Error())
			return err
		}

		fixComplete := containsMarker(raw, "FIX_COMPLETE")
		p.recordEvent(issue, nil, "engineer", round, engStart, fmt.Sprintf("fix_complete=%v critic_rework=true", fixComplete), fixComplete, "", "")
		if !fixComplete {
			log.WithFields(Fields{
				"repo":  issue.Repo,
				"issue": issue.IssueNumber,
				"round": round,
			}).Warn("engineer rework (critic loop) did not produce FIX_COMPLETE, proceeding to next critic round")
		}

		// Re-run internal reviewer after critic-driven rework.
		// The critic covers only maintainer-scope concerns (backward compat, API
		// contracts, security) and explicitly does NOT check tests/lint — see
		// prompts/critic.md. Without this pass, engineer rework could introduce
		// regressions that the critic misses, causing them to ship.
		if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusReviewing, ""); err != nil {
			log.WithError(err).Warn("update status to reviewing (post-critic-rework)")
		}
		var postCriticReview CodeReviewResult
		revCtx := p.buildReviewerCtx(issue, analyst, round, nil)
		revStart := time.Now()
		if _, err := p.runner.RunJSON(ctx, "reviewer", workspace, revCtx, &postCriticReview); err != nil {
			log.WithError(err).Warn("reviewer parse error after critic rework, treating as approve")
			postCriticReview.Verdict = "approve"
		}
		p.recordEvent(issue, nil, "reviewer", round, revStart, postCriticReview.Verdict, postCriticReview.Verdict == "approve", "", "")
		if postCriticReview.Verdict != "approve" {
			p.markAbandoned(issue, fmt.Sprintf("reviewer rejected code after critic rework at round %d", round))
			return fmt.Errorf("reviewer rejected critic-driven rework at round %d for %s#%d: %s",
				round, issue.Repo, issue.IssueNumber, postCriticReview.ReworkInstructions)
		}
	}

	if p.maxCriticRounds > 0 {
		p.markAbandoned(issue, fmt.Sprintf("max critic rounds (%d) exceeded", p.maxCriticRounds))
		return fmt.Errorf("max critic rounds exceeded for %s#%d", issue.Repo, issue.IssueNumber)
	}
	return nil
}

// --- Engineer ⇄ Reviewer loop ---

func (p *Pipeline) engineerReviewLoop(ctx context.Context, issue *models.Issue, workspace string, analyst *AnalystResult) error {
	var lastReview *CodeReviewResult

	for round := 1; round <= p.maxReview; round++ {
		// Engineer
		if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusEngineering, ""); err != nil {
			log.WithError(err).Warn("update status to engineering")
		}

		engCtx := p.buildEngineerCtx(issue, analyst, lastReview, round)
		engStart := time.Now()
		raw, err := p.runner.Run(ctx, "engineer", workspace, engCtx)
		if err != nil {
			p.recordEvent(issue, nil, "engineer", round, engStart, "", false, "", err.Error())
			p.markFailed(issue, "engineer_failed", err.Error())
			return err
		}

		fixComplete := containsMarker(raw, "FIX_COMPLETE")
		if !fixComplete {
			log.WithFields(Fields{
				"repo":  issue.Repo,
				"issue": issue.IssueNumber,
				"round": round,
			}).Warn("engineer did not produce FIX_COMPLETE marker, proceeding to review anyway")
		}
		p.recordEvent(issue, nil, "engineer", round, engStart, fmt.Sprintf("fix_complete=%v", fixComplete), fixComplete, "", "")

		// Reviewer
		if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusReviewing, ""); err != nil {
			log.WithError(err).Warn("update status to reviewing")
		}

		var review CodeReviewResult
		revCtx := p.buildReviewerCtx(issue, analyst, round, lastReview)
		revStart := time.Now()
		if _, err := p.runner.RunJSON(ctx, "reviewer", workspace, revCtx, &review); err != nil {
			log.WithError(err).Warn("reviewer parse error, treating as approve")
			review.Verdict = "approve"
		}
		reviewSummary, _ := json.Marshal(map[string]any{"verdict": review.Verdict, "confidence": review.Confidence, "issues": len(review.IssuesFound)})
		p.recordEvent(issue, nil, "reviewer", round, revStart, review.Verdict, review.Verdict == "approve", string(reviewSummary), "")

		p.logReview(issue, &review, round)

		if review.Verdict == "approve" {
			log.WithFields(Fields{
				"repo":  issue.Repo,
				"issue": issue.IssueNumber,
				"round": round,
			}).Info("review approved")
			return nil
		}

		// Rework needed
		lastReview = &review
		if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusRework, ""); err != nil {
			log.WithError(err).Warn("update status to rework")
		}

		if round == p.maxReview {
			p.markAbandoned(issue, fmt.Sprintf("max review rounds (%d) exceeded", p.maxReview))
			return fmt.Errorf("max review rounds exceeded for %s#%d", issue.Repo, issue.IssueNumber)
		}

		log.WithFields(Fields{
			"repo":  issue.Repo,
			"issue": issue.IssueNumber,
			"round": round,
		}).Info("rework required")
	}

	return nil
}
