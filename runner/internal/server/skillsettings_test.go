package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/skills"
)

func callSkillSettings(fx *mcpFx, method, token, query, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/skills/settings"+query, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	fx.api.HandleSkillSettings(w, r)
	return w
}

func TestSkillSettingsLaunchPrecedence(t *testing.T) {
	fx := newMCPFx(t)
	human := fx.human()
	admin := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: mcpAcct, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Caps: []string{"session.start", "account.manage"}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	put := func(token, query, body string, want int) {
		t.Helper()
		w := callSkillSettings(fx, "PUT", token, query, body)
		if w.Code != want {
			t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
		}
	}
	launch := func(parent string) (string, *skills.Policy) {
		t.Helper()
		result, problem := fx.api.StartSession(t.Context(), fx.owner(), RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", ParentSessionID: parent}, "")
		if problem != nil {
			t.Fatal(problem)
		}
		spawn := recvSpawn(t, fx.dc)
		if spawn.SkillPolicy == nil {
			t.Fatal("spawn lacks skill policy")
		}
		return result.SessionID, spawn.SkillPolicy
	}
	root, defaults := launch("")
	if !defaults.TDD || !defaults.UX || !defaults.Coordinating {
		t.Fatalf("defaults: %+v", defaults)
	}
	put(human, "", `{"scope":"install","skills":{"ux-design":false}}`, 403)
	put(admin, "", `{"scope":"install","skills":{"ux-design":false}}`, 200)
	put(human, "", `{"scope":"account","skills":{"ux-design":true,"test-driven-development":false}}`, 200)
	put(human, "?workspace="+root, `{"scope":"workspace","skills":{"ux-design":true,"test-driven-development":true}}`, 200)
	_, child := launch(root)
	if child.TDD || child.UX || child.Coordinating {
		t.Fatalf("off switches lost on wire: %+v", child)
	}
	saved, err := sessionSkills(t.Context(), fx.pool, root)
	if err != nil || *saved != *defaults {
		t.Fatalf("existing inventory changed: %+v %v", saved, err)
	}
	put(admin, "", `{"scope":"install","skills":{"ux-design":true}}`, 200)
	put(human, "", `{"scope":"account","skills":{"test-driven-development":true}}`, 200)
	put(human, "?workspace="+root, `{"scope":"workspace","skills":{"ux-design":false}}`, 200)
	_, workspace := launch(root)
	if !workspace.TDD || workspace.UX || workspace.Coordinating {
		t.Fatalf("workspace policy: %+v", workspace)
	}
	_, other := launch("")
	if !other.TDD || !other.UX || !other.Coordinating {
		t.Fatalf("workspace leaked to unrelated session: %+v", other)
	}
	// Read through the browser session panel path, then verify it refuses an unrelated account.
	r := httptest.NewRequest("GET", "/api/sessions/"+root+"/skills", nil)
	r.SetPathValue("id", root)
	r.Header.Set("Authorization", "Bearer "+human)
	w := httptest.NewRecorder()
	fx.api.HandleSessionSkills(w, r)
	var panel skills.Policy
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &panel) != nil || panel != *defaults {
		t.Fatalf("panel: %d %s", w.Code, w.Body.String())
	}
	foreign := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: "someone-else", Aud: coreAuthAudience, Kind: "human", Sid: testSID, Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if err := fx.api.MarkPrivate(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+foreign)
	w = httptest.NewRecorder()
	fx.api.HandleSessionSkills(w, r)
	if w.Code != 404 {
		t.Fatalf("foreign panel: %d", w.Code)
	}
}

