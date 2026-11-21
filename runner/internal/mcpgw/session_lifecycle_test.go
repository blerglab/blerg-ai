package mcpgw

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionLifecycleUsesInternalControl(t *testing.T) {
	for _, action := range []string{"renew", "suspend", "close"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/internal/mcp/session-connections/lifecycle" || r.Header.Get("X-Internal-Key") != "synthetic-key" {
					t.Error("wrong internal lifecycle request")
				}
				var body struct {
					SessionDispatchRequest
					Action string `json:"action"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Action != action || body.SessionDispatchRequest != dispatchFixture() {
					t.Error("lifecycle scope lost")
				}
				generation := dispatchFixture().CredentialGeneration
				if action == "suspend" {
					generation++
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"applied": true, "credential_generation": generation})
			}))
			defer srv.Close()
			c := &HTTPCoreClient{BaseURL: srv.URL, InternalKey: "synthetic-key"}
			generation, err := c.SessionLifecycle(context.Background(), dispatchFixture(), action)
			expected := dispatchFixture().CredentialGeneration
			if action == "suspend" {
				expected++
			}
			if err != nil || calls != 1 || generation != expected {
				t.Fatal("lifecycle was not delivered", err, calls)
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "not found", 404) }))
	defer srv.Close()
	c := &HTTPCoreClient{BaseURL: srv.URL, InternalKey: "synthetic-key"}
	if _, err := c.SessionLifecycle(context.Background(), dispatchFixture(), "close"); !errors.Is(err, ErrConnectionGone) {
		t.Fatal("generation conflict not superseded", err)
	}
}

type lifecycleCapture struct {
	calls  []string
	scopes []SessionDispatchRequest
	err    error
}

func (c *lifecycleCapture) SessionLifecycle(_ context.Context, in SessionDispatchRequest, action string) (int64, error) {
	c.calls = append(c.calls, action)
	c.scopes = append(c.scopes, in)
	generation := in.CredentialGeneration
	if action == "suspend" {
		generation++
	}
	return generation, c.err
}
func TestSessionBindingDeliveryRenewsSuspendsAndCloses(t *testing.T) {
	h, gh, in, _ := newScopedGatewayHarness(t)
	ctx := context.Background()
	core := &lifecycleCapture{}
	if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
		t.Fatal(err)
	}
	if len(core.calls) != 1 || core.calls[0] != "renew" || core.scopes[0].MCPSessionScope != in.MCPSessionScope {
		t.Fatal("live binding not renewed exactly", core.calls)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE sessions SET status='disconnected' WHERE id=$1`, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
		t.Fatal(err)
	}
	if len(core.calls) != 2 || core.calls[1] != "suspend" {
		t.Fatal("outage not suspended", core.calls)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE sessions SET ended_at=now(),status='completed',end_reason='process_exited' WHERE id=$1`, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
		t.Fatal(err)
	}
	if len(core.calls) != 3 || core.calls[2] != "close" || core.scopes[2].CredentialGeneration != in.CredentialGeneration+1 {
		t.Fatal("terminal completion not closed", core.calls)
	}
	if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil || len(core.calls) != 3 {
		t.Fatal("delivered close repeated", err, core.calls)
	}
}
func TestSessionBindingDeliveryRetriesOutageAndNeverGuessesGeneration(t *testing.T) {
	h, gh, in, _ := newScopedGatewayHarness(t)
	ctx := context.Background()
	core := &lifecycleCapture{err: errors.New("synthetic transport failure")}
	if _, err := h.pool.Exec(ctx, `UPDATE sessions SET status='disconnected' WHERE id=$1`, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileSessionBindings(ctx, h.pool, core, true); err == nil {
		t.Fatal("delivery error disappeared")
	}
	if err := ReconcileSessionBindings(ctx, h.pool, core, true); err == nil || len(core.calls) != 2 {
		t.Fatal("outage lost retry", err, core.calls)
	}
	core.err = ErrConnectionGone
	if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
		t.Fatal(err)
	}
	if core.scopes[2].CredentialGeneration != in.CredentialGeneration {
		t.Fatal("guessed a newer credential generation")
	}
	if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil || len(core.calls) != 3 {
		t.Fatal("superseded generation kept retrying", err, core.calls)
	}
}

func TestDisabledGatewayNeverRenewsBindings(t *testing.T) {
	h, _, _, _ := newScopedGatewayHarness(t)
	ctx := context.Background()
	core := &lifecycleCapture{}
	if err := ReconcileSessionBindings(ctx, h.pool, core, false); err != nil || len(core.calls) != 0 {
		t.Fatal("disabled gateway renewed a lease", err, core.calls)
	}
	if err := ReconcileSessionBindings(ctx, h.pool, core, false); err != nil || len(core.calls) != 1 || core.calls[0] != "suspend" {
		t.Fatal("disabled gateway lost suspension delivery", err, core.calls)
	}
}

func TestLifecycleRejectsUnacknowledgedGeneration(t *testing.T) {
	for _, generation := range []string{"0", "99"} {
		t.Run(generation, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"applied":true,"credential_generation":` + generation + `}`))
			}))
			defer srv.Close()
			c := &HTTPCoreClient{BaseURL: srv.URL, InternalKey: "synthetic-key"}
			if _, err := c.SessionLifecycle(context.Background(), dispatchFixture(), "suspend"); err == nil {
				t.Fatal("arbitrary generation accepted")
			}
		})
	}
}

