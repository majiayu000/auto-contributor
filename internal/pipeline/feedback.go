package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// ProcessPR is the state machine entry point. Called by the feedback loop for each tracked PR.
func (p *Pipeline) ProcessPR(ctx context.Context, pr *models.PullRequest) error {
	// Extract repo from PR URL (authoritative, not from issue which may be stale)
	prRepo := repoFromPRURL(pr.PRURL)
	if prRepo == "" {
		return fmt.Errorf("cannot parse repo from PR URL: %s", pr.PRURL)
	}

	// Fetch current state from GitHub (retry once on transient EOF)
	prInfo, err := p.gh.GetPRInfo(ctx, prRepo, pr.PRNumber)
	if err != nil {
		if strings.Contains(err.Error(), "EOF") {
			time.Sleep(3 * time.Second)
			prInfo, err = p.gh.GetPRInfo(ctx, prRepo, pr.PRNumber)
		}
		if err != nil {
			return fmt.Errorf("get PR info: %w", err)
		}
	}

	// Terminal state transitions from GitHub
	switch prInfo.State {
	case "MERGED":
		p.db.UpdatePRStatus(pr.ID, models.PRStatusMerged)
		if err := p.db.SyncRepoProfileStats(prRepo); err != nil {
			log.WithFields(Fields{"repo": prRepo, "error": err}).Warn("sync repo profile stats after merge")
		}
		p.extractAndStoreLessons(ctx, pr, prRepo, prInfo)
		p.cleanupWorkspace(pr)
		log.WithField("pr", pr.PRURL).Info("PR merged")
		return nil
	case "CLOSED":
		p.db.UpdatePRStatus(pr.ID, models.PRStatusClosed)
		if err := p.db.SyncRepoProfileStats(prRepo); err != nil {
			log.WithFields(Fields{"repo": prRepo, "error": err}).Warn("sync repo profile stats after close")
		}
		p.extractAndStoreLessons(ctx, pr, prRepo, prInfo)
		p.cleanupWorkspace(pr)
		log.WithField("pr", pr.PRURL).Info("PR closed")
		return nil
	}

	log.WithFields(Fields{
		"pr":     pr.PRURL,
		"status": pr.Status,
		"state":  prInfo.State,
	}).Info("processing PR")

	// Auto-close stale PRs: 30 days open with no human review
	if p.shouldAutoClose(pr, prInfo) {
		log.WithField("pr", pr.PRURL).Info("auto-closing stale PR")
		if err := p.gh.ClosePR(ctx, prRepo, pr.PRNumber, "Closing due to extended inactivity. Happy to reopen if there's still interest."); err != nil {
			log.WithError(err).Warn("failed to auto-close stale PR")
		} else {
			p.db.UpdatePRStatus(pr.ID, models.PRStatusClosed)
			if err := p.db.SyncRepoProfileStats(prRepo); err != nil {
				log.WithFields(Fields{"repo": prRepo, "error": err}).Warn("sync repo profile stats after stale auto-close")
			}
		}
		return nil
	}

	// Dispatch based on local state
	switch pr.Status {
	case models.PRStatusDraft:
		return p.handleDraft(ctx, pr, prRepo, prInfo)
	case models.PRStatusOpen:
		return p.handleOpen(ctx, pr, prRepo, prInfo)
	default:
		return nil
	}
}

// ProcessFeedback is an alias for ProcessPR for backward compatibility.
func (p *Pipeline) ProcessFeedback(ctx context.Context, pr *models.PullRequest) error {
	return p.ProcessPR(ctx, pr)
}

