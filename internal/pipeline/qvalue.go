package pipeline

import (
	"encoding/json"
	"strings"

	"github.com/majiayu000/auto-contributor/internal/rules"
)

// qAlpha is the MemRL learning rate for Q-value updates.
// Q_new = Q_old + alpha * (reward - Q_old)
const qAlpha = 0.1

// rewardForOutcome maps outcome labels to scalar rewards.
//
//	merged         → 1.0  (full positive signal)
//	unknown_closed → 0.2  (closed without clear reason; may not be our fault)
//	auto_closed    → 0.5  (closed for external reasons such as inactivity/CI timeout;
//	                       not attributable to rule quality — neutral signal)
//	hostile_spam   → 0.5  (closed for hostile/spam moderation; neutral signal)
//	everything else → 0.0  (rejected for a specific reason)
func rewardForOutcome(outcomeLabel string) float64 {
	switch outcomeLabel {
	case OutcomeMerged:
		return 1.0
	case OutcomeUnknownClosed:
		return 0.2
	case OutcomeAutoClosed, OutcomeHostileSpam:
		return 0.5
	default:
		return 0.0
	}
}

// updateQValues backward-updates Q-values for all rules that participated in the
// pipeline for the given issue, once the PR has reached a terminal state.
//
// It reads experiences_used from each pipeline event (set at agent-run time),
// collects unique rule IDs, then applies the MemRL update rule:
//
//	Q_new = Q_old + alpha * (reward - Q_old)
//
// Called after extractAndStoreLessons, which has already labelled all events
// with the outcome via LabelEventsByIssue.
func (p *Pipeline) updateQValues(issueID int64) {
	events, err := p.db.GetEventsByIssue(issueID)
	if err != nil || len(events) == 0 {
		return
	}

	// Determine outcome from the first event that has a label set.
	var outcomeLabel string
	for _, e := range events {
		if e.OutcomeLabel != "" {
			outcomeLabel = e.OutcomeLabel
			break
		}
	}
	if outcomeLabel == "" {
		return
	}

	reward := rewardForOutcome(outcomeLabel)
	rulesDir := p.ruleLoader.RulesDir()

	// Collect unique participation keys across all events for this issue.
	// Keys are stored as "stage/ruleID" (new format) or bare "ruleID" (legacy).
	seen := make(map[string]bool)
	var participantKeys []string
	for _, e := range events {
		if e.ExperiencesUsed == "" {
			continue
		}
		var ids []string
		if err := json.Unmarshal([]byte(e.ExperiencesUsed), &ids); err != nil {
			continue
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				participantKeys = append(participantKeys, id)
			}
		}
	}

	if len(participantKeys) == 0 {
		return
	}

	// Apply Q-value update for each participating rule.
	updated := 0
	for _, key := range participantKeys {
		// Keys are stored as "stage/ruleID" (new format) or bare "ruleID" (legacy).
		var rule *rules.Rule
		var ruleID string
		if idx := strings.Index(key, "/"); idx >= 0 {
			rule = p.ruleLoader.ByStageAndID(key[:idx], key[idx+1:])
			ruleID = key[idx+1:]
		} else {
			rule = p.ruleLoader.ByID(key)
			ruleID = key
		}
		if rule == nil {
			continue
		}

		// Initialise QValue to 0.5 for rules that predate this feature.
		qOld := rule.QValue
		if qOld == 0 {
			qOld = 0.5
		}

		newQ := qOld + qAlpha*(reward-qOld)
		newRetrievals := rule.RetrievalCount + 1
		newSuccess := rule.SuccessCount
		if reward >= 1.0 {
			newSuccess++
		}

		if err := rules.UpdateRuleQValue(rulesDir, ruleID, rule.Stage, newQ, newRetrievals, newSuccess); err != nil {
			log.WithFields(Fields{"rule": key, "error": err}).Warn("failed to update rule Q-value")
			continue
		}
		updated++
	}

	if updated > 0 {
		// Reload in-memory cache so subsequent calls in this process use the
		// freshly-written Q-values/counters rather than the stale baseline.
		if err := p.ruleLoader.Reload(); err != nil {
			log.WithError(err).Warn("failed to reload rules after Q-value update")
		}
		log.WithFields(Fields{
			"issue":   issueID,
			"outcome": outcomeLabel,
			"rules":   updated,
			"reward":  reward,
		}).Info("updated rule Q-values (MemRL)")
	}
}
