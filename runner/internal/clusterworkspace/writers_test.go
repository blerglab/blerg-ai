package clusterworkspace

import (
	"context"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"reflect"
	"testing"
	"time"
)

type fakeWriters struct {
	events *[]string
	fail   string
}

func (w *fakeWriters) call(event string) error {
	*w.events = append(*w.events, event)
	if w.fail == event {
		return errors.New("uncertain containment")
	}
	return nil
}
func (w *fakeWriters) Freeze(context.Context) error { return w.call("freeze") }
func (w *fakeWriters) Thaw(context.Context) error   { return w.call("thaw") }
func (w *fakeWriters) Stop(context.Context) error   { return w.call("stop") }

type frozenGit struct {
	*fakeGit
	events *[]string
}

func (g frozenGit) Push(ctx context.Context, m Member, branch, head string) error {
	*g.events = append(*g.events, "push:"+m.ID)
	return g.fakeGit.Push(ctx, m, branch, head)
}
func TestClusterWritersFreezeSyncThawAndStop(t *testing.T) {
	events := []string{}
	g := frozenGit{newFake(), &events}
	r, _ := New(testPlan(), g)
	if r.ArmWriters() == nil {
		t.Fatal("unprepared/uncontained agent admitted")
	}
	if err := r.SetWriters(&fakeWriters{events: &events}); err != nil {
		t.Fatal(err)
	}
	if err := r.Prepare(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := r.ArmWriters(); err != nil {
		t.Fatal(err)
	}
	r.PushRound(context.Background())
	if !reflect.DeepEqual(events, []string{"freeze", "push:one", "push:two", "thaw"}) {
		t.Fatal(events)
	}
	events = nil
	r.Stop(context.Background())
	if !reflect.DeepEqual(events, []string{"stop"}) {
		t.Fatal(events)
	}
	if r.ArmWriters() == nil {
		t.Fatal("ended agent rearmed")
	}
}
func TestClusterWritersFailuresRetainAndFence(t *testing.T) {
	for _, failure := range []string{"freeze", "thaw", "stop"} {
		t.Run(failure, func(t *testing.T) {
			events := []string{}
			w := &fakeWriters{events: &events, fail: failure}
			r, _ := New(testPlan(), frozenGit{newFake(), &events})
			_ = r.SetWriters(w)
			_ = r.Prepare(context.Background(), false)
			_ = r.ArmWriters()
			if failure == "stop" {
				r.Stop(context.Background())
			} else {
				r.PushRound(context.Background())
			}
			for _, result := range r.Results() {
				if !result.Pending || result.SyncError == "" || result.RecoveryLocation == "" {
					t.Fatal(result)
				}
			}
			if r.Prepared() {
				t.Fatal("uncertain runtime still admitted requests")
			}
			if failure != "thaw" {
				for _, e := range events {
					if e == "push:one" || e == "push:two" {
						t.Fatal("push before containment proof", events)
					}
				}
			}
			// A failed freeze still requires cleanup; stopped is not cleanup proof.
			w.fail = ""
			events = nil
			r.Stop(context.Background())
			if len(events) != 1 || events[0] != "stop" {
				t.Fatal("uncertain writers were not cleaned", events)
			}
		})
	}
}

func TestClusterWritersAtomicShellStartAndStop(t *testing.T) {
	events := []string{}
	p := testPlan()
	r, _ := New(p, newFake())
	launch := protocol.SupervisorLaunch{SessionID: p.SessionID, LaunchGeneration: p.Runtime.LaunchGeneration, RuntimeGeneration: p.Runtime.Generation, Runtime: "cluster", ConnectionID: "connection"}
	supervisor := &fakeSupervisor{launch: launch}
	_ = r.SetSupervisor(supervisor, launch)
	_ = r.SetWriters(&fakeWriters{events: &events})
	_ = r.Prepare(context.Background(), false)
	_ = r.ArmWriters()
	shell := protocol.SupervisorShell{Launch: launch, ShellGeneration: "shell"}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.StartShell(context.Background(), shell, func() error { close(started); <-release; return nil })
	}()
	<-started
	stopDone := make(chan struct{})
	go func() { r.Stop(context.Background()); close(stopDone) }()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-stopDone
	called := false
	if r.StartShell(context.Background(), shell, func() error { called = true; return nil }) == nil || called {
		t.Fatal("late shell exec crossed stop fence")
	}
}

