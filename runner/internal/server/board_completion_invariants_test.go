package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestBoardCodeDoneReasonAuthor(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		actor, text string
		want        bool
	}{
		{"human", "Done reason: Documentation-only decision.", false},
		{"agent", "Done reason: Worker says complete.", true},
		{"human", "Done reason:", true},
		{"human", "Looks done.", true},
	} {
		c := liveBoardCard{Number: 585, ColumnName: "Done"}
		raw, _ := json.Marshal(map[string]any{"trail": []any{map[string]any{"type": "moved", "to_column": "Done", "created_at": cutoff.Add(time.Hour).Format(time.RFC3339)}, map[string]any{"type": "comment", "actor": tc.actor, "text": tc.text}}})
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		d := boardCodeDecision(c, nil, cutoff.Add(2*time.Hour), false, boardCodeEvidence{Since: cutoff})
		if (d.Drift == "done_without_main_merge") != tc.want {
			t.Fatalf("actor=%s text=%q: %+v", tc.actor, tc.text, d)
		}
	}
}

func TestBoardCodeToTestRequiresDeployedMerge(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		history []protocol.CodeCommit
		want    bool
	}{
		{"no merge", nil, true},
		{"merge not deployed", []protocol.CodeCommit{{SHA: fixtureSHA, Date: at.Format(time.RFC3339), Subject: "Merge BLG-585", Parents: []string{"a", "b"}}}, true},
		{"deployed merge", []protocol.CodeCommit{{SHA: fixtureSHA, Date: at.Format(time.RFC3339), Subject: "Merge BLG-585", Parents: []string{"a", "b"}, Deployed: true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := liveBoardCard{Number: 585, ColumnName: "Done", Tags: []string{"to-test"}}
			d := boardCodeDecision(c, tc.history, at, false)
			if d.Undeployed != tc.want {
				t.Fatalf("decision: %+v", d)
			}
		})
	}
}

func TestBoardCodeToTestCutover(t *testing.T) {
	first := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		at   string
		want bool
	}{{"", false}, {"invalid", false}, {"2026-01-01T00:00:00Z", false}, {"2026-01-02T00:00:00Z", true}, {"2026-01-03T00:00:00Z", true}} {
		c := liveBoardCard{}
		if tc.at != "" {
			c.ToTestAddedAt = &tc.at
		}
		if got := toTestAfterDeploy(c, first); got != tc.want {
			t.Fatalf("at=%q got=%t", tc.at, got)
		}
		if toTestAfterDeploy(c, time.Time{}) {
			t.Fatal("no deployment boundary")
		}
	}
}

func TestBoardCodeUndeployedDriftAggregates(t *testing.T) {
	f := undeployedInvariantFact([]int{9, 8, 7, 6, 5, 4, 3, 2, 1})
	if !strings.Contains(f.text, "9 to-test") || !strings.Contains(f.text, "#1, #2, #3, #4, #5") || strings.Contains(f.text, "#6") || strings.Count(f.text, "blerg-runner") != 1 {
		t.Fatal(f.text)
	}
}

func TestBoardCodeStalledInvariantEvidence(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	c := liveBoardCard{Number: 585, ColumnName: "In progress", UpdatedAt: now.Format(time.RFC3339)}
	for _, tc := range []struct {
		name    string
		running bool
		proof   boardCodeEvidence
		want    bool
	}{
		{"no commit despite comment", false, boardCodeEvidence{}, true},
		{"live worker", true, boardCodeEvidence{}, false},
		{"open Main checklist item", false, boardCodeEvidence{ChecklistActive: true}, false},
		{"recent branch commit", false, boardCodeEvidence{Activity: []protocol.CodeActivity{{Subject: "BLG-585 implementation", Date: now.Add(-time.Hour).Format(time.RFC3339)}}}, false},
		{"old branch commit", false, boardCodeEvidence{Activity: []protocol.CodeActivity{{Subject: "BLG-585 implementation", Date: now.Add(-5 * time.Hour).Format(time.RFC3339)}}}, true},
		{"other card commit", false, boardCodeEvidence{Activity: []protocol.CodeActivity{{Subject: "BLG-5850 implementation", Date: now.Format(time.RFC3339)}}}, true},
		{"activity read unavailable", false, boardCodeEvidence{ActivityUnavailable: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := boardCodeDecision(c, nil, now, tc.running, tc.proof)
			if (d.Drift == "stalled_without_worker") != tc.want {
				t.Fatalf("decision: %+v", d)
			}
		})
	}
}
