package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/core/internal/api"
)

func newBoardProof(e *mcpEnv) map[string]any {
	return map[string]any{"account_id": e.acctA, "session_id": e.sidA, "intent": fixtureCreateIntent, "digest": boardfiling.BoardDigest(boardfiling.BoardDraft{Name: "Launch"})}
}

func makeAdmin(t *testing.T, e *mcpEnv, account string) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), `UPDATE accounts SET role='admin' WHERE id=$1`, account); err != nil {
		t.Fatal(err)
	}
}

func TestNewBoardExchangeSignsOneBoardForALiveAdministrator(t *testing.T) {
	var calls atomic.Int32
	e := boardAccessEnv(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	makeAdmin(t, e, e.acctA)
	code, out, headers := e.internal(t, "/internal/tokens/exchange/board-new", testInternalKey, newBoardProof(e))
	if code != 200 {
		t.Fatalf("status=%d %s", code, out)
	}
	var response exchangeResp
	if json.Unmarshal([]byte(out), &response) != nil || response.Token == "" {
		t.Fatal("missing credential")
	}
	p, err := e.verifyBoard(t, response.Token)
	if err != nil {
		t.Fatal(err)
	}
	intent, digest, ok := boardfiling.ParseBoardCapability(p.Caps)
	if !ok || intent != fixtureCreateIntent || digest != newBoardProof(e)["digest"] || p.Sub != fixtureCreateIntent || p.Project != "" ||
		p.OnBehalfOf != e.acctA || p.Lineage != e.acctA || p.ExpiresAt-p.IssuedAt > 30 || len(p.Caps) != 1 {
		t.Fatalf("credential exceeds the approved board: %+v", p)
	}
	if headers.Get("Cache-Control") != "no-store" || calls.Load() != 0 {
		t.Fatal("missing cache protection, or Board was contacted")
	}
}

func TestNewBoardExchangeRefusesMembersAndInvalidProofs(t *testing.T) {
	e := boardAccessEnv(t, func(http.ResponseWriter, *http.Request) {})
	// A member (no board.admin) cannot create boards, so cannot approve one either.
	if code, out, _ := e.internal(t, "/internal/tokens/exchange/board-new", testInternalKey, newBoardProof(e)); code != 403 || strings.Contains(out, "eyJ") {
		t.Fatalf("member status=%d", code)
	}
	makeAdmin(t, e, e.acctA)
	for _, change := range []map[string]any{{"session_id": "", "token_id": "11111111-1111-1111-1111-111111111111"}, {"session_id": e.sidB}, {"intent": "bad"}, {"digest": strings.Repeat("A", 64)}, {"account_id": "bad"}} {
		body := newBoardProof(e)
		for k, v := range change {
			body[k] = v
		}
		code, out, _ := e.internal(t, "/internal/tokens/exchange/board-new", testInternalKey, body)
		if code != 400 && code != 404 {
			t.Fatalf("invalid proof %v status=%d", change, code)
		}
		if strings.Contains(out, "eyJ") {
			t.Fatal("invalid proof received a credential")
		}
	}
	if code, _, _ := e.internal(t, "/internal/tokens/exchange/board-new", "wrong-key", newBoardProof(e)); code == 200 {
		t.Fatal("wrong internal key accepted")
	}
	for _, sql := range []string{`UPDATE accounts SET must_change_password=true WHERE id=$1`, `UPDATE human_sessions SET revoked_at=now() WHERE account_id=$1`} {
		if _, err := e.pool.Exec(t.Context(), sql, e.acctA); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := e.internal(t, "/internal/tokens/exchange/board-new", testInternalKey, newBoardProof(e)); code != 404 {
			t.Fatalf("ineligible approval status=%d", code)
		}
	}
}

// The second liveness and role check, after the mint, stops an approval whose sign-in ended or whose
// role changed while the credential was being minted.
func TestNewBoardExchangeRechecksAfterMinting(t *testing.T) {
	for _, mode := range []string{"signed-out", "demoted"} {
		t.Run(mode, func(t *testing.T) {
			e := boardAccessEnv(t, func(http.ResponseWriter, *http.Request) {})
			makeAdmin(t, e, e.acctA)
			sql := map[string]string{"signed-out": `UPDATE human_sessions SET revoked_at=now() WHERE account_id=$1`, "demoted": `UPDATE accounts SET role='member' WHERE id=$1`}[mode]
			defer api.SetNewBoardBeforeRecheck(func() {
				if _, err := e.pool.Exec(t.Context(), sql, e.acctA); err != nil {
					t.Error(err)
				}
			})()
			code, out, _ := e.internal(t, "/internal/tokens/exchange/board-new", testInternalKey, newBoardProof(e))
			if code == 200 || strings.Contains(out, "eyJ") {
				t.Fatalf("%s mid-mint: status=%d", mode, code)
			}
			want := map[string]int{"signed-out": 404, "demoted": 403}[mode]
			if code != want {
				t.Fatalf("%s: status=%d, want %d", mode, code, want)
			}
		})
	}
}

func TestNewBoardExchangeSaysNotLiveInJSON(t *testing.T) {
	e := boardAccessEnv(t, func(http.ResponseWriter, *http.Request) {})
	makeAdmin(t, e, e.acctA)
	if _, err := e.pool.Exec(t.Context(), `UPDATE human_sessions SET revoked_at=now() WHERE account_id=$1`, e.acctA); err != nil {
		t.Fatal(err)
	}
	code, out, _ := e.internal(t, "/internal/tokens/exchange/board-new", testInternalKey, newBoardProof(e))
	if code != 404 || !strings.Contains(out, `"not_live"`) {
		t.Fatalf("not live = %d %s", code, out)
	}
}