func TestClusterWritersCleanupContinuesAfterPeerFailure(t *testing.T) {
	events := []string{}
	p := testPlan()
	r, _ := New(p, newFake())
	launch := protocol.SupervisorLaunch{SessionID: p.SessionID, LaunchGeneration: p.Runtime.LaunchGeneration, RuntimeGeneration: p.Runtime.Generation, Runtime: "cluster", ConnectionID: "connection"}
	peer := &fakeSupervisor{launch: launch}
	_ = r.SetSupervisor(peer, launch)
	_ = r.SetWriters(&fakeWriters{events: &events})
	_ = r.Prepare(context.Background(), false)
	_ = r.ArmWriters()
	peer.badProof = true
	r.Stop(context.Background())
	if !reflect.DeepEqual(events, []string{"stop"}) {
		t.Fatal("agent cleanup skipped after shell failure", events)
	}
	for _, result := range r.Results() {
		if !result.Pending || result.SyncError == "" {
			t.Fatal(result)
		}
	}
}

type failedPhaseSupervisor struct{ *fakeSupervisor }

func (f failedPhaseSupervisor) Notify(_ context.Context, _ protocol.SupervisorLaunch, phase protocol.SupervisorPhase) error {
	if phase == "syncing" {
		return errors.New("uncertain admission fence")
	}
	return nil
}
func TestClusterWritersFailedAdmissionFenceStopsSettlement(t *testing.T) {
	events := []string{}
	p := testPlan()
	r, _ := New(p, newFake())
	launch := protocol.SupervisorLaunch{SessionID: p.SessionID, LaunchGeneration: p.Runtime.LaunchGeneration, RuntimeGeneration: p.Runtime.Generation, Runtime: "cluster", ConnectionID: "connection"}
	_ = r.SetSupervisor(failedPhaseSupervisor{&fakeSupervisor{launch: launch}}, launch)
	_ = r.SetWriters(&fakeWriters{events: &events})
	_ = r.Prepare(context.Background(), false)
	_ = r.ArmWriters()
	r.PushRound(context.Background())
	if r.Prepared() || len(events) != 0 {
		t.Fatal("failed admission fence allowed settlement", events)
	}
	r.Stop(context.Background())
	if !reflect.DeepEqual(events, []string{"stop"}) {
		t.Fatal("failed admission fence skipped cleanup", events)
	}
}

type phaseRecorder struct {
	*fakeSupervisor
	phases []protocol.SupervisorPhase
}

func (p *phaseRecorder) Notify(_ context.Context, _ protocol.SupervisorLaunch, phase protocol.SupervisorPhase) error {
	p.phases = append(p.phases, phase)
	return nil
}
func TestClusterUnchangedTurnSkipsContainment(t *testing.T) {
	p := testPlan()
	g := newFake()
	events := []string{}
	r, _ := New(p, g)
	launch := protocol.SupervisorLaunch{SessionID: p.SessionID, LaunchGeneration: p.Runtime.LaunchGeneration, RuntimeGeneration: p.Runtime.Generation, Runtime: "cluster", ConnectionID: "connection"}
	peer := &phaseRecorder{fakeSupervisor: &fakeSupervisor{launch: launch}}
	_ = r.SetSupervisor(peer, launch)
	_ = r.SetWriters(&fakeWriters{events: &events})
	_ = r.Prepare(context.Background(), false)
	_ = r.ArmWriters()
	r.PushRound(context.Background())
	events = nil
	peer.phases = nil
	r.PushRound(context.Background())
	if len(events) != 0 || len(peer.phases) != 0 {
		t.Fatal("unchanged turn froze writers", events, peer.phases)
	}
	// A failed member still needs a bounded retry, even at the same head.
	r.results["two"] = protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "two", LaunchGeneration: p.Runtime.LaunchGeneration}, Head: g.heads["two"], Pending: true, Branch: "wip/" + p.SessionID}
	before := len(g.calls)
	r.PushRound(context.Background())
	if len(events) != 0 || len(peer.phases) != 0 || r.Results()[1].Pending || len(g.calls) != before+1 {
		t.Fatal("unchanged pending retry froze or was lost", events, peer.phases, r.Results())
	}
}

