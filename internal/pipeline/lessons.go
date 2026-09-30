package pipeline

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ghclient "github.com/majiayu000/auto-contributor/internal/github"
	"github.com/majiayu000/auto-contributor/internal/rules"
	"github.com/majiayu000/auto-contributor/pkg/models"
)

// lessonCategory maps keywords to feedback categories.
var lessonCategories = []struct {
	category string
	keywords []string
}{
	{"testing", []string{"test", "mock", "unittest", "coverage", "assert", "spec"}},
	{"scope", []string{"unnecessary", "unrelated", "scope", "minimal", "over-engineer", "extra", "not needed", "remove"}},
	{"style", []string{"naming", "format", "style", "convention", "indent", "whitespace", "lint"}},
	{"docs", []string{"comment", "docstring", "documentation", "readme", "typo"}},
	{"ci", []string{"ci", "build", "compile", "workflow", "action"}},
	{"logic", []string{"bug", "logic", "incorrect", "wrong", "fix", "error", "crash", "race", "nil"}},
	{"repo_structure", []string{"upstream", "belongs in", "other repo", "separate repo", "dependency bump", "go-mysql-server", "the actual change is in", "change is in"}},
}

// categorizeComment returns the best-matching category for a reviewer comment.
func categorizeComment(body string) string {
	lower := strings.ToLower(body)
	for _, cat := range lessonCategories {
		for _, kw := range cat.keywords {
			if strings.Contains(lower, kw) {
				return cat.category
			}
		}
	}
	return "other"
}

// extractLessons extracts actionable lessons from PR reviews and inline comments.
// It filters out bot comments and trivial approvals.
func extractLessons(
	pr *models.PullRequest,
	repo string,
	reviews []ghclient.PRReview,
	comments []ghclient.PRReviewComment,
) []*models.ReviewLesson {
	var lessons []*models.ReviewLesson

	// Extract from review-level comments (CHANGES_REQUESTED or substantive COMMENTED)
	for _, r := range reviews {
		if r.Body == "" || len(r.Body) < 20 {
			continue
		}
		if isBot(r.Author) {
			continue
		}
		// Only learn from substantive feedback
		if r.State != "CHANGES_REQUESTED" && r.State != "COMMENTED" {
			continue
		}

		lessons = append(lessons, &models.ReviewLesson{
			PRID:          pr.ID,
			Repo:          repo,
			Category:      categorizeComment(r.Body),
			Lesson:        summarizeToLesson(r.Body),
			SourceComment: truncate(r.Body, 500),
			Reviewer:      r.Author,
		})
	}

	// Extract from inline comments (these are usually the most specific feedback)
	for _, c := range comments {
		if c.Body == "" || len(c.Body) < 10 {
			continue
		}
		if isBot(c.Author) {
			continue
		}

		lesson := summarizeToLesson(c.Body)
		if c.Path != "" {
			lesson = fmt.Sprintf("[%s] %s", c.Path, lesson)
		}

		lessons = append(lessons, &models.ReviewLesson{
			PRID:          pr.ID,
			Repo:          repo,
			Category:      categorizeComment(c.Body),
			Lesson:        lesson,
			SourceComment: truncate(c.Body, 500),
			Reviewer:      c.Author,
		})
	}

	return lessons
}

// extractLessonsFromIssueComments extracts lessons from issue-level comments on a closed PR.
// Maintainers often explain close reasons here (e.g. "fix belongs in upstream dependency X").
func extractLessonsFromIssueComments(
	pr *models.PullRequest,
	repo string,
	comments []ghclient.IssueComment,
) []*models.ReviewLesson {
	var lessons []*models.ReviewLesson
	for _, c := range comments {
		if c.Body == "" || len(c.Body) < 20 {
			continue
		}
		if isBot(c.Author) || c.Body == responderCloseComment {
			continue
		}
		category := categorizeComment(c.Body)
		// Only record comments that match a known actionable category
		if category == "other" {
			continue
		}
		lessons = append(lessons, &models.ReviewLesson{
			PRID:          pr.ID,
			Repo:          repo,
			Category:      category,
			Lesson:        summarizeToLesson(c.Body),
			SourceComment: truncate(c.Body, 500),
			Reviewer:      c.Author,
		})
	}
	return lessons
}

// summarizeToLesson trims a reviewer comment to a concise actionable lesson.
func summarizeToLesson(body string) string {
	// Take first sentence or first 200 chars, whichever is shorter
	body = strings.TrimSpace(body)

	// Find first sentence boundary
	for i, ch := range body {
		if (ch == '.' || ch == '。') && i > 10 && i < 200 {
			return body[:i+1]
		}
	}

	if len(body) > 200 {
		return body[:200] + "..."
	}
	return body
}

// isBot returns true for known bot account patterns.
func isCodecovBot(author string) bool {
	lower := strings.ToLower(author)
	return strings.Contains(lower, "codecov")
}

