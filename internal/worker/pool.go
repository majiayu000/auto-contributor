package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/majiayu000/auto-contributor/internal/claude"
	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// WorkerPhase represents the current phase of work
type WorkerPhase string

const (
	PhaseIdle      WorkerPhase = "idle"
	PhaseCloning   WorkerPhase = "cloning"
	PhaseEvaluating WorkerPhase = "evaluating"
	PhaseSolving   WorkerPhase = "solving"
	PhaseTesting   WorkerPhase = "testing"
	PhaseCreatingPR WorkerPhase = "creating_pr"
)

// WorkerStatus holds real-time status of a worker
type WorkerStatus struct {
	ID             int         `json:"id"`
	Phase          WorkerPhase `json:"phase"`
	CurrentIssue   *models.Issue `json:"current_issue,omitempty"`
	Progress       float64     `json:"progress"`
	LastOutput     string      `json:"last_output"`
	StartedAt      *time.Time  `json:"started_at,omitempty"`
	TasksCompleted int         `json:"tasks_completed"`
	TasksFailed    int         `json:"tasks_failed"`
	Error          string      `json:"error,omitempty"`
}

// Pool manages a group of workers
type Pool struct {
	config     *config.Config
	db         *db.DB
	ghClient   *github.Client
	executor   *claude.Executor

	workers    []*Worker
	workersMu  sync.RWMutex

	issueQueue chan *models.Issue
	results    chan *WorkResult

	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	// Callbacks for monitoring
	onStatusChange func(workerID int, status *WorkerStatus)
}

// WorkResult holds the result of processing an issue
type WorkResult struct {
	WorkerID  int
	Issue     *models.Issue
	Success   bool
	PRCreated bool
	PRURL     string
	Error     error
	Duration  time.Duration
}

// Worker represents a single worker goroutine
type Worker struct {
	ID       int
	pool     *Pool
	status   *WorkerStatus
	statusMu sync.RWMutex
}

