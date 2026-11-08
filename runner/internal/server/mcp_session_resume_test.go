package server

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"strings"
	"testing"
)

func TestMixedResumeDetachesScopedGrants(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "scoped only", true: "mixed"}[mixed], func(t *testing.T) {
			fx := newMCPFx(t)
			ctx := context.Background()
			sid := newUUID()
			owner := "00000000-0000-4000-8000-000000000451"
			human := "00000000-0000-4000-8000-000000000452"
			scoped := newUUID()
			account := newUUID()
			if err := db.InsertSession(ctx, fx.pool, sid, fx.dc.ID, "running", "/fixture", "fixture", "fixture", ""); err != nil {
				t.Fatal(err)
			}
			scope := &db.MCPSessionScope{AccountID: owner, RunnerInstallationID: "fixture-runner", RunnerSessionID: sid, HumanSessionID: human, LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: strings.Repeat("a", 64)}
			specs := []mcpgw.GrantSpec{{ConnectionID: scoped, Name: "remote", URL: fx.up.url(), SessionScope: scope}}
			if mixed {
				specs = append(specs, mcpgw.GrantSpec{ConnectionID: account, Name: "account", URL: fx.up.url()})
			}
			tokens, err := mcpgw.CreateGrants(ctx, fx.pool, sid, owner, mcpgw.Proof{SessionID: human}, specs)
			if err != nil {
				t.Fatal(err)
			}
			old, err := db.ListMCPGrantsForSession(ctx, fx.pool, sid)
			if err != nil {
				t.Fatal(err)
			}
			eligible, problem := grantResumeProblem(ctx, fx.pool, sid, owner, owner, human)
			if problem != nil {
				t.Fatal(problem)
			}
			for _, g := range eligible {
				if g.SessionScope != nil {
					t.Error("generic resume included scoped authority")
				}
			}
			_, beforeAutomatic := grantResumeProblem(ctx, fx.pool, sid, owner, "", "")
			if mixed && beforeAutomatic == nil {
				t.Fatal("mixed grants allowed unattended account recovery")
			}
			if !mixed && beforeAutomatic != nil {
				t.Fatal("scoped grants required a human for detached recovery")
			}
			config, err := reissueGrants(ctx, fx.hub, fx.pool, sid, owner, human, old)
			if err != nil {
				t.Fatal(err)
			}
			if config != nil {
				for _, server := range config.Servers {
					if server.Name == "remote" {
						t.Error("resume configuration restored scoped server")
					}
				}
			}
			remaining, err := db.ListMCPGrantsForSession(ctx, fx.pool, sid)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if mixed {
				want = 1
			}
			if len(remaining) != want {
				t.Fatal("wrong account grant preservation", len(remaining), want)
			}
			if mixed && remaining[0].ConnectionID != account {
				t.Fatal("scoped grant converted into account grant")
			}
			if fx.tokenBelongsTo(tokens[scoped], sid) {
				t.Fatal("old scoped token survived resume")
			}
			_, automatic := grantResumeProblem(ctx, fx.pool, sid, owner, "", "")
			if mixed && automatic == nil {
				t.Fatal("account grant lost existing human-proof requirement")
			}
			if !mixed && automatic != nil {
				t.Fatal("detached scoped server blocked unattended conversation recovery")
			}
		})
	}
}