func isCLABot(author string) bool {
	lower := strings.ToLower(author)
	claPatterns := []string{"cla-assistant", "claassistant", "cla-bot", "contributor-assistant"}
	for _, p := range claPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func isBot(author string) bool {
	lower := strings.ToLower(author)
	botPatterns := []string{"bot", "codecov", "netlify", "vercel", "dependabot", "renovate", "github-actions"}
	for _, p := range botPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// storeLessons uses the same feedback for outcome classification and lessons.
// Responder closes pass their polled comments to avoid fetching again after close.
func (p *Pipeline) storeLessons(pr *models.PullRequest, prRepo string, prInfo *ghclient.PRInfo, comments []ghclient.PRReviewComment, issueComments []ghclient.IssueComment) error {
	// Always update outcome and label events unconditionally — a transient DB error on a prior
	// call must not permanently stall outcome tracking even when lessons were already saved.
	label := ClassifyOutcome(prInfo, issueComments, comments, pr, p.cfg.GitHubUsername)
	if err := p.db.LabelEventsByIssue(pr.IssueID, label); err != nil {
		return fmt.Errorf("label pipeline events: %w", err)
	} else {
		log.WithFields(Fields{"pr": pr.PRURL, "outcome": label}).Info("labeled pipeline events")
	}
	success := label == OutcomeMerged
	if err := p.db.UpdateTrajectoryOutcome(pr.IssueID, pr.PRNumber, label, success); err != nil {
		// GitHub-synced PRs have no solve trajectory. Keep that existing absence
		// visible, while actual database failures leave terminal learning pending.
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("update trajectory outcome: %w", err)
		}
		log.WithError(err).WithField("pr", pr.PRURL).Warn("no trajectory to label")
	}

	// Stamp last_validated_at on synthesized rules when a PR merges.
	// Merged PRs confirm that the rules guiding those stages produced good output.
	if label == OutcomeMerged {
		if err := p.stampRuleValidation(pr); err != nil {
			return err
		}
	}

	// Skip lesson extraction if we already have lessons for this PR.
	count, err := p.db.CountLessonsByPR(pr.ID)
	if err != nil {
		return fmt.Errorf("count review lessons: %w", err)
	}
	if count > 0 {
		return nil
	}

	lessons := extractLessons(pr, prRepo, prInfo.Reviews, comments)

	// Also extract from issue comments when PR was closed without merge
	if prInfo.State == "CLOSED" {
		lessons = append(lessons, extractLessonsFromIssueComments(pr, prRepo, issueComments)...)
	}

	if len(lessons) > 0 {
		if err := p.db.SaveReviewLesson(lessons...); err != nil {
			return fmt.Errorf("save review lessons: %w", err)
		}
		log.WithFields(Fields{
			"pr":      pr.PRURL,
			"lessons": len(lessons),
		}).Info("extracted review lessons")
	}
	return nil
}

// stampRuleValidation sets last_validated_at on all synthesized rules for the pipeline
// stages that were active during this PR's lifecycle. Called only on merged outcomes.
func (p *Pipeline) stampRuleValidation(pr *models.PullRequest) error {
	events, err := p.db.GetEventsByIssue(pr.IssueID)
	if err != nil {
		return fmt.Errorf("fetch events for rule validation stamp: %w", err)
	}

	today := time.Now().Format("2006-01-02")
	rulesDir := p.ruleLoader.RulesDir()
	seenRules := make(map[string]bool)
	stamped := 0

	stampRule := func(stage, ruleID, originalKey string) error {
		normalizedKey := stage + "/" + ruleID
		if seenRules[normalizedKey] {
			return nil
		}
		seenRules[normalizedKey] = true

		rule := p.ruleLoader.ByStageAndID(stage, ruleID)
		if rule == nil || rule.Source != "synthesized" {
			return nil
		}
		if err := rules.UpdateRuleLastValidatedAt(rulesDir, ruleID, stage, today); err != nil {
			return fmt.Errorf("stamp rule %s last_validated_at: %w", originalKey, err)
		}
		stamped++
		return nil
	}

	for _, e := range events {
		if e.ExperiencesUsed == "" {
			continue
		}

		var ruleKeys []string
		if err := json.Unmarshal([]byte(e.ExperiencesUsed), &ruleKeys); err != nil {
			log.WithError(err).WithFields(Fields{"event": e.ID, "stage": e.Stage}).Warn("skipping malformed experiences_used for rule validation stamp")
			continue
		}

		for _, key := range ruleKeys {
			if parts := strings.SplitN(key, "/", 2); len(parts) == 2 {
				if err := stampRule(parts[0], parts[1], key); err != nil {
					return err
				}
				continue
			}

			// Legacy records stored bare rule IDs. Those IDs came from rules injected
			// for the event stage, which includes stage-specific and global rules.
			if err := stampRule(e.Stage, key, key); err != nil {
				return err
			}
			if e.Stage != "global" {
				if err := stampRule("global", key, key); err != nil {
					return err
				}
			}
		}
	}

	// Reload the in-memory rule cache so the next decay pass sees the updated
	// last_validated_at values and does not wrongly decay the just-stamped rules.
	if err := p.ruleLoader.Reload(); err != nil {
		return fmt.Errorf("reload rule cache after stamping last_validated_at: %w", err)
	}

	log.WithFields(Fields{
		"pr":    pr.PRURL,
		"rules": stamped,
	}).Info("stamped last_validated_at on synthesized rules for merged PR")
	return nil
}

// isNonActionable returns true for reviews that are just approvals/LGTM with no actionable content.
func isNonActionable(body string) bool {
	lower := strings.ToLower(strings.TrimSpace(body))
	if len(lower) < 30 {
		nonActionable := []string{"lgtm", "looks good", "approved", "ship it", "+1", "👍", "🚀"}
		for _, p := range nonActionable {
			if strings.Contains(lower, p) {
				return true
			}
		}
	}
	return false
}

// formatLessonsForPrompt formats stored lessons into text suitable for injection into agent prompts.
func formatLessonsForPrompt(lessons []*models.ReviewLesson) string {
	if len(lessons) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Lessons from Past Reviews\n\n")
	b.WriteString("Previous contributions received this feedback from upstream reviewers. Avoid repeating these mistakes:\n\n")

	seen := make(map[string]bool)
	for _, l := range lessons {
		// Deduplicate by lesson text
		key := l.Category + ":" + l.Lesson
		if seen[key] {
			continue
		}
		seen[key] = true

		fmt.Fprintf(&b, "- [%s] %s (from %s)\n", l.Category, l.Lesson, l.Repo)
	}

	return b.String()
}
