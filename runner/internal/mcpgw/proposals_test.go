package mcpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const testRunnerURL = "https://runner.example"

func withSink(limit int, publicURL string) func(*Config) {
	return func(c *Config) {
		c.Sink = NewProposalSink(c.DB, publicURL, limit)
		c.CallTimeout = 2 * time.Second
	}
}

func proposalRows(t *testing.T, h *harness) []db.Proposal {
	t.Helper()
	rows, err := db.ListOwnedProposals(context.Background(), h.pool, "acct-1", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestProposeFreezesTheCallAndNeverReachesTheUpstream(t *testing.T) {
	h := newHarness(t, withSink(50, testRunnerURL))
	gh := h.grant("alpha", map[string]string{"echo": "allow", "send": "propose"}, 0)

	// A propose tool is listed to the agent exactly as an allow tool is.
	if names := toolNames(h.call(gh, "tools/list", map[string]any{})); strings.Join(names, ",") != "echo,send" {
		t.Fatalf("tools/list = %v, want echo and send", names)
	}
	_, _, callsBefore := h.up.snapshot()

	r := h.callTool(gh, "send", map[string]any{"to": "a@example.com", "body": "hi"})
	if r.result() == nil || r.result()["isError"] == true {
		t.Fatalf("propose must answer with a normal result: %s", r.raw)
	}
	if _, _, calls := h.up.snapshot(); calls != callsBefore {
		t.Fatal("a propose call must never reach the upstream")
	}
	rows := proposalRows(t, h)
	if len(rows) != 1 {
		t.Fatalf("%d proposals stored, want 1", len(rows))
	}
	p := rows[0]
	text := resultText(r)
	if !strings.Contains(text, p.ID) || !strings.Contains(text, testRunnerURL+"/proposals?id="+p.ID) {
		t.Errorf("the agent must get the id and the runner URL, got %q", text)
	}
	if p.State != db.ProposalPending || p.Tool != "send" || p.ConnectionName != "alpha" || p.ConnectionID != gh.connectionID {
		t.Errorf("row = %+v", p)
	}
	if p.SessionID != gh.sessionID || p.AccountID != "acct-1" {
		t.Errorf("session/account = %q / %q", p.SessionID, p.AccountID)
	}
	if p.URLSnapshot != h.up.url() {
		t.Errorf("url_snapshot = %q, want the grant's %q", p.URLSnapshot, h.up.url())
	}
	if p.ToolHash != h.up.hash("send") {
		t.Errorf("tool_hash = %q, want the pinned hash", p.ToolHash)
	}
	var args map[string]string
	if err := json.Unmarshal(p.Arguments, &args); err != nil || args["to"] != "a@example.com" || args["body"] != "hi" {
		t.Errorf("frozen arguments = %s (%v)", p.Arguments, err)
	}
	if !strings.Contains(p.AgentSummary, "send") || len(p.AgentSummary) > 400 {
		t.Errorf("summary = %q", p.AgentSummary)
	}
}

func TestProposeWithoutPublicURLReturnsTheIDOnly(t *testing.T) {
	h := newHarness(t, withSink(50, ""))
	gh := h.grant("alpha", map[string]string{"send": "propose"}, 0)
	r := h.callTool(gh, "send", map[string]any{})
	rows := proposalRows(t, h)
	if len(rows) != 1 || !strings.Contains(resultText(r), rows[0].ID) || strings.Contains(resultText(r), "http") {
		t.Fatalf("text = %q rows = %d", resultText(r), len(rows))
	}
	if string(rows[0].Arguments) != "{}" {
		t.Errorf("absent arguments freeze as {}, got %s", rows[0].Arguments)
	}
}

func TestProposePendingCapRefusesButStillCountsTowardTheBudget(t *testing.T) {
	h := newHarness(t, withSink(3, testRunnerURL))
	gh := h.grant("alpha", map[string]string{"send": "propose"}, 10)
	for i := range 3 {
		if r := h.callTool(gh, "send", map[string]any{"n": i}); r.result()["isError"] == true {
			t.Fatalf("proposal %d refused too early: %s", i, r.raw)
		}
	}
	used := func() int {
		var n int
		if err := h.pool.QueryRow(context.Background(), `SELECT calls_used FROM session_mcp_grants WHERE session_id = $1`, gh.sessionID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := used()
	r := h.callTool(gh, "send", map[string]any{"n": 99})
	if r.result()["isError"] != true || !strings.Contains(resultText(r), "pending") {
		t.Fatalf("over the cap the agent must get a clear tool error, got %s", r.raw)
	}
	if n := len(proposalRows(t, h)); n != 3 {
		t.Errorf("%d proposals stored, want 3", n)
	}
	if used() != before+1 {
		t.Errorf("a refused proposal must still count toward the call budget (%d -> %d)", before, used())
	}
}

func TestProposeArgumentsOverTheCapAreRefused(t *testing.T) {
	h := newHarness(t, withSink(50, testRunnerURL))
	gh := h.grant("alpha", map[string]string{"send": "propose"}, 0)
	r := h.callTool(gh, "send", map[string]any{"body": strings.Repeat("a", 70<<10)})
	if r.result()["isError"] != true || !strings.Contains(resultText(r), "64 KiB") {
		t.Fatalf("an oversized call must be refused with the limit named: %s", r.raw)
	}
	if n := len(proposalRows(t, h)); n != 0 {
		t.Errorf("nothing may be stored, got %d", n)
	}
	// Exactly at the cap is fine (the limit is on the frozen JSON).
	ok := h.callTool(gh, "send", map[string]any{"body": strings.Repeat("a", 60<<10)})
	if ok.result()["isError"] == true {
		t.Errorf("60 KiB must be accepted: %s", ok.raw)
	}
}

func TestProposeArgumentsTheDatabaseCannotStoreAreRefused(t *testing.T) {
	h := newHarness(t, withSink(50, testRunnerURL))
	gh := h.grant("alpha", map[string]string{"send": "propose"}, 0)
	// JSON allows \u0000 in a string; jsonb does not.
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": "send", "arguments": json.RawMessage(`{"x":"a\u0000b"}`)}})
	r := h.post(gh.token, gh.name, string(b), nil)
	if r.result()["isError"] != true {
		t.Fatalf("got %s", r.raw)
	}
	if n := len(proposalRows(t, h)); n != 0 {
		t.Errorf("stored %d", n)
	}
}

func TestSummaryIsBoundedAndPlain(t *testing.T) {
	args := json.RawMessage(`{"a":"` + strings.Repeat("x", 1000) + `","b":{"deep":1},"c":[1,2],"d":"line1\nline2","e":3}`)
	s := summarizeProposal("send", args)
	if len(s) > 300 || !strings.HasPrefix(s, "send") {
		t.Errorf("summary = %q (%d)", s, len(s))
	}
	if strings.ContainsAny(s, "\n\r") {
		t.Errorf("summary must be one line: %q", s)
	}
}

// ---- ExecuteApproved ---------------------------------------------------------------------

func approved(h *harness, tool string, args string) ApprovedCall {
	return ApprovedCall{
		Proof:        Proof{AccountID: "acct-1", SessionID: "login-approver"},
		ConnectionID: "c0c0c0c0-0000-4000-8000-000000000000",
		URLSnapshot:  h.up.url(),
		Tool:         tool, ToolHash: h.up.hash(tool),
		Arguments: json.RawMessage(args),
	}
}

func TestExecuteApprovedSendsExactlyTheFrozenArguments(t *testing.T) {
	h := newHarness(t, withSink(50, testRunnerURL))
	want := `{"to":"a@example.com","n":12345678901234567890,"nested":{"k":[1,2,3]}}`
	res, err := h.gw.ExecuteApproved(context.Background(), approved(h, "send", want))
	if err != nil {
		t.Fatalf("ExecuteApproved: %v", err)
	}
	_, _, calls := h.up.snapshot()
	if calls != 1 {
		t.Fatalf("upstream called %d times, want 1", calls)
	}
	var got, exp any
	h.up.mu.Lock()
	last := h.up.lastArgs
	h.up.mu.Unlock()
	d1 := json.NewDecoder(bytes.NewReader(last))
	d1.UseNumber()
	d2 := json.NewDecoder(strings.NewReader(want))
	d2.UseNumber()
	if err := d1.Decode(&got); err != nil {
		t.Fatal(err)
	}
	_ = d2.Decode(&exp)
	gb, _ := json.Marshal(got)
	eb, _ := json.Marshal(exp)
	if string(gb) != string(eb) {
		t.Errorf("upstream received %s, want %s", gb, eb)
	}
	if !strings.Contains(string(res.Result), "ok:send") || res.IsError {
		t.Errorf("result = %s (isError %v)", res.Result, res.IsError)
	}
	// The credential was fetched with the approver's login as proof.
	h.core.mu.Lock()
	proof := h.core.lastProof
	h.core.mu.Unlock()
	if proof.SessionID != "login-approver" || proof.AccountID != "acct-1" || proof.TokenID != "" {
		t.Errorf("proof = %+v", proof)
	}
}

func TestExecuteApprovedResultIsTextOnlyAndCapped(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxResultBytes = 100 })
	h.up.mu.Lock()
	h.up.callBody = func(string, json.RawMessage) any {
		return map[string]any{"content": []map[string]any{
			{"type": "image", "data": "AAAA", "mimeType": "image/png"},
			{"type": "text", "text": strings.Repeat("z", 500)},
		}}
	}
	h.up.mu.Unlock()
	res, err := h.gw.ExecuteApproved(context.Background(), approved(h, "send", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	s := string(res.Result)
	if strings.Contains(s, "AAAA") || !strings.Contains(s, "[image omitted]") || !strings.Contains(s, "truncated") || len(s) > 600 {
		t.Errorf("result = %s", s)
	}
}

func TestExecuteApprovedUpstreamToolErrorIsADefiniteFailure(t *testing.T) {
	h := newHarness(t, nil)
	h.up.mu.Lock()
	h.up.callBody = func(string, json.RawMessage) any {
		return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "nope"}}}
	}
	h.up.mu.Unlock()
	res, err := h.gw.ExecuteApproved(context.Background(), approved(h, "send", `{}`))
	if err != nil {
		t.Fatalf("a tool-reported error is a result, not a transport error: %v", err)
	}
	if !res.IsError {
		t.Errorf("IsError must be set: %s", res.Result)
	}
}

func TestExecuteApprovedPreflightMismatchesNeverCallTheTool(t *testing.T) {
	t.Run("url changed", func(t *testing.T) {
		h := newHarness(t, nil)
		c := approved(h, "send", `{}`)
		c.URLSnapshot = h.up.url() + "?x=1" // same host, different URL
		_, err := h.gw.ExecuteApproved(context.Background(), c)
		expectDefinite(t, h, err)
	})
	t.Run("host changed", func(t *testing.T) {
		h := newHarness(t, nil)
		c := approved(h, "send", `{}`)
		c.URLSnapshot = "http://127.0.0.1:1/mcp"
		_, err := h.gw.ExecuteApproved(context.Background(), c)
		expectDefinite(t, h, err)
	})
	t.Run("tool changed", func(t *testing.T) {
		h := newHarness(t, nil)
		c := approved(h, "send", `{}`)
		h.up.setDescription("send", "now it does something else")
		_, err := h.gw.ExecuteApproved(context.Background(), c)
		expectDefinite(t, h, err)
	})
	t.Run("tool gone", func(t *testing.T) {
		h := newHarness(t, nil)
		c := approved(h, "send", `{}`)
		c.Tool = "vanished"
		_, err := h.gw.ExecuteApproved(context.Background(), c)
		expectDefinite(t, h, err)
	})
	t.Run("connection deleted", func(t *testing.T) {
		h := newHarness(t, nil)
		h.core.err = ErrConnectionGone
		_, err := h.gw.ExecuteApproved(context.Background(), approved(h, "send", `{}`))
		if !errors.Is(err, ErrConnectionGone) {
			t.Errorf("err = %v, want ErrConnectionGone", err)
		}
		expectDefinite(t, h, err)
	})
}

func expectDefinite(t *testing.T, h *harness, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("ExecuteApproved succeeded, want a refusal")
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Errorf("nothing was sent, so the outcome is not unknown: %v", err)
	}
	if _, _, calls := h.up.snapshot(); calls != 0 {
		t.Errorf("the tool was called %d times", calls)
	}
}