// handleDraft manages draft PRs: check CI, promote to ready, or fix failures.
func (p *Pipeline) handleDraft(ctx context.Context, pr *models.PullRequest, prRepo string, prInfo *ghclient.PRInfo) error {
	// If GitHub says NOT draft anymore (manually marked ready), transition to open
	if !prInfo.IsDraft {
		p.db.UpdatePRStatus(pr.ID, models.PRStatusOpen)
		log.WithField("pr", pr.PRURL).Info("draft PR manually promoted, transitioning to open")
		pr.Status = models.PRStatusOpen
		return p.handleOpen(ctx, pr, prRepo, prInfo)
	}

	// Check CI status
	ci := p.gh.GetCIResult(ctx, prRepo, pr.PRNumber)
	switch {
	case ci.Status == "success" || ci.Status == "unknown":
		// All checks pass or no CI configured — promote to ready
		if err := p.gh.MarkPRReady(ctx, prRepo, pr.PRNumber); err != nil {
			log.WithError(err).WithField("pr", pr.PRURL).Warn("failed to mark PR ready")
			return nil
		}
		p.db.UpdatePRStatus(pr.ID, models.PRStatusOpen)
		log.WithFields(Fields{"pr": pr.PRURL, "ci": ci.Status}).Info("draft PR promoted to ready")
		return nil

	case ci.Status == "failure" && !ci.CodeFailures:
		// Only metadata checks failed — promote anyway
		log.WithFields(Fields{"pr": pr.PRURL, "failed": ci.FailedChecks}).Info("only metadata checks failed, promoting draft PR")
		if err := p.gh.MarkPRReady(ctx, prRepo, pr.PRNumber); err != nil {
			log.WithError(err).WithField("pr", pr.PRURL).Warn("failed to mark PR ready")
			return nil
		}
		p.db.UpdatePRStatus(pr.ID, models.PRStatusOpen)
		return nil

	case ci.Status == "failure" && ci.CodeFailures:
		// Auto-close if CI has been failing for 7+ days
		if time.Since(pr.CreatedAt) > 7*24*time.Hour && pr.FeedbackRound >= 2 {
			log.WithField("pr", pr.PRURL).Warn("CI failing for 7+ days with no fix, auto-closing")
			if err := p.gh.ClosePR(ctx, prRepo, pr.PRNumber, "Closing: CI failures remain unresolved after multiple attempts."); err != nil {
				log.WithError(err).Warn("failed to auto-close CI-failed PR")
			} else {
				p.db.UpdatePRStatus(pr.ID, models.PRStatusClosed)
				if err := p.db.SyncRepoProfileStats(prRepo); err != nil {
					log.WithFields(Fields{"repo": prRepo, "error": err}).Warn("sync repo profile stats after CI auto-close")
				}
			}
			return nil
		}
		// Code checks failed — run engineer agent to fix
		log.WithFields(Fields{"pr": pr.PRURL, "failed": ci.FailedChecks}).Warn("draft PR has code CI failures, attempting fix")
		issue, err := p.db.GetIssueByID(pr.IssueID)
		if err != nil {
			return fmt.Errorf("load issue %d: %w", pr.IssueID, err)
		}
		p.attemptCIFix(ctx, pr, prRepo, issue, ci)
		return nil

	case ci.Status == "pending":
		log.WithField("pr", pr.PRURL).Debug("draft PR waiting for CI")
		return nil
	}

	return nil
}

