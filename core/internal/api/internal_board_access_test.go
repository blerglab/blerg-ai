package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/discovery"
)

func boardAccessEnv(t *testing.T, handler http.HandlerFunc) *mcpEnv {
	t.Helper()
	e := newTokEnv(t)
	board := httptest.NewServer(handler)
	t.Cleanup(board.Close)
	e.deps.Registry = discovery.NewRegistry(e.deps.Store, time.Minute)
	if err := e.deps.Registry.Register(context.Background(), agentsmanifest.ComponentEntry{Name: "blerg-board", BaseURL: board.URL}); err != nil {
		t.Fatal(err)
	}
	e.srv.Close()
	e.srv = httptest.NewServer(api.NewRouter(e.deps))
	t.Cleanup(e.srv.Close)
	return e
}

func newExchangeEnv(t *testing.T) *mcpEnv {
	return boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": strings.TrimPrefix(r.URL.Path, "/api/boards/")})
	})
}

func TestBoardDiscoveryReadOnlyProbeAndNoCredential(t *testing.T) {
	var e *mcpEnv
	e = boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/boards" {
			t.Error("wrong board route")
		}
		p, err := e.verifyBoard(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || p.Project != "" || len(p.Caps) != 1 || p.Caps[0] != "card.read" || p.OnBehalfOf != e.acctA || p.ExpiresAt-p.IssuedAt > 30 {
			t.Error("invalid read probe")
		}
		_, _ = w.Write([]byte(`[{"id":"b1","name":"Work","sensitive":"omitted"}]`))
	})
	code, out, _ := e.internal(t, "/internal/boards/list", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA})
	if code != 200 || out != "{\"boards\":[{\"id\":\"b1\",\"name\":\"Work\"}]}\n" {
		t.Fatalf("list = %d %s", code, out)
	}
}

func TestBoardExchangeVerificationFailuresRevoke(t *testing.T) {
	for _, mode := range []string{"denied", "wrong-id", "redirect", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			var received atomic.Int32
			var raw atomic.Value
			e := boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				raw.Store(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
				switch mode {
				case "denied":
					w.WriteHeader(403)
				case "wrong-id":
					_, _ = w.Write([]byte(`{"id":"other"}`))
				case "redirect":
					w.Header().Set("Location", "/leak")
					w.WriteHeader(302)
				case "malformed":
					_, _ = w.Write([]byte(`{`))
				}
			})
			code, out := e.exchange(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": "b1"})
			if code != 404 && code != 503 {
				t.Fatalf("failure = %d", code)
			}
			if received.Load() != 1 {
				t.Fatal("redirect followed")
			}
			if strings.Contains(out, "eyJ") {
				t.Fatal("credential leaked")
			}
			if _, err := e.verifyBoard(t, raw.Load().(string)); err == nil {
				t.Fatal("refused exchange remains usable")
			}
		})
	}
}

