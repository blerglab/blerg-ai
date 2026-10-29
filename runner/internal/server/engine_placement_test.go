package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"slices"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestHeadlessPlacementUsesRequestedEngine(t *testing.T) {
	for _, scratch := range []bool{false, true} {
		t.Run(map[bool]string{false: "repository", true: "scratch"}[scratch], func(t *testing.T) {
			hub := NewHub()
			first := &DaemonConn{ID: "00000000-0000-4000-8000-000000000065", send: make(chan []byte, 8)}
			first.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
			first.SetCheckedOutRepos([]string{"project"})
			first.SetAvailableEngines([]string{"claude"})
			second := &DaemonConn{ID: "00000000-0000-4000-8000-000000000068", send: make(chan []byte, 8)}
			second.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
			second.SetCheckedOutRepos([]string{"project"})
			second.SetAvailableEngines([]string{"codex"})
			hub.Register(first)
			hub.Register(second)
			api := ownedInventoryAPI(t, hub, "fixture-daemon-token", nil, "")
			req := runnerStartRequest{Repo: "project", Runtime: "daemon", Engine: "codex", Model: "gpt-example"}
			if scratch {
				req.Repo, req.NoRepo = "", true
			}
			if _, err := api.StartSession(context.Background(), workspaceFixturePrincipal(), req, ""); err != nil {
				t.Fatal(err)
			}
			select {
			case raw := <-second.send:
				var spawn protocol.SpawnSession
				if json.Unmarshal(raw, &spawn) != nil || spawn.Engine != "codex" {
					t.Fatal("requested engine was not preserved")
				}
			default:
				t.Fatal("Codex request was not sent to the Codex-capable host")
			}
			select {
			case <-first.send:
				t.Fatal("selected host that explicitly lacks Codex")
			default:
			}
		})
	}
}

func TestRegisteredCapabilitiesDriveCronAndUsage(t *testing.T) {
	t.Cleanup(engines.Register(engines.Descriptor{ID: "fixture-engine", Name: "Fixture", Restricted: true, LimitResume: true, UsageWindows: []string{"fixture-window"}, Runtimes: []string{"docker", "cluster"}, CredentialEnvKeys: []string{"FIXTURE_AUTH"}}))
	if problem := cronStartProblem(runnerStartRequest{CronID: "fixture", Runtime: "docker", Engine: "fixture-engine", NoRepo: true, AutoStop: true, NoOperatorFallback: true}); problem != "" {
		t.Fatal(problem)
	}
	if !engineLimitResumeSupported("fixture-engine") || engineLimitResumeSupported("unregistered") {
		t.Fatal("continuation does not honor adapter capability")
	}
	now := time.Now()
	got, ok := sanitizeUsageLimits(protocol.UsageLimits{Provider: "fixture-engine", Windows: []protocol.UsageWindow{{ID: "fixture-window", UsedPercent: 25}, {ID: "5h", UsedPercent: 50}}}, now)
	if !ok || len(got.Windows) != 1 || got.Windows[0].ID != "fixture-window" {
		t.Fatal("provider window policy was not honored")
	}
	key, ok := singleEngineCredentialKey("fixture-engine")
	if !ok || key != "FIXTURE_AUTH" || !slices.Contains(sessionCredentialEnvKeys(), key) || !foreignEngineCredential(key, "codex") || foreignEngineCredential(key, "fixture-engine") {
		t.Fatal("credential wiring does not use registered ownership")
	}
}

func TestHeadlessPinnedTargetNeverFallsBack(t *testing.T) {
	hub := NewHub()
	first := &DaemonConn{ID: "00000000-0000-4000-8000-000000000072", send: make(chan []byte, 8)}
	first.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	first.SetAvailableEngines([]string{"claude"})
	second := &DaemonConn{ID: "00000000-0000-4000-8000-000000000076", send: make(chan []byte, 8)}
	second.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	second.SetAvailableEngines([]string{"codex"})
	hub.Register(first)
	hub.Register(second)
	api := ownedInventoryAPI(t, hub, "fixture-token", nil, "")
	for _, pin := range []string{first.ID, "fixture-disconnected"} {
		_, problem := api.StartSession(t.Context(), workspaceFixturePrincipal(), runnerStartRequest{NoRepo: true, Runtime: "daemon", Engine: "codex", TargetDaemonID: pin}, "")
		if problem == nil {
			t.Fatal("incompatible/disconnected pin silently changed hosts")
		}
	}
	select {
	case <-second.send:
		t.Fatal("spawned on an unrequested host")
	default:
	}
	_, problem := api.StartSession(t.Context(), workspaceFixturePrincipal(), runnerStartRequest{NoRepo: true, Runtime: "daemon", Engine: "codex", TargetDaemonID: second.ID}, "")
	if problem != nil {
		t.Fatal(problem)
	}
	select {
	case <-second.send:
	default:
		t.Fatal("compatible pin was not honored")
	}
}

func TestSessionAdvertisesRegisteredContinuationCapability(t *testing.T) {
	t.Cleanup(engines.Register(engines.Descriptor{ID: "fixture-continuation", Name: "Fixture", LimitResume: true}))
	engine := "fixture-continuation"
	if !sessionRowToInfo(db.SessionRow{Kind: "agent", Engine: &engine}, "").LimitResumeSupported {
		t.Fatal("registered continuation was not advertised")
	}
	engine = "unregistered"
	if sessionRowToInfo(db.SessionRow{Kind: "agent", Engine: &engine}, "").LimitResumeSupported {
		t.Fatal("unknown capability was advertised")
	}
	engine = "codex"
	if sessionRowToInfo(db.SessionRow{Kind: "tmux", Engine: &engine}, "").LimitResumeSupported {
		t.Fatal("terminal continuation was advertised")
	}
}

func TestHeadlessModelEffortValidatesSelectedHost(t *testing.T) {
	hub := NewHub()
	actual := reportingDaemon(hub, "00000000-0000-4000-8000-000000000071", map[string]models.Report{"codex": rep(time.Now(), mdl("gpt-fixture", "low"))})
	actual.SetAvailableEngines([]string{"codex"})
	other := reportingDaemon(hub, "00000000-0000-4000-8000-000000000075", map[string]models.Report{"codex": rep(time.Now(), mdl("gpt-fixture", "high"))})
	other.SetAvailableEngines([]string{"codex"})
	api := ownedInventoryAPI(t, hub, "fixture-token", nil, "")
	api.SetModelSources(NewModelSources(nil, hub))
	req := runnerStartRequest{NoRepo: true, Runtime: "daemon", TargetDaemonID: actual.ID, Engine: "codex", Model: "gpt-fixture", Effort: "high"}
	if _, problem := api.StartSession(t.Context(), workspaceFixturePrincipal(), req, ""); problem == nil || problem.Status != 422 {
		t.Fatal("accepted another host's effort levels")
	}
	if len(actual.send) != 0 || len(other.send) != 0 {
		t.Fatal("invalid effort reached a host")
	}
	req.Effort = "low"
	if _, problem := api.StartSession(t.Context(), workspaceFixturePrincipal(), req, ""); problem != nil {
		t.Fatal(problem)
	}
}
