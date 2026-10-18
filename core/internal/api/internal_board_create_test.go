package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
)

const fixtureCreateIntent = "12345678-1234-4321-8123-123456789abc"

func createProof(e *mcpEnv) map[string]any {
	return map[string]any{"account_id": e.acctA, "session_id": e.sidA, "board_id": "b1", "intent": fixtureCreateIntent, "digest": strings.Repeat("a", 64)}
}
func createExchange(t *testing.T, e *mcpEnv, body map[string]any) (int, string, http.Header) {
	t.Helper()
	return e.internal(t, "/internal/tokens/exchange/board-create", testInternalKey, body)
}

func TestApprovedBoardCreateExchangeOnlySignsOneIntentAndScopedDigest(t *testing.T) {
	var e *mcpEnv
	var probeCalls atomic.Int32
	e = boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) {
		probeCalls.Add(1)
		if r.URL.Path != "/api/boards/b1" {
			t.Error("probe wrong target")
		}
		p, err := e.verifyBoard(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || p.Project != "b1" || len(p.Caps) != 1 || p.Caps[0] != "card.read" || p.ExpiresAt-p.IssuedAt > 30 {
			t.Error("probe must be narrowly scoped read credential")
		}
		_, _ = w.Write([]byte(`{"id":"b1"}`))
	})
	code, out, headers := createExchange(t, e, createProof(e))
	if code != 200 {
		t.Fatalf("approved exchange status=%d", code)
	}
	var response exchangeResp
	if json.Unmarshal([]byte(out), &response) != nil || response.Token == "" {
		t.Fatal("missing signed create credential")
	}
	p, err := e.verifyBoard(t, response.Token)
	if err != nil {
		t.Fatal("create credential verification failed")
	}
	intent, digest, ok := boardfiling.ParseCapability(p.Caps)
	if !ok || intent != fixtureCreateIntent || digest != strings.Repeat("a", 64) || p.Sub != fixtureCreateIntent || p.Project != "b1" || p.OnBehalfOf != e.acctA || p.Lineage != e.acctA || p.ExpiresAt-p.IssuedAt > 30 {
		t.Fatal("create credential authority exceeds approved intent")
	}
	if headers.Get("Cache-Control") != "no-store" || probeCalls.Load() != 1 {
		t.Fatal("missing cache protection or scoped access check")
	}
	var persisted int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_tokens WHERE id=$1`, fixtureCreateIntent).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 0 {
		t.Fatal("one-shot credential became an exchangeable agent identity")
	}
}

func TestApprovedBoardCreateExchangeRejectsNonHumanMalformedAndForeignProofs(t *testing.T) {
	var calls atomic.Int32
	e := boardAccessEnv(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	for _, change := range []map[string]any{{"session_id": "", "token_id": "11111111-1111-1111-1111-111111111111"}, {"session_id": e.sidB}, {"intent": "bad"}, {"digest": strings.Repeat("A", 64)}, {"board_id": "../escape"}, {"account_id": "bad"}} {
		body := createProof(e)
		for k, v := range change {
			body[k] = v
		}
		code, out, _ := createExchange(t, e, body)
		if code != 400 && code != 404 {
			t.Fatalf("invalid proof status=%d", code)
		}
		if strings.Contains(out, "eyJ") {
			t.Fatal("invalid proof received credential")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid proof reached Board")
	}
}

func TestApprovedBoardCreateExchangeRefusesBoardAndMidflightRevocation(t *testing.T) {
	for _, mode := range []string{"denied", "wrong-id", "redirect", "malformed", "revoke-human", "revoke-role"} {
		t.Run(mode, func(t *testing.T) {
			var e *mcpEnv
			var calls atomic.Int32
			e = boardAccessEnv(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "denied":
					w.WriteHeader(403)
					return
				case "wrong-id":
					_, _ = w.Write([]byte(`{"id":"other"}`))
					return
				case "redirect":
					w.Header().Set("Location", "/leak")
					w.WriteHeader(302)
					return
				case "malformed":
					_, _ = w.Write([]byte(`{`))
					return
				case "revoke-human":
					if _, err := e.pool.Exec(t.Context(), `UPDATE human_sessions SET revoked_at=now() WHERE chain_id=$1`, e.sidA); err != nil {
						t.Error(err)
					}
				case "revoke-role":
					if _, err := e.pool.Exec(t.Context(), `UPDATE accounts SET role='observer' WHERE id=$1`, e.acctA); err != nil {
						t.Error(err)
					}
				}
				_, _ = w.Write([]byte(`{"id":"b1"}`))
			})
			code, out, _ := createExchange(t, e, createProof(e))
			if code != 404 && code != 503 {
				t.Fatalf("refused exchange status=%d", code)
			}
			if strings.Contains(out, "eyJ") {
				t.Fatal("refused exchange leaked credential")
			}
			if calls.Load() != 1 {
				t.Fatal("probe followed redirect or repeated")
			}
		})
	}
}

func TestApprovedBoardCreateExchangeGatesCurrentHumanWriteAuthority(t *testing.T) {
	for _, mode := range []string{"disabled", "password-change", "read-only-role", "revoked", "expired"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			e := boardAccessEnv(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
			sql := map[string]string{"disabled": `UPDATE accounts SET disabled_at=now() WHERE id=$1`, "password-change": `UPDATE accounts SET must_change_password=true WHERE id=$1`, "read-only-role": `UPDATE accounts SET role='observer' WHERE id=$1`, "revoked": `UPDATE human_sessions SET revoked_at=now() WHERE account_id=$1`, "expired": `UPDATE human_sessions SET expires_at=now()-interval '1 minute' WHERE account_id=$1`}[mode]
			if _, err := e.pool.Exec(t.Context(), sql, e.acctA); err != nil {
				t.Fatal(err)
			}
			code, _, _ := createExchange(t, e, createProof(e))
			if code != 404 || calls.Load() != 0 {
				t.Fatal("ineligible human approval reached Board or minted a credential")
			}
		})
	}
}

func TestBoardReadOnlyExchangeDoesNotMintGeneralWriteAuthority(t *testing.T) {
	for _, mode := range []string{"human", "agent-read", "agent-session-start", "agent-write-only"} {
		t.Run(mode, func(t *testing.T) {
			e := newExchangeEnv(t)
			proof := map[string]any{"account_id": e.acctA, "target": "board", "board_id": "b1", "read_only": true}
			if mode == "human" {
				proof["session_id"] = e.sidA
			} else {
				record, _, err := e.idSvc.CreateAgentToken(t.Context(), e.acctA, "fixture-read-proof", "board", 0)
				if err != nil {
					t.Fatal(err)
				}
				caps := map[string][]string{"agent-read": {"card.read"}, "agent-session-start": {"session.start"}, "agent-write-only": {"card.write"}}[mode]
				if _, err := e.pool.Exec(t.Context(), `UPDATE agent_tokens SET caps=$2 WHERE id=$1`, record.ID, caps); err != nil {
					t.Fatal(err)
				}
				proof["token_id"] = record.ID
			}
			code, out := e.exchange(t, proof)
			if mode == "agent-write-only" {
				if code != 403 {
					t.Fatalf("readless proof returned %d", code)
				}
				return
			}
			if code != 200 {
				t.Fatalf("read-only exchange status=%d", code)
			}
			var response exchangeResp
			if json.Unmarshal([]byte(out), &response) != nil || response.Token == "" {
				t.Fatal("missing read-only credential")
			}
			p, err := e.verifyBoard(t, response.Token)
			if err != nil || len(p.Caps) != 1 || p.Caps[0] != "card.read" {
				t.Fatal("read-only exchange granted extra authority")
			}
		})
	}
}
