package pipeline

import (
	"fmt"
	"strings"

	"github.com/majiayu000/auto-contributor/pkg/models"
	log "github.com/sirupsen/logrus"
)

func containsMarker(output, marker string) bool {
	return strings.Contains(output, marker)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

func (p *Pipeline) markFailed(issue *models.Issue, reason, details string) {
	msg := fmt.Sprintf("%s: %s", reason, details)
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if err := p.db.MarkIssueFailed(issue.ID, msg, false); err != nil {
		log.WithError(err).Warn("mark issue failed")
	}
}

func (p *Pipeline) markAbandoned(issue *models.Issue, reason string) {
	if err := p.db.UpdateIssueStatus(issue.ID, models.IssueStatusAbandoned, reason); err != nil {
		log.WithError(err).Warn("mark issue abandoned")
	}
}

func (p *Pipeline) logReview(issue *models.Issue, review *CodeReviewResult, round int) {
	log.WithFields(log.Fields{
		"repo":       issue.Repo,
		"issue":      issue.IssueNumber,
		"round":      round,
		"verdict":    review.Verdict,
		"confidence": review.Confidence,
		"issues":     len(review.IssuesFound),
		"summary":    review.Summary,
	}).Info("review completed")
}
