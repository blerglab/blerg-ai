package runtimelifecycle

import "testing"

func TestCleanupProofScope(t *testing.T) {
	b := Launch{SessionID: "session", LaunchGeneration: "selection", RuntimeGeneration: 2, Runtime: "cluster", ConnectionID: "connection"}
	p := Cleanup{Launch: b, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
	if err := p.ValidateFor(b, ""); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Cleanup){
		func(p *Cleanup) { p.SessionID = "other" }, func(p *Cleanup) { p.LaunchGeneration = "old" }, func(p *Cleanup) { p.RuntimeGeneration-- }, func(p *Cleanup) { p.ConnectionID = "old" }, func(p *Cleanup) { p.Runtime = "host" },
		func(p *Cleanup) { p.ShellGeneration = "one-of-four" }, func(p *Cleanup) { p.ContainmentEmpty = false }, func(p *Cleanup) { p.PTYsClosed = false }, func(p *Cleanup) { p.Reaped = false },
	} {
		bad := p
		mutate(&bad)
		if bad.ValidateFor(b, "") == nil {
			t.Fatal("unsafe all-launch proof", bad)
		}
	}
	p.ShellGeneration = "shell"
	if p.ValidateFor(b, "shell") != nil {
		t.Fatal("shell proof rejected")
	}
	if p.ValidateFor(b, "other") == nil {
		t.Fatal("other shell accepted")
	}
	p.ShellGeneration = ""
	if p.ValidateFor(b, "shell") == nil {
		t.Fatal("scope must be exact")
	}
}
func TestSupervisorBindingsAndLimits(t *testing.T) {
	for _, runtime := range []string{"host", "sandbox", "cluster"} {
		b := Launch{SessionID: "session", LaunchGeneration: "selection", RuntimeGeneration: 1, Runtime: runtime, ConnectionID: "connection"}
		if b.Validate() != nil {
			t.Fatal(runtime)
		}
		if (Shell{Launch: b}).Validate() == nil {
			t.Fatal("missing shell identity")
		}
		if (Shell{Launch: b, ShellGeneration: "shell"}).Validate() != nil {
			t.Fatal("valid shell")
		}
		b.RuntimeGeneration = 0
		if b.Validate() == nil {
			t.Fatal("missing incarnation")
		}
	}
	if MaxShells != 4 || OutputBytesPerSecond != 65536 || InputBytesPerSecond != 16384 {
		t.Fatal("session limits changed")
	}
	for _, p := range []Phase{Running, Syncing, Paused, Stopping, Replacing, Ended, Discarded} {
		if !p.Valid() {
			t.Fatal(p)
		}
	}
	if Phase("unknown").Valid() {
		t.Fatal("unknown phase")
	}
}