func TestSkillSettingsRejectUnknownAndAgent(t *testing.T) {
	fx := newMCPFx(t)
	for _, body := range []string{
		`{"scope":"account","skills":{"coordinating":false}}`,
		`{"scope":"account","skills":{"ux-design":0}}`,
		`{"scope":"account","skills":{"ux-design":null}}`,
		`{"scope":"account","skills":{"ux-design":false}} {}`,
		`{"scope":"workspace","skills":{"ux-design":false}}`,
	} {
		w := callSkillSettings(fx, "PUT", fx.human(), "", body)
		if w.Code != 400 {
			t.Errorf("accepted %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	// An agent cannot read or change the owner's settings, even with session.start.
	token := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: "agent-one", OnBehalfOf: mcpAcct, Aud: coreAuthAudience, Kind: "agent", Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if w := callSkillSettings(fx, "GET", token, "", ""); w.Code != 403 {
		t.Fatalf("agent read: %d", w.Code)
	}
	// An account can only choose an owned root workspace.
	if err := db.PutAccountPreference(t.Context(), fx.pool, "someone-else", skillPreference, json.RawMessage(`{"ux-design":false}`)); err != nil {
		t.Fatal(err)
	}
	w := callSkillSettings(fx, "GET", fx.human(), "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"ux-design":false`) {
		t.Fatalf("account isolation: %d %s", w.Code, w.Body.String())
	}
}

func TestSkillPolicyBrowserClusterLaunchAndRecovery(t *testing.T) {
	fx := newMCPFx(t)
	w := callSkillSettings(fx, "PUT", fx.human(), "", `{"scope":"account","skills":{"ux-design":false}}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	id := fx.sessionID(fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"runtime": "cluster", "kind": "agent", "no_repo": true}))
	env := jobEnv(t, fx.k8s.created[len(fx.k8s.created)-1])
	raw, ok := env[skillPolicyEnv]["value"].(string)
	var got skills.Policy
	if !ok || json.Unmarshal([]byte(raw), &got) != nil || got.UX || !got.TDD || !got.Coordinating {
		t.Fatalf("cluster inventory: %v", env[skillPolicyEnv])
	}
	saved, err := sessionSkills(t.Context(), fx.pool, id)
	if err != nil || *saved != got {
		t.Fatalf("recovery inventory: %+v %v", saved, err)
	}
}

func TestSkillPolicyEnvironmentCannotBeForged(t *testing.T) {
	k8s := &fakeK8s{}
	jm := newTestJobManager(t, k8s)
	err := jm.CreateSessionJob(SessionJobSpec{SessionID: "forged-skills", Repo: "proj", ExtraEnv: map[string]string{skillPolicyEnv: `{"coordinating":true}`}})
	if err == nil || len(k8s.created) != 0 {
		t.Fatal("forged skill inventory submitted")
	}
}

func TestSkillPolicyPersonChannel(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	if err := db.PutAccountPreference(t.Context(), fx.pool, "owner-1", skillPreference+":"+fx.parent, json.RawMessage(`{"ux-design":false}`)); err != nil {
		t.Fatal(err)
	}
	status, body, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/channels", fx.human("owner-1"), map[string]any{"title": "Design", "engine": "codex", "private": true}, nil)
	if status != 202 {
		t.Fatalf("channel start: %d %v", status, body)
	}
	policy := fx.spawn().SkillPolicy
	if policy == nil || !policy.TDD || policy.UX || policy.Coordinating {
		t.Fatalf("plain channel skills: %+v", policy)
	}
}

func TestSessionSkillsFallback(t *testing.T) {
	fx := newMCPFx(t)
	result, problem := fx.api.StartSession(t.Context(), fx.owner(), RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker"}, "")
	if problem != nil {
		t.Fatal(problem)
	}
	recvSpawn(t, fx.dc)
	child, problem := fx.api.StartSession(t.Context(), fx.owner(), RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", ParentSessionID: result.SessionID}, "")
	if problem != nil {
		t.Fatal(problem)
	}
	recvSpawn(t, fx.dc)
	// Invalid persisted inventories must never prevent either kind of session from starting.
	for _, tc := range []struct {
		id   string
		main bool
	}{{result.SessionID, true}, {child.SessionID, false}, {newUUID(), false}} {
		if _, err := fx.pool.Exec(t.Context(), `UPDATE session_skill_policy SET policy='{}'::jsonb || '{"ux-design":"invalid"}'::jsonb WHERE session_id=$1`, tc.id); err != nil {
			t.Fatal(err)
		}
		policy, err := sessionSkills(t.Context(), fx.pool, tc.id)
		if err != nil || policy == nil || *policy != skills.Resolve(tc.main) {
			t.Errorf("fallback main=%v: %+v, %v", tc.main, policy, err)
		}
	}
	// A settings read failure also falls back, after ancestry establishes Main.
	if _, err := fx.pool.Exec(t.Context(), `DELETE FROM session_skill_policy`); err != nil {
		t.Fatal(err)
	}
	if err := db.PutAccountPreference(t.Context(), fx.pool, mcpAcct, skillPreference, json.RawMessage(`{"ux-design":"invalid"}`)); err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{"", result.SessionID} {
		_, problem := fx.api.StartSession(t.Context(), fx.owner(), RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", ParentSessionID: parent}, "")
		if problem != nil {
			t.Fatal(problem)
		}
		policy := recvSpawn(t, fx.dc).SkillPolicy
		if policy == nil || *policy != skills.Resolve(parent == "") {
			t.Fatalf("settings fallback on spawn: %+v", policy)
		}
	}
}
