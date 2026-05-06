package github

import "testing"

func TestParsePRInfoOutput_PopulatesLockReason(t *testing.T) {
	data := []byte(`{
		"state":"CLOSED",
		"isDraft":false,
		"lockReason":"SPAM",
		"createdAt":"2026-04-01T00:00:00Z",
		"mergedAt":"",
		"closedAt":"2026-04-02T00:00:00Z",
		"reviews":[
			{
				"author":{"login":"reviewer1"},
				"state":"COMMENTED",
				"body":"needs work",
				"submittedAt":"2026-04-01T01:00:00Z"
			}
		]
	}`)

	info, err := parsePRInfoOutput(data)
	if err != nil {
		t.Fatalf("parsePRInfoOutput() error = %v", err)
	}
	if info.LockReason != "SPAM" {
		t.Fatalf("LockReason = %q, want %q", info.LockReason, "SPAM")
	}
	if len(info.Reviews) != 1 {
		t.Fatalf("len(Reviews) = %d, want 1", len(info.Reviews))
	}
	if info.Reviews[0].Author != "reviewer1" {
		t.Fatalf("Reviews[0].Author = %q, want %q", info.Reviews[0].Author, "reviewer1")
	}
}
