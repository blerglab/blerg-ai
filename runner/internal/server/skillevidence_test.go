package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/skills"
)

func TestSkillEvidencePersonAndOwnerFence(t *testing.T) {
	fx := newTopicFx(t)
	request := func(owner, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/skill-evidence", strings.NewReader(body))
		req.SetPathValue("id", fx.sess)
		req.Header.Set("Authorization", "Bearer "+fx.token(owner, "human"))
		w := httptest.NewRecorder()
		fx.api.HandleSkillEvidence(w, req)
		return w
	}
	body := `{"skill":"ux-design","version":"` + strings.Repeat("a", 64) + `","rule":"say-once","result":"missed","source":"agent"}`
	for i := 0; i < 2; i++ {
		w := request(topicOwner, body)
		var evidence skills.Evidence
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &evidence) != nil || evidence.Source != "person" {
			t.Fatalf("person evidence: %d %s", w.Code, w.Body.String())
		}
	}
	if w := request(topicOther, body); w.Code != 404 {
		t.Fatalf("foreign correction accepted: %d", w.Code)
	}
	if w := request(topicOwner, strings.Replace(body, "say-once", "unknown-rule", 1)); w.Code != 422 {
		t.Fatalf("unknown rule accepted: %d", w.Code)
	}
	var count int
	if err := fx.pool.QueryRow(t.Context(), "SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='skill_evidence'", fx.sess).Scan(&count); err != nil || count != 2 {
		t.Fatalf("corrections lost: %d %v", count, err)
	}
}

func TestDaemonCannotAuthorPersonSkillEvidence(t *testing.T) {
	msg := protocol.AgentEvent{SessionID: "own-session", Kind: "skill_evidence", Payload: json.RawMessage(`{"skill":"ux-design","version":"` + strings.Repeat("a", 64) + `","rule":"say-once","result":"missed","source":"person","session_id":"another-session"}`)}
	if !canonicalDaemonSkillEvidence(&msg) {
		t.Fatal("valid agent evidence refused")
	}
	var p skills.Evidence
	if json.Unmarshal(msg.Payload, &p) != nil || p.Source != "agent" || p.SessionID != "own-session" {
		t.Fatal("daemon forged a person correction or another session")
	}
}

func TestSkillEvidenceHTTPRateLimit(t *testing.T) {
	fx := newTopicFx(t)
	body := `{"skill":"ux-design","version":"` + strings.Repeat("a", 64) + `","rule":"say-once","result":"missed"}`
	token := fx.token(topicOwner, "human")
	for i := 0; i < 31; i++ {
		req := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/skill-evidence", strings.NewReader(body))
		req.SetPathValue("id", fx.sess)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		fx.api.HandleSkillEvidence(w, req)
		want := 200
		if i == 30 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("assessment %d: %d %s", i, w.Code, w.Body.String())
		}
		if i == 30 && w.Header().Get("Retry-After") != "60" {
			t.Fatal("missing retry guidance")
		}
	}
}

func TestDroppedSkillTelemetryLogsSafeBoundedCause(t *testing.T) {
	var out bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	t.Cleanup(func() { failurelog.Default = old })
	dc := &DaemonConn{send: make(chan []byte, 8)}
	for _, kind := range []string{"skill_load", "skill_applicability", "skill_evidence"} {
		for i := 0; i < 2; i++ {
			HandleAgentEvent(t.Context(), NewHub(), nil, dc, protocol.AgentEvent{Kind: kind, Payload: json.RawMessage(`{"private":"canary-secret-should-not-log"}`)})
		}
	}
	text := out.String()
	if strings.Contains(text, "canary-secret") || strings.Count(text, `"suppressed":0`) != 3 {
		t.Fatalf("unsafe or unbounded diagnostics: %s", text)
	}
	for _, cause := range []string{"invalid_skill_load", "invalid_skill_applicability", "invalid_skill_evidence"} {
		if !strings.Contains(text, cause) {
			t.Errorf("missing cause %s", cause)
		}
	}
	if len(dc.send) != 6 {
		t.Fatal("dropped telemetry was not acknowledged")
	}
}

func TestSkillEvidenceAgentRequiresLoadedVersion(t *testing.T) {
	fx := newTopicFx(t)
	token := fx.sessTok
	version := strings.Repeat("a", 64)
	body := `{"skill":"ux-design","version":"` + version + `","rule":"say-once","result":"followed","source":"person"}`
	request := func() int {
		req := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/skill-evidence", strings.NewReader(body))
		req.SetPathValue("id", fx.sess)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		fx.api.HandleSkillEvidence(w, req)
		return w.Code
	}
	if got := request(); got != 422 {
		t.Fatalf("unloaded version: %d", got)
	}
	_, _, err := db.AppendAgentEvent(t.Context(), fx.pool, fx.sess, newUUID(), "skill_load", `{"skill":"ux-design","version":"`+version+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := request(); got != 200 {
		t.Fatalf("loaded version: %d", got)
	}
}

func TestDaemonSkillEvidenceRequiresLoadedVersion(t *testing.T) {
	fx := newTopicFx(t)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	version := strings.Repeat("a", 64)
	payload := json.RawMessage(`{"skill":"ux-design","version":"` + version + `","rule":"say-once","result":"followed","source":"person"}`)
	send := func() {
		HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, dc, protocol.AgentEvent{SessionID: fx.sess, ClientEventID: newUUID(), Kind: "skill_evidence", Payload: payload})
	}
	count := func() int {
		var n int
		if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='skill_evidence'`, fx.sess).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	send()
	if count() != 0 {
		t.Fatal("daemon bypassed loaded-version check")
	}
	_, _, err := db.AppendAgentEvent(t.Context(), fx.pool, fx.sess, newUUID(), "skill_load", `{"skill":"ux-design","version":"`+version+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	send()
	if count() != 1 {
		t.Fatal("loaded daemon evidence was lost")
	}
}