func TestRuntimeDetachmentRetainsTrustedPinsAndSpentBudget(t *testing.T) {
	h, gh, _, _ := newScopedGatewayHarness(t)
	ctx := context.Background()
	if ok, err := db.ConsumeMCPCall(ctx, h.pool, gh.sessionID, gh.connectionID); err != nil || !ok {
		t.Fatal("fixture budget", err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE session_mcp_grants SET tools=jsonb_set(tools,'{echo,hash}','"accepted-new-hash"'::jsonb) WHERE session_id=$1 AND connection_id=$2`, gh.sessionID, gh.connectionID); err != nil {
		t.Fatal(err)
	}
	before, err := db.MCPGrantByTokenHash(ctx, h.pool, hashToken(gh.token))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.pool.Exec(ctx, `UPDATE sessions SET status='starting' WHERE id=$1`, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = h.pool.QueryRow(ctx, `SELECT retained_grant FROM mcp_session_bindings WHERE session_id=$1 AND connection_id=$2`, gh.sessionID, gh.connectionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 7 {
		t.Fatal("retained profile exceeded non-secret field allowlist")
	}
	for key := range fields {
		switch key {
		case "name", "url", "tools", "preset", "known_tools", "call_budget", "calls_used":
		default:
			t.Fatal("unexpected retained profile field", key)
		}
	}
	var saved struct {
		Name       string                     `json:"name"`
		URL        string                     `json:"url"`
		Tools      map[string]db.MCPGrantTool `json:"tools"`
		CallsUsed  int                        `json:"calls_used"`
		CallBudget int                        `json:"call_budget"`
	}
	if json.Unmarshal(raw, &saved) != nil || saved.Name != before.Name || saved.URL != before.URLSnapshot || saved.CallsUsed != 1 || saved.CallBudget != before.CallBudget || saved.Tools["echo"].Hash != "accepted-new-hash" || len(saved.Tools) != len(before.Tools) {
		t.Fatal("detachment lost trusted pins or spent budget")
	}
	if _, err = db.MCPGrantByTokenHash(ctx, h.pool, hashToken(gh.token)); !errors.Is(err, db.ErrMCPGrantNotFound) {
		t.Fatal("retention revived the gateway token", err)
	}
}

func TestRetainedPinsAndBudgetOutliveCredentialRetention(t *testing.T) {
	for _, state := range []string{"done", "superseded"} {
		t.Run(state, func(t *testing.T) {
			h, gh, _, _ := newScopedGatewayHarness(t)
			ctx := context.Background()
			core := &lifecycleCapture{}
			if _, err := h.pool.Exec(ctx, `UPDATE mcp_session_bindings SET desired_action=$2,updated_at=now()-interval '192 hours' WHERE session_id=$1`, gh.sessionID, state); err != nil {
				t.Fatal(err)
			}
			if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_session_bindings WHERE session_id=$1`, gh.sessionID).Scan(&count); err != nil || count != 1 {
				t.Fatal("retained conversation lost trusted budget history", count, err)
			}
			if _, err := h.pool.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, gh.sessionID); err != nil {
				t.Fatal(err)
			}
			if state == "superseded" {
				// Deletion now retains a terminal obligation, even without a grant.
				if _, err := h.pool.Exec(ctx, `UPDATE mcp_session_bindings SET updated_at=now()-interval '192 hours' WHERE session_id=$1`, gh.sessionID); err != nil {
					t.Fatal(err)
				}
				work, err := db.ListMCPBindingWork(ctx, h.pool)
				if err != nil || len(work) != 1 || work[0].Action != "close" {
					t.Fatal("deleted conversation lost pending terminal cleanup", err)
				}
				if err = ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.pool.Exec(ctx, `UPDATE mcp_session_bindings SET updated_at=now()-interval '192 hours' WHERE session_id=$1`, gh.sessionID); err != nil {
				t.Fatal(err)
			}
			if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
				t.Fatal(err)
			}
			if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_session_bindings WHERE session_id=$1`, gh.sessionID).Scan(&count); err != nil || count != 0 {
				t.Fatal("deleted conversation retained completed delivery tombstone", count, err)
			}
		})
	}
}

// Contract fake: suspension consumes one generation, retries acknowledge that
// same suspended generation, and a stale close refuses until its fence is known.
type suspensionAckCore struct {
	generation              int64
	suspended, lost, erased bool
}

func (c *suspensionAckCore) SessionLifecycle(_ context.Context, in SessionDispatchRequest, action string) (int64, error) {
	switch action {
	case "renew":
		if c.suspended || in.CredentialGeneration != c.generation {
			return 0, ErrConnectionGone
		}
	case "suspend":
		if !c.suspended && in.CredentialGeneration == c.generation {
			c.suspended = true
			c.generation++
		}
		if !c.suspended || (in.CredentialGeneration != c.generation && in.CredentialGeneration+1 != c.generation) {
			return 0, ErrConnectionGone
		}
		if c.lost {
			c.lost = false
			return 0, errors.New("synthetic lost acknowledgment")
		}
	case "close":
		if in.CredentialGeneration != c.generation {
			return 0, ErrConnectionGone
		}
		c.erased = true
	}
	return c.generation, nil
}
func TestTerminalCloseRecoversMissedSuspensionFence(t *testing.T) {
	for _, mode := range []string{"lost ack", "sweep first", "resumed sweep first"} {
		t.Run(mode, func(t *testing.T) {
			h, gh, in, _ := newScopedGatewayHarness(t)
			ctx := context.Background()
			core := &suspensionAckCore{generation: in.CredentialGeneration}
			if mode == "lost ack" {
				core.lost = true
				if _, err := h.pool.Exec(ctx, `UPDATE sessions SET status='disconnected' WHERE id=$1`, gh.sessionID); err != nil {
					t.Fatal(err)
				}
				if err := ReconcileSessionBindings(ctx, h.pool, core, true); err == nil {
					t.Fatal("lost acknowledgment not observed")
				}
			} else {
				core.suspended = true
				core.generation++
				if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
					t.Fatal(err)
				}
				var action string
				if err := h.pool.QueryRow(ctx, `SELECT desired_action FROM mcp_session_bindings WHERE session_id=$1`, gh.sessionID).Scan(&action); err != nil || action != "superseded" {
					t.Fatal("fixture did not supersede stale renewal", action, err)
				}
				// A later runtime loss removes the local grant; terminal intent
				// must still reach the retained superseded control record.
				if _, err := h.pool.Exec(ctx, `UPDATE sessions SET status='disconnected' WHERE id=$1`, gh.sessionID); err != nil {
					t.Fatal(err)
				}

			}
			if mode == "resumed sweep first" {
				if _, err := h.pool.Exec(ctx, `UPDATE sessions SET status='starting',ended_at=NULL,end_reason=NULL WHERE id=$1`, gh.sessionID); err != nil {
					t.Fatal(err)
				}
				if _, err := h.pool.Exec(ctx, `UPDATE sessions SET status='running' WHERE id=$1`, gh.sessionID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.pool.Exec(ctx, `UPDATE sessions SET ended_at=now(),status='completed',end_reason='process_exited' WHERE id=$1`, gh.sessionID); err != nil {
				t.Fatal(err)
			}
			if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
				t.Fatal(err)
			}
			if !core.erased {
				t.Fatal("terminal close deferred to seven-day retention")
			}
		})
	}
}

func TestEndedSessionUpdateDoesNotRewriteTerminalFact(t *testing.T) {
	h, gh, _, _ := newScopedGatewayHarness(t)
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `UPDATE sessions SET ended_at=now(),status='error',end_reason='daemon_lost' WHERE id=$1`, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	work, err := db.ListMCPBindingWork(ctx, h.pool)
	if err != nil || len(work) != 1 {
		t.Fatal(err)
	}
	if err = db.CompleteMCPBindingWork(ctx, h.pool, work[0], false, work[0].ControlGeneration); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err = h.pool.QueryRow(ctx, `SELECT revision FROM mcp_session_bindings WHERE session_id=$1`, gh.sessionID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = h.pool.Exec(ctx, `UPDATE sessions SET status=status,ended_at=ended_at WHERE id=$1`, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	if err = h.pool.QueryRow(ctx, `SELECT revision FROM mcp_session_bindings WHERE session_id=$1`, gh.sessionID).Scan(&after); err != nil || before != after {
		t.Fatal("unchanged terminal session rewrote control intent", before, after, err)
	}
}

func TestTerminalRecoveryNeverClosesNewerActiveAttachment(t *testing.T) {
	h, gh, in, _ := newScopedGatewayHarness(t)
	ctx := context.Background()
	core := &suspensionAckCore{generation: in.CredentialGeneration + 1}
	if _, err := h.pool.Exec(ctx, `UPDATE sessions SET discard_requested_at=now() WHERE id=$1`, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
		t.Fatal(err)
	}
	if core.erased || core.suspended {
		t.Fatal("terminal recovery changed a newer active binding")
	}
}

func TestTerminalFactSurvivesFlappingBeforeReconciliation(t *testing.T) {
	h, gh, in, _ := newScopedGatewayHarness(t)
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `UPDATE sessions SET ended_at=now(),status='completed',end_reason='process_exited' WHERE id=$1`, gh.sessionID); err != nil {
		t.Fatal(err)
	}
	// Every end has a different timestamp, with no reconciliation between ends.
	for i := 0; i < 40; i++ {
		if _, err := h.pool.Exec(ctx, `UPDATE sessions SET status='starting',ended_at=NULL,end_reason=NULL WHERE id=$1`, gh.sessionID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.pool.Exec(ctx, `UPDATE sessions SET status='error',ended_at=clock_timestamp(),end_reason='daemon_lost' WHERE id=$1`, gh.sessionID); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := h.pool.QueryRow(ctx, `SELECT jsonb_array_length(terminal_record) FROM mcp_session_bindings WHERE session_id=$1`, gh.sessionID).Scan(&count); err != nil || count > 32 {
		t.Fatal("flapping created an unbounded terminal-fact backlog", count, err)
	}
	core := &suspensionAckCore{generation: in.CredentialGeneration}
	if err := ReconcileSessionBindings(ctx, h.pool, core, true); err != nil {
		t.Fatal(err)
	}
	if !core.erased {
		t.Fatal("later recoverable ends overwrote a terminal event")
	}
}
