package api_test

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"net/http"
	"testing"
)

func TestMetadataExchangeOnlySignsScopedPatchForLiveAdmin(t *testing.T) {
	e := boardAccessEnv(t, func(http.ResponseWriter, *http.Request) {})
	body := newBoardProof(e)
	body["board_id"] = "11111111-1111-4111-8111-111111111111"
	route := "/internal/tokens/exchange/board-metadata"
	if code, _, _ := e.internal(t, route, testInternalKey, body); code != 403 {
		t.Fatalf("member=%d", code)
	}
	makeAdmin(t, e, e.acctA)
	code, out, _ := e.internal(t, route, testInternalKey, body)
	if code != 200 {
		t.Fatalf("admin=%d", code)
	}
	var response exchangeResp
	if json.Unmarshal([]byte(out), &response) != nil {
		t.Fatal("response")
	}
	claims, err := e.verifyBoard(t, response.Token)
	if err != nil {
		t.Fatal(err)
	}
	intent, digest, ok := boardfiling.ParseMetadataCapability(claims.Caps)
	if !ok || intent != fixtureCreateIntent || digest != body["digest"] || claims.Project != body["board_id"] || claims.OnBehalfOf != e.acctA || claims.ExpiresAt-claims.IssuedAt > 30 {
		t.Fatal("broader authority")
	}
	for _, change := range []map[string]any{{"session_id": e.sidB}, {"token_id": fixtureCreateIntent}, {"board_id": "invalid"}, {"digest": "invalid"}} {
		bad := newBoardProof(e)
		bad["board_id"] = body["board_id"]
		for k, v := range change {
			bad[k] = v
		}
		if code, _, _ := e.internal(t, route, testInternalKey, bad); code != 400 && code != 404 {
			t.Fatalf("invalid proof=%d", code)
		}
	}
	restore := api.SetNewBoardBeforeRecheck(func() {
		if _, err := e.pool.Exec(t.Context(), `UPDATE human_sessions SET revoked_at=now() WHERE account_id=$1`, e.acctA); err != nil {
			t.Fatal(err)
		}
	})
	defer restore()
	if code, _, _ := e.internal(t, route, testInternalKey, body); code != 404 {
		t.Fatalf("midflight logout=%d", code)
	}
}