func TestBoardDiscoveryRefusesBadProofBeforeBoard(t *testing.T) {
	var calls atomic.Int32
	e := boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, proof := range []map[string]any{{"account_id": e.acctA, "session_id": e.sidB}, {"account_id": e.acctA, "token_id": "11111111-1111-1111-1111-111111111111"}} {
		code, _, _ := e.internal(t, "/internal/boards/list", testInternalKey, proof)
		if code != 404 {
			t.Fatalf("invalid proof = %d", code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("Board contacted before proof authorization")
	}
}

func TestBoardExchangeWithoutRegistryFailsClosed(t *testing.T) {
	e := newTokEnv(t)
	code, out := e.exchange(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": "b1"})
	if code != 503 || strings.Contains(out, "eyJ") {
		t.Fatalf("unconfigured = %d", code)
	}
	var active int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_tokens WHERE kind='exchange' AND revoked_at IS NULL`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("unconfigured board left active exchange credential")
	}

}

func TestBoardDiscoveryRejectsMalformedAndRedirectedResponses(t *testing.T) {
	for name, body := range map[string]string{"pathID": `[{"id":"../x","name":"Work"}]`, "duplicate": `[{"id":"b1","name":"Work"},{"id":"b1","name":"Work"}]`, "nameControl": `[{"id":"b1","name":"a\nb"}]`, "oversized": strings.Repeat(" ", 1<<20) + `[]`, "trailing": `[] {}`} {
		t.Run(name, func(t *testing.T) {
			e := boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
			code, out, _ := e.internal(t, "/internal/boards/list", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA})
			if code != 503 || strings.Contains(out, "eyJ") {
				t.Fatalf("malformed response = %d", code)
			}
		})
	}
}

func TestBoardDiscoveryHonoursProofCapsAndAccountRestrictions(t *testing.T) {
	for _, mode := range []string{"no-caps", "disabled", "must-change-password", "session-start"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			e := boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write([]byte(`[]`)) })
			proof := map[string]any{"account_id": e.acctA, "session_id": e.sidA}
			want := 404
			if mode == "no-caps" || mode == "session-start" {
				rec, _, err := e.idSvc.CreateAgentToken(t.Context(), e.acctA, "proof", "run-sessions", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "no-caps" {
					if _, err := e.pool.Exec(t.Context(), `UPDATE agent_tokens SET caps='{}' WHERE id=$1`, rec.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					want = 200
				}
				proof = map[string]any{"account_id": e.acctA, "token_id": rec.ID}
			} else {
				sql := `UPDATE accounts SET disabled_at=now() WHERE id=$1`
				if mode == "must-change-password" {
					sql = `UPDATE accounts SET must_change_password=true WHERE id=$1`
				}
				if _, err := e.pool.Exec(t.Context(), sql, e.acctA); err != nil {
					t.Fatal(err)
				}
			}
			code, _, _ := e.internal(t, "/internal/boards/list", testInternalKey, proof)
			if code != want {
				t.Fatalf("%s = %d, want %d", mode, code, want)
			}
			if want != 200 && calls.Load() != 0 {
				t.Fatal("Board contacted for forbidden proof")
			}
		})
	}
}

func TestBoardDiscoveryRechecksLivenessAfterFetch(t *testing.T) {
	var e *mcpEnv
	e = boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if _, err := e.pool.Exec(t.Context(), `UPDATE human_sessions SET revoked_at=now() WHERE chain_id=$1`, e.sidA); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`[{"id":"b1","name":"Private"}]`))
	})
	code, out, _ := e.internal(t, "/internal/boards/list", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA})
	if code != 404 || strings.Contains(out, "Private") {
		t.Fatalf("revoked-during-fetch = %d", code)
	}
}

func TestBoardExchangeRequiresPasswordRotation(t *testing.T) {
	var calls atomic.Int32
	e := boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	if _, err := e.pool.Exec(t.Context(), `UPDATE accounts SET must_change_password=true WHERE id=$1`, e.acctA); err != nil {
		t.Fatal(err)
	}
	before := e.countTokens(t, e.acctA)
	code, out := e.exchange(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": "b1"})
	if code != 404 || calls.Load() != 0 || e.countTokens(t, e.acctA) != before || strings.Contains(out, "eyJ") {
		t.Fatalf("password-rotation refusal = %d", code)
	}
}

func TestBoardExchangeVerifiesScopedCredential(t *testing.T) {
	var e *mcpEnv
	e = boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) {
		p, err := e.verifyBoard(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || p.Project != "b1" || p.OnBehalfOf != e.acctA || p.Lineage != e.acctA || r.URL.Path != "/api/boards/b1" {
			t.Error("verification did not use selected board's scoped credential")
		}
		_, _ = w.Write([]byte(`{"id":"b1"}`))
	})
	e.exchangeOK(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": "b1"})
}

func TestBoardExchangeRechecksLivenessAfterVerification(t *testing.T) {
	var e *mcpEnv
	var raw atomic.Value
	e = boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) {
		raw.Store(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if _, err := e.pool.Exec(t.Context(), `UPDATE human_sessions SET revoked_at=now() WHERE chain_id=$1`, e.sidA); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"id":"b1"}`))
	})
	code, out := e.exchange(t, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "target": "board", "board_id": "b1"})
	if code != 404 || strings.Contains(out, "eyJ") {
		t.Fatalf("revoked-during-verification = %d", code)
	}
	if _, err := e.verifyBoard(t, raw.Load().(string)); err == nil {
		t.Fatal("revoked proof left active scoped token")
	}
}
