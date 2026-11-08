package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
	"strings"
	"testing"
)

func TestClearTaskSessionScope(t *testing.T) {
	fx := newRenameFx(t)
	other, _ := fx.session(sessOpts{owner: "owner-1"})
	for _, sid := range []string{fx.parent, other} {
		if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, sid, newUUID(), "background_task_done", `{"task_id":"task","status":"failed"}`); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		sid  string
		want int
	}{{other, http.StatusForbidden}, {fx.parent, http.StatusOK}} {
		code, out, _ := fx.do(http.MethodPost, "/api/sessions/"+tc.sid+"/tasks/task/clear", fx.parentTok, map[string]string{}, nil)
		if code != tc.want {
			t.Fatalf("status %d want %d: %v", code, tc.want, out)
		}
	}
	code, out, _ := fx.do(http.MethodGet, "/api/sessions/"+fx.parent+"/topics", fx.human("owner-1"), nil, nil)
	raw, _ := json.Marshal(out)
	if code != http.StatusOK || !strings.Contains(string(raw), "Background command failed") {
		t.Fatalf("history lost command: %d %s", code, raw)
	}
	var n int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='background_task_cleared'`, other).Scan(&n); err != nil || n != 0 {
		t.Fatalf("foreign clears %d: %v", n, err)
	}
}
