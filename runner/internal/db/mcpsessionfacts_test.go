package db

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
	"strconv"
	"testing"
	"time"
)

// A retention class may merge facts only when the canonical Go predicate agrees.
// New reasons are automatically included from the actual Go vocabulary. If the
// predicate gains another input, its retention class contract must expand too.
func TestMCPFactRetentionClassesMatchCanonicalTerminality(t *testing.T) {
	pool := testsupport.Open(t, "TEST_DATABASE_URL", "mcp-fact-classes", RunMigrations)
	var sqlReasons []string
	if err := pool.QueryRow(t.Context(), `SELECT mcp_binding_fact_reasons()`).Scan(&sqlReasons); err != nil {
		t.Fatal(err)
	}
	if len(sqlReasons) != len(validEndReasons) {
		t.Fatal("SQL and Go end-reason vocabularies differ")
	}
	seen := map[string]bool{}
	for _, reason := range sqlReasons {
		if !validEndReasons[reason] || seen[reason] {
			t.Fatal("SQL reason is missing from Go or duplicated", reason)
		}
		seen[reason] = true
	}
	reasons := []*string{nil}
	for reason := range validEndReasons {
		r := reason
		reasons = append(reasons, &r)
	}
	for _, reason := range []string{"unknown-a", "unknown-b", ""} {
		r := reason
		reasons = append(reasons, &r)
	}
	statuses := []string{"starting", "running", "idle", "waiting", "stopped", "error", "ended", "completed", "disconnected", "sleeping", "stopping", "unknown", ""}
	now := time.Now()
	classes := map[string]bool{}
	for _, reason := range reasons {
		for _, status := range statuses {
			row := SessionRow{Status: status, EndedAt: &now, EndReason: reason}
			raw, err := json.Marshal(map[string]any{"status": status, "ended_at": now, "end_reason": reason})
			if err != nil {
				t.Fatal(err)
			}
			var class string
			if err = pool.QueryRow(t.Context(), `SELECT mcp_binding_fact_class($1::jsonb)`, raw).Scan(&class); err != nil {
				t.Fatal(err)
			}
			wantReason := "<unknown>"
			if reason != nil && validEndReasons[*reason] {
				wantReason = *reason
			}
			if class != wantReason+":"+strconv.FormatBool(status == "stopped") {
				t.Fatal("SQL retention class does not use the exact Go reason vocabulary", class)
			}
			terminal := TerminallyEnded(&row)
			if previous, exists := classes[class]; exists && previous != terminal {
				t.Fatal("retention class could discard a terminal event", class)
			}
			classes[class] = terminal
			row.EndedAt = nil
			if TerminallyEnded(&row) {
				t.Fatal("null end unexpectedly terminal")
			}
		}
	}
	if len(classes) > 32 {
		t.Fatal("reason vocabulary exceeded bounded retention capacity", len(classes))
	}
}