type cancelPushGit struct {
	*fakeGit
	cancel context.CancelFunc
}

func (g cancelPushGit) Push(ctx context.Context, _ Member, _, _ string) error {
	g.cancel()
	return ctx.Err()
}

type contextWriters struct {
	thawed bool
	expire bool
}

func (w *contextWriters) Freeze(ctx context.Context) error { return ctx.Err() }
func (w *contextWriters) Thaw(ctx context.Context) error {
	w.thawed = ctx.Err() == nil
	if w.expire {
		<-ctx.Done()
	}
	return ctx.Err()
}
func (w *contextWriters) Stop(ctx context.Context) error { return ctx.Err() }
func TestClusterCanceledPushStillThawsWithReservedCleanup(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint(expire), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r, _ := New(testPlan(), cancelPushGit{newFake(), cancel})
			w := &contextWriters{expire: expire}
			_ = r.SetWriters(w)
			_ = r.Prepare(ctx, false)
			_ = r.ArmWriters()
			r.PushRound(ctx)
			if !w.thawed {
				t.Fatal("thaw inherited canceled push context")
			}
			if r.Prepared() == expire {
				t.Fatal("runtime admission disagrees with thaw proof")
			}
			if expire {
				for _, v := range r.Results() {
					if !v.Pending || v.SyncError == "" {
						t.Fatal(v)
					}
				}
			}
		})
	}
}

type snapshotWriters struct {
	frozen, live bool
	events       *[]string
}

func (w *snapshotWriters) Freeze(ctx context.Context) error {
	w.frozen = true
	*w.events = append(*w.events, "freeze")
	return ctx.Err()
}
func (w *snapshotWriters) Thaw(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	w.frozen = false
	*w.events = append(*w.events, "thaw")
	return nil
}
func (w *snapshotWriters) Stop(ctx context.Context) error { w.frozen = false; return ctx.Err() }

type snapshotGit struct {
	*fakeGit
	writers      *snapshotWriters
	denyTwo      bool
	cancelCopy   context.CancelFunc
	cancelMember string
}

