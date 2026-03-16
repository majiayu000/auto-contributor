package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/internal/prompt"
	"github.com/majiayu000/auto-contributor/pkg/models"
	log "github.com/sirupsen/logrus"
)

// Pipeline orchestrates the 5-stage agent workflow:
// Scout → Pre-communicate → Fork/Clone → Analyst → Engineer ⇄ Reviewer → Submitter
//
// The orchestrator is a pure state machine. All business logic lives in
// the prompt templates (prompts/*.md). This design follows the Symphony
// pattern: orchestrator schedules, agents handle business logic.
type Pipeline struct {
	cfg       *config.Config
	db        *db.DB
	gh        *ghclient.Client
	prompts   *prompt.Store
	runner    *AgentRunner
	maxReview int
}

// New creates a Pipeline. promptsDir is the path to the prompts/ directory.
func New(cfg *config.Config, database *db.DB, gh *ghclient.Client, promptsDir string) (*Pipeline, error) {
	ps := prompt.NewStore(promptsDir)
	if err := ps.Load(); err != nil {
		return nil, fmt.Errorf("load prompts: %w", err)
	}

	timeout := cfg.ClaudeTimeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}

	maxReview := cfg.MaxReviewRounds
	if maxReview <= 0 {
		maxReview = 3
	}

	return &Pipeline{
		cfg:       cfg,
		db:        database,
		gh:        gh,
		prompts:   ps,
		runner:    NewAgentRunner(ps, timeout),
		maxReview: maxReview,
	}, nil
}

// ProcessIssue runs the full pipeline for a single issue.
// It updates DB status at each stage boundary.
func (p *Pipeline) ProcessIssue(ctx context.Context, issue *models.Issue) error {
	// Stage 1: Scout
	scout, err := p.runScout(ctx, issue)
	if err != nil {
		p.markFailed(issue, "scout_failed", err.Error())
		return err
	}
	if scout.Verdict != "PROCEED" {
		p.markAbandoned(issue, fmt.Sprintf("scout: %s", scout.Reason))
		return nil
	}

	// Stage 1.5: Pre-communication (Contributor Skill defense #6)
	p.preComment(ctx, issue, scout)

	// Stage 1.6: Fork + Clone (Contributor Skill Phase 3.1)
	workspace, err := p.forkAndClone(ctx, issue)
	if err != nil {
		p.markFailed(issue, "fork_clone_failed", err.Error())
		return err
	}

	// Stage 2: Analyst
	if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusAnalyzing, ""); err != nil {
		log.WithError(err).Warn("update status to analyzing")
	}

	analyst, err := p.runAnalyst(ctx, issue, workspace, scout)
	if err != nil {
		p.markFailed(issue, "analyst_failed", err.Error())
		return err
	}
	if !analyst.CanFix {
		p.markAbandoned(issue, fmt.Sprintf("analyst: %s", analyst.Reason))
		return nil
	}

	// Stage 3+4: Engineer ⇄ Reviewer loop
	if err := p.engineerReviewLoop(ctx, issue, workspace, analyst); err != nil {
		return err
	}

	// Stage 5: Submitter
	if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusSubmitting, ""); err != nil {
		log.WithError(err).Warn("update status to submitting")
	}

	submit, err := p.runSubmitter(ctx, issue, workspace, analyst)
	if err != nil {
		p.markFailed(issue, "submit_failed", err.Error())
		return err
	}

	if submit.Status != "submitted" {
		p.markFailed(issue, "submit_aborted", submit.Reason)
		return nil
	}

	// Record PR
	pr := &models.PullRequest{
		IssueID:    issue.ID,
		PRURL:      submit.PRURL,
		PRNumber:   submit.PRNumber,
		BranchName: analyst.BranchName,
		Status:     models.PRStatusOpen,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := p.db.CreatePullRequest(pr); err != nil {
		log.WithError(err).Warn("save PR record")
	}

	if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusCompleted, ""); err != nil {
		log.WithError(err).Warn("update status to completed")
	}

	log.WithFields(log.Fields{
		"repo":   issue.Repo,
		"issue":  issue.IssueNumber,
		"pr_url": submit.PRURL,
	}).Info("pipeline completed successfully")

	return nil
}

