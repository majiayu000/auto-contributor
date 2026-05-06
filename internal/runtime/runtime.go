package runtime

import (
	"context"
)

// ExecutionPolicy controls whether a runtime may opt into privileged host access.
// The zero value is treated as untrusted/restricted.
type ExecutionPolicy string

const (
	ExecutionPolicyUntrusted ExecutionPolicy = "untrusted"
	ExecutionPolicyTrusted   ExecutionPolicy = "trusted"
)

func (p ExecutionPolicy) allowsPrivilegedExecution() bool {
	return p == ExecutionPolicyTrusted
}

// Runtime abstracts the agent execution backend (Claude, Codex, Aider, etc.)
type Runtime interface {
	// Name returns the runtime identifier (e.g. "claude", "codex", "aider")
	Name() string

	// Execute runs a prompt in the given working directory and returns raw output.
	Execute(ctx context.Context, workDir string, prompt string, policy ExecutionPolicy) (string, error)

	// ExecuteStdin is like Execute but passes the prompt via stdin instead of args.
	// Used for long prompts that exceed arg length limits (e.g. discovery).
	ExecuteStdin(ctx context.Context, prompt string, policy ExecutionPolicy) (string, error)
}
