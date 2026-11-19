package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDatasetProtocolRoundTrip(t *testing.T) {
	reference := DatasetReference{Kind: "dataset", Name: "blerg.cards.merged", Parameters: map[string]string{"bucket": "day", "timezone": "America/New_York", "release": "0.6.0", "base": "v0.5.4"}}
	if !reference.Valid() {
		t.Fatal("documented reference refused")
	}
	sample := DatasetDelta{Type: "dataset_delta", Epoch: "epoch", BaseSequence: 4, Sequence: 5, Upserts: []DatasetRow{{"key": "bucket", "count": 0}}, Removals: []string{"old"}, ObservedAt: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), Status: "ready"}
	raw, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	var result DatasetDelta
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.BaseSequence != 4 || result.Sequence != 5 || result.Upserts[0]["count"] != float64(0) || len(result.Removals) != 1 {
		t.Fatalf("changeset lost: %+v", result)
	}
}

func TestDatasetCardTimeCatalog(t *testing.T) {
	q := DatasetReference{Kind: "dataset", Name: "blerg.cards.time", Parameters: map[string]string{"board": "board-a", "card": "7"}}
	if !q.Valid() {
		t.Fatal("documented card-time reference refused")
	}
	for _, p := range []map[string]string{{"board": "board-a", "card": "0"}, {"board": "https://private-canary", "card": "7"}, {"board": "board-a", "card": "7", "token": "private-canary"}} {
		q.Parameters = p
		if q.Valid() {
			t.Fatal("unsafe card parameters accepted")
		}
	}
}

func TestDatasetReviewPairsCatalog(t *testing.T) {
	if !(DatasetReference{Kind: "dataset", Name: "blerg.reviews.pairs", Parameters: map[string]string{"board": "board-a", "card": "7"}}).Valid() {
		t.Fatal("review engine pair reference refused")
	}
}

func TestDatasetAggregateCatalog(t *testing.T) {
	for _, d := range []DatasetReference{
		{Kind: "dataset", Name: "blerg.gates.bins", Parameters: map[string]string{"width": "60"}},
		{Kind: "dataset", Name: "blerg.cards.merged.heatmap", Parameters: map[string]string{"bucket": "hour", "timezone": "America/New_York"}},
		{Kind: "dataset", Name: "blerg.workers.timeline", Parameters: map[string]string{}},
	} {
		if !d.Valid() {
			t.Fatalf("aggregate reference refused: %s", d.Name)
		}
	}
}

func TestDatasetMCPReference(t *testing.T) {
	source := DatasetReference{Kind: "dataset", Name: "blerg.mcp", Parameters: map[string]string{"binding": "00000000-0000-4000-8000-000000000514"}}
	if !source.Valid() {
		t.Fatal("opaque binding refused")
	}
	for _, parameters := range []map[string]string{{}, {"binding": "unknown"}, {"binding": source.Parameters["binding"], "token": "canary"}, {"binding": source.Parameters["binding"], "tool": "quotes"}} {
		source.Parameters = parameters
		if source.Valid() {
			t.Fatal("unbound MCP parameters accepted")
		}
	}
}
