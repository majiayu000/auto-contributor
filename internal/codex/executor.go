package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/executor"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// Executor runs OpenAI Codex CLI to solve issues
type Executor struct {
	config       *config.Config
	outputMu     sync.Mutex
	lastOutput   string
	outputBuffer []executor.OutputLine
	bufferMu     sync.RWMutex
	logDir       string
}

// New creates a new Codex executor
func New(cfg *config.Config) *Executor {
	// Create log directory
	logDir := filepath.Join(os.TempDir(), "auto-contributor", "codex-logs")
	os.MkdirAll(logDir, 0755)

	return &Executor{
		config:       cfg,
		outputBuffer: make([]executor.OutputLine, 0, 200),
		logDir:       logDir,
	}
}

// saveLog saves output to a log file for debugging
func (e *Executor) saveLog(operation string, issue *models.Issue, prompt string, output string, duration time.Duration) {
	if e.logDir == "" {
		return
	}

	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("%s_%s_%d_%s.log", timestamp, sanitizeForFilename(issue.Repo), issue.IssueNumber, operation)
	logPath := filepath.Join(e.logDir, filename)

	content := fmt.Sprintf(`=== Codex %s Log ===
Time: %s
Repo: %s
Issue: #%d - %s
Duration: %v

=== PROMPT ===
%s

=== OUTPUT ===
%s
`, operation, time.Now().Format(time.RFC3339), issue.Repo, issue.IssueNumber, issue.Title, duration, prompt, output)

	os.WriteFile(logPath, []byte(content), 0644)
	e.addOutput("log", fmt.Sprintf("Log saved to: %s", logPath))
}

// sanitizeForFilename removes invalid characters from repo name
func sanitizeForFilename(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "/", "_"), "\\", "_")
}

// hasOutputMarker checks if a marker appears in Codex's actual output (not in the echoed prompt)
// Markers in the prompt appear in instruction text, while actual markers appear:
// 1. Near the end of output
// 2. On a line by themselves or followed by whitespace/punctuation
func hasOutputMarker(output, marker string) bool {
	// Look for marker in the last 2000 characters of output (actual Codex response)
	searchArea := output
	if len(output) > 2000 {
		searchArea = output[len(output)-2000:]
	}

	// Check for marker on its own line or at start of line
	lines := strings.Split(searchArea, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Match: "MARKER", "MARKER ", "MARKER:", "MARKER\n", etc.
		if strings.HasPrefix(trimmed, marker) {
			// Make sure it's not part of a longer word or instruction
			rest := strings.TrimPrefix(trimmed, marker)
			if rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == ':' || rest[0] == '.' {
				return true
			}
		}
	}
	return false
}

// GetOutputBuffer returns recent output lines
func (e *Executor) GetOutputBuffer() []executor.OutputLine {
	e.bufferMu.RLock()
	defer e.bufferMu.RUnlock()
	result := make([]executor.OutputLine, len(e.outputBuffer))
	copy(result, e.outputBuffer)
	return result
}

// ClearOutputBuffer clears the output buffer
func (e *Executor) ClearOutputBuffer() {
	e.bufferMu.Lock()
	defer e.bufferMu.Unlock()
	e.outputBuffer = e.outputBuffer[:0]
}

// GetLastOutput returns the most recent output line
func (e *Executor) GetLastOutput() string {
	e.outputMu.Lock()
	defer e.outputMu.Unlock()
	return e.lastOutput
}

// addOutput adds a line to the output buffer
func (e *Executor) addOutput(outputType, content string) {
	e.bufferMu.Lock()
	defer e.bufferMu.Unlock()

	line := executor.OutputLine{
		Time:    time.Now(),
		Type:    outputType,
		Content: content,
	}
	e.outputBuffer = append(e.outputBuffer, line)

	// Keep only last 200 lines
	if len(e.outputBuffer) > 200 {
		e.outputBuffer = e.outputBuffer[len(e.outputBuffer)-200:]
	}
}

