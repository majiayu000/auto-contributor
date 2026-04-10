package rules

import (
	"math"
	"strings"
	"unicode"
)

// DedupAction describes the outcome of a deduplication check.
type DedupAction string

const (
	// DedupActionNew means the candidate rule is sufficiently novel — create it.
	DedupActionNew DedupAction = "new"
	// DedupActionMerge means the candidate is nearly identical to an existing rule
	// (similarity >= MergeThreshold). Increment the matched rule's evidence_count
	// instead of creating a new rule.
	DedupActionMerge DedupAction = "merge"
	// DedupActionPossibleDuplicate means the candidate is similar but not identical
	// (similarity in [ReviewThreshold, MergeThreshold)). Flag for human review.
	DedupActionPossibleDuplicate DedupAction = "possible_duplicate"

	// MergeThreshold is the cosine-similarity score above which two rules are
	// considered duplicates and should be merged.
	MergeThreshold = 0.85
	// ReviewThreshold is the lower bound for flagging a rule as a possible duplicate.
	ReviewThreshold = 0.70
)

// DedupResult is the outcome of CheckDedup for a single candidate rule.
type DedupResult struct {
	// Action is what the synthesizer should do with the candidate rule.
	Action DedupAction
	// Score is the highest cosine-similarity score found against existing rules.
	Score float64
	// MatchedRuleID is the ID of the most-similar existing rule (empty when Action == DedupActionNew).
	MatchedRuleID string
}

// CheckDedup computes cosine similarity between a candidate rule text and all
// existing rules, then returns the recommended action.
//
// candidateText should be the concatenation of the candidate rule's Condition
// and Body fields.
func CheckDedup(candidateText string, existingRules []*Rule) DedupResult {
	if len(existingRules) == 0 || strings.TrimSpace(candidateText) == "" {
		return DedupResult{Action: DedupActionNew}
	}

	candVec := termFreq(tokenize(candidateText))

	bestScore := 0.0
	bestID := ""
	for _, r := range existingRules {
		existingText := r.Condition + " " + r.Body
		existVec := termFreq(tokenize(existingText))
		score := cosineSimilarity(candVec, existVec)
		if score > bestScore {
			bestScore = score
			bestID = r.ID
		}
	}

	action := DedupActionNew
	switch {
	case bestScore >= MergeThreshold:
		action = DedupActionMerge
	case bestScore >= ReviewThreshold:
		action = DedupActionPossibleDuplicate
	}

	return DedupResult{
		Action:        action,
		Score:         bestScore,
		MatchedRuleID: bestID,
	}
}

// DedupIndex holds pre-computed TF vectors for a set of existing rules.
// Build it once with NewDedupIndex, then call Check for each candidate.
// This avoids re-tokenizing and re-vectorizing existing rules on every call,
// reducing CheckDedup complexity from O(candidates×rules×text) to O(candidates×rules).
type DedupIndex struct {
	entries []dedupEntry
}

type dedupEntry struct {
	ruleID string
	vec    map[string]float64
}

// NewDedupIndex pre-computes TF vectors for each rule in existing.
func NewDedupIndex(existing []*Rule) *DedupIndex {
	entries := make([]dedupEntry, 0, len(existing))
	for _, r := range existing {
		text := r.Condition + " " + r.Body
		if strings.TrimSpace(text) == "" {
			continue
		}
		entries = append(entries, dedupEntry{ruleID: r.ID, vec: termFreq(tokenize(text))})
	}
	return &DedupIndex{entries: entries}
}

// Add pre-computes a TF vector for r and appends it to the index.
// Call this after writing a new rule so subsequent candidates in the same batch
// are checked against it.
func (idx *DedupIndex) Add(r *Rule) {
	text := r.Condition + " " + r.Body
	if strings.TrimSpace(text) == "" {
		return
	}
	idx.entries = append(idx.entries, dedupEntry{ruleID: r.ID, vec: termFreq(tokenize(text))})
}

// Check returns the dedup action for candidateText using pre-computed vectors.
func (idx *DedupIndex) Check(candidateText string) DedupResult {
	if idx == nil || len(idx.entries) == 0 || strings.TrimSpace(candidateText) == "" {
		return DedupResult{Action: DedupActionNew}
	}

	candVec := termFreq(tokenize(candidateText))
	bestScore := 0.0
	bestID := ""
	for _, e := range idx.entries {
		score := cosineSimilarity(candVec, e.vec)
		if score > bestScore {
			bestScore = score
			bestID = e.ruleID
		}
	}

	action := DedupActionNew
	switch {
	case bestScore >= MergeThreshold:
		action = DedupActionMerge
	case bestScore >= ReviewThreshold:
		action = DedupActionPossibleDuplicate
	}

	return DedupResult{Action: action, Score: bestScore, MatchedRuleID: bestID}
}

// tokenize splits text into lowercase alphanumeric tokens.
func tokenize(text string) []string {
	text = strings.ToLower(text)
	var tokens []string
	var cur strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// termFreq returns a normalized term-frequency map for a token list.
// Each value is count/total, so the vector sums to 1.
func termFreq(tokens []string) map[string]float64 {
	if len(tokens) == 0 {
		return nil
	}
	freq := make(map[string]float64, len(tokens))
	for _, t := range tokens {
		freq[t]++
	}
	total := float64(len(tokens))
	for k := range freq {
		freq[k] /= total
	}
	return freq
}

// cosineSimilarity returns the cosine similarity between two TF vectors.
// Returns 0 for empty vectors.
func cosineSimilarity(a, b map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for term, valA := range a {
		normA += valA * valA
		if valB, ok := b[term]; ok {
			dot += valA * valB
		}
	}
	for _, valB := range b {
		normB += valB * valB
	}

	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
