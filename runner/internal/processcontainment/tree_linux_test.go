//go:build linux

package processcontainment

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestFreezeDiagnostics(t *testing.T) {
	tasks := map[int]int{os.Getpid(): -1, 2147483647: -1, 2147483646: 0}
	var out bytes.Buffer
	logUnheldTasks(&out, "pending over 2s", tasks, map[int]int{os.Getpid(): 42}, func(tid int) bool { return tasks[tid] >= 0 })
	for _, want := range []string{"pending over 2s: unheld=2", fmt.Sprintf("tid=%d tasks=-1 vforkWait=42 present=true", os.Getpid()), "State=", "Tgid=", "TracerPid=", "PPid=", "tid=2147483647 tasks=-1 vforkWait=0 present=false gone"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, &out)
		}
	}
	if strings.Contains(out.String(), "tid=2147483646") {
		t.Fatal("reported a held task")
	}
}

func TestFreezeDiagnosticsBounded(t *testing.T) {
	tasks := map[int]int{}
	for tid := 2147483547; tid < 2147483647; tid++ {
		tasks[tid] = -1
	}
	var out bytes.Buffer
	logUnheldTasks(&out, "failed", tasks, nil, func(int) bool { return false })
	if got := strings.Count(out.String(), "tid="); got != 32 {
		t.Fatalf("reported %d tasks, want 32", got)
	}
	if !strings.Contains(out.String(), "unheld=100") {
		t.Fatal("missing total")
	}
}

// wait4 may return a child's first stop and even its exit before its creator's event. Force each
// order: an event that arrives after the child was reaped must not track it again.
func TestBirthsReconcileEitherReportOrder(t *testing.T) {
	b := newBirths()
	// child stop, child exit, then the creator's event
	if !b.reported(100, false) {
		t.Fatal("an untracked TID's first report is a birth")
	}
	if track, _ := b.created(100, false); track {
		t.Fatal("a reaped child would be tracked again by its creator's late event")
	}
	// the creator's event first: the child is pending until it reports itself
	if track, reborn := b.created(101, false); !track || reborn {
		t.Fatal("a child announced first must be tracked as pending")
	}
	if b.reported(101, true) {
		t.Fatal("a pending child's first stop is not another birth")
	}
	if len(b.early) != 0 || len(b.pending) != 0 {
		t.Fatalf("leftover state: %+v", b)
	}
}

// One TID can be reported, reaped and reused before either creator event arrives.
func TestBirthsCountOverlappingGenerations(t *testing.T) {
	b := newBirths()
	b.reported(100, false) // first task, reaped
	b.reported(100, false) // reused TID, reaped too
	for i := range 2 {
		if track, _ := b.created(100, false); track {
			t.Fatalf("creator event %d would track a dead task", i+1)
		}
	}
	if track, _ := b.created(100, false); !track {
		t.Fatal("a third, unreported child must still be tracked as pending")
	}
}

// A creator event naming a task that is tracked and settled is the one thing the exec barrier rules
// out. It is noticed, and the new task is still tracked.
// A task that came past the barrier can report, exit and be reaped with its creator's event still to
// come. That event must not track it. If the event already came, its own report is still due.
func TestBirthsKeepATaskPastTheBarrier(t *testing.T) {
	b := newBirths()
	b.kept(7)
	if track, reborn := b.created(7, false); track || reborn {
		t.Fatalf("the creator's event tracked a gone task: track=%t reborn=%t", track, reborn)
	}
	if b.created(8, true); !b.pending[8] {
		t.Fatal("not pending")
	}
	b.kept(8)
	if b.early[8] != 0 {
		t.Fatal("a creator's event already seen was counted as still due")
	}
}

func TestBirthsNoticeATrackedTIDReborn(t *testing.T) {
	b := newBirths()
	b.reported(200, false)
	b.created(200, true)
	if track, reborn := b.created(200, true); !track || !reborn {
		t.Fatal("a new task under a tracked TID went unnoticed")
	}
	if b.reported(200, true) {
		t.Fatal("its own first stop pairs with its creator event")
	}
}
