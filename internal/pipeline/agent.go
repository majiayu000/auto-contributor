package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/majiayu000/auto-contributor/internal/prompt"
)

// AgentRunner executes Claude CLI with a rendered prompt and parses JSON output.
type AgentRunner struct {
	prompts *prompt.Store
	timeout time.Duration
}

// NewAgentRunner creates a runner bound to the given prompt store.
func NewAgentRunner(ps *prompt.Store, timeout time.Duration) *AgentRunner {
	return &AgentRunner{prompts: ps, timeout: timeout}
}

// Run renders the named prompt template with ctx, invokes Claude CLI in workDir,
// and returns the raw output string.
func (r *AgentRunner) Run(ctx context.Context, agentName string, workDir string, tmplCtx map[string]any) (string, error) {
	rendered, err := r.prompts.Render(agentName, tmplCtx)
	if err != nil {
		return "", fmt.Errorf("render prompt %s: %w", agentName, err)
	}

	timeout := r.timeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	log.WithFields(Fields{
		"agent":   agentName,
		"workdir": workDir,
	}).Info("running agent")

	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"--dangerously-skip-permissions",
		"-p", rendered,
		"--output-format", "text")
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude for %s: %w", agentName, err)
	}

	var out strings.Builder
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		out.WriteString(line + "\n")
	}

	if err := cmd.Wait(); err != nil {
		return out.String(), fmt.Errorf("agent %s exited with error: %w\noutput: %s", agentName, err, truncate(out.String(), 500))
	}

	return out.String(), nil
}

// RunJSON is like Run but also extracts the first JSON object from the output.
func (r *AgentRunner) RunJSON(ctx context.Context, agentName string, workDir string, tmplCtx map[string]any, dest any) (string, error) {
	raw, err := r.Run(ctx, agentName, workDir, tmplCtx)
	if err != nil {
		return raw, err
	}

	if err := extractJSON(raw, dest); err != nil {
		return raw, fmt.Errorf("parse %s JSON output: %w", agentName, err)
	}

	return raw, nil
}

// extractJSON finds and unmarshals the first JSON object in text.
func extractJSON(text string, dest any) error {
	start := strings.Index(text, "{")
	if start < 0 {
		return fmt.Errorf("no JSON object found in output")
	}

	// Find matching closing brace, handling nesting
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return json.Unmarshal([]byte(text[start:i+1]), dest)
			}
		}
	}

	return fmt.Errorf("unclosed JSON object in output")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