func TestExecuteApprovedTimeoutAfterSendingIsUnknownAndNotRetried(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.CallTimeout = 400 * time.Millisecond })
	h.up.mu.Lock()
	h.up.gate = make(chan struct{}) // the tool call hangs
	h.up.mu.Unlock()
	t.Cleanup(func() { close(h.up.gate) })
	_, err := h.gw.ExecuteApproved(context.Background(), approved(h, "send", `{}`))
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, _, calls := h.up.snapshot(); calls != 1 {
		t.Errorf("the tool was called %d times: an unknown outcome must never be retried", calls)
	}
}

func TestExecuteApprovedConnectionDroppedAfterSendingIsUnknown(t *testing.T) {
	h := newHarness(t, nil)
	var delivered int
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"tools/call"`)) {
			delivered++
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.up.handle(w, r)
	}))
	t.Cleanup(wrapper.Close)
	h.core.url = wrapper.URL + "/mcp"
	c := approved(h, "send", `{}`)
	c.URLSnapshot = wrapper.URL + "/mcp"
	_, err := h.gw.ExecuteApproved(context.Background(), c)
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	if delivered != 1 {
		t.Errorf("delivered %d times, want exactly 1", delivered)
	}
}

func TestExecuteApprovedUnreachableUpstreamIsADefiniteFailure(t *testing.T) {
	h := newHarness(t, nil)
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL + "/mcp"
	dead.Close() // nothing listens: the connection is refused before anything is sent
	h.core.url = u
	c := approved(h, "send", `{}`)
	c.URLSnapshot = u
	_, err := h.gw.ExecuteApproved(context.Background(), c)
	if err == nil || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want a definite failure", err)
	}
}

func TestExecuteApprovedUpstreamRejectingTheCredentialIsDefinite(t *testing.T) {
	h := newHarness(t, nil)
	h.up.mu.Lock()
	h.up.status = http.StatusUnauthorized
	h.up.mu.Unlock()
	_, err := h.gw.ExecuteApproved(context.Background(), approved(h, "send", `{}`))
	if err == nil || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-1") {
		t.Error("the error must never carry the credential")
	}
}
