package activity

import (
	"strings"
	"testing"
	"time"
)

func TestWorkspaceSnapshotHardCap(t *testing.T) {
	w := Workspace{SessionID: "00000000-0000-4000-8000-000000000001", Workers: []Worker{}}
	for range 1000 {
		w.Workers = append(w.Workers, Worker{SessionID: strings.Repeat("x", 4096), Status: "running", AgeSeconds: 123456789})
	}
	got := w.Snapshot()
	if len(got) >= 600 || strings.Contains(got, "\n") {
		t.Fatalf("snapshot %d bytes", len(got))
	}
	if !strings.Contains(got, "running=1000") || !strings.Contains(got, w.SessionID) {
		t.Fatalf("snapshot: %s", got)
	}
}
func TestWorkspaceThresholdActions(t *testing.T) {
	w := Workspace{SessionID: "main", Workers: []Worker{{SessionID: "worker", Status: "running", AgeSeconds: 1801, GateFraction: .7}, {SessionID: "idle", Status: "idle", AgeSeconds: 901}}}
	facts := w.Nudges(time.Unix(0, 0))
	if len(facts) != 3 {
		t.Fatalf("facts: %+v", facts)
	}
	for _, f := range facts {
		if !strings.Contains(f.Text, "blerg-runner") {
			t.Fatalf("no action: %+v", f)
		}
	}
}

func TestWorkspaceOperationalNudges(t *testing.T) {
	now := time.Unix(10000, 0)
	w := Workspace{SessionID: "main", GeneratedAt: now, Machine: &Machine{SessionID: "main", At: now, Load1: 4, CPUs: 2, DiskFreeBytes: 1 << 20}, Undelivered: []Notice{{SessionID: "worker", TurnSeq: 7, AgeSeconds: 301}}}
	for _, id := range []string{"a", "b", "c"} {
		w.Runs = append(w.Runs, Run{ID: id, SessionID: "worker", Suite: "runner-unit", Commit: "abcdef1", State: "finished", FullSuite: true})
	}
	w.Runs = append(w.Runs, Run{ID: "stuck", SessionID: "worker", Suite: "runner-unit", Commit: "abcdef1", State: "running", ProgressAt: now.Add(-16 * time.Minute)})
	w.Workers = []Worker{{SessionID: "worker", Status: "idle", Turns: []Turn{{Seq: 7, TurnEndSeconds: map[string]float64{"sync": 31}}}}}
	facts := w.Nudges(now)
	if len(facts) != 6 {
		t.Fatalf("facts: %+v", facts)
	}
	for _, f := range facts {
		if (!strings.Contains(f.Text, "Session ") && !strings.Contains(f.Text, "Workspace ")) || !strings.Contains(f.Text, "blerg-runner") {
			t.Fatalf("unscoped/actionless fact: %+v", f)
		}
	}
	got := w.Snapshot()
	if len(got) >= 600 {
		t.Fatalf("snapshot %d bytes", len(got))
	}
	t.Logf("populated snapshot: %d bytes", len(got))
}

func TestTelemetryDoesNotNudgeMainOrStoppedWorkersOrBucketFacts(t *testing.T) {
	now := time.Unix(1801, 0)
	w := Workspace{SessionID: "main", Workers: []Worker{{SessionID: "main", Status: "running", AgeSeconds: 4000, GateFraction: .8}, {SessionID: "stopped", Status: "stopped", AgeSeconds: 4000, GateFraction: .8}, {SessionID: "failed", Status: "failed", AgeSeconds: 4000, GateFraction: .8}, {SessionID: "worker", Status: "idle", AgeSeconds: 1000}}}
	a, b := w.Nudges(now), w.Nudges(now.Add(30*time.Minute))
	if len(a) != 1 || len(b) != 1 || a[0].Key != b[0].Key {
		t.Fatalf("unstable/spurious facts: %+v %+v", a, b)
	}
	if got := (Workspace{SessionID: "main"}).Snapshot(); got != "" {
		t.Fatalf("empty workspace snapshot: %q", got)
	}
}