// NewPool creates a new worker pool
func NewPool(cfg *config.Config, database *db.DB, ghClient *github.Client) *Pool {
	ctx, cancel := context.WithCancel(context.Background())

	return &Pool{
		config:     cfg,
		db:         database,
		ghClient:   ghClient,
		executor:   claude.New(cfg),
		issueQueue: make(chan *models.Issue, cfg.WorkerQueueSize),
		results:    make(chan *WorkResult, cfg.WorkerQueueSize),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// SetStatusCallback sets the callback for worker status changes
func (p *Pool) SetStatusCallback(cb func(workerID int, status *WorkerStatus)) {
	p.onStatusChange = cb
}

// Start launches all workers
func (p *Pool) Start() {
	p.workersMu.Lock()
	p.workers = make([]*Worker, p.config.WorkerCount)

	for i := 0; i < p.config.WorkerCount; i++ {
		w := &Worker{
			ID:   i,
			pool: p,
			status: &WorkerStatus{
				ID:    i,
				Phase: PhaseIdle,
			},
		}
		p.workers[i] = w

		p.wg.Add(1)
		go w.run()
	}
	p.workersMu.Unlock()
}

// Stop gracefully stops all workers
func (p *Pool) Stop() {
	p.cancel()
	close(p.issueQueue)
	p.wg.Wait()
	close(p.results)
}

// Submit adds an issue to the queue
func (p *Pool) Submit(issue *models.Issue) error {
	select {
	case p.issueQueue <- issue:
		return nil
	default:
		return fmt.Errorf("queue is full")
	}
}

// QueueSize returns the current queue size
func (p *Pool) QueueSize() int {
	return len(p.issueQueue)
}

// Results returns the results channel
func (p *Pool) Results() <-chan *WorkResult {
	return p.results
}

// GetAllStatus returns status of all workers
func (p *Pool) GetAllStatus() []*WorkerStatus {
	p.workersMu.RLock()
	defer p.workersMu.RUnlock()

	statuses := make([]*WorkerStatus, len(p.workers))
	for i, w := range p.workers {
		w.statusMu.RLock()
		// Create a copy
		status := *w.status
		statuses[i] = &status
		w.statusMu.RUnlock()
	}

	return statuses
}

// GetSystemStats returns aggregated system statistics
func (p *Pool) GetSystemStats() *models.SystemStats {
	statuses := p.GetAllStatus()

	stats := &models.SystemStats{
		QueueSize: p.QueueSize(),
		Workers:   make([]models.WorkerState, len(statuses)),
	}

	for i, s := range statuses {
		if s.Phase == PhaseIdle {
			stats.IdleWorkers++
		} else {
			stats.ActiveWorkers++
		}

		workerState := models.WorkerState{
			ID:             s.ID,
			Phase:          string(s.Phase),
			Progress:       s.Progress,
			LastOutput:     s.LastOutput,
			TasksCompleted: s.TasksCompleted,
			TasksFailed:    s.TasksFailed,
		}
		if s.Phase != PhaseIdle {
			workerState.Status = "running"
		} else {
			workerState.Status = "idle"
		}
		if s.CurrentIssue != nil {
			workerState.CurrentIssue = s.CurrentIssue
		}
		if s.StartedAt != nil {
			workerState.StartedAt = *s.StartedAt
		}

		stats.Workers[i] = workerState
	}

	return stats
}

// run is the main worker loop
func (w *Worker) run() {
	defer w.pool.wg.Done()

	for {
		select {
		case <-w.pool.ctx.Done():
			return
		case issue, ok := <-w.pool.issueQueue:
			if !ok {
				return
			}
			w.processIssue(issue)
		}
	}
}

// updateStatus updates worker status and notifies callback
func (w *Worker) updateStatus(phase WorkerPhase, progress float64, output string) {
	w.statusMu.Lock()
	w.status.Phase = phase
	w.status.Progress = progress
	if output != "" {
		w.status.LastOutput = output
	}
	status := *w.status
	w.statusMu.Unlock()

	if w.pool.onStatusChange != nil {
		w.pool.onStatusChange(w.ID, &status)
	}
}

// processIssue handles a single issue end-to-end
func (w *Worker) processIssue(issue *models.Issue) {
	startTime := time.Now()

	// Update status
	w.statusMu.Lock()
	w.status.CurrentIssue = issue
	now := time.Now()
	w.status.StartedAt = &now
	w.status.Error = ""
	w.statusMu.Unlock()

	result := &WorkResult{
		WorkerID: w.ID,
		Issue:    issue,
	}

	// Update issue status in DB
	w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusProcessing, "")

	// Create solve attempt record
	attempt := &models.SolveAttempt{
		IssueID:       issue.ID,
		AttemptNumber: 1,
		StartedAt:     startTime,
		ModelUsed:     "claude-sonnet-4",
	}

	// Get attempt count
	count, _ := w.pool.db.GetAttemptCount(issue.ID)
	attempt.AttemptNumber = count + 1

	w.pool.db.CreateSolveAttempt(attempt)

	defer func() {
		// Update attempt completion
		completedAt := time.Now()
		attempt.CompletedAt = &completedAt
		attempt.DurationSeconds = time.Since(startTime).Seconds()
		attempt.Success = result.Success
		if result.Error != nil {
			attempt.ErrorDetails = result.Error.Error()
		}
		w.pool.db.UpdateSolveAttempt(attempt)

		// Update worker stats
		w.statusMu.Lock()
		if result.Success {
			w.status.TasksCompleted++
		} else {
			w.status.TasksFailed++
		}
		w.status.CurrentIssue = nil
		w.status.StartedAt = nil
		w.status.Phase = PhaseIdle
		w.status.Progress = 0
		w.statusMu.Unlock()

		// Send result
		result.Duration = time.Since(startTime)
		select {
		case w.pool.results <- result:
		default:
		}
	}()

	// Phase 1: Clone repository
	w.updateStatus(PhaseCloning, 0.1, "Cloning repository...")

	repoDir := filepath.Join(w.pool.config.WorkspaceDir, fmt.Sprintf("worker-%d", w.ID), issue.Repo)
	os.RemoveAll(repoDir) // Clean up previous work
	os.MkdirAll(filepath.Dir(repoDir), 0755)

	repoURL := fmt.Sprintf("https://github.com/%s.git", issue.Repo)
	if err := w.pool.executor.CloneRepo(w.pool.ctx, repoURL, repoDir); err != nil {
		result.Error = fmt.Errorf("clone failed: %w", err)
		attempt.FailureReason = models.FailureReasonCloneFailed
		w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned, result.Error.Error())
		return
	}

	// Setup git config
	w.pool.executor.SetupGitConfig(repoDir)

	// Phase 2: Evaluate complexity
	w.updateStatus(PhaseEvaluating, 0.2, "Evaluating project complexity...")

	complexity, err := w.pool.executor.EvaluateComplexity(w.pool.ctx, repoDir, issue)
	if err != nil {
		// Continue with default complexity
		complexity = &claude.ComplexityResult{
			IsComplex:      false,
			CanTestLocally: true,
		}
	}

	attempt.IsComplex = &complexity.IsComplex
	attempt.CanTestLocally = &complexity.CanTestLocally
	attempt.TestFramework = complexity.TestFramework
	if len(complexity.Reasons) > 0 {
		reasonsJSON, _ := json.Marshal(complexity.Reasons)
		attempt.ComplexityReasons = string(reasonsJSON)
	}

	// Check if too complex
	if complexity.IsComplex && !complexity.CanTestLocally {
		w.updateStatus(PhaseIdle, 0, "Issue too complex, skipping...")
		result.Error = fmt.Errorf("issue too complex")
		attempt.FailureReason = models.FailureReasonComplexityHigh
		w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned, "Issue too complex for automated solving")
		return
	}

	// Check for existing PR
	hasPR, _ := w.pool.ghClient.HasExistingPR(w.pool.ctx, issue.Repo, issue.IssueNumber)
	if hasPR {
		result.Error = fmt.Errorf("issue already has PR")
		attempt.FailureReason = models.FailureReasonAlreadyHasPR
		w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned, "Issue already has a PR")
		return
	}

	// Phase 3: Solve with Claude
	w.updateStatus(PhaseSolving, 0.4, "Claude is working on the fix...")

	// Create branch
	branchName := claude.SanitizeBranchName(issue.IssueNumber, issue.Title)
	if err := w.pool.executor.CreateBranch(repoDir, branchName); err != nil {
		result.Error = fmt.Errorf("create branch: %w", err)
		return
	}

	// Run Claude to solve
	solveResult, err := w.pool.executor.Solve(w.pool.ctx, repoDir, issue, complexity)
	if err != nil {
		result.Error = fmt.Errorf("solve failed: %w", err)
		attempt.FailureReason = models.FailureReasonUnknown
		w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned, result.Error.Error())
		return
	}

	// Record solve attempt details
	attempt.FixCompleteMarker = solveResult.FixComplete
	attempt.ClaudeTestsPassed = solveResult.TestsPassed
	if len(solveResult.FilesChanged) > 0 {
		filesJSON, _ := json.Marshal(solveResult.FilesChanged)
		attempt.FilesChanged = string(filesJSON)
	}
	if len(solveResult.Output) > 1000 {
		attempt.ClaudeOutputPreview = solveResult.Output[:1000]
	} else {
		attempt.ClaudeOutputPreview = solveResult.Output
	}

	// Check if fix is complete
	if !solveResult.FixComplete || len(solveResult.FilesChanged) == 0 {
		result.Error = fmt.Errorf("fix incomplete or no changes made")
		attempt.FailureReason = models.FailureReasonNoChanges
		w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned, "Fix incomplete")
		return
	}

	// Phase 4: Run external tests (if applicable)
	if complexity.CanTestLocally {
		w.updateStatus(PhaseTesting, 0.7, "Running tests...")

		testPassed, testOutput, testDuration, _ := w.pool.executor.RunTests(w.pool.ctx, repoDir, complexity.TestFramework)

		attempt.ExternalTestPassed = &testPassed
		attempt.TestDurationSeconds = testDuration.Seconds()
		if len(testOutput) > 1000 {
			attempt.TestOutputPreview = testOutput[:1000]
		} else {
			attempt.TestOutputPreview = testOutput
		}

		if !testPassed {
			result.Error = fmt.Errorf("tests failed")
			attempt.FailureReason = models.FailureReasonTestsFailed
			w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned, "Tests failed")
			return
		}
	}

	// Phase 5: Create PR
	w.updateStatus(PhaseCreatingPR, 0.9, "Creating pull request...")

	// Commit changes
	commitMsg := fmt.Sprintf("Fix: %s\n\nFixes #%d\n\n🤖 Generated with Claude Code", issue.Title, issue.IssueNumber)
	if err := w.pool.executor.CommitChanges(repoDir, commitMsg); err != nil {
		result.Error = fmt.Errorf("commit failed: %w", err)
		return
	}

	// Fork and push
	err = w.pool.ghClient.ForkRepo(w.pool.ctx, issue.Repo)
	if err != nil && !isAlreadyForked(err) {
		result.Error = fmt.Errorf("fork failed: %w", err)
		return
	}

	// Add fork as remote and push
	forkRemote := fmt.Sprintf("https://github.com/%s/%s.git",
		w.pool.config.GitHubUsername,
		filepath.Base(issue.Repo))

	// Configure remote
	configureRemote(repoDir, "fork", forkRemote)

	if err := w.pool.executor.PushBranch(repoDir, "fork", branchName); err != nil {
		result.Error = fmt.Errorf("push failed: %w", err)
		return
	}

	// Create PR
	prTitle := fmt.Sprintf("Fix: %s", issue.Title)
	prBody := fmt.Sprintf(`## Summary
This PR fixes #%d

## Changes
%s

---
🤖 Generated with [Claude Code](https://claude.com/claude-code)`,
		issue.IssueNumber,
		formatChangedFiles(solveResult.FilesChanged))

	head := fmt.Sprintf("%s:%s", w.pool.config.GitHubUsername, branchName)
	prURL, prNumber, err := w.pool.ghClient.CreatePullRequest(w.pool.ctx, issue.Repo, prTitle, prBody, head, "main")
	if err != nil {
		result.Error = fmt.Errorf("create PR failed: %w", err)
		attempt.FailureReason = models.FailureReasonPRFailed
		return
	}

	// Success!
	result.Success = true
	result.PRCreated = true
	result.PRURL = prURL

	// Save PR to database
	prRecord := &models.PullRequest{
		IssueID:    issue.ID,
		PRURL:      prURL,
		PRNumber:   prNumber,
		BranchName: branchName,
		Status:     models.PRStatusOpen,
		CIStatus:   "pending",
	}
	w.pool.db.CreatePullRequest(prRecord)

	// Update issue status
	w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusPRCreated, "")

	w.updateStatus(PhaseIdle, 1.0, fmt.Sprintf("PR created: %s", result.PRURL))
}

func isAlreadyForked(err error) bool {
	return err != nil && (
		contains(err.Error(), "already exists") ||
		contains(err.Error(), "try again later"))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func configureRemote(repoDir, name, url string) error {
	// Try to add remote
	addCmd := exec.Command("git", "remote", "add", name, url)
	addCmd.Dir = repoDir
	if err := addCmd.Run(); err != nil {
		// If add fails (already exists), try to set-url
		setCmd := exec.Command("git", "remote", "set-url", name, url)
		setCmd.Dir = repoDir
		return setCmd.Run()
	}
	return nil
}

func formatChangedFiles(files []string) string {
	if len(files) == 0 {
		return "No files changed"
	}

	var result string
	for _, f := range files {
		result += fmt.Sprintf("- `%s`\n", f)
	}
	return result
}
