package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ClaudeRuntime executes prompts via Claude Code CLI.
type ClaudeRuntime struct {
	cliPath string
}

func NewClaude(cliPath string) *ClaudeRuntime {
	if cliPath == "" {
		cliPath = "claude"
	}
	return &ClaudeRuntime{cliPath: cliPath}
}

func (r *ClaudeRuntime) Name() string { return "claude" }

func (r *ClaudeRuntime) Execute(ctx context.Context, workDir string, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, r.cliPath,
		"--print",
		"--dangerously-skip-permissions",
		"-p", prompt,
		"--output-format", "text",
	)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("claude exited with error: %w\nstderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (r *ClaudeRuntime) ExecuteStdin(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, r.cliPath,
		"--print",
		"--dangerously-skip-permissions",
		"--output-format", "text",
	)
	cmd.Stdin = strings.NewReader(prompt)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("claude timed out: %w", ctx.Err())
		}
		return "", fmt.Errorf("claude failed: %w", err)
	}
	return string(output), nil
}
