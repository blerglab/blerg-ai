package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Restrictions must never disappear while the rest of a launch succeeds.
func TestLaunchRejectsUnknownPermissionRulesBeforeDispatch(t *testing.T) {
	for _, route := range []string{"launch", "post"} {
		for _, permissions := range []string{
			`{"command_policy":{"default":"deny"}}`,
			`{"git_policy":{"protected_branches":["main"]}}`,
			`{"path_scopes":["src/"]}`,
			`{"mcp_tools":[{"connection_id":"connection","tool":"write","command_policy":"deny"}]}`,
			`{"children":["docker"],"command_policy":{"default":"deny"}}`,
		} {
			t.Run(route+"/"+permissions, func(t *testing.T) {
				hub := NewHub()
				dc := &DaemonConn{ID: "00000000-0000-4000-8000-0000000000f1", Name: "desktop", send: make(chan []byte, 8)}
				dc.SetSandboxClones(true)
				dc.SetWorkBranches(true)
				hub.Register(dc)
				api := ownedInventoryAPI(t, hub, "daemon-tok-1234567890", nil, "")
				token := enableBrowserAuth(t, api)("session.start")
				body := `{"daemon_id":"` + dc.ID + `","repo":"app","runtime":"docker","kind":"agent","permissions":` + permissions + `}`
				req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+token)
				rec := httptest.NewRecorder()
				if route == "launch" {
					api.HandleLaunchSession(rec, req)
				} else {
					api.HandlePostSessions(rec, req)
				}
				if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "unsupported or invalid session permissions") {
					t.Errorf("got %d %s; want 422 with explicit unsupported permissions error", rec.Code, rec.Body.String())
				}
				select {
				case <-dc.send:
					t.Error("rejected launch reached daemon")
				default:
				}
			})
		}
	}
}

func TestLaunchPermissionsClosedSchema(t *testing.T) {
	for _, raw := range []string{
		``, `null`, `{}`, `{"children":["docker"]}`,
		`{"boards":["board"],"board_levels":[{"board_id":"board","level":"read"}],"mcp_tools":[{"connection_id":"connection","tool":"write"}]}`,
	} {
		if problem := launchPermissionsJSONProblem([]byte(raw), "agent"); problem != "" {
			t.Errorf("supported permissions %s: %s", raw, problem)
		}
	}
	for _, raw := range []string{
		`{"board_levels":[{"board_id":"board","level":"read","path_scopes":["src/"]}]}`,
		`{"sensitive-field":"sensitive-value"}`, `[]`, `false`, `"deny"`, `{"children":"docker"}`, `{`, `{} {}`,
	} {
		if problem := launchPermissionsJSONProblem([]byte(raw), "agent"); problem != "unsupported or invalid session permissions" {
			t.Errorf("invalid permissions returned %q; want fixed error without caller data", problem)
		}
	}
}