// EvaluateComplexity asks Codex to evaluate project complexity
func (e *Executor) EvaluateComplexity(ctx context.Context, repoDir string, issue *models.Issue) (*executor.ComplexityResult, error) {
	prompt := fmt.Sprintf(`Analyze this project and determine if the issue can be solved with local testing.

Issue #%d: %s
%s

Evaluate:
1. Is this a complex issue requiring external dependencies (APIs, databases, cloud services)?
2. Can tests be run locally with standard tools?
3. What test framework does this project use?

Respond ONLY with JSON:
{
  "is_complex": true/false,
  "can_test_locally": true/false,
  "reasons": ["reason1", "reason2"],
  "test_framework": "pytest/jest/go test/etc"
}`, issue.IssueNumber, issue.Title, issue.Body)

	cmd := exec.CommandContext(ctx, "codex", "exec", "--yolo", prompt)
	cmd.Dir = repoDir

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("codex evaluation failed: %w", err)
	}

	// Parse JSON from output
	result := &executor.ComplexityResult{}
	outputStr := string(output)

	// Find JSON in output
	jsonStart := strings.Index(outputStr, "{")
	jsonEnd := strings.LastIndex(outputStr, "}")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		jsonStr := outputStr[jsonStart : jsonEnd+1]
		if err := json.Unmarshal([]byte(jsonStr), result); err != nil {
			// Default to conservative estimate
			result.IsComplex = true
			result.CanTestLocally = false
		}
	}

	return result, nil
}

// Solve runs Codex to fix an issue
func (e *Executor) Solve(ctx context.Context, repoDir string, issue *models.Issue, complexity *executor.ComplexityResult) (*executor.Result, error) {
	startTime := time.Now()

	// Clear output buffer for new task
	e.ClearOutputBuffer()
	e.addOutput("info", fmt.Sprintf("Starting fix for %s#%d: %s", issue.Repo, issue.IssueNumber, issue.Title))

	prompt := e.buildSolvePrompt(issue, complexity)

	// Create timeout context
	timeout := e.config.ClaudeTimeout // Reuse same timeout config
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "codex", "exec", "--yolo", prompt)
	cmd.Dir = repoDir

	// Capture output
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}

	// Read output in background
	var outputBuilder strings.Builder
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			outputBuilder.WriteString(line + "\n")

			if line != "" {
				outputType := "text"
				if strings.Contains(line, "Tool:") || strings.Contains(line, "Using tool") {
					outputType = "tool"
				} else if strings.Contains(line, "Error") || strings.Contains(line, "error") {
					outputType = "stderr"
				}
				e.addOutput(outputType, line)

				e.outputMu.Lock()
				e.lastOutput = line
				e.outputMu.Unlock()
			}
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			outputBuilder.WriteString("[stderr] " + line + "\n")
			e.addOutput("stderr", line)
		}
	}()

	// Wait for command to finish
	cmdErr := cmd.Wait()
	wg.Wait()

	output := outputBuilder.String()
	duration := time.Since(startTime)

	result := &executor.Result{
		Output:   output,
		Duration: duration,
	}

	// Parse markers from output - look for markers on their own line (not in prompt text)
	// The prompt echoes markers in instructions, so we need to find the actual output markers
	// which appear at the end or on a line by themselves
	result.AlreadyFixed = hasOutputMarker(output, "ALREADY_FIXED")
	result.FixComplete = hasOutputMarker(output, "FIX_COMPLETE")
	if hasOutputMarker(output, "FIX_INCOMPLETE") {
		result.FixComplete = false
	}

	// Parse test results
	if strings.Contains(output, "TESTS_PASSED: true") {
		passed := true
		result.TestsPassed = &passed
	} else if strings.Contains(output, "TESTS_PASSED: false") {
		passed := false
		result.TestsPassed = &passed
	}

	// Set complexity info
	if complexity != nil {
		result.IsComplex = &complexity.IsComplex
		result.CanTestLocally = &complexity.CanTestLocally
		result.ComplexityReasons = complexity.Reasons
	}

	// Get changed files
	result.FilesChanged = e.getChangedFiles(repoDir)

	// Determine success
	if cmdErr != nil {
		result.Error = cmdErr
		result.Success = false
	} else {
		result.Success = result.FixComplete && len(result.FilesChanged) > 0
	}

	// Save log for debugging
	e.saveLog("solve", issue, prompt, output, duration)

	return result, nil
}

