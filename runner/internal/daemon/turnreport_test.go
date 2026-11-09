package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestTurnDoneReportTimeoutDoesNotFailDelivery(t *testing.T) {
	for _, name := range []string{"unchanged", "committed", "sync-refused"} {
		t.Run(name, func(t *testing.T) {
			commit, syncFailed := name == "committed", name == "sync-refused"
			w, _, storage, sources := localWorkspaceFixture(t)
			if commit {
				childCommit(t, w.state.Members[0].Clone, "turn.txt")
			}
			head := childRepoGit(t, w.state.Members[0].Clone, "rev-parse", "HEAD")
			w.state.Binding = &protocol.WorkspaceRuntimeBinding{}
			if err := w.save(); err != nil {
				t.Fatal(err)
			}
			storage.invalid = syncFailed
			guard, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			w.authorize = func(context.Context) error { return nil }
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var released sync.Once
			unblock := func() { released.Do(func() { close(release) }) }
			defer unblock()
			host, sender, _ := newTestHost(t, &scriptedAgentProvider{}, "")
			host.cfg.PrepareHostWorkspace = func(context.Context, protocol.SpawnSession) (string, WorkspaceExplorer, error) {
				return w.state.Root, &authorizedHostExplorer{workspace: w}, nil
			}
			host.cfg.Workspaces = &LocalWorkspaceService{Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
				close(entered)
				<-release // Model a slow RPC, with explicit completion instead of sleeping.
				return context.DeadlineExceeded
			}}
			host.cfg.OnTurnDone = func(string) { close(done) }
			host.Spawn(protocol.SpawnSession{SessionID: w.state.Session, Kind: "agent", Workspace: &w.state.Launch, InitialPrompt: "finish"})
			defer func() {
				unblock()
				if sess := host.get(w.state.Session); sess != nil {
					sess.cancel()
					<-sess.runDone
				}
			}()
			select {
			case <-entered:
			case <-guard.Done():
				t.Fatal("report not reached")
			}
			if commit {
				if got := childRepoGit(t, sources[0].Path, "rev-parse", "wip/"+w.state.Session); got != head {
					t.Fatalf("delivery = %s, want %s", got, head)
				}
			}
			unblock()
			select {
			case <-done:
			case <-guard.Done():
				t.Fatal("turn did not complete")
			}
			var reportNotice, syncNotice bool
			for _, ev := range sender.agentEvents() {
				if ev.Kind != "error" {
					continue
				}
				var p agent.ErrorPayload
				if err := json.Unmarshal(ev.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if p.Code == agent.ErrorCodeChildSync {
					syncNotice = true
					if !syncFailed || strings.Contains(p.Message, "deadline exceeded") {
						t.Fatalf("report timeout mislabeled as delivery failure: %s", p.Message)
					}
				}
				if strings.Contains(p.Message, "workspace result report") && strings.Contains(p.Message, "context deadline exceeded") {
					reportNotice = true
					if strings.Contains(p.Message, "sync completed") == syncFailed {
						t.Fatalf("incorrect sync outcome: %s", p.Message)
					}
				}
			}
			if !reportNotice {
				t.Fatal("missing separate result report notice")
			}
			if syncNotice != syncFailed {
				t.Fatalf("sync failure notice = %v, want %v", syncNotice, syncFailed)
			}
		})
	}
}