func (g *snapshotGit) Head(ctx context.Context, m Member) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return g.fakeGit.Head(ctx, m)
}
func (g *snapshotGit) Snapshot(ctx context.Context, m Member, _ string) error {
	if g.writers.live && !g.writers.frozen {
		return errors.New("copy outside containment freeze")
	}
	*g.writers.events = append(*g.writers.events, "copy:"+m.ID)
	if g.writers.live && g.cancelCopy != nil && (g.cancelMember == "" || g.cancelMember == m.ID) {
		g.cancelCopy()
	}
	return ctx.Err()
}
func (g *snapshotGit) PushSnapshot(ctx context.Context, m Member, _, _ string) error {
	if g.writers.frozen {
		return errors.New("network ran before thaw")
	}
	*g.writers.events = append(*g.writers.events, "network:"+m.ID)
	if m.ID == "two" && g.denyTwo {
		return errors.New("remote rejected")
	}
	return ctx.Err()
}
func TestClusterSnapshotThawsBeforeNetworkAndRetriesWithoutFreeze(t *testing.T) {
	events := []string{}
	w := &snapshotWriters{events: &events}
	g := &snapshotGit{fakeGit: newFake(), writers: w, denyTwo: true}
	r, _ := New(testPlan(), g)
	_ = r.SetWriters(w)
	if err := r.Prepare(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := r.ArmWriters(); err != nil {
		t.Fatal(err)
	}
	w.live = true
	events = nil
	g.heads["one"] = "new-one"
	r.PushRound(context.Background())
	want := []string{"freeze", "copy:one", "thaw", "network:one", "network:two"}
	if !reflect.DeepEqual(events, want) || !r.Results()[1].Pending {
		t.Fatal(events, r.Results())
	}
	g.denyTwo = false
	events = nil
	r.PushRound(context.Background())
	if !reflect.DeepEqual(events, []string{"network:two"}) || r.Results()[1].Pending {
		t.Fatal("pending same-head retry copied/froze again", events, r.Results())
	}
	events = nil
	r.PushRound(context.Background())
	if len(events) != 0 {
		t.Fatal("unchanged success froze", events)
	}
}
func TestClusterCanceledSnapshotThawsAndPreservesOtherSuccess(t *testing.T) {
	events := []string{}
	w := &snapshotWriters{events: &events}
	g := &snapshotGit{fakeGit: newFake(), writers: w}
	r, _ := New(testPlan(), g)
	_ = r.SetWriters(w)
	_ = r.Prepare(context.Background(), false)
	_ = r.ArmWriters()
	w.live = true
	r.PushRound(context.Background()) // publish initial prepared snapshots without freezing
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.cancelCopy = cancel
	g.heads["one"] = "new-one"
	events = nil
	out := r.PushRound(ctx)
	if w.frozen || !r.Prepared() || !out[0].Pending || out[1].Pending {
		t.Fatal("copy cancellation stranded writers or lost unrelated success", events, out)
	}
	if !reflect.DeepEqual(events, []string{"freeze", "copy:one", "thaw"}) {
		t.Fatal(events)
	}
}

// Cancellation models a member consuming the shared copy deadline, without an
// 80-second sleep. Each new round has fresh heads and a fresh operation context.
func TestClusterCopyStageRotatesBeforeBudgetConsumer(t *testing.T) {
	events := []string{}
	w := &snapshotWriters{events: &events}
	g := &snapshotGit{fakeGit: newFake(), writers: w, cancelMember: "one"}
	r, _ := New(testPlan(), g)
	_ = r.SetWriters(w)
	if err := r.Prepare(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	_ = r.ArmWriters()
	w.live = true
	for round := range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		g.cancelCopy = cancel
		g.heads["one"] += "-new"
		g.heads["two"] += "-new"
		events = nil
		r.PushRound(ctx)
		cancel()
		first := "copy:one"
		if round == 1 {
			first = "copy:two"
		}
		if len(events) < 3 || events[0] != "freeze" || events[1] != first || events[len(events)-1] != "thaw" {
			t.Fatal("copy opportunity did not rotate", round, events)
		}
		if round == 1 && r.copied["two"] != g.heads["two"] {
			t.Fatal("later member never copied", events)
		}
	}
}

func TestClusterAtomicWriterAdmissionFencedAndContended(t *testing.T) {
	events := []string{}
	r, err := New(testPlan(), newFake())
	if err != nil {
		t.Fatal(err)
	}
	start := func() error { return nil }
	if r.StartWriter(t.Context(), start) == nil {
		t.Fatal("unprepared writer admitted")
	}
	if err = r.SetWriters(&fakeWriters{events: &events}); err != nil {
		t.Fatal(err)
	}
	if err = r.Prepare(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if err = r.ArmWriters(); err != nil {
		t.Fatal(err)
	}
	if err = r.StartWriter(t.Context(), start); err != nil {
		t.Fatal("ready writer refused", err)
	}
	r.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- r.StartWriter(t.Context(), start) }()
	var blocked bool
	select {
	case err = <-done:
		if err == nil {
			t.Error("contended writer admitted")
		}
	case <-time.After(time.Second):
		blocked = true
	}
	r.mu.Unlock()
	if blocked {
		<-done
		t.Fatal("writer waited on outer mutex from lifecycle worker")
	}
	r.FenceAdmission()
	called := false
	if err = r.StartWriter(t.Context(), func() error { called = true; return nil }); err == nil || called {
		t.Fatal("late engine/PTY creation crossed the closing fence")
	}
}
