package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestInsightsRootAndChildUsagePartitionsReplaceAggregateAndCountTurnOnce(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-000000000701"
	endTestSession(t, p, sid, "idle")
	for i, event := range []struct{ kind, payload string }{
		{"turn_done", `{"model":"model-b","usage":{"input_tokens":10,"output_tokens":2,"cache_read_tokens":0,"cache_write_tokens":0},"usage_by_model":{"model-a":{"input_tokens":6,"output_tokens":1,"cache_read_tokens":0,"cache_write_tokens":0},"model-b":{"input_tokens":4,"output_tokens":1,"cache_read_tokens":0,"cache_write_tokens":0}}}`},
		{"subagent_done", `{"model":"model-b","usage":{"input_tokens":5,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0},"usage_by_model":{"model-a":{"input_tokens":3,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0},"model-b":{"input_tokens":2,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0}}}`},
	} {
		if _, _, err := db.AppendAgentEvent(ctx, p, sid, fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), event.kind, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.ListSessionTokenUsage(ctx, p, sid)
	if err != nil {
		t.Fatal(err)
	}
	var input, turns int64
	seen := map[string]int64{}
	for _, row := range rows {
		input += row.Input
		turns += row.Turns
		seen[fmt.Sprintf("%t/%s", row.Subagent, row.Model)] = row.Input
	}
	if input != 15 || turns != 1 || seen["false/model-a"] != 6 || seen["false/model-b"] != 4 || seen["true/model-a"] != 3 || seen["true/model-b"] != 2 {
		t.Fatalf("partition aggregate doubled/lost or turn multiplied: input=%d turns=%d rows=%+v", input, turns, rows)
	}
	global, err := db.ListTokenUsage(ctx, p, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	input, turns = 0, 0
	for _, row := range global {
		input += row.Input
		turns += row.Turns
	}
	if input != 15 || turns != 1 {
		t.Fatalf("global partition accounting: %+v", global)
	}
}
func TestInsightsMalformedUsagePartitionsFallBackToUnknownAggregate(t *testing.T) {
	cases := []struct {
		name  string
		parts any
	}{
		{"mismatch", map[string]any{"model-a": map[string]any{"input_tokens": 9, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}}},
		{"negative", map[string]any{"model-a": map[string]any{"input_tokens": -1, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}}},
		{"numeric-string", map[string]any{"model-a": map[string]any{"input_tokens": "10", "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}}},
		{"unsafe-model", map[string]any{"unsafe model": map[string]any{"input_tokens": 10, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}}},
		{"present-null-bucket", map[string]any{"model-a": map[string]any{"input_tokens": 10, "output_tokens": nil}}},
		{"oversized-count", map[string]any{"model-a": map[string]any{"input_tokens": 1e20, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}}},
		{"oversized-bytes", map[string]any{"model-a": map[string]any{"input_tokens": 10, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0, "extra": strings.Repeat("x", 20000)}}},
		{"wrong-shape", []string{"model-a"}}, {"empty", map[string]any{}}, {"null", nil}, {"number", 10},
	}
	tooMany := map[string]any{}
	for i := 0; i < 33; i++ {
		tooMany[fmt.Sprintf("model-%d", i)] = map[string]any{"input_tokens": 0, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}
	}
	tooMany["model-0"] = map[string]any{"input_tokens": 10, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}
	cases = append(cases, struct {
		name  string
		parts any
	}{"too-many", tooMany})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := endTestPool(t)
			ctx := context.Background()
			sid := "00000000-0000-4000-8000-000000000702"
			endTestSession(t, p, sid, "idle")
			body, _ := json.Marshal(map[string]any{"model": "model-b", "usage": map[string]int{"input_tokens": 10, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}, "usage_by_model": tc.parts})
			if _, _, err := db.AppendAgentEvent(ctx, p, sid, "00000000-0000-4000-8000-000000000711", "turn_done", string(body)); err != nil {
				t.Fatal(err)
			}
			rows, err := db.ListSessionTokenUsage(ctx, p, sid)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Model != "" || rows[0].Input != 10 || rows[0].Turns != 1 {
				t.Fatalf("malformed partition priced as model/lost count: %+v", rows)
			}
		})
	}
}

func TestInsightsLegacyUsageWithoutPartitionRetainsModel(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-000000000703"
	endTestSession(t, p, sid, "idle")
	if _, _, err := db.AppendAgentEvent(ctx, p, sid, "00000000-0000-4000-8000-000000000712", "turn_done", `{"model":"model-legacy","usage":{"input_tokens":10}}`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListSessionTokenUsage(ctx, p, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Model != "model-legacy" || rows[0].Input != 10 || rows[0].Turns != 1 {
		t.Fatalf("legacy accounting changed: %+v", rows)
	}
}

func TestInsightsPartitionsAcceptOmittedZeroBuckets(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-000000000704"
	endTestSession(t, p, sid, "idle")
	if _, _, err := db.AppendAgentEvent(ctx, p, sid, "00000000-0000-4000-8000-000000000713", "turn_done", `{"model":"model-final","usage":{"input_tokens":10},"usage_by_model":{"model-a":{"input_tokens":6},"model-b":{"input_tokens":4}}}`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListSessionTokenUsage(ctx, p, sid)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	var turns int64
	for _, row := range rows {
		seen[row.Model] = row.Input
		turns += row.Turns
	}
	if len(rows) != 2 || seen["model-a"] != 6 || seen["model-b"] != 4 || turns != 1 {
		t.Fatalf("omitted zeros invalidated compatible partition: %+v", rows)
	}
}
