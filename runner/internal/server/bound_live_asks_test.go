package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// This real Runner HTTP/SQL path proves that the bound ask forwards the actual
// browser token and frozen digest, never a sid + service key or an agent token.
// Core's separate-process suite proves independent verification of that token.
func TestBoundLiveAskRelay(t *testing.T) {
	fx := newTopicFx(t)
	owner := "00000000-0000-4000-8000-000000000003"
	if err := db.SetSessionSpawningAccount(t.Context(), fx.pool, fx.sess, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2 WHERE id=$1`, topicDaemonID, owner); err != nil {
		t.Fatal(err)
	}
	config := ld.Revision{Card: &ld.CardSpec{Kind: "stat"}, Scope: ld.Scope{Installation: "00000000-0000-4000-8000-000000000001", Runner: "00000000-0000-4000-8000-000000000002", Owner: owner, Binding: "00000000-0000-4000-8000-000000000004", Connection: "00000000-0000-4000-8000-000000000005", Tool: "quotes", Hash: strings.Repeat("a", 64), Revision: 1, OwnerEpoch: 1, ConnectionGeneration: 1, AuthorityEpoch: 1}, Arguments: json.RawMessage(`{"symbol":"NVDA"}`), Mapping: livemcp.Mapping{RowsPath: "/quotes", Fields: []livemcp.Field{{Name: "value", Path: "/close", Type: "number"}}, Keys: []string{"value"}}, IntervalSeconds: 60, ExpiresAt: time.Now().UTC().Add(time.Hour), OwnerOnly: true}
	config.ToolDefinition = json.RawMessage(`{"description":"Market quotes","inputSchema":{"additionalProperties":false,"properties":{"symbol":{"type":"string"}},"required":["symbol"],"type":"object"},"name":"quotes"}`)
	sum := sha256.Sum256(config.ToolDefinition)
	config.Scope.Hash = hex.EncodeToString(sum[:])
	if err := db.UpsertToolDefinition(t.Context(), fx.pool, db.ToolDefinition{Hash: config.Scope.Hash, Name: "quotes", Canonical: string(config.ToolDefinition), Size: len(config.ToolDefinition)}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertMCPGrants(t.Context(), fx.pool, []db.MCPGrant{{SessionID: fx.sess, ConnectionID: config.Scope.Connection, Name: "Market", AccountID: owner, Tools: map[string]db.MCPGrantTool{"quotes": {Mode: "allow", Hash: config.Scope.Hash}}, ProofKind: "session_id", ProofValue: testSID, URLSnapshot: "https://market.example/mcp", TokenHash: bytes.Repeat([]byte{1}, 32), CallBudget: 10}}); err != nil {
		t.Fatal(err)
	}
	lister := &boundLiveLister{definition: config.ToolDefinition, readOnly: true}
	fx.api.setMCPStart(&mcpStartConfig{Hasher: lister})
	var id string
	assertUnlocked := func() {
		t.Helper()
		tx, err := fx.pool.Begin(t.Context())
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		var available bool
		if err = tx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended('bound-draft:' || $1,0))`, fx.sess).Scan(&available); err != nil || !available {
			t.Error("upstream call holds workspace lock", err)
		}
		if id != "" {
			if _, err = tx.Exec(t.Context(), `SELECT 1 FROM bound_asks WHERE message_id=$1 FOR UPDATE NOWAIT`, id); err != nil {
				t.Error("upstream call holds ask lock", err)
			}
		}
	}
	lister.beforeList = assertUnlocked
	needFresh := false
	human := fx.token(owner, "human")
	calls := 0
	loseReply := true
	active := false
	var approved ld.Decision
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-component" {
			t.Error("missing component gate")
			w.WriteHeader(401)
			return
		}
		assertUnlocked()
		switch r.URL.Path {
		case "/internal/live-data/proposals":
			var req struct {
				Configuration ld.Revision `json:"configuration"`
				AskID         string      `json:"ask_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			frozen, _, err := req.Configuration.Canonical(time.Now())
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(ld.Proposal{Configuration: frozen, Digest: frozen.Scope.Digest, Status: "pending", AskID: req.AskID})
		case "/internal/live-data/approve":
			if needFresh {
				w.WriteHeader(401)
				return
			}
			calls++
			_ = json.NewDecoder(r.Body).Decode(&approved)
			if approved.AccessToken != human || approved.AccountID != owner {
				t.Error("actual owner token not forwarded")
				w.WriteHeader(404)
				return
			}
			status := "approved"
			if !approved.Approve {
				status = "declined"
				active = false
			}
			if loseReply {
				loseReply = false
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
			_ = json.NewEncoder(w).Encode(ld.DecisionReceipt{BindingID: approved.BindingID, Revision: approved.Revision, Digest: approved.Digest, AskID: approved.AskID, Status: status})
		case "/internal/live-data/confirm", "/internal/live-data/receipt", "/internal/live-data/cancel":
			var d ld.Decision
			_ = json.NewDecoder(r.Body).Decode(&d)
			status := "approved"
			if r.URL.Path == "/internal/live-data/confirm" {
				var local string
				if err := fx.pool.QueryRow(t.Context(), `SELECT status FROM bound_asks WHERE message_id=$1`, d.AskID).Scan(&local); err != nil || local != "committed" {
					t.Error("confirmation preceded durable Runner commit", err)
				}
				active = true
			}
			if r.URL.Path == "/internal/live-data/cancel" {
				active = false
				status = "declined"
			}
			_ = json.NewEncoder(w).Encode(ld.DecisionReceipt{BindingID: d.BindingID, Revision: d.Revision, Digest: d.Digest, AskID: d.AskID, Status: status})
		default:
			t.Error("unexpected Core operation")
			w.WriteHeader(404)
		}
	}))
	defer core.Close()
	fx.api.SetCoreCredentials(core.URL, "fixture-component")
	fx.api.SetLiveDataApprovalURL(core.URL)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/{id}/reply", fx.api.HandlePostMessageReply)
	call := func(path, token string, body any) (int, []byte) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	assertUnsupported := func() {
		for _, card := range []any{nil, map[string]any{"kind": "chart", "spec": map[string]any{"mark": "line"}}, map[string]any{"kind": "stat", "sparkline": true}} {
			rawConfig, _ := json.Marshal(config)
			var setup map[string]any
			if err := json.Unmarshal(rawConfig, &setup); err != nil {
				t.Fatal(err)
			}
			setup["card"] = card
			code, body := call("/api/messages", fx.sessTok, map[string]any{"kind": "ask", "subject": map[string]any{"kind": "live_binding", "live_binding": setup}})
			if code < 400 || !bytes.Contains(body, []byte("stat and gauge")) || lister.calls != 0 || calls != 0 {
				t.Fatal("unsupported setup did not refuse before I/O with supported types", code, string(body))
			}
			var count int
			if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM bound_asks`).Scan(&count); err != nil || count != 0 {
				t.Fatal("unsupported setup created ask", count, err)
			}
		}
	}
	assertUnsupported()
	if code, _ := call("/api/messages", fx.sessTok, map[string]any{"kind": "ask", "subject": map[string]any{"kind": "live_binding", "live_binding": config}}); code < 400 || lister.calls != 0 || calls != 0 {
		t.Fatalf("disabled ask reached upstream: status=%d listings=%d calls=%d", code, lister.calls, calls)
	}
	fx.api.liveDataPollingEnabled = true // Only this isolated approval fixture opts in; invocation stays unreachable.
	assertUnsupported()
	code, raw := call("/api/messages", fx.sessTok, map[string]any{"kind": "ask", "body": "agent prose must not become approval", "subject": map[string]any{"kind": "live_binding", "live_binding": config}})
	if code != 200 && code != 201 {
		t.Fatalf("create %d %s", code, raw)
	}
	var ask struct {
		MessageID string `json:"message_id"`
		ID        string `json:"id"`
	}
	_ = json.Unmarshal(raw, &ask)
	id = ask.MessageID
	if id == "" {
		id = ask.ID
	}
	if id == "" {
		t.Fatalf("missing ask id: %s", raw)
	}
	var marker bool
	if err := fx.pool.QueryRow(t.Context(), `SELECT (payload->>'live_binding')::boolean FROM agent_events WHERE session_id=$1 AND kind='ask' AND payload->>'message_id'=$2`, fx.sess, id).Scan(&marker); err != nil || !marker {
		t.Fatal("missing persisted live-binding routing marker", err)
	}
	var body string
	var private bool
	if err := fx.pool.QueryRow(t.Context(), `SELECT body,owner_only FROM messages WHERE id=$1`, id).Scan(&body, &private); err != nil || !private || !strings.Contains(body, "Revision digest:") || strings.Contains(body, "agent prose") {
		t.Fatal("ask not server generated/private", err)
	}
	for _, token := range []string{"forged", fx.token(topicOther, "human"), fx.token(owner, "agent")} {
		code, _ = call("/api/messages/"+id+"/reply", token, map[string]string{"answer": "yes"})
		if code < 400 {
			t.Fatal("nonowner decision accepted", code)
		}
	}
	if calls != 0 {
		t.Fatal("unverified credential reached Core")
	}
	lister.readOnly = false
	if denied, _ := call("/api/messages/"+id+"/reply", human, map[string]string{"answer": "yes"}); denied < 400 {
		t.Fatal("read-only lost before approval")
	}
	lister.readOnly = true
	needFresh = true
	if status, _ := call("/api/messages/"+id+"/reply", human, map[string]string{"answer": "yes"}); status != 401 {
		t.Fatal("fresh login must be retryable", status)
	}
	needFresh = false
	code, raw = call("/api/messages/"+id+"/reply", human, map[string]string{"answer": "yes"})
	if code != 200 {
		t.Fatalf("owner answer %d %s", code, raw)
	}
	frozen, _, _ := config.Canonical(time.Now())
	if !active {
		t.Fatal("durable approval was not confirmed")
	}
	if calls != 1 || approved.Digest != frozen.Scope.Digest || approved.AskID != id {
		t.Fatal("decision lost exact subject")
	}
	code, _ = call("/api/messages/"+id+"/reply", human, map[string]string{"answer": "yes"})
	if code != 200 || calls != 1 {
		t.Fatal("replay dispatched again", code, calls)
	}
	var persisted string
	if err := fx.pool.QueryRow(t.Context(), `SELECT subject::text||coalesce(decision::text,'') FROM bound_asks WHERE message_id=$1`, id).Scan(&persisted); err != nil || strings.Contains(persisted, human) {
		t.Fatal("person token persisted", err)
	}

	code, _ = call("/api/messages/"+id+"/reply", human, map[string]string{"answer": "no"})
	if code != 200 || active {
		t.Fatal("decline after approval failed to revoke", code)
	}
	config.Scope.Revision = 2
	lister.readOnly = false
	if denied, _ := call("/api/messages", fx.sessTok, map[string]any{"kind": "ask", "body": "untrusted", "subject": map[string]any{"kind": "live_binding", "live_binding": config}}); denied < 400 {
		t.Fatal("non-read-only proposal accepted")
	}
	lister.readOnly = true
	code, raw = call("/api/messages", fx.sessTok, map[string]any{"kind": "ask", "body": "untrusted", "subject": map[string]any{"kind": "live_binding", "live_binding": config}})
	if code != 200 && code != 201 {
		t.Fatal("second ask", code)
	}
	_ = json.Unmarshal(raw, &ask)
	id = ask.MessageID
	if id == "" {
		id = ask.ID
	}
	active = true // Unknown Core outcome while the Runner still has an open ask.
	if _, err := fx.pool.Exec(t.Context(), `UPDATE messages SET status='answered',answer='',answered_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := fx.api.reconcileLiveDecisions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("closed ask did not notify Core")
	}
}

type boundLiveLister struct {
	beforeList func()
	definition json.RawMessage
	readOnly   bool
	calls      int
}

func (l *boundLiveLister) LiveToolHashes(context.Context, mcpgw.Proof, string, string) (map[string]string, error) {
	return nil, nil
}
func (l *boundLiveLister) ListToolsComplete(context.Context, mcpgw.Proof, string, string) ([]mcpgw.ListedTool, bool, error) {
	l.calls++
	if l.beforeList != nil {
		l.beforeList()
	}
	var d struct {
		Name, Description string
		InputSchema       json.RawMessage
	}
	_ = json.Unmarshal(l.definition, &d)
	annotations, _ := json.Marshal(map[string]bool{"readOnlyHint": l.readOnly})
	return []mcpgw.ListedTool{{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema, Hash: mcpgw.ToolHash(d.Name, d.Description, d.InputSchema), Annotations: annotations}}, true, nil
}
