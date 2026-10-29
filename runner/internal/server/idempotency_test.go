package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/idempotency"
)

const wantKeyError = "idempotency key must be a single value of 1-128 characters using only A-Z, a-z, 0-9, '_', '.', ':' or '-'"

func TestIdempotencyKeyEndpoints(t *testing.T) {
	setups := map[string]func(*testing.T) (http.Handler, string, string, string){
		"start": func(t *testing.T) (http.Handler, string, string, string) {
			api, _ := desktopContractAPI(t)
			return agentContractMux(api), "/api/runner/start", runnerTestKey, `{"repo":"org/proj"}`
		},
		"children": func(t *testing.T) (http.Handler, string, string, string) {
			fx := newDelegFx(t)
			return fx.mux, "/api/sessions/" + fx.parent + "/children", fx.parentTok, `{"engine":"codex","repo":"app"}`
		},
		"marks": func(t *testing.T) (http.Handler, string, string, string) {
			fx := newTopicFx(t)
			return fx.mux, "/api/sessions/" + fx.sess + "/marks", fx.sessTok, `{"kind":"finding","text":"A finding"}`
		},
		"board-create": func(t *testing.T) (http.Handler, string, string, string) {
			fx, _ := newBoardCreateFx(t)
			return fx.mux, "/api/sessions/" + fx.sess + "/board-create", fx.sessTok, `{"name":"Launch","repos":["app"]}`
		},
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			mux, path, tok, body := setup(t)
			for _, keys := range [][]string{{" "}, {" key"}, {"key "}, {"key with spaces"}, {"\tkey"}, {"key\t"}, {""}, {strings.Repeat("k", 129)}, {"key@invalid"}, {"key/with/slash"}, {"key%3Aencoded"}, {"key+plus"}, {"key,comma"}, {"key\ttab"}, {"key\nnewline"}, {"key\x00nul"}, {"key\x7fdelete"}, {"key-é"}, {"first-key", "second-key"}} {
				t.Run(strings.Join(keys, "|"), func(t *testing.T) {
					req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					req.Header.Set("Authorization", "Bearer "+tok)
					req.Header["Idempotency-Key"] = keys
					rec := httptest.NewRecorder()
					mux.ServeHTTP(rec, req)
					var out map[string]any
					_ = json.Unmarshal(rec.Body.Bytes(), &out)
					if rec.Code != 400 || out["error"] != wantKeyError {
						t.Errorf("keys %q: %d %s; want 400 and %q", keys, rec.Code, rec.Body.String(), wantKeyError)
					}
				})
			}
		})
	}
}

func TestStartScopeEscapesParts(t *testing.T) {
	a := (runnerPrincipal{Kind: "agent", Sub: "one:two"}).startScope()
	b := (runnerPrincipal{Kind: "agent:one", Sub: "two"}).startScope()
	if a == b {
		t.Fatalf("different parts share scope %q", a)
	}
}

func TestIdempotencyScopeClaimsStaySeparate(t *testing.T) {
	pool := runnerContractPool(t)
	ctx := t.Context()
	// These tuples both joined to "marks:session:agent:owner:account" before.
	a := idempotency.Scope("marks", "session", "agent:owner", "account")
	b := idempotency.Scope("marks", "session:agent", "owner", "account")
	for i, scope := range []string{a, b} {
		sid := newUUID()
		_, claimed, err := db.ClaimIdempotencyKey(ctx, pool, scope, "same-key", "hash", sid, time.Hour)
		if err != nil || !claimed {
			t.Fatalf("scope %d: claimed=%v err=%v", i, claimed, err)
		}
		row, claimed, err := db.ClaimIdempotencyKey(ctx, pool, scope, "same-key", "hash", newUUID(), time.Hour)
		if err != nil || claimed || row == nil || row.SessionID != sid {
			t.Fatalf("scope %d replay: row=%+v claimed=%v err=%v", i, row, claimed, err)
		}
	}
	// Legacy claims must never be used as a fallback: their boundaries are ambiguous.
	if a == "marks:session:agent:owner:account" || b == "marks:session:agent:owner:account" {
		t.Fatal("scope aliases a legacy namespace")
	}
}

func TestStartSessionValidatesInternalKeys(t *testing.T) {
	api := &API{}
	for _, key := range []string{"key@invalid", "key/slash", "key\x00nul"} {
		_, err := api.StartSession(t.Context(), RunnerPrincipal{}, RunnerStartRequest{}, key)
		if err == nil || err.Status != http.StatusBadRequest || err.Message != wantKeyError {
			t.Errorf("key %q: %v", key, err)
		}
	}
}

func TestIdempotencyScopeNamespaces(t *testing.T) {
	for _, tc := range []struct {
		principal runnerPrincipal
		want      string
	}{
		{runnerPrincipal{Kind: runnerKeyPrincipalKind, Sub: "ignored"}, "v2|10:runner_key"},
		{runnerPrincipal{Kind: "agent"}, "v2|10:runner_key"},
		{runnerPrincipal{Kind: "human", Sub: "owner:one"}, "v2|5:human9:owner:one"},
		{runnerPrincipal{Kind: "agent", Sub: "owner"}, "v2|5:agent5:owner"},
	} {
		if got := tc.principal.startScope(); got != tc.want {
			t.Errorf("principal %+v: scope %q, want %q", tc.principal, got, tc.want)
		}
	}
	if got := delegateDecideScope("p:one"); got != "v2|23:session_delegate_decide5:p:one" {
		t.Errorf("approval scope: %q", got)
	}
}

func TestMarksUsesEncodedScope(t *testing.T) {
	fx := newTopicFx(t)
	owner := topicOwner + ":with:colon"
	if err := db.SetSessionSpawningAccount(t.Context(), fx.pool, fx.sess, owner); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []struct{ token, poster, owner string }{
		{fx.sessTok, "agent", ""}, {fx.token(owner, "human"), "owner", owner},
	} {
		body := map[string]any{"kind": "finding", "text": "Recorded finding"}
		code, out := fx.mark(fx.sess, actor.token, body, "same-key")
		if code != http.StatusCreated || out["recorded"] != float64(1) {
			t.Fatalf("first %s: %d %v", actor.poster, code, out)
		}
		scope := idempotency.Scope("marks", fx.sess, actor.poster, actor.owner)
		row, err := db.GetIdempotencyKey(t.Context(), fx.pool, scope, "same-key")
		if err != nil || row == nil || row.SessionID != fx.sess {
			t.Fatalf("claim for %s: %+v %v", actor.poster, row, err)
		}
		code, out = fx.mark(fx.sess, actor.token, body, "same-key")
		if code != http.StatusCreated || out["recorded"] != float64(0) || out["duplicates"] != float64(1) {
			t.Fatalf("retry %s: %d %v", actor.poster, code, out)
		}
	}
}
