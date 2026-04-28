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
	"github.com/majiayu000/auto-contributor/internal/rules"
	"github.com/majiayu000/auto-contributor/internal/runtime"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// Pipeline orchestrates the 5-stage agent workflow:
// Scout → Pre-communicate → Fork/Clone → Analyst → Engineer ⇄ Reviewer → Submitter
//
// The orchestrator is a pure state machine. All business logic lives in
// the prompt templates (prompts/*.md). This design follows the Symphony
// pattern: orchestrator schedules, agents handle business logic.
type Pipeline struct {
	cfg             *config.Config
	db              *db.DB
	gh              *ghclient.Client
	prompts         *prompt.Store
	runner          *AgentRunner
	ruleLoader      *rules.RuleLoader
	ruleRetriever   stageRuleRetriever
	maxReview       int
	maxCriticRounds int
}

type stageRuleRetriever interface {
	Retrieve(stage string, issue *models.Issue) ([]string, string, error)
	Sync() error
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

	// Initialize agent runtime
	rt, err := runtime.New(cfg.RuntimeType, cfg.RuntimePath)
	if err != nil {
		return nil, fmt.Errorf("create runtime: %w", err)
	}
	log.WithFields(Fields{"runtime": rt.Name()}).Info("agent runtime initialized")

	maxReview := cfg.MaxReviewRounds
	if maxReview <= 0 {
		maxReview = 3
	}

	maxCriticRounds := cfg.MaxCriticRounds
	if maxCriticRounds < 0 {
		maxCriticRounds = 2
	}

	// Load self-learning rules
	rulesDir := cfg.RulesDir
	if rulesDir == "" {
		rulesDir = filepath.Join(filepath.Dir(promptsDir), "rules")
	}
	rl := rules.NewRuleLoader(rulesDir)
	if err := rl.Load(); err != nil {
		log.WithFields(Fields{"error": err}).Warn("failed to load rules, continuing without self-learning rules")
	} else if len(rl.All()) > 0 {
		log.WithFields(Fields{"count": len(rl.All())}).Info("loaded self-learning rules")
	}

	retriever, err := rules.NewRuleRetriever(cfg, database, rl)
	if err != nil {
		log.WithFields(Fields{"error": err}).Warn("failed to initialize semantic rule retrieval, falling back to full prompt snapshots")
		retriever = nil
	} else if retriever != nil {
		if err := retriever.Sync(); err != nil {
			log.WithFields(Fields{"error": err}).Warn("failed to sync semantic rule index, falling back to full prompt snapshots")
		}
	}

	return &Pipeline{
		cfg:             cfg,
		db:              database,
		gh:              gh,
		prompts:         ps,
		runner:          NewAgentRunner(ps, rt, timeout),
		ruleLoader:      rl,
		ruleRetriever:   retriever,
		maxReview:       maxReview,
		maxCriticRounds: maxCriticRounds,
	}, nil
}

func (p *Pipeline) getRulesForStageIssue(stage string, issue *models.Issue) ([]string, string) {
	fallbackIDs, fallbackPrompt := p.ruleLoader.PromptSnapshot(stage)
	if p.ruleRetriever == nil {
		return fallbackIDs, fallbackPrompt
	}

	ids, promptText, err := p.ruleRetriever.Retrieve(stage, issue)
	if err != nil {
		fields := Fields{"stage": stage, "error": err}
		if issue != nil {
			fields["repo"] = issue.Repo
			fields["issue"] = issue.IssueNumber
		}
		log.WithFields(fields).Warn("semantic rule retrieval failed, falling back to full prompt snapshot")
		return fallbackIDs, fallbackPrompt
	}

	return ids, promptText
}

func (p *Pipeline) syncRuleRetriever(reason string) {
	if p.ruleRetriever == nil {
		return
	}
	if err := p.ruleRetriever.Sync(); err != nil {
		log.WithFields(Fields{"reason": reason, "error": err}).Warn("failed to sync semantic rule index")
	}
}

// saveTrajectory persists an experience-replay trajectory. Non-fatal on error.
// prNumber is the GitHub PR number for this attempt; pass 0 when no PR was created.
func (p *Pipeline) saveTrajectory(issue *models.Issue, scout *ScoutResult, analyst *AnalystResult, reviewRounds int, reviewSummary string, prNumber int) {
	t := buildTrajectory(issue, scout, analyst, reviewRounds, reviewSummary, prNumber)
	if err := p.db.SaveTrajectory(t); err != nil {
		log.WithFields(Fields{"error": err, "issue": issue.IssueNumber}).Warn("failed to save trajectory")
	}
}

