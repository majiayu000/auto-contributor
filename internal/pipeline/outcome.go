package pipeline

import (
	"strings"

	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// Outcome labels for pipeline events.
const (
	OutcomeMerged          = "merged"
	OutcomeRejectedScope   = "rejected_scope"
	OutcomeRejectedQuality = "rejected_quality"
	OutcomeRejectedStyle   = "rejected_style"
	OutcomeRejectedDupe    = "rejected_duplicate"
	OutcomeRejectedUnwant  = "rejected_unwanted"
	OutcomeAutoClosed      = "auto_closed"
	OutcomeUnknownClosed   = "unknown_closed"
)

var outcomeKeywords = map[string][]string{
	OutcomeRejectedDupe:    {"duplicate", "already fixed", "already addressed", "existing pr", "another pr"},
	OutcomeRejectedScope:   {"scope", "unrelated", "unnecessary", "not needed", "over-engineer", "out of scope", "extra changes"},
	OutcomeRejectedQuality: {"incorrect", "wrong", "bug", "broken", "test fail", "doesn't work", "not working", "logic error"},
	OutcomeRejectedStyle:   {"style", "format", "convention", "naming", "lint", "whitespace"},
	OutcomeRejectedUnwant:  {"won't fix", "wontfix", "not needed", "closing", "not accepting", "won't merge"},
}

// ClassifyOutcome determines why a PR reached its terminal state.
func ClassifyOutcome(prInfo *ghclient.PRInfo, issueComments []ghclient.IssueComment, pr *models.PullRequest) string {
	if prInfo.State == "MERGED" {
		return OutcomeMerged
	}

	// Check if we auto-closed it
	for _, c := range issueComments {
		lower := strings.ToLower(c.Body)
		if c.Author == "majiayu000" && (strings.Contains(lower, "closing due to extended inactivity") || strings.Contains(lower, "ci failures remain unresolved")) {
			return OutcomeAutoClosed
		}
	}

	// Collect all feedback text
	var allText strings.Builder
	for _, r := range prInfo.Reviews {
		if isBot(r.Author) {
			continue
		}
		allText.WriteString(strings.ToLower(r.Body))
		allText.WriteString(" ")
	}
	for _, c := range issueComments {
		if isBot(c.Author) {
			continue
		}
		allText.WriteString(strings.ToLower(c.Body))
		allText.WriteString(" ")
	}

	text := allText.String()

	// Match keywords in priority order (duplicate first, most specific)
	for _, label := range []string{OutcomeRejectedDupe, OutcomeRejectedScope, OutcomeRejectedQuality, OutcomeRejectedStyle, OutcomeRejectedUnwant} {
		for _, kw := range outcomeKeywords[label] {
			if strings.Contains(text, kw) {
				return label
			}
		}
	}

	return OutcomeUnknownClosed
}
