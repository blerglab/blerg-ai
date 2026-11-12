package cardtime

import "testing"

func TestCardTotalEmpty(t *testing.T) {
	seconds, ok := CardTotal(nil)
	if ok || seconds != 0 {
		t.Fatalf("empty: got (%v,%v), want (0,false)", seconds, ok)
	}
}

func TestCardTotalSingleWorker(t *testing.T) {
	seconds, ok := CardTotal([]SessionAttribution{{SessionID: "s1", ActiveSeconds: 120}})
	if !ok || seconds != 120 {
		t.Fatalf("single worker: got (%v,%v), want (120,true)", seconds, ok)
	}
}

func TestCardTotalSumsDistinctWorkers(t *testing.T) {
	seconds, ok := CardTotal([]SessionAttribution{
		{SessionID: "s1", ActiveSeconds: 100},
		{SessionID: "s2", ActiveSeconds: 50},
	})
	if !ok || seconds != 150 {
		t.Fatalf("two workers: got (%v,%v), want (150,true)", seconds, ok)
	}
}

// H1: a session linked to more than one card is excluded from every card's total — matching
// Insights' own cardActiveTime (frontend/src/lib/insightsActivity.ts), which returns null for the
// whole card when any linked session is shared. A card total we're not sure of shows nothing.
func TestCardTotalSharedSessionMakesTotalUnknown(t *testing.T) {
	seconds, ok := CardTotal([]SessionAttribution{
		{SessionID: "s1", ActiveSeconds: 100},
		{SessionID: "s2", ActiveSeconds: 50, Shared: true},
	})
	if ok || seconds != 0 {
		t.Fatalf("shared session present: got (%v,%v), want (0,false)", seconds, ok)
	}
}

func TestCardTotalNativeActiveTimeUnknownMakesTotalUnknown(t *testing.T) {
	seconds, ok := CardTotal([]SessionAttribution{
		{SessionID: "s1", ActiveSeconds: 100},
		{SessionID: "s2", NativeActiveTimeUnknown: true},
	})
	if ok || seconds != 0 {
		t.Fatalf("native unknown present: got (%v,%v), want (0,false)", seconds, ok)
	}
}

func TestCardTotalUnknownBucketMakesTotalUnknown(t *testing.T) {
	seconds, ok := CardTotal([]SessionAttribution{
		{SessionID: "s1", ActiveSeconds: 100},
		{SessionID: "s2", ActiveSeconds: 10, UnknownSeconds: 5},
	})
	if ok || seconds != 0 {
		t.Fatalf("unknown bucket present: got (%v,%v), want (0,false)", seconds, ok)
	}
}

func TestCardTotalCardAttributionUnknownMakesTotalUnknown(t *testing.T) {
	seconds, ok := CardTotal([]SessionAttribution{
		{SessionID: "s1", ActiveSeconds: 100, CardAttributionUnknown: true},
	})
	if ok || seconds != 0 {
		t.Fatalf("attribution unknown present: got (%v,%v), want (0,false)", seconds, ok)
	}
}

// Overlapping distinct workers both count, additively — real parallel work, not double-counting
// one worker's own overlapping tool spans (that clamp lives in activity.SplitOrdered already).
func TestCardTotalOverlappingWorkersAreAdditive(t *testing.T) {
	seconds, ok := CardTotal([]SessionAttribution{
		{SessionID: "s1", ActiveSeconds: 3600},
		{SessionID: "s2", ActiveSeconds: 3600},
	})
	if !ok || seconds != 7200 {
		t.Fatalf("overlapping workers: got (%v,%v), want (7200,true)", seconds, ok)
	}
}

// Re-pinning (a second link row for the same session/card/source or a different source) must not
// duplicate a session's own contribution: the caller is responsible for passing one
// SessionAttribution per distinct session, never per link row. This test documents and locks that
// contract: a duplicate SessionID in the input is deduplicated by the last-wins value, not summed.
func TestCardTotalDuplicateSessionIDIsNotDoubleCounted(t *testing.T) {
	seconds, ok := CardTotal([]SessionAttribution{
		{SessionID: "s1", ActiveSeconds: 100},
		{SessionID: "s1", ActiveSeconds: 100},
	})
	if !ok || seconds != 100 {
		t.Fatalf("duplicate session id: got (%v,%v), want (100,true) (deduplicated, not summed)", seconds, ok)
	}
}