// recordEvent records a pipeline event for learning. Non-fatal on error.
// ruleIDs is the list of rule IDs that were injected into the agent prompt for this stage.
func (p *Pipeline) recordEvent(issue *models.Issue, prID *int64, stage string, round int, startedAt time.Time, verdict string, success bool, outputSummary string, errMsg string, ruleIDs []string) {
	now := time.Now()
	var experiencesUsed string
	if len(ruleIDs) > 0 {
		if b, err := marshalRuleIDs(ruleIDs); err == nil {
			experiencesUsed = b
		}
	}
	event := &models.PipelineEvent{
		IssueID:         issue.ID,
		PRID:            prID,
		Repo:            issue.Repo,
		IssueNumber:     issue.IssueNumber,
		Stage:           stage,
		Round:           round,
		StartedAt:       startedAt,
		CompletedAt:     &now,
		DurationSeconds: now.Sub(startedAt).Seconds(),
		OutputSummary:   truncate(outputSummary, 500),
		Verdict:         verdict,
		Success:         success,
		ErrorMessage:    truncate(errMsg, 500),
		ExperiencesUsed: experiencesUsed,
	}
	if err := p.db.RecordEvent(event); err != nil {
		log.WithFields(Fields{"error": err, "stage": stage}).Warn("failed to record pipeline event")
	}
}

// marshalRuleIDs encodes a slice of rule IDs to a JSON string.
func marshalRuleIDs(ids []string) (string, error) {
	b, err := json.Marshal(ids)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ProcessIssue runs the full pipeline for a single issue.
// It updates DB status at each stage boundary.
func (p *Pipeline) ProcessIssue(ctx context.Context, issue *models.Issue) error {
	// Rate limit: max open PRs per repo (higher for repos that merged our PRs)
	maxPR := p.cfg.MaxPRsPerRepo
	if maxPR <= 0 {
		maxPR = 2
	}
	mergedCount, _ := p.db.CountMergedPRsByRepo(issue.Repo)
	if mergedCount > 0 {
		maxPR = maxPR + mergedCount // e.g. 1 merged → max 2, 2 merged → max 3
	}
	openCount, _ := p.db.CountOpenPRsByRepo(issue.Repo)
	if openCount >= maxPR {
		p.markAbandoned(issue, fmt.Sprintf("rate limit: %d open PRs on %s (max %d)", openCount, issue.Repo, maxPR))
		log.WithFields(Fields{
			"repo": issue.Repo,
			"open": openCount,
			"max":  maxPR,
		}).Warn("skipping issue: too many open PRs on this repo")
		return nil
	}

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

	// Stage 1.6: Fork + Clone
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

	// Fail fast: if critic gate is enabled but template is absent, reject before
	// spending tokens on engineer/reviewer work (stale prompt-bundle detection).
	if p.maxCriticRounds > 0 && !p.prompts.Has("critic") {
		err := fmt.Errorf("critic gate is enabled (max_critic_rounds=%d) but critic prompt template is missing; refusing to bypass safety gate", p.maxCriticRounds)
		p.markFailed(issue, "critic_template_missing", err.Error())
		return err
	}

	// Stage 3+4: Engineer ⇄ Reviewer loop
	reviewRounds, reviewSummary, loopErr := p.engineerReviewLoopWithStats(ctx, issue, workspace, analyst)
	if loopErr != nil {
		p.saveTrajectory(issue, scout, analyst, reviewRounds, reviewSummary, 0)
		return loopErr
	}

	// Stage 4.5: Critic gate (external maintainer perspective)
	if err := p.criticLoop(ctx, issue, workspace, analyst); err != nil {
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
		Status:     models.PRStatusDraft,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := p.db.CreatePullRequest(pr); err != nil {
		log.WithError(err).Warn("save PR record")
	}

	if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusCompleted, ""); err != nil {
		log.WithError(err).Warn("update status to completed")
	}

	// Save trajectory for experience replay (outcome will be updated when PR merges/closes)
	p.saveTrajectory(issue, scout, analyst, reviewRounds, reviewSummary, submit.PRNumber)

	log.WithFields(Fields{
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
		log.WithFields(Fields{
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

// --- Workspace ---

func (p *Pipeline) createWorkspace(issue *models.Issue) (string, error) {
	dir := filepath.Join(p.cfg.WorkspaceDir, fmt.Sprintf("%s-%d",
		sanitizeRepoName(issue.Repo), issue.IssueNumber))

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// cleanupWorkspace removes the workspace directory for a PR after it reaches a terminal state.
func (p *Pipeline) cleanupWorkspace(pr *models.PullRequest) {
	issue, err := p.db.GetIssueByID(pr.IssueID)
	if err != nil || issue == nil {
		return
	}
	dir := filepath.Join(p.cfg.WorkspaceDir, fmt.Sprintf("%s-%d",
		sanitizeRepoName(issue.Repo), issue.IssueNumber))
	if err := os.RemoveAll(dir); err != nil {
		log.WithFields(Fields{"dir": dir, "error": err}).Warn("failed to cleanup workspace")
	} else {
		log.WithFields(Fields{"dir": dir, "pr": pr.PRURL}).Info("workspace cleaned up")
	}
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
