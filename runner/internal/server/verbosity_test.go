package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestVerbosityPreference(t *testing.T) {
	for _, value := range []string{"narrated", "standard", "terse"} {
		raw := json.RawMessage(`"` + value + `"`)
		if _, err := validatePreference("verbosity", raw); err != nil {
			t.Errorf("%s: %v", value, err)
		}
	}
	for _, value := range []string{`null`, `""`, `"Terse"`, `true`, `"unknown"`} {
		if _, err := validatePreference("verbosity", json.RawMessage(value)); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
}

func TestVerbosityLaunchResolutionAndPersistence(t *testing.T) {
	fx := newMCPFx(t)
	ctx := t.Context()
	if w := callPrefs(fx, "PUT", fx.human(), `{"verbosity":"narrated"}`); w.Code != 200 {
		t.Fatalf("preference: %d %s", w.Code, w.Body.String())
	}
	if w := callPrefs(fx, "GET", fx.human(), ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"verbosity":"narrated"`) {
		t.Fatalf("read default: %d %s", w.Code, w.Body.String())
	}
	var launched []string
	for _, interaction := range []string{"interactive", "unattended"} {
		for _, asked := range []string{"", "standard", "terse"} {
			want := asked
			if want == "" {
				want = "narrated"
			}
			res, problem := fx.api.StartSession(ctx, fx.owner(), RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", Interaction: interaction,
				Verbosity: asked}, "")
			if problem != nil {
				t.Fatal(problem)
			}
			launched = append(launched, res.SessionID)
			msg := recvSpawn(t, fx.dc)
			if msg.Verbosity != want || msg.Interaction != interaction {
				t.Fatalf("spawn: %+v", msg)
			}
			row, err := db.GetSession(ctx, fx.pool, res.SessionID)
			if err != nil || row == nil {
				t.Fatal(err)
			}
			if row.Verbosity != want || sessionRowToInfo(*row, mcpAcct).Verbosity != want {
				t.Fatalf("stored verbosity %q", row.Verbosity)
			}
			status, problem := fx.api.SessionStatus(ctx, fx.owner(), res.SessionID)
			if problem != nil || status["verbosity"] != want {
				t.Fatalf("status %v: %v", status, problem)
			}
		}
	}
	if w := callPrefs(fx, "PUT", fx.human(), `{"verbosity":"terse"}`); w.Code != 200 {
		t.Fatalf("change default: %d", w.Code)
	}
	old, err := db.GetSession(ctx, fx.pool, launched[0])
	if err != nil || old.Verbosity != "narrated" {
		t.Fatalf("default edit changed existing session: %+v %v", old, err)
	}
	value, problem := fx.api.resolveVerbosity(ctx, "", mcpAcct, true)
	if problem != nil || value != "terse" {
		t.Fatalf("child: %q %v", value, problem)
	}
	value, problem = fx.api.resolveVerbosity(ctx, "standard", mcpAcct, true)
	if problem != nil || value != "standard" {
		t.Fatalf("explicit child: %q %v", value, problem)
	}
	value, problem = fx.api.resolveVerbosity(ctx, "", "another-account", false)
	if problem != nil || value != "standard" {
		t.Fatalf("other account: %q %v", value, problem)
	}
	before := fx.count("sessions")
	_, problem = fx.api.StartSession(ctx, fx.owner(), RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", Verbosity: "loud"}, "invalid-verbosity")
	if problem == nil || problem.Status != 400 || fx.count("sessions") != before || len(fx.dc.send) != 0 {
		t.Fatalf("invalid start: %v", problem)
	}
}

func TestBrowserVerbosityAcrossRuntimes(t *testing.T) {
	fx := newMCPFx(t)
	for _, runtime := range []string{"docker", "cluster"} {
		body := map[string]any{"runtime": runtime, "kind": "agent", "verbosity": "terse", "no_repo": true}
		if runtime == "docker" {
			body["daemon_id"] = fx.dc.ID
		}
		sid := fx.sessionID(fx.do(fx.api.HandlePostSessions, fx.human(), body))
		row, err := db.GetSession(t.Context(), fx.pool, sid)
		if err != nil || row == nil || row.Verbosity != "terse" {
			t.Fatalf("%s row: %+v %v", runtime, row, err)
		}
		if runtime == "docker" {
			if msg := recvSpawn(t, fx.dc); msg.Verbosity != "terse" {
				t.Fatal("daemon missing verbosity")
			}
		} else {
			env := jobEnv(t, fx.k8s.created[len(fx.k8s.created)-1])
			if env[verbosityEnvVar]["value"] != "terse" {
				t.Fatalf("cluster env: %v", env[verbosityEnvVar])
			}
		}
	}
}

func TestSessionVerbosityChange(t *testing.T) {
	fx := newMCPFx(t)
	sid := fx.sessionID(fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"runtime": "docker", "kind": "agent", "no_repo": true, "daemon_id": fx.dc.ID}))
	_ = recvSpawn(t, fx.dc)
	fx.hub.SetSessionOwner(sid, fx.dc.ID)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='idle' WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	call := func(token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/sessions/"+sid+"/verbosity", strings.NewReader(body))
		r.SetPathValue("id", sid)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		fx.api.HandleSessionVerbosity(w, r)
		return w
	}
	for _, body := range []string{`{"verbosity":"unknown"}`, `{"verbosity":"terse","extra":true}`, `{"verbosity":"terse"} {}`} {
		if w := call(fx.human(), body); w.Code != 400 {
			t.Fatalf("bad input: %d %s", w.Code, w.Body.String())
		}
	}
	if w := call(fx.token("agent", ""), `{"verbosity":"terse"}`); w.Code != 403 {
		t.Fatalf("agent: %d", w.Code)
	}
	other := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: "other-account", Aud: coreAuthAudience, Kind: "human", Sid: testSID, Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if w := call(other, `{"verbosity":"terse"}`); w.Code != 404 {
		t.Fatalf("foreign account: %d", w.Code)
	}
	if w := call(fx.human(), `{"verbosity":"terse"}`); w.Code != 200 {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='ended',ended_at=now() WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if w := call(fx.human(), `{"verbosity":"narrated"}`); w.Code != 409 {
		t.Fatalf("ended session: %d", w.Code)
	}
	var frame protocol.SetSessionVerbosity
	if err := json.Unmarshal(<-fx.dc.send, &frame); err != nil || frame.Verbosity != "terse" || frame.SessionID != sid {
		t.Fatalf("frame %+v: %v", frame, err)
	}
	row, err := db.GetSession(t.Context(), fx.pool, sid)
	if err != nil || row.Verbosity != "terse" || row.Interaction != "interactive" {
		t.Fatalf("row %+v: %v", row, err)
	}
}