// buildSolvePrompt constructs the prompt for solving an issue
func (e *Executor) buildSolvePrompt(issue *models.Issue, complexity *executor.ComplexityResult) string {
	return fmt.Sprintf(`You are fixing GitHub issue #%d in repository %s.

## Issue
**Title:** %s
**Body:**
%s

## Instructions

### Phase 1: Deep Understanding (DO NOT SKIP)

1. **Read and understand the issue thoroughly:**
   - Analyze what the issue is REALLY asking for
   - Understand the user's intent, not just the surface request
   - Identify what success looks like

2. **Verify the issue still needs fixing (BE VERY CAREFUL HERE):**
   - Search codebase thoroughly for the EXACT functionality requested
   - Finding similar code is NOT enough - verify it actually solves THIS specific issue
   - ONLY mark as ALREADY_FIXED if you find CONCRETE EVIDENCE:
     * The exact feature/fix described in the issue is fully implemented
     * You can point to specific code that addresses every point in the issue
     * Tests exist that cover the requested functionality
   - When in doubt, ATTEMPT THE FIX - do not give up early
   - If you mark ALREADY_FIXED, you MUST explain exactly where the fix exists

3. **Study the project:**
   - Read CONTRIBUTING.md if exists
   - Check .github/workflows/*.yml for CI requirements
   - Understand code style and patterns from existing code
   - Identify test framework used

### Phase 2: Implementation (MINIMAL & CORRECT)

4. **Plan before coding:**
   - Design the minimal fix
   - NO over-engineering, NO unnecessary abstractions
   - NO hardcoding unless absolutely required
   - Follow existing patterns exactly

5. **Implement the fix:**
   - Make ONLY changes that directly address the issue
   - Add tests if project has test files and it makes sense
   - Each test must verify the specific fix

### Phase 3: Verification (ALL MUST PASS)

6. **Run ALL project tests:**
   - Find test command from: Makefile, package.json, CI config
   - Run: go test ./... OR npm test OR pytest OR make test
   - If tests fail: FIX them and re-run
   - Do NOT proceed until ALL tests pass

7. **Format and lint:**
   - Go: gofmt -w .
   - JS/TS: prettier (if used)
   - Python: black/ruff (if used)
   - Run project's own lint: make lint, npm run lint, etc.

### Phase 4: Final Commit

8. **Git config and commit:**
   git config user.name "majiayu000"
   git config user.email "1835304752@qq.com"
   git add -A
   git commit -s -m "fix: <concise description>"

   **CRITICAL COMMIT RULES:**
   - ALWAYS use -s flag for DCO sign-off on EVERY commit (required by many projects)
   - If you need to make additional commits (e.g., lint fixes), ALWAYS use: git commit -s -m "..."
   - NEVER add "Generated with Codex" or similar AI markers
   - NEVER add "Co-Authored-By" headers
   - Keep commit message simple and human-like

### Quality Checklist (verify ALL before FIX_COMPLETE)
- [ ] Fix addresses the actual issue requirement
- [ ] No debug code, no console.log, no print statements left
- [ ] No unrelated changes included
- [ ] All existing tests pass
- [ ] New tests added where appropriate
- [ ] Code follows project style
- [ ] No hardcoded values unless necessary
- [ ] No meaningless comments (e.g. "// Initialize the variable", "// Return the result")
- [ ] ALL commits use -s flag for DCO sign-off
- [ ] NO AI markers in commit messages (no "Generated with Codex", no "Co-Authored-By")

## Output Markers (output ONE on its own line)
- FIX_COMPLETE - fix done, ALL tests pass locally
- FIX_INCOMPLETE - cannot complete (explain why)
- ALREADY_FIXED - issue already resolved in codebase (REQUIRES PROOF: cite specific file:line where fix exists)

**IMPORTANT**: Only use ALREADY_FIXED if you have DEFINITIVE proof. If unsure, attempt the fix!

Also output: TESTS_PASSED: true/false`,
		issue.IssueNumber, issue.Repo, issue.Title, issue.Body)
}