// --- Pre-communication (Contributor Skill defense #6) ---

func (p *Pipeline) preComment(ctx context.Context, issue *models.Issue, scout *ScoutResult) {
	body := fmt.Sprintf(
		"Hi, I've been looking into this and traced the root cause.\n\n"+
			"**Proposed approach:** %s\n\n"+
			"I'll open a draft PR shortly. Happy to adjust based on your preference.",
		scout.SuggestedApproach)

	if err := p.gh.CommentOnIssue(ctx, issue.Repo, issue.IssueNumber, body); err != nil {
		log.WithError(err).Warn("pre-communication comment failed (non-blocking)")
	} else {
		log.WithFields(log.Fields{
			"repo":  issue.Repo,
			"issue": issue.IssueNumber,
		}).Info("pre-communication comment posted")
	}
}

// --- Fork + Clone (Contributor Skill Phase 3.1) ---

func (p *Pipeline) forkAndClone(ctx context.Context, issue *models.Issue) (string, error) {
	workspace, err := p.createWorkspace(issue)
	if err != nil {
		return "", fmt.Errorf("create workspace dir: %w", err)
	}

	// Check if already cloned (idempotent)
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err == nil {
		log.WithField("workspace", workspace).Info("repo already cloned, reusing")
		return workspace, nil
	}

	// Fork to user's account
	log.WithField("repo", issue.Repo).Info("forking repo")
	if err := p.gh.ForkRepo(ctx, issue.Repo); err != nil {
		return "", fmt.Errorf("fork %s: %w", issue.Repo, err)
	}

	// Clone into workspace
	log.WithField("repo", issue.Repo).Info("cloning repo")
	// Remove workspace dir first since CloneRepo expects empty target
	os.RemoveAll(workspace)
	if err := p.gh.CloneRepo(ctx, issue.Repo, workspace); err != nil {
		return "", fmt.Errorf("clone %s: %w", issue.Repo, err)
	}

	// Add fork remote for pushing
	if err := p.gh.SetupForkRemote(ctx, workspace, issue.Repo); err != nil {
		log.WithError(err).Warn("setup fork remote (non-blocking)")
	}

	return workspace, nil
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
		raw, err := p.runner.Run(ctx, "engineer", workspace, engCtx)
		if err != nil {
			p.markFailed(issue, "engineer_failed", err.Error())
			return err
		}

		// Check engineer markers
		if !containsMarker(raw, "FIX_COMPLETE") {
			p.markFailed(issue, "fix_incomplete", "engineer did not produce FIX_COMPLETE marker")
			return fmt.Errorf("engineer did not complete fix (round %d)", round)
		}

		// Reviewer
		if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusReviewing, ""); err != nil {
			log.WithError(err).Warn("update status to reviewing")
		}

		var review CodeReviewResult
		revCtx := p.buildReviewerCtx(issue, analyst, round, lastReview)
		if _, err := p.runner.RunJSON(ctx, "reviewer", workspace, revCtx, &review); err != nil {
			log.WithError(err).Warn("reviewer parse error, treating as approve")
			review.Verdict = "approve"
		}

		p.logReview(issue, &review, round)

		if review.Verdict == "approve" {
			log.WithFields(log.Fields{
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

		log.WithFields(log.Fields{
			"repo":  issue.Repo,
			"issue": issue.IssueNumber,
			"round": round,
		}).Info("rework required")
	}

	return nil
}

// --- Scout ---

func (p *Pipeline) runScout(ctx context.Context, issue *models.Issue) (*ScoutResult, error) {
	tmplCtx := map[string]any{
		"Repo":        issue.Repo,
		"IssueNumber": issue.IssueNumber,
		"IssueTitle":  issue.Title,
		"IssueBody":   issue.Body,
		"IssueLabels": issue.Labels,
	}

	var result ScoutResult
	if _, err := p.runner.RunJSON(ctx, "scout", p.cfg.WorkspaceDir, tmplCtx, &result); err != nil {
		return nil, err
	}
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
	}

	var result AnalystResult
	if _, err := p.runner.RunJSON(ctx, "analyst", workspace, tmplCtx, &result); err != nil {
		return nil, err
	}

	// Default base branch
	if result.BaseBranch == "" {
		result.BaseBranch = "main"
	}

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
	}

	var result SubmitResult
	if _, err := p.runner.RunJSON(ctx, "submitter", workspace, tmplCtx, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// --- Feedback (Responder Agent) ---

// ProcessFeedback checks a single open PR for review feedback and addresses it.
func (p *Pipeline) ProcessFeedback(ctx context.Context, pr *models.PullRequest) error {
	// Load associated issue
	issue, err := p.db.GetIssueByID(pr.IssueID)
	if err != nil {
		return fmt.Errorf("load issue %d: %w", pr.IssueID, err)
	}

	// Check if PR is still open
	state, err := p.gh.GetPRStateAndRepo(ctx, issue.Repo, pr.PRNumber)
	if err != nil {
		log.WithError(err).Warn("failed to check PR state")
	} else {
		switch state {
		case "MERGED":
			p.db.UpdatePRStatus(pr.ID, models.PRStatusMerged)
			log.WithField("pr", pr.PRURL).Info("PR merged")
			return nil
		case "CLOSED":
			p.db.UpdatePRStatus(pr.ID, models.PRStatusClosed)
			log.WithField("pr", pr.PRURL).Info("PR closed")
			return nil
		}
	}

	// Fetch reviews and inline comments
	reviews, err := p.gh.GetPRReviews(ctx, issue.Repo, pr.PRNumber)
	if err != nil {
		return fmt.Errorf("get reviews: %w", err)
	}
	comments, err := p.gh.GetPRReviewComments(ctx, issue.Repo, pr.PRNumber)
	if err != nil {
		return fmt.Errorf("get comments: %w", err)
	}

	// Check if there's new feedback since last check
	totalFeedback := len(reviews) + len(comments)
	if totalFeedback == 0 {
		log.WithField("pr", pr.PRURL).Debug("no feedback on PR")
		p.db.UpdatePRFeedbackCheck(pr.ID, pr.FeedbackRound)
		return nil
	}

	// Filter to new feedback only (after last check)
	hasNew := false
	if pr.LastFeedbackCheckAt == nil {
		hasNew = totalFeedback > 0
	} else {
		for _, r := range reviews {
			if r.SubmittedAt > pr.LastFeedbackCheckAt.Format(time.RFC3339) {
				hasNew = true
				break
			}
		}
		if !hasNew {
			for _, c := range comments {
				if c.CreatedAt > pr.LastFeedbackCheckAt.Format(time.RFC3339) {
					hasNew = true
					break
				}
			}
		}
	}

	if !hasNew {
		log.WithField("pr", pr.PRURL).Debug("no new feedback since last check")
		p.db.UpdatePRFeedbackCheck(pr.ID, pr.FeedbackRound)
		return nil
	}

	// Cap feedback rounds
	if pr.FeedbackRound >= p.maxReview {
		log.WithFields(log.Fields{
			"pr":    pr.PRURL,
			"round": pr.FeedbackRound,
		}).Warn("max feedback rounds exceeded, skipping")
		return nil
	}

	// Update review stats
	firstReview := time.Now()
	p.db.UpdatePRReviewStats(pr.ID, totalFeedback, &firstReview)

	// Locate workspace
	workspace, err := p.createWorkspace(issue)
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}

	// Build context for responder agent
	tmplCtx := p.buildResponderCtx(issue, pr, reviews, comments)

	log.WithFields(log.Fields{
		"pr":       pr.PRURL,
		"reviews":  len(reviews),
		"comments": len(comments),
		"round":    pr.FeedbackRound + 1,
	}).Info("processing feedback")

	// Run responder agent
	var result FeedbackResult
	if _, err := p.runner.RunJSON(ctx, "responder", workspace, tmplCtx, &result); err != nil {
		log.WithError(err).Warn("responder parse error, treating as no_action")
		result.Action = "no_action"
	}

	// Post replies
	for _, reply := range result.Replies {
		if err := p.gh.ReplyToPRComment(ctx, issue.Repo, pr.PRNumber, reply.CommentID, reply.Body); err != nil {
			log.WithError(err).Warn("failed to post reply")
		}
	}

	// Update DB
	newRound := pr.FeedbackRound + 1
	p.db.UpdatePRFeedbackCheck(pr.ID, newRound)

	if result.Action == "close" {
		p.db.UpdatePRStatus(pr.ID, models.PRStatusClosed)
	}

	log.WithFields(log.Fields{
		"pr":      pr.PRURL,
		"action":  result.Action,
		"summary": result.Summary,
		"round":   newRound,
	}).Info("feedback processed")

	return nil
}

