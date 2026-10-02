package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
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
// Called after storeLessons, which has already labelled all events
// with the outcome via LabelEventsByIssue.
func (p *Pipeline) updateQValues(issueID int64, rewardID string) (result error) {
	events, err := p.db.GetEventsByIssue(issueID)
	if err != nil {
		return fmt.Errorf("get events for Q-value update: %w", err)
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
		return nil
	}

	reward := rewardForOutcome(outcomeLabel)
	// A retry can receive feedback that changes the reward. Apply each PR/reward
	// once; reclassification with the same reward must not increment counts again.
	rewardID = fmt.Sprintf("%s#%g", rewardID, reward)

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
			log.WithError(err).WithFields(Fields{"event": e.ID, "stage": e.Stage}).Warn("skipping malformed experiences_used for Q-value update")
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
		return nil
	}

	rulesDir := p.ruleLoader.RulesDir()
	// Refresh after every successful writer call, including an already-applied
	// reward: the previous attempt may have committed but failed to Reload.
	updated := 0
	defer func() {
		if updated > 0 {
			if err := p.ruleLoader.Reload(); err != nil {
				result = errors.Join(result, fmt.Errorf("reload rules after Q-value update: %w", err))
			}
		}
	}()
	// Apply Q-value update for each participating rule.
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

		if err := rules.UpdateRuleQValue(rulesDir, ruleID, rule.Stage, rewardID, reward, qAlpha); err != nil {
			return fmt.Errorf("update rule %s Q-value: %w", key, err)
		}
		updated++
	}

	if updated > 0 {
		log.WithFields(Fields{
			"issue":   issueID,
			"outcome": outcomeLabel,
			"rules":   updated,
			"reward":  reward,
		}).Info("processed rule Q-value rewards (MemRL)")
	}
	return nil
}
