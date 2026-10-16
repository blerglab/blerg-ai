package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestRuntimeDataPlaneIsRestrictedToItsCurrentRepository(t *testing.T) {
	for _, scenario := range []string{"current", "foreign-project", "restricted", "replaced", "message-only", "no-repository", "operator"} {
		t.Run(scenario, func(t *testing.T) {
			api, _, dc, pool := spawnTokenFixture(t)
			ctx := context.Background()
			sid := "00000000-0000-4000-8000-000000000004"
			repo := "org/project"
			if scenario == "no-repository" {
				repo = ""
			}
			_, err := pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,engine,runtime,spawning_account_id,restrict_tools) VALUES($1,$2,'running','/repos/project',$3,'codex','cluster','fixture-owner',$4)`, sid, dc.ID, repo, scenario == "restricted")
			if err != nil {
				t.Fatal(err)
			}
			runtime, message, err := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "replaced" {
				if _, _, err = db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			bearer := runtime
			project := "org/project"
			if scenario == "foreign-project" {
				project = "org/foreign"
			}
			if scenario == "message-only" {
				bearer = message
			}
			if scenario == "operator" {
				bearer = spawnTokenDaemonTok
				project = "operator/project"
			}
			run := func(handler http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+bearer)
				w := httptest.NewRecorder()
				handler(w, r)
				return w
			}
			write := run(api.HandlePostMemory, http.MethodPost, "/api/agent/memories", `{"project":"`+project+`","name":"fixture","content":"fixture memory"}`)
			allowed := scenario == "current" || scenario == "operator"
			if (write.Code == 204) != allowed {
				t.Fatalf("memory write status=%d", write.Code)
			}
			list := run(api.HandleGetMemories, http.MethodGet, "/api/agent/memories?project="+project, "")
			if (list.Code == 200) != allowed {
				t.Fatalf("memory read status=%d", list.Code)
			}
			search := run(api.HandleKnowledgeSearch, http.MethodPost, "/api/agent/knowledge/search", `{"project":"`+project+`","query":"fixture"}`)
			if (search.Code == 200) != allowed {
				t.Fatalf("memory search status=%d", search.Code)
			}
			propose := run(api.HandlePostRule, http.MethodPost, "/api/agent/rules", `{"project":"`+project+`","content":"fixture rule"}`)
			if (propose.Code == 201) != allowed {
				t.Fatalf("rule proposal status=%d", propose.Code)
			}
			readRules := run(api.HandleGetRules, http.MethodGet, "/api/agent/rules?project="+project, "")
			if (readRules.Code == 200) != allowed {
				t.Fatalf("rules read status=%d", readRules.Code)
			}
			if scenario == "current" && strings.Contains(readRules.Body.String(), "fixture rule") {
				t.Fatal("unapproved rule was exposed as standing instructions")
			}
			if scenario != "operator" {
				approve := run(api.HandlePatchRule, http.MethodPatch, "/api/agent/rules/fixture", `{"enabled":true}`)
				if approve.Code == 200 || approve.Code == 204 {
					t.Fatal("runtime approved its own rule")
				}
				config := run(api.HandlePostAgentConfig, http.MethodPost, "/api/agent-config", "fixture")
				if config.Code == 204 {
					t.Fatal("runtime replaced shared agent configuration")
				}
			}
		})
	}
}
