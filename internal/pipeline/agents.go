package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
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
			"repo":       issue.Repo,
			"issue":      issue.IssueNumber,
			"output_len": len(raw),
			"output_tail": truncate(raw, 500),
		}).Warn("submitter failed to produce valid JSON")
		p.recordEvent(issue, nil, "submitter", 1, start, "", false, "", err.Error())
		return nil, err
	}
	p.recordEvent(issue, nil, "submitter", 1, start, result.Status, result.Status == "submitted", fmt.Sprintf("pr=%s", result.PRURL), "")
	return &result, nil
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
