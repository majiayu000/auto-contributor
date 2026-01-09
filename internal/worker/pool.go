package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/majiayu000/auto-contributor/internal/claude"
	"github.com/majiayu000/auto-contributor/internal/codex"
	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/internal/executor"
	"github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// WorkerPhase represents the current phase of work
type WorkerPhase string

const (
	PhaseIdle       WorkerPhase = "idle"
	PhaseCloning    WorkerPhase = "cloning"
	PhaseEvaluating WorkerPhase = "evaluating"
	PhaseSolving    WorkerPhase = "solving"
	PhaseTesting    WorkerPhase = "testing"
	PhaseValidating WorkerPhase = "validating"
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
	executor   executor.Executor

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
	WillRetry bool // true if this issue will be retried
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

	// Select executor based on config
	var exec executor.Executor
	switch cfg.ExecutorType {
	case "codex":
		exec = codex.New(cfg)
	default:
		exec = claude.New(cfg)
	}

	return &Pool{
		config:     cfg,
		db:         database,
		ghClient:   ghClient,
		executor:   exec,
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

// QueueSize returns the current queue size (from DB + memory queue)
func (p *Pool) QueueSize() int {
	dbCount, err := p.db.GetPendingIssueCount()
	if err != nil {
		dbCount = 0
	}
	return dbCount + len(p.issueQueue)
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

// GetExecutorOutput returns the executor output buffer
func (p *Pool) GetExecutorOutput() []executor.OutputLine {
	return p.executor.GetOutputBuffer()
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

// run is the main worker loop - now pulls from DB instead of memory queue
func (w *Worker) run() {
	defer w.pool.wg.Done()

	for {
		select {
		case <-w.pool.ctx.Done():
			return
		default:
			// Try to claim an issue from DB
			issue, err := w.pool.db.ClaimNextPendingIssue(w.ID)
			if err != nil {
				// Log error and wait before retrying
				time.Sleep(5 * time.Second)
				continue
			}

			if issue == nil {
				// No pending issues, also check the legacy in-memory queue
				select {
				case <-w.pool.ctx.Done():
					return
				case memIssue, ok := <-w.pool.issueQueue:
					if !ok {
						return
					}
					w.processIssue(memIssue)
				case <-time.After(10 * time.Second):
					// No issues available, wait and retry
					continue
				}
			} else {
				// Process the issue from DB
				w.processIssue(issue)
			}
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
		if w.handleFailure(issue, models.FailureReasonCloneFailed, result.Error.Error()) {
			result.WillRetry = true
		}
		return
	}

	// Setup git config
	w.pool.executor.SetupGitConfig(repoDir)

	// Phase 2: Evaluate complexity
	w.updateStatus(PhaseEvaluating, 0.2, "Evaluating project complexity...")

	complexity, err := w.pool.executor.EvaluateComplexity(w.pool.ctx, repoDir, issue)
	if err != nil {
		// Continue with default complexity
		complexity = &executor.ComplexityResult{
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
		w.handleFailure(issue, models.FailureReasonComplexityHigh, "Issue too complex for automated solving")
		return
	}

	// Check for existing PR
	hasPR, _ := w.pool.ghClient.HasExistingPR(w.pool.ctx, issue.Repo, issue.IssueNumber)
	if hasPR {
		result.Error = fmt.Errorf("issue already has PR")
		attempt.FailureReason = models.FailureReasonAlreadyHasPR
		w.handleFailure(issue, models.FailureReasonAlreadyHasPR, "Issue already has a PR")
		return
	}

	// Phase 3: Solve with AI
	w.updateStatus(PhaseSolving, 0.4, "AI is working on the fix...")

	// Create branch
	branchName := executor.SanitizeBranchName(issue.IssueNumber, issue.Title)
	if err := w.pool.executor.CreateBranch(repoDir, branchName); err != nil {
		result.Error = fmt.Errorf("create branch: %w", err)
		return
	}

	// Fork repo BEFORE solve (so AI can push to fork)
	err = w.pool.ghClient.ForkRepo(w.pool.ctx, issue.Repo)
	if err != nil && !isAlreadyForked(err) {
		result.Error = fmt.Errorf("fork failed: %w", err)
		return
	}

	// Add fork as remote
	forkRemote := fmt.Sprintf("https://github.com/%s/%s.git",
		w.pool.config.GitHubUsername,
		filepath.Base(issue.Repo))
	configureRemote(repoDir, "fork", forkRemote)

	// Run AI executor to solve
	solveResult, err := w.pool.executor.Solve(w.pool.ctx, repoDir, issue, complexity)
	if err != nil {
		result.Error = fmt.Errorf("solve failed: %w", err)
		attempt.FailureReason = models.FailureReasonUnknown
		if w.handleFailure(issue, models.FailureReasonUnknown, result.Error.Error()) {
			result.WillRetry = true
		}
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

	// Check if issue was already fixed in the codebase
	if solveResult.AlreadyFixed {
		result.Error = fmt.Errorf("issue already fixed in codebase")
		attempt.FailureReason = models.FailureReasonAlreadyFixed
		w.handleFailure(issue, models.FailureReasonAlreadyFixed, "Issue already fixed in codebase")
		return
	}

	// Check if fix is complete
	if !solveResult.FixComplete || len(solveResult.FilesChanged) == 0 {
		result.Error = fmt.Errorf("fix incomplete or no changes made")
		attempt.FailureReason = models.FailureReasonNoChanges
		if w.handleFailure(issue, models.FailureReasonNoChanges, "Fix incomplete") {
			result.WillRetry = true
		}
		return
	}

	// Trust AI's test results - it already ran tests in the solve phase
	// Only fail if AI explicitly reported tests failed
	if solveResult.TestsPassed != nil && !*solveResult.TestsPassed {
		result.Error = fmt.Errorf("AI reported tests failed")
		attempt.FailureReason = models.FailureReasonTestsFailed
		if w.handleFailure(issue, models.FailureReasonTestsFailed, "Tests failed during solve") {
			result.WillRetry = true
		}
		return
	}

	w.updateStatus(PhaseValidating, 0.8, "AI completed fix with passing tests")

	// Phase 6: Review, Fix, and Create PR (all handled by AI)
	w.updateStatus(PhaseCreatingPR, 0.9, "AI is reviewing and creating PR...")

	// Push current branch to fork (fork remote was configured before solve)
	if err := w.pool.executor.PushBranch(repoDir, "fork", branchName); err != nil {
		result.Error = fmt.Errorf("push failed: %w", err)
		return
	}

	// Let AI review, fix any issues, and create PR
	reviewResult, err := w.pool.executor.Review(w.pool.ctx, repoDir, issue, 3)
	if err != nil {
		result.Error = fmt.Errorf("review failed: %w", err)
		attempt.FailureReason = models.FailureReasonPRFailed
		return
	}

	if !reviewResult.Passed || reviewResult.PRURL == "" {
		result.Error = fmt.Errorf("review failed or PR not created: %s", reviewResult.Output)
		attempt.FailureReason = models.FailureReasonPRFailed
		return
	}

	// Success!
	result.Success = true
	result.PRCreated = true
	result.PRURL = reviewResult.PRURL

	// Save PR to database
	prRecord := &models.PullRequest{
		IssueID:    issue.ID,
		PRURL:      reviewResult.PRURL,
		PRNumber:   reviewResult.PRNumber,
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

// hasUncommittedChanges checks if there are uncommitted or untracked changes
func hasUncommittedChanges(repoDir string) bool {
	// Check for uncommitted changes (staged or unstaged)
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

// handleFailure handles issue failure with retry logic
// Returns true if issue will be retried, false if abandoned
func (w *Worker) handleFailure(issue *models.Issue, reason models.FailureReason, errorMsg string) bool {
	// Check if shutting down
	select {
	case <-w.pool.ctx.Done():
		// Shutting down, don't retry
		w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusDiscovered, "Shutdown during processing")
		return false
	default:
	}

	// Check if this failure is retryable
	if !reason.IsRetryable() {
		w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned, errorMsg)
		return false
	}

	// Check retry count
	if issue.RetryCount >= models.MaxRetries {
		w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned,
			fmt.Sprintf("%s (max retries %d reached)", errorMsg, models.MaxRetries))
		return false
	}

	// Increment retry count and requeue
	issue.RetryCount++
	w.pool.db.IncrementIssueRetryCount(issue.ID)
	w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusDiscovered,
		fmt.Sprintf("Retry %d/%d: %s", issue.RetryCount, models.MaxRetries, errorMsg))

	// Re-add to queue for retry (with context check to avoid closed channel panic)
	select {
	case <-w.pool.ctx.Done():
		// Shutting down
		return false
	case w.pool.issueQueue <- issue:
		return true
	default:
		// Queue full, abandon
		w.pool.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned, "Retry failed: queue full")
		return false
	}
}