// getChangedFiles returns list of modified files using git
func (e *Executor) getChangedFiles(repoDir string) []string {
	var files []string

	// Check for committed changes on the current branch vs origin/HEAD
	cmd := exec.Command("git", "diff", "--name-only", "HEAD~1")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !contains(files, line) {
				files = append(files, line)
			}
		}
	}

	// Also try comparing against origin/main or origin/master
	for _, baseBranch := range []string{"origin/main", "origin/master"} {
		cmd = exec.Command("git", "diff", "--name-only", baseBranch+"...HEAD")
		cmd.Dir = repoDir
		output, err = cmd.Output()
		if err == nil {
			for _, line := range strings.Split(string(output), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !contains(files, line) {
					files = append(files, line)
				}
			}
			break
		}
	}

	// Check uncommitted changes
	cmd = exec.Command("git", "diff", "--name-only")
	cmd.Dir = repoDir
	output, err = cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !contains(files, line) {
				files = append(files, line)
			}
		}
	}

	// Check staged files
	cmd = exec.Command("git", "diff", "--name-only", "--staged")
	cmd.Dir = repoDir
	output, err = cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !contains(files, line) {
				files = append(files, line)
			}
		}
	}

	// Check untracked files
	cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = repoDir
	output, err = cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !contains(files, line) {
				files = append(files, line)
			}
		}
	}

	return files
}

// RunTests executes tests in the repository
func (e *Executor) RunTests(ctx context.Context, repoDir string, framework string) (bool, string, time.Duration, error) {
	startTime := time.Now()

	var cmd *exec.Cmd
	switch strings.ToLower(framework) {
	case "pytest", "python":
		cmd = exec.CommandContext(ctx, "pytest", "-v", "--tb=short")
	case "jest", "npm", "javascript", "typescript":
		cmd = exec.CommandContext(ctx, "npm", "test")
	case "go", "go test":
		cmd = exec.CommandContext(ctx, "go", "test", "./...")
	case "cargo", "rust":
		cmd = exec.CommandContext(ctx, "cargo", "test")
	default:
		// Try to detect
		if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err == nil {
			cmd = exec.CommandContext(ctx, "go", "test", "./...")
		} else if _, err := os.Stat(filepath.Join(repoDir, "package.json")); err == nil {
			cmd = exec.CommandContext(ctx, "npm", "test")
		} else if _, err := os.Stat(filepath.Join(repoDir, "pytest.ini")); err == nil {
			cmd = exec.CommandContext(ctx, "pytest", "-v")
		} else if _, err := os.Stat(filepath.Join(repoDir, "Cargo.toml")); err == nil {
			cmd = exec.CommandContext(ctx, "cargo", "test")
		} else {
			return false, "Unknown test framework", 0, fmt.Errorf("unknown test framework")
		}
	}

	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	duration := time.Since(startTime)

	passed := err == nil
	return passed, string(output), duration, nil
}

// ValidateCode uses Codex to validate changes based on project's CI config
func (e *Executor) ValidateCode(ctx context.Context, repoDir string) (*executor.ValidationResult, error) {
	result := &executor.ValidationResult{Passed: true}

	changedFiles := e.getChangedFiles(repoDir)
	if len(changedFiles) == 0 {
		result.Warnings = append(result.Warnings, "No changed files to validate")
		return result, nil
	}

	prompt := fmt.Sprintf(`You are validating code changes for a PR.

## Changed Files
%s

## Instructions
1. First, examine the project's CI configuration:
   - Check .github/workflows/*.yml for CI commands
   - Check Makefile for lint/test targets
   - Check package.json scripts (for JS projects)
   - Check pyproject.toml or setup.cfg (for Python projects)

2. Run ONLY the project's own validation commands that apply to the changed files:
   - If project has "make lint", use that
   - If project has "npm run lint", use that
   - If project has CI lint commands, use those
   - Do NOT run generic golangci-lint if the project doesn't use it

3. For basic formatting:
   - Go: run gofmt -w on changed .go files
   - JS/TS: run prettier if project uses it
   - Python: run black/ruff if project uses it

4. Focus ONLY on the changed files, not the entire project

## Output
Output exactly one of:
- VALIDATION_PASSED: true - if your changes pass validation
- VALIDATION_PASSED: false - if validation fails

If validation fails, briefly explain why.
Be concise.`, strings.Join(changedFiles, "\n"))

	cmd := exec.CommandContext(ctx, "codex", "exec", "--yolo", prompt)
	cmd.Dir = repoDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		result.Warnings = append(result.Warnings, "Codex validation skipped: "+err.Error())
		return result, nil
	}

	outputStr := string(output)

	if strings.Contains(outputStr, "VALIDATION_PASSED: false") {
		result.Passed = false
		lines := strings.Split(outputStr, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "VALIDATION_PASSED") {
				result.Errors = append(result.Errors, line)
			}
		}
	} else {
		result.Passed = true
		result.Warnings = append(result.Warnings, "Codex validation passed")
	}

	return result, nil
}

