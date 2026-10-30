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

// wait4 may return a child's initial stop and even its exit before its creator's event. Force each
// order: an event that arrives after the child was reaped must not track it again.
func TestBirthsReconcileEitherReportOrder(t *testing.T) {
	b := newBirths()
	// child stop, child exit, then the creator's event
	if !b.reported(100, false, false) {
		t.Fatal("an untracked TID's first report is a birth")
	}
	if track, _ := b.created(100, false); track {
		t.Fatal("a reaped child would be tracked again by its creator's late event")
	}
	// the creator's event first: the child is pending until it reports itself
	if track, superseded := b.created(101, false); !track || superseded {
		t.Fatal("a child announced first must be tracked as pending")
	}
	if b.reported(101, true, true) {
		t.Fatal("a pending child's first stop is not another birth")
	}
	if len(b.early) != 0 || len(b.pending) != 0 || len(b.stale) != 0 {
		t.Fatalf("leftover state: %+v", b)
	}
}

// One TID can be reported, reaped and reused before either creator event arrives.
func TestBirthsCountOverlappingGenerations(t *testing.T) {
	b := newBirths()
	b.reported(100, false, false) // first task, reaped
	b.reported(100, false, false) // reused TID, reaped too
	for i := range 2 {
		if track, _ := b.created(100, false); track {
			t.Fatalf("creator event %d would track a dead task", i+1)
		}
	}
	if track, _ := b.created(100, false); !track {
		t.Fatal("a third, unreported child must still be tracked as pending")
	}
}

// A non-leader thread T execs: Linux frees T's TID before the tracer collects the EXEC event that
// names it. A new task C reusing that TID is a birth in either report order, and the EXEC event
// removes only the stale entry, never C's.
func TestBirthsTIDReusedDuringPendingExec(t *testing.T) {
	const T = 200
	t.Run("child stop first", func(t *testing.T) {
		b := newBirths()
		b.reported(T, false, false) // T itself, long ago
		b.created(T, true)
		if !b.ambiguous(T) || !b.reported(T, true, true) {
			t.Fatal("C's auto-attach stop under T's stale TID must be a birth (ledger record, no continue before it)")
		}
		if track, _ := b.created(T, true); track {
			t.Fatal("C's creator event must pair with C's early report")
		}
		if b.execed(T) {
			t.Fatal("T's EXEC event would delete C's live entry")
		}
		if !b.execed(T) {
			t.Fatal("a later exec of the same TID removes its entry again")
		}
	})
	t.Run("creator event first", func(t *testing.T) {
		b := newBirths()
		b.reported(T, false, false)
		b.created(T, true)
		track, superseded := b.created(T, true)
		if !track || !superseded {
			t.Fatal("C's creator event under T's stale TID must track C as a new, pending task")
		}
		if b.ambiguous(T) || b.reported(T, true, true) {
			t.Fatal("C's own first stop pairs with its creator event")
		}
		if b.execed(T) {
			t.Fatal("T's EXEC event would delete C's live entry")
		}
	})
	t.Run("no reuse", func(t *testing.T) {
		b := newBirths()
		b.reported(T, false, false)
		b.created(T, true)
		if b.reported(T, true, false) {
			t.Fatal("a freeze stop of a tracked task is not a birth")
		}
		if !b.execed(T) {
			t.Fatal("T's EXEC event removes T's former TID")
		}
	})
}