func (p *Pipeline) buildResponderCtx(
	issue *models.Issue,
	pr *models.PullRequest,
	reviews []ghclient.PRReview,
	comments []ghclient.PRReviewComment,
) map[string]any {
	// Format reviews as readable text
	var reviewsText string
	for _, r := range reviews {
		reviewsText += fmt.Sprintf("- **%s** (%s): %s\n", r.Author, r.State, r.Body)
	}
	if reviewsText == "" {
		reviewsText = "(none)"
	}

	// Format inline comments
	var commentsText string
	for _, c := range comments {
		commentsText += fmt.Sprintf("- [%s:%d] **%s** (id:%d): %s\n", c.Path, c.Line, c.Author, c.ID, c.Body)
	}
	if commentsText == "" {
		commentsText = "(none)"
	}

	return map[string]any{
		"Repo":           issue.Repo,
		"IssueNumber":    issue.IssueNumber,
		"IssueTitle":     issue.Title,
		"IssueBody":      issue.Body,
		"PRNumber":       pr.PRNumber,
		"PRURL":          pr.PRURL,
		"BranchName":     pr.BranchName,
		"FeedbackRound":  pr.FeedbackRound + 1,
		"Reviews":        reviewsText,
		"InlineComments": commentsText,
	}
}

// --- Workspace ---

