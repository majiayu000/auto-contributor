package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"

	"github.com/majiayu000/auto-contributor/internal/config"
)

// Client wraps the gh CLI for GitHub API calls
type Client struct {
	config *config.Config
}

// New creates a new GitHub client using gh CLI
func New(cfg *config.Config) *Client {
	return &Client{config: cfg}
}

// ghAPI executes a gh api command and returns the result
func (c *Client) ghAPI(ctx context.Context, endpoint string, args ...string) ([]byte, error) {
	cmdArgs := []string{"api", endpoint}
	cmdArgs = append(cmdArgs, args...)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", cmdArgs...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// GetUsername gets the authenticated username
func (c *Client) GetUsername(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".login")

	// Capture both stdout and stderr for better debugging
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("gh api user failed: %w, stderr: %s", err, stderr.String())
	}

	username := strings.TrimSpace(stdout.String())
	if username == "" {
		return "", fmt.Errorf("gh returned empty username, stderr: %s", stderr.String())
	}

	return username, nil
}

// isExcludedRepo checks if a repo is in the exclude list
func (c *Client) isExcludedRepo(repo string) bool {
	for _, excluded := range c.config.ExcludeRepos {
		if repo == excluded {
			return true
		}
	}
	return false
}

// estimateDifficulty provides initial difficulty estimate
func (c *Client) estimateDifficulty(labels []string, repo *RepoInfo) float64 {
	score := 0.5

	for _, label := range labels {
		name := strings.ToLower(label)
		if strings.Contains(name, "good first") || strings.Contains(name, "beginner") {
			score -= 0.2
		}
		if strings.Contains(name, "complex") || strings.Contains(name, "difficult") {
			score += 0.2
		}
	}

	if repo.Stars > 10000 {
		score += 0.1
	}

	if repo.HasClaudeMD {
		score -= 0.15
	}

	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}

	return score
}

// decodeBase64 decodes a base64 string
func decodeBase64(s string) (string, error) {
	// GitHub API returns base64 with possible line breaks
	s = strings.ReplaceAll(s, "\\n", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\"", "")

	// Use encoding/base64 to decode
	decoded, err := base64Decode(s)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// base64Decode decodes base64 string using standard library
func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