// handleOpen manages open (non-draft) PRs: check for feedback, run responder.
func (p *Pipeline) handleOpen(ctx context.Context, pr *models.PullRequest, prRepo string, prInfo *ghclient.PRInfo) error {
	// If GitHub says it's a draft (was converted back), transition to draft
	if prInfo.IsDraft {
		p.db.UpdatePRStatus(pr.ID, models.PRStatusDraft)
		log.WithField("pr", pr.PRURL).Info("open PR converted to draft")
		return nil
	}

	// Load issue from DB
	issue, err := p.db.GetIssueByID(pr.IssueID)
	if err != nil {
		return fmt.Errorf("load issue %d: %w", pr.IssueID, err)
	}

	// Get reviews + comments from GitHub
	reviews := prInfo.Reviews
	comments, err := p.gh.GetPRReviewComments(ctx, prRepo, pr.PRNumber)
	if err != nil {
		return fmt.Errorf("get comments: %w", err)
	}

	// Check issue-level comments for CLA bot requests
	issueComments, err := p.gh.GetPRIssueComments(ctx, prRepo, pr.PRNumber)
	if err != nil {
		log.WithError(err).Warn("failed to fetch issue comments, skipping CLA check")
	} else {
		for _, c := range issueComments {
			if !isCLABot(c.Author) {
				continue
			}
			body := strings.ToLower(c.Body)
			if pr.Status == models.PRStatusNeedsAttention {
				// Check if CLA has since been signed
				if strings.Contains(body, "signed") || strings.Contains(body, "thank") || strings.Contains(body, "all contributors") {
					log.WithField("pr", pr.PRURL).Info("CLA signed — restoring to open")
					p.db.UpdatePRStatus(pr.ID, models.PRStatusOpen)
					pr.Status = models.PRStatusOpen
				}
				// Still waiting — skip
				return nil
			}
			// First time seeing CLA request
			if strings.Contains(body, "cla") && !strings.Contains(body, "signed") && !strings.Contains(body, "thank") {
				log.WithFields(Fields{"pr": pr.PRURL, "bot": c.Author}).Warn("CLA required — marking needs_attention")
				p.db.UpdatePRStatus(pr.ID, models.PRStatusNeedsAttention)
				return nil
			}
		}
	}

	// Check for Codecov coverage gaps — trigger engineer to add tests
	if issueComments != nil {
		for _, c := range issueComments {
			if !isCodecovBot(c.Author) {
				continue
			}
			if strings.Contains(c.Body, "missing coverage") || strings.Contains(c.Body, ":x:") {
				// Only act once per PR (check if we already handled this)
				if pr.FeedbackRound > 0 {
					break
				}
				log.WithField("pr", pr.PRURL).Info("codecov reports missing coverage — triggering engineer to add tests")
				issue, err := p.db.GetIssueByID(pr.IssueID)
				if err != nil {
					break
				}
				workspace, err := p.createWorkspace(issue)
				if err != nil {
					break
				}
				tmplCtx := map[string]any{
					"Repo":        prRepo,
					"IssueNumber": issue.IssueNumber,
					"IssueTitle":  issue.Title,
					"PRNumber":    pr.PRNumber,
					"PRURL":       pr.PRURL,
					"BranchName":  pr.BranchName,
					"IsRework":    true,
					"ReworkRound": pr.FeedbackRound + 1,
					"ReworkInstructions": "Codecov reports missing test coverage on changed lines. " +
						"Read the Codecov comment on the PR to identify which lines need coverage. " +
						"Add tests to cover the missing lines, then push.",
				}
				raw, err := p.runner.Run(ctx, "engineer", workspace, tmplCtx)
				if err == nil && containsMarker(raw, "FIX_COMPLETE") {
					log.WithField("pr", pr.PRURL).Info("engineer pushed coverage fix")
					p.db.UpdatePRFeedbackCheck(pr.ID, pr.FeedbackRound+1)
				}
				break
			}
		}
	}

	// Filter out bot reviews, approvals, and non-actionable feedback
	var humanReviews []ghclient.PRReview
	for _, r := range reviews {
		if isBot(r.Author) {
			continue
		}
		if r.State == "APPROVED" || r.State == "DISMISSED" {
			continue
		}
		if isNonActionable(r.Body) {
			continue
		}
		humanReviews = append(humanReviews, r)
	}
	var humanComments []ghclient.PRReviewComment
	for _, c := range comments {
		if !isBot(c.Author) {
			humanComments = append(humanComments, c)
		}
	}

	// Include non-bot issue-level comments (e.g. maintainer replies like "the intended behaviour is...")
	var humanIssueComments []ghclient.IssueComment
	for _, c := range issueComments {
		if !isBot(c.Author) && !isCLABot(c.Author) && !isCodecovBot(c.Author) {
			humanIssueComments = append(humanIssueComments, c)
		}
	}

	// Check for new feedback since last check (humans only)
	totalFeedback := len(humanReviews) + len(humanComments) + len(humanIssueComments)
	if totalFeedback == 0 {
		log.WithField("pr", pr.PRURL).Info("no feedback on PR")
		p.db.UpdatePRFeedbackCheck(pr.ID, pr.FeedbackRound)
		return nil
	}

	hasNew := false
	if pr.LastFeedbackCheckAt == nil {
		hasNew = totalFeedback > 0
	} else {
		for _, r := range humanReviews {
			if r.SubmittedAt > pr.LastFeedbackCheckAt.Format(time.RFC3339) {
				hasNew = true
				break
			}
		}
		if !hasNew {
			for _, c := range humanComments {
				if c.CreatedAt > pr.LastFeedbackCheckAt.Format(time.RFC3339) {
					hasNew = true
					break
				}
			}
		}
		if !hasNew {
			for _, c := range humanIssueComments {
				if c.CreatedAt > pr.LastFeedbackCheckAt.Format(time.RFC3339) {
					hasNew = true
					break
				}
			}
		}
	}

	if !hasNew {
		log.WithField("pr", pr.PRURL).Info("no new feedback since last check")
		p.db.UpdatePRFeedbackCheck(pr.ID, pr.FeedbackRound)
		return nil
	}

	// Cap feedback rounds (separate from pipeline review rounds — feedback needs more iterations)
	maxFeedbackRounds := 10
	if pr.FeedbackRound >= maxFeedbackRounds {
		log.WithFields(Fields{
			"pr":    pr.PRURL,
			"round": pr.FeedbackRound,
		}).Warn("max feedback rounds exceeded, skipping")
		p.db.UpdatePRFeedbackCheck(pr.ID, pr.FeedbackRound)
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

	// Build context for responder agent (human feedback only)
	tmplCtx := p.buildResponderCtx(issue, pr, humanReviews, humanComments, humanIssueComments)

	log.WithFields(Fields{
		"pr":       pr.PRURL,
		"reviews":  len(humanReviews),
		"comments": len(humanComments),
		"round":    pr.FeedbackRound + 1,
	}).Info("processing feedback")

	// Run responder agent
	var result FeedbackResult
	if _, err := p.runner.RunJSON(ctx, "responder", workspace, tmplCtx, &result); err != nil {
		log.WithError(err).Warn("responder parse error, treating as no_action")
		result.Action = "no_action"
	}

	// Post replies and collect replied comment IDs
	repliedIDs := make(map[int64]bool)
	for _, reply := range result.Replies {
		if err := p.gh.ReplyToPRComment(ctx, prRepo, pr.PRNumber, reply.CommentID, reply.Body); err != nil {
			log.WithError(err).Warn("failed to post reply")
			continue
		}
		repliedIDs[reply.CommentID] = true
	}

	// Resolve review threads for addressed comments
	if result.Action == "addressed" && len(repliedIDs) > 0 {
		p.resolveAddressedThreads(ctx, prRepo, pr.PRNumber, repliedIDs)
	}

	// Update DB
	newRound := pr.FeedbackRound + 1
	p.db.UpdatePRFeedbackCheck(pr.ID, newRound)

	if result.Action == "close" {
		p.db.UpdatePRStatus(pr.ID, models.PRStatusClosed)
	}

	log.WithFields(Fields{
		"pr":      pr.PRURL,
		"action":  result.Action,
		"summary": result.Summary,
		"round":   newRound,
	}).Info("feedback processed")

	return nil
}

// attemptCIFix runs the engineer agent to fix code-level CI failures on a draft PR.
func (p *Pipeline) attemptCIFix(ctx context.Context, pr *models.PullRequest, prRepo string, issue *models.Issue, ci *ghclient.CIResult) {
	workspace, err := p.createWorkspace(issue)
	if err != nil {
		log.WithError(err).Warn("cannot create workspace for CI fix")
		return
	}

	tmplCtx := map[string]any{
		"Repo":         prRepo,
		"IssueNumber":  issue.IssueNumber,
		"IssueTitle":   issue.Title,
		"PRNumber":     pr.PRNumber,
		"PRURL":        pr.PRURL,
		"BranchName":   pr.BranchName,
		"FailedChecks": strings.Join(ci.FailedChecks, ", "),
		"IsRework":     true,
		"ReworkRound":  pr.FeedbackRound + 1,
		"ReworkInstructions": fmt.Sprintf(
			"CI checks failed: %s. Read the CI logs, identify the root cause, fix the code, and push.",
			strings.Join(ci.FailedChecks, ", "),
		),
	}

	raw, err := p.runner.Run(ctx, "engineer", workspace, tmplCtx)
	if err != nil {
		log.WithError(err).WithField("pr", pr.PRURL).Warn("engineer CI fix failed")
		return
	}

	if containsMarker(raw, "FIX_COMPLETE") {
		log.WithField("pr", pr.PRURL).Info("engineer pushed CI fix, will check next cycle")
		p.db.UpdatePRFeedbackCheck(pr.ID, pr.FeedbackRound+1)
	} else {
		log.WithField("pr", pr.PRURL).Warn("engineer could not fix CI failures")
	}
}

// resolveAddressedThreads resolves review threads whose comments were replied to by the responder.
func (p *Pipeline) resolveAddressedThreads(ctx context.Context, repo string, prNum int, repliedIDs map[int64]bool) {
	threads, err := p.gh.GetReviewThreads(ctx, repo, prNum)
	if err != nil {
		log.WithError(err).Warn("failed to fetch review threads for resolving")
		return
	}

	resolved := 0
	for _, t := range threads {
		if t.IsResolved {
			continue
		}
		if !repliedIDs[t.CommentID] {
			continue
		}
		if err := p.gh.ResolveReviewThread(ctx, t.ThreadID); err != nil {
			log.WithError(err).WithField("thread", t.ThreadID).Warn("failed to resolve thread")
			continue
		}
		resolved++
	}

	if resolved > 0 {
		log.WithFields(Fields{"repo": repo, "pr": prNum, "resolved": resolved}).Info("resolved review threads")
	}
}

func (p *Pipeline) buildResponderCtx(
	issue *models.Issue,
	pr *models.PullRequest,
	reviews []ghclient.PRReview,
	comments []ghclient.PRReviewComment,
	issueComments []ghclient.IssueComment,
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

	// Format issue-level comments (maintainer replies on the PR thread)
	var issueCommentsText string
	for _, c := range issueComments {
		issueCommentsText += fmt.Sprintf("- **%s** (id:%d): %s\n", c.Author, c.ID, c.Body)
	}
	if issueCommentsText == "" {
		issueCommentsText = "(none)"
	}

	return map[string]any{
		"Repo":                  issue.Repo,
		"IssueNumber":           issue.IssueNumber,
		"IssueTitle":            issue.Title,
		"IssueBody":             issue.Body,
		"OriginalIssueComments": p.fetchOriginalIssueComments(issue),
		"PRNumber":              pr.PRNumber,
		"PRURL":                 pr.PRURL,
		"BranchName":            pr.BranchName,
		"FeedbackRound":         pr.FeedbackRound + 1,
		"Reviews":               reviewsText,
		"InlineComments":        commentsText,
		"IssueComments":         issueCommentsText,
		"Rules":                 p.ruleLoader.FormatForPrompt("responder"),
	}
}

// fetchOriginalIssueComments fetches recent comments from the original issue (not the PR).
// Maintainers may add clarifications, new context, or updated requirements on the issue.
func (p *Pipeline) fetchOriginalIssueComments(issue *models.Issue) string {
	ctx := context.Background()
	comments, err := p.gh.GetPRIssueComments(ctx, issue.Repo, issue.IssueNumber)
	if err != nil || len(comments) == 0 {
		return "(none)"
	}

	var sb strings.Builder
	for _, c := range comments {
		if isBot(c.Author) {
			continue
		}
		sb.WriteString(fmt.Sprintf("- **%s**: %s\n", c.Author, c.Body))
	}
	if sb.Len() == 0 {
		return "(none)"
	}
	return sb.String()
}

// shouldAutoClose returns true if a PR should be auto-closed due to staleness.
// Criteria: 30+ days old with no human review activity.
func (p *Pipeline) shouldAutoClose(pr *models.PullRequest, prInfo *ghclient.PRInfo) bool {
	age := time.Since(pr.CreatedAt)
	if age < 30*24*time.Hour {
		return false
	}

	// Don't close if any human reviewed (approved or changes_requested)
	for _, r := range prInfo.Reviews {
		if isBot(r.Author) {
			continue
		}
		if r.State == "APPROVED" || r.State == "CHANGES_REQUESTED" || r.State == "COMMENTED" {
			return false // has human engagement, don't auto-close
		}
	}

	return true
}

// repoFromPRURL extracts "owner/repo" from a GitHub PR URL.
// e.g. "https://github.com/ollama/ollama/pull/14671" -> "ollama/ollama"
func repoFromPRURL(prURL string) string {
	// Expected format: https://github.com/{owner}/{repo}/pull/{number}
	const prefix = "github.com/"
	idx := strings.Index(prURL, prefix)
	if idx < 0 {
		return ""
	}
	rest := prURL[idx+len(prefix):] // "owner/repo/pull/123"
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) < 3 || parts[2] != "pull" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