func (p *Pipeline) createWorkspace(issue *models.Issue) (string, error) {
	dir := filepath.Join(p.cfg.WorkspaceDir, fmt.Sprintf("%s-%d",
		sanitizeRepoName(issue.Repo), issue.IssueNumber))

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func sanitizeRepoName(repo string) string {
	result := make([]byte, 0, len(repo))
	for _, c := range repo {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
			result = append(result, byte(c))
		} else {
			result = append(result, '-')
		}
	}
	return string(result)
}

// --- Helpers ---

func containsMarker(output, marker string) bool {
	for _, line := range splitLines(output) {
		if trimmed := trimSpace(line); trimmed == marker {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

func (p *Pipeline) markFailed(issue *models.Issue, reason, details string) {
	msg := fmt.Sprintf("%s: %s", reason, details)
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if err := p.db.MarkIssueFailed(issue.ID, msg, false); err != nil {
		log.WithError(err).Warn("mark issue failed")
	}
}

func (p *Pipeline) markAbandoned(issue *models.Issue, reason string) {
	if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned, reason); err != nil {
		log.WithError(err).Warn("mark issue abandoned")
	}
}

func (p *Pipeline) logReview(issue *models.Issue, review *CodeReviewResult, round int) {
	log.WithFields(log.Fields{
		"repo":       issue.Repo,
		"issue":      issue.IssueNumber,
		"round":      round,
		"verdict":    review.Verdict,
		"confidence": review.Confidence,
		"issues":     len(review.IssuesFound),
		"summary":    review.Summary,
	}).Info("review completed")
}
