package server

import (
	"context"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Placement must not call the gateway; its presence is configuration only.
type policyOnlyHasher struct{}

func (policyOnlyHasher) LiveToolHashes(context.Context, mcpgw.Proof, string, string) (map[string]string, error) {
	panic("placement policy called the gateway")
}

func TestGrantPlacementPolicy(t *testing.T) {
	dc := &DaemonConn{}
	dc.SetMCPGateway(true)
	dc.SetRestrictTools(true)
	cfg := &mcpStartConfig{Hasher: policyOnlyHasher{}, GatewayURL: "https://gateway.example.test"}
	for _, c := range []struct {
		name    string
		target  grantTarget
		cfg     *mcpStartConfig
		status  int
		message string
	}{
		{"cluster", clusterTarget(), cfg, 0, ""},
		{"docker", grantTarget{Runtime: "docker", Kind: "agent", Daemon: dc}, cfg, 0, ""},
		{"engine", grantTarget{Runtime: "cluster", Kind: "agent", Engine: "hermes"}, cfg, 422, msgMCPEngine},
		{"terminal", grantTarget{Runtime: "cluster"}, cfg, 422, msgMCPKind},
		{"bare host", grantTarget{Runtime: "daemon", Kind: "agent", Daemon: dc}, cfg, 422, msgMCPRuntime},
		{"missing runtime", grantTarget{Kind: "agent"}, cfg, 422, msgMCPRuntime},
		{"old daemon", grantTarget{Runtime: "docker", Kind: "agent", Daemon: &DaemonConn{}}, cfg, 422, msgMCPDaemonOld},
		{"missing daemon", grantTarget{Runtime: "docker", Kind: "agent"}, cfg, 422, msgMCPDaemonOld},
		{"missing gateway", clusterTarget(), &mcpStartConfig{GatewayURL: cfg.GatewayURL}, 503, msgMCPNoGateway},
		{"unwired", clusterTarget(), nil, 503, msgMCPNoGateway},
		{"missing gateway URL", clusterTarget(), &mcpStartConfig{Hasher: policyOnlyHasher{}}, 503, msgMCPNoGatewayURL},
	} {
		t.Run(c.name, func(t *testing.T) {
			problem := checkGrantTarget(c.target, c.cfg)
			if c.status == 0 {
				if problem != nil {
					t.Fatal(problem)
				}
			} else if problem == nil || problem.Status != c.status || !strings.Contains(problem.Message, c.message) {
				t.Fatalf("problem=%v, want %d %q", problem, c.status, c.message)
			}
		})
	}
}

func TestGrantRequesterPolicy(t *testing.T) {
	for _, c := range []struct {
		name    string
		who     grantRequester
		status  int
		message string
	}{
		{"agent", grantRequester{Kind: "agent", Account: mcpAcct}, 403, msgMCPHumanOnly},
		{"runner key", grantRequester{}, 403, msgMCPHumanOnly},
		{"service", grantRequester{Kind: "service", Account: mcpAcct, Sid: testSID}, 403, msgMCPHumanOnly},
		{"no login", grantRequester{Kind: "human", Account: mcpAcct}, 401, msgMCPSignIn},
		{"no account", grantRequester{Kind: "human", Sid: testSID}, 401, msgMCPSignIn},
		{"human", humanWho(), 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			problem := checkGrantRequester(c.who)
			if c.status == 0 {
				if problem != nil {
					t.Fatal(problem)
				}
			} else if problem == nil || problem.Status != c.status || !strings.Contains(problem.Message, c.message) {
				t.Fatalf("problem=%v, want %d %q", problem, c.status, c.message)
			}
		})
	}
}

func TestWorkspaceResultVisibilityPolicy(t *testing.T) {
	owner, docker := "owner", "docker"
	for _, c := range []struct {
		name                                            string
		visible                                         bool
		runtime, manifestRuntime, manifestOwner, viewer string
		missing                                         int
		want                                            bool
	}{
		{"owner", true, docker, "sandbox", owner, owner, 0, true},
		{"foreign", true, docker, "sandbox", owner, "foreign", 0, false},
		{"unknown", false, docker, "sandbox", owner, owner, 0, false},
		{"reassigned", false, docker, "sandbox", owner, owner, 0, false},
		{"runtime changed", true, "cluster", "sandbox", owner, owner, 0, false},
		{"host", true, "daemon", "host", owner, owner, 0, true},
		{"cluster", true, "cluster", "cluster", owner, owner, 0, true},
		{"internal", true, docker, "sandbox", owner, "", 0, false},
		{"manifest owner mismatch", true, docker, "sandbox", "foreign", owner, 0, false},
		{"missing workspace", true, docker, "sandbox", owner, owner, 1, false},
		{"missing manifest", true, docker, "sandbox", owner, owner, 2, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			row := &db.SessionRow{Runtime: &c.runtime, SpawningAccountID: &owner}
			ws := &db.SessionWorkspace{Manifest: &protocol.WorkspaceManifest{OwnerID: c.manifestOwner, Runtime: c.manifestRuntime}}
			switch c.missing {
			case 1:
				ws = nil
			case 2:
				ws.Manifest = nil
			}
			if got := workspaceResultVisible(c.visible, row, ws, c.viewer); got != c.want {
				t.Fatalf("visible=%v, want %v", got, c.want)
			}
		})
	}
}
