package releaseperf

import (
	"context"
	"testing"
)

func TestBootSeedMatchesSessionEventScaleWithoutNewCardSchema(t *testing.T) {
	pool := migratedRunnerPool(t)
	s, err := SeedBootDataset(context.Background(), pool, "boot-test-account")
	if err != nil {
		t.Fatal(err)
	}
	if s.Workspaces != 25 || s.LongTranscriptEvents != 8000 || s.InsightsConversations != 420 || s.InsightsEvents != 168000 || s.InsightsCardRefs != 0 {
		t.Fatalf("boot seed shape = %+v", s)
	}
	var sessions, events int
	if err = pool.QueryRow(context.Background(), "SELECT count(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(context.Background(), "SELECT count(*) FROM agent_events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if sessions != 446 || events != 176000 {
		t.Fatalf("sessions=%d events=%d", sessions, events)
	}
}
