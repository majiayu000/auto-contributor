package pipeline

import (
	"encoding/json"

	"github.com/majiayu000/auto-contributor/internal/rules"
)

// qAlpha is the MemRL learning rate for Q-value updates.
// Q_new = Q_old + alpha * (reward - Q_old)
const qAlpha = 0.1

// rewardForOutcome maps outcome labels to scalar rewards.
//
//	merged        → 1.0  (full positive signal)
//	unknown_closed → 0.2  (closed without clear reason; may not be our fault)
//	everything else → 0.0  (rejected for a specific reason)
func rewardForOutcome(outcomeLabel string) float64 {
	switch outcomeLabel {
	case OutcomeMerged:
		return 1.0
	case OutcomeUnknownClosed:
		return 0.2
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

	// Collect unique rule IDs across all events for this issue.
	seen := make(map[string]bool)
	var ruleIDs []string
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
				ruleIDs = append(ruleIDs, id)
			}
		}
	}

	if len(ruleIDs) == 0 {
		return
	}

	// Apply Q-value update for each participating rule.
	updated := 0
	for _, ruleID := range ruleIDs {
		r := p.ruleLoader.ByID(ruleID)
		if r == nil {
			continue
		}

		// Initialise QValue to 0.5 for rules that predate this feature.
		qOld := r.QValue
		if qOld == 0 {
			qOld = 0.5
		}

		newQ := qOld + qAlpha*(reward-qOld)
		newRetrievals := r.RetrievalCount + 1
		newSuccess := r.SuccessCount
		if reward >= 1.0 {
			newSuccess++
		}

		if err := rules.UpdateRuleQValue(rulesDir, ruleID, r.Stage, newQ, newRetrievals, newSuccess); err != nil {
			log.WithFields(Fields{"rule": ruleID, "error": err}).Warn("failed to update rule Q-value")
			continue
		}
		updated++
	}

	if updated > 0 {
		log.WithFields(Fields{
			"issue":   issueID,
			"outcome": outcomeLabel,
			"rules":   updated,
			"reward":  reward,
		}).Info("updated rule Q-values (MemRL)")
	}
}