// Review runs Codex to review, fix, and create PR
func (e *Executor) Review(ctx context.Context, repoDir string, issue *models.Issue, maxRounds int) (*executor.ReviewResult, error) {
	startTime := time.Now()

	if maxRounds <= 0 {
		maxRounds = 3
	}

	e.addOutput("info", fmt.Sprintf("Starting review for %s#%d", issue.Repo, issue.IssueNumber))

	changedFiles := e.getChangedFiles(repoDir)
	if len(changedFiles) == 0 {
		return &executor.ReviewResult{Passed: false, Output: "No changes to review"}, nil
	}

	prompt := e.buildReviewPrompt(issue, changedFiles, maxRounds)

	timeout := e.config.ClaudeTimeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "codex", "exec", "--yolo", prompt)
	cmd.Dir = repoDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("review failed: %w", err)
	}

	outputStr := string(output)
	result := &executor.ReviewResult{
		Output: outputStr,
	}

	if strings.Contains(outputStr, "REVIEW_PASSED") {
		result.Passed = true
		result.PRURL, result.PRNumber = extractPRURL(outputStr)
	} else if strings.Contains(outputStr, "REVIEW_FAILED") {
		result.Passed = false
	} else {
		prURL, prNum := extractPRURL(outputStr)
		if prURL != "" {
			result.Passed = true
			result.PRURL = prURL
			result.PRNumber = prNum
		}
	}

	// Save log for debugging
	e.saveLog("review", issue, prompt, outputStr, time.Since(startTime))

	return result, nil
}

// extractPRURL extracts PR URL from output
func extractPRURL(output string) (string, int) {
	prPattern := regexp.MustCompile(`https://github\.com/[^/]+/[^/]+/pull/(\d+)`)
	matches := prPattern.FindStringSubmatch(output)
	if len(matches) >= 2 {
		prNum := 0
		fmt.Sscanf(matches[1], "%d", &prNum)
		return matches[0], prNum
	}
	return "", 0
}

