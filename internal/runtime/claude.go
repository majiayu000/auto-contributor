package runtime

import (
	"bytes"
	"context"
	"encoding/json"
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

func (r *ClaudeRuntime) Execute(ctx context.Context, workDir string, prompt string, policy ExecutionPolicy) (string, error) {
	args := []string{"--print"}
	if policy.allowsPrivilegedExecution() {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, "-p", prompt, "--output-format", "text")
	cmd := exec.CommandContext(ctx, r.cliPath, args...)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("claude exited with error: %w\nstderr: %s", err, strings.TrimSpace(stderr.String()))
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		stderrMsg := strings.TrimSpace(stderr.String())
		if stderrMsg != "" {
			return "", fmt.Errorf("claude returned empty output, stderr: %s", stderrMsg)
		}
		return "", fmt.Errorf("claude returned empty output (silent failure)")
	}
	return output, nil
}

// ExecuteJSON runs a prompt with a JSON schema constraint for reliable structured output.
// Uses --output-format json --json-schema for constrained decoding (~99% reliability).
// Returns the structured_output field from the JSON envelope, falling back to result.
func (r *ClaudeRuntime) ExecuteJSON(ctx context.Context, workDir string, prompt string, jsonSchema string, policy ExecutionPolicy) (string, error) {
	args := []string{"--print"}
	if policy.allowsPrivilegedExecution() {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, "-p", prompt, "--output-format", "json", "--json-schema", jsonSchema)
	cmd := exec.CommandContext(ctx, r.cliPath, args...)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("claude exited with error: %w\nstderr: %s", err, strings.TrimSpace(stderr.String()))
	}

	raw := stdout.String()
	if strings.TrimSpace(raw) == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		if strings.Contains(strings.ToLower(stderrStr), "rate limit") || strings.Contains(stderrStr, "429") {
			return "", fmt.Errorf("claude rate limited: %s", stderrStr)
		}
		return "", fmt.Errorf("claude returned empty output")
	}

	// Extract structured_output from the JSON envelope
	var envelope struct {
		StructuredOutput json.RawMessage `json:"structured_output"`
		Result           string          `json:"result"`
		IsError          bool            `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		// Not a JSON envelope — return raw for caller to parse
		return raw, nil
	}
	if envelope.IsError {
		return "", fmt.Errorf("claude returned error in JSON envelope")
	}
	if len(envelope.StructuredOutput) > 0 {
		return string(envelope.StructuredOutput), nil
	}
	if envelope.Result != "" {
		return envelope.Result, nil
	}
	return raw, nil
}

func (r *ClaudeRuntime) ExecuteStdin(ctx context.Context, prompt string, policy ExecutionPolicy) (string, error) {
	args := []string{"--print"}
	if policy.allowsPrivilegedExecution() {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, "--output-format", "text")
	cmd := exec.CommandContext(ctx, r.cliPath, args...)
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
