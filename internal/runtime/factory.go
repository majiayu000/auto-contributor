package runtime

import "fmt"

// New creates a Runtime by name. cliPath overrides the default binary location.
func New(name string, cliPath string) (Runtime, error) {
	switch name {
	case "claude", "":
		return NewClaude(cliPath), nil
	case "codex":
		return NewCodex(cliPath), nil
	default:
		return nil, fmt.Errorf("unknown runtime: %q (supported: claude, codex)", name)
	}
}
