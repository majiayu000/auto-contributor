package runtime

import (
	"context"
)

// Runtime abstracts the agent execution backend (Claude, Codex, Aider, etc.)
type Runtime interface {
	// Name returns the runtime identifier (e.g. "claude", "codex", "aider")
	Name() string

	// Execute runs a prompt in the given working directory and returns raw output.
	Execute(ctx context.Context, workDir string, prompt string) (string, error)

	// ExecuteStdin is like Execute but passes the prompt via stdin instead of args.
	// Used for long prompts that exceed arg length limits (e.g. discovery).
	ExecuteStdin(ctx context.Context, prompt string) (string, error)
}
