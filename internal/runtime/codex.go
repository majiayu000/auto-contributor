package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CodexRuntime executes prompts via OpenAI Codex CLI.
type CodexRuntime struct {
	cliPath string
}

func NewCodex(cliPath string) *CodexRuntime {
	if cliPath == "" {
		cliPath = "codex"
	}
	return &CodexRuntime{cliPath: cliPath}
}

func (r *CodexRuntime) Name() string { return "codex" }

func (r *CodexRuntime) Execute(ctx context.Context, workDir string, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, r.cliPath,
		"exec",
		"--dangerously-bypass-approvals-and-sandbox",
		prompt,
	)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("codex exited with error: %w\nstderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (r *CodexRuntime) ExecuteStdin(ctx context.Context, prompt string) (string, error) {
	// Codex exec doesn't support stdin prompts, pass as argument
	cmd := exec.CommandContext(ctx, r.cliPath,
		"exec",
		"--dangerously-bypass-approvals-and-sandbox",
		prompt,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("codex timed out: %w", ctx.Err())
		}
		return "", fmt.Errorf("codex failed: %w\nstderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
