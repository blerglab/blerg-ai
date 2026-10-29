package clusterworkspace

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func testPlan() Plan {
	p := Plan{SessionID: "session", Manifest: protocol.WorkspaceManifest{Version: 2, Layout: protocol.WorkspaceLayoutNames, Kind: "folder", OwnerID: "owner", Runtime: "cluster", LaunchGeneration: "launch", Digest: "digest"}, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}}
	for _, id := range []string{"one", "two"} {
		p.Manifest.Members = append(p.Manifest.Members, protocol.WorkspaceMember{ID: id, Name: id, Path: id, SourceIdentity: "source-" + id, CommonDirectoryIdentity: "git-" + id, Provider: "github", Destination: "org/" + id})
		p.Members = append(p.Members, Member{ID: id, Provider: "github", CloneURL: "https://github.com/org/" + id + ".git", PushURL: "https://github.com/org/" + id + ".git", DestinationIdentity: "org/" + id, CredentialHost: "github.com", Token: "fixture-private-token-" + id, PushAuthorized: true})
	}
	return p
}

type fakeGit struct {
	mu                  sync.Mutex
	calls               []string
	heads               map[string]string
	prepareErr, pushErr string
}

func (f *fakeGit) Prepare(_ context.Context, m Member, _ string, _ bool) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "prepare:"+m.ID)
	if m.ID == f.prepareErr {
		return "", "", errors.New("secret upstream body")
	}
	return "base-" + m.ID, f.heads[m.ID], nil
}
func (f *fakeGit) Head(_ context.Context, m Member) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heads[m.ID], nil
}
func (f *fakeGit) Push(_ context.Context, m Member, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "push:"+m.ID)
	if m.ID == f.pushErr {
		return errors.New("secret upstream body")
	}
	return nil
}
func newFake() *fakeGit {
	return &fakeGit{heads: map[string]string{"one": "head-one", "two": "head-two"}}
}
func TestCloneAllBeforeReadyAndPartialFailure(t *testing.T) {
	p := testPlan()
	f := newFake()
	f.prepareErr = "two"
	r, e := New(p, f)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Prepare(context.Background(), false); e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal("clone failure not sanitized", e)
	}
	r.PushRound(context.Background())
	if len(f.calls) != 2 {
		t.Fatal("push before all clones ready", f.calls)
	}
	f.prepareErr = ""
	if e = r.Prepare(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	out := r.PushRound(context.Background())
	if out[0].Pending || out[1].Pending {
		t.Fatal(out)
	}
}
func TestPartialPushResumePreservesSuccess(t *testing.T) {
	p := testPlan()
	f := newFake()
	f.pushErr = "two"
	r, _ := New(p, f)
	if e := r.Prepare(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	first := r.PushRound(context.Background())
	if first[0].Pending || !first[1].Pending || first[1].PushError == "" {
		t.Fatal(first)
	}
	for _, v := range first {
		if strings.Contains(v.PushError, "secret") {
			t.Fatal("diagnostic leaked")
		}
	}
	p.PreviousResults = first
	p.Runtime.Generation++
	f2 := newFake()
	resumed, e := New(p, f2)
	if e != nil {
		t.Fatal(e)
	}
	if e = resumed.Prepare(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	// Preparation must not turn a successfully pushed unchanged head into pending.
	final := resumed.PushRound(context.Background())
	if final[0].Pending || final[1].Pending {
		t.Fatal(final)
	}
	pushes := 0
	for _, call := range f2.calls {
		if call == "push:one" {
			t.Fatal("resume overwrote successful result")
		}
		if call == "push:two" {
			pushes++
		}
	}
	if pushes != 1 {
		t.Fatal(f2.calls)
	}
}
func TestPublicCloneDoesNotGrantPushAndStopSerializes(t *testing.T) {
	p := testPlan()
	p.Members[0].PushAuthorized = false
	p.Members[0].Token = ""
	f := newFake()
	r, e := New(p, f)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Prepare(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	wg.Go(func() { r.PushRound(context.Background()) })
	wg.Go(func() { r.Stop(context.Background()) })
	wg.Wait()
	result := r.Results()
	if !result[0].Pending || result[0].PushError == "" || result[1].Pending {
		t.Fatal(result)
	}
	for _, call := range f.calls {
		if call == "push:one" {
			t.Fatal("public clone supplied push authorization")
		}
	}
	before := len(f.calls)
	r.PushRound(context.Background())
	if len(f.calls) != before {
		t.Fatal("stopped runtime pushed")
	}
}
func TestPlanCredentialIsolation(t *testing.T) {
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Members[1].CredentialHost = "github.com.evil.example" },
		func(p *Plan) { p.Members[1].CloneURL = "https://github.com.evil.example/org/two.git" },
		func(p *Plan) { p.Members[1].PushURL = "https://gitlab.com/org/two.git" },
		func(p *Plan) { p.Members[1].Provider = "gitlab" },
		func(p *Plan) { p.Members[1].Token = "" },
		func(p *Plan) { p.Members[1].ID = "../one" },
		func(p *Plan) { p.Members[1].ID = "one" },
		func(p *Plan) { p.Members[1].CloneURL = "https://u:secret@github.com/org/two.git" },
		func(p *Plan) { p.Runtime.LaunchGeneration = "old" },
	} {
		p := testPlan()
		mutate(&p)
		if p.Validate() == nil {
			t.Fatal("forged plan admitted")
		}
	}
	p := testPlan()
	p.Members[1].Provider = "gitlab"
	p.Manifest.Members[1].Provider = "gitlab"
	p.Members[1].CloneURL = "https://gitlab.com/org/two.git"
	p.Members[1].PushURL = p.Members[1].CloneURL
	p.Members[1].CredentialHost = "gitlab.com"
	if p.Validate() != nil {
		t.Fatal("mixed providers with duplicate names refused")
	}
}

type fakeSupervisor struct {
	launch   protocol.SupervisorLaunch
	admitted int
	badProof bool
	closed   bool
}

func (f *fakeSupervisor) AdmitShell(_ context.Context, _ protocol.SupervisorShell) error {
	if f.closed || f.admitted >= 4 {
		return ErrPlan
	}
	f.admitted++
	return nil
}
func (f *fakeSupervisor) Notify(context.Context, protocol.SupervisorLaunch, protocol.SupervisorPhase) error {
	return nil
}
func (f *fakeSupervisor) CloseShell(_ context.Context, s protocol.SupervisorShell, _ string) (protocol.SupervisorCleanup, error) {
	p := f.proof()
	p.ShellGeneration = s.ShellGeneration
	return p, nil
}
func (f *fakeSupervisor) CloseLaunch(context.Context, protocol.SupervisorLaunch, string) (protocol.SupervisorCleanup, error) {
	f.closed = true
	f.admitted = 0
	return f.proof(), nil
}
func (f *fakeSupervisor) RecoverLaunch(context.Context, protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	return f.proof(), nil
}
func (f *fakeSupervisor) proof() protocol.SupervisorCleanup {
	return protocol.SupervisorCleanup{Launch: f.launch, ContainmentEmpty: !f.badProof, PTYsClosed: true, Reaped: true}
}
func TestSupervisorAdmissionStopAndUncertainCleanup(t *testing.T) {
	for _, bad := range []bool{false, true} {
		p := testPlan()
		git := newFake()
		r, _ := New(p, git)
		launch := protocol.SupervisorLaunch{SessionID: p.SessionID, LaunchGeneration: p.Runtime.LaunchGeneration, RuntimeGeneration: p.Runtime.Generation, Runtime: "cluster", ConnectionID: "connection"}
		supervisor := &fakeSupervisor{launch: launch}
		if e := r.SetSupervisor(supervisor, launch); e != nil {
			t.Fatal(e)
		}
		if e := r.Prepare(context.Background(), false); e != nil {
			t.Fatal(e)
		}
		for i := range 4 {
			if e := r.AdmitShell(context.Background(), protocol.SupervisorShell{Launch: launch, ShellGeneration: string(rune('a' + i))}); e != nil {
				t.Fatal(e)
			}
		}
		if e := r.AdmitShell(context.Background(), protocol.SupervisorShell{Launch: launch, ShellGeneration: "fifth"}); e == nil {
			t.Fatal("fifth supervisor admitted")
		}
		supervisor.badProof = bad
		var wg sync.WaitGroup
		wg.Go(func() { r.Stop(context.Background()) })
		wg.Go(func() {
			_ = r.AdmitShell(context.Background(), protocol.SupervisorShell{Launch: launch, ShellGeneration: "late"})
		})
		wg.Wait()
		if !supervisor.closed || supervisor.admitted != 0 {
			t.Fatal("launch close missed shell writer")
		}
		if r.AdmitShell(context.Background(), protocol.SupervisorShell{Launch: launch, ShellGeneration: "after-stop"}) == nil {
			t.Fatal("admitted after stop")
		}
		if bad {
			for _, result := range r.Results() {
				if !result.Pending || result.SyncError == "" {
					t.Fatal("unproven cleanup released settlement", result)
				}
			}
		}
	}
}

func TestExpiredPreparationAndCanceledRoundRetainMembers(t *testing.T) {
	p := testPlan()
	p.PrepareDeadlineMillis = 1
	git := newFake()
	r, _ := New(p, git)
	if r.Prepare(context.Background(), false) == nil {
		t.Fatal("expired session preparation proceeded")
	}
	// Cancellation must retain every unattempted member.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result := r.PushRound(canceled)
	for _, member := range result {
		if !member.Pending {
			t.Fatal("unprepared member lost recovery state")
		}
	}
}