// buildReviewPrompt constructs the prompt for reviewing and creating PR
func (e *Executor) buildReviewPrompt(issue *models.Issue, changedFiles []string, maxRounds int) string {
	return fmt.Sprintf(`You are reviewing code changes for GitHub issue #%d in repository %s.

## Issue
**Title:** %s
**Body:**
%s

## Changed Files
%s

## Review & Submit Process (up to %d review rounds)

### Step 1: Critical Review

Analyze deeply:

1. **Does it ACTUALLY solve the issue?**
   - Re-read the issue carefully
   - Verify the fix addresses the REAL requirement
   - Check for misunderstandings of the issue intent

2. **Is it minimal and correct?**
   - Remove any over-engineering
   - Remove unnecessary abstractions
   - Remove unrelated changes
   - NO hardcoded values unless necessary

3. **Code quality check:**
   - Follows project conventions
   - No debug code or print statements left
   - Clean and readable
   - No meaningless comments (e.g. "// Initialize the variable", "// Return the result")

4. **Tests verification:**
   - Run ALL project tests
   - Fix any failures before proceeding

If issues found: FIX them, run tests again, re-review.

### Step 2: Final Verification

Before creating PR, verify:
- [ ] All tests pass: go test ./... OR npm test OR pytest
- [ ] Code is formatted: gofmt -w . OR prettier OR black
- [ ] Lint passes: make lint OR npm run lint (if exists)

### Step 3: Commit and Push

git config user.name "majiayu000"
git config user.email "1835304752@qq.com"
git add -A
git commit -s -m "fix: <concise description>"

**CRITICAL COMMIT/PR RULES:**
- NEVER add "Generated with Codex" or any AI markers in commit or PR
- NEVER add "Co-Authored-By" headers
- NEVER add emojis in commit messages or PR body
- Keep everything simple and human-like

Get branch: git branch --show-current
Push: git push fork <branch-name> --force

### Step 4: Create Pull Request

Get default branch:
gh repo view %s --json defaultBranchRef -q .defaultBranchRef.name

Create PR (keep body SHORT and direct, NO AI markers):
gh pr create --repo %s --title "fix: %s" --body "Fixes #%d

## Changes
- <key change 1>
- <key change 2>" --head majiayu000:<branch-name> --base <default-branch>

### Output
- REVIEW_PASSED - PR created (include PR URL)
- REVIEW_FAILED - cannot fix (explain why)`,
		issue.IssueNumber, issue.Repo, issue.Title, issue.Body,
		strings.Join(changedFiles, "\n"), maxRounds,
		issue.Repo, issue.Repo, issue.Title, issue.IssueNumber)
}

// CommitChanges commits all changes with DCO sign-off
func (e *Executor) CommitChanges(repoDir, message string) error {
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	cmd = exec.Command("git", "commit", "-s", "-m", message)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	return nil
}

// CreateBranch creates and switches to a new branch
func (e *Executor) CreateBranch(repoDir, branchName string) error {
	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = repoDir
	return cmd.Run()
}

// PushBranch pushes a branch to remote
func (e *Executor) PushBranch(repoDir, remote, branchName string) error {
	cmd := exec.Command("git", "push", "-u", remote, branchName)
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %s - %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// CloneRepo clones a repository using gh CLI
func (e *Executor) CloneRepo(ctx context.Context, repoURL, destDir string) error {
	os.RemoveAll(destDir)
	if err := os.MkdirAll(filepath.Dir(destDir), 0755); err != nil {
		return fmt.Errorf("failed to create parent dir: %w", err)
	}

	repoName := strings.TrimSuffix(strings.TrimPrefix(repoURL, "https://github.com/"), ".git")

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		cmd := exec.CommandContext(ctx, "gh", "repo", "clone", repoName, destDir, "--", "--depth", "1")
		output, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}

		lastErr = fmt.Errorf("attempt %d: %s - %w", attempt, strings.TrimSpace(string(output)), err)
		outputStr := string(output)

		if strings.Contains(outputStr, "Could not resolve") ||
			strings.Contains(outputStr, "not found") ||
			strings.Contains(outputStr, "Repository not found") {
			return fmt.Errorf("repository not accessible: %s", repoName)
		}

		os.RemoveAll(destDir)

		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 3 * time.Second)
		}
	}

	return lastErr
}

// SetupGitConfig configures git user for commits
func (e *Executor) SetupGitConfig(repoDir string) error {
	setupCmd := exec.Command("gh", "auth", "setup-git")
	setupCmd.Run()

	cmds := [][]string{
		{"git", "config", "user.name", e.config.GitHubUsername},
		{"git", "config", "user.email", e.config.GitHubEmail},
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			return err
		}
	}

	return nil
}

// SanitizeBranchName creates a valid branch name from issue title
func SanitizeBranchName(issueNum int, title string) string {
	reg := regexp.MustCompile(`[^a-zA-Z0-9\s-]`)
	clean := reg.ReplaceAllString(title, "")
	clean = strings.ReplaceAll(clean, " ", "-")
	clean = strings.ToLower(clean)
	if len(clean) > 30 {
		clean = clean[:30]
	}
	timestamp := time.Now().Format("0102-1504")
	return fmt.Sprintf("fix-%d-%s-%s", issueNum, clean, timestamp)
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
