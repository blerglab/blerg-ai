package api

import (
	"net/http/httptest"
	"testing"
)

func TestLiveDataTerminalRefusalSignal(t *testing.T) {
	for _, cause := range []string{"decision_revoked", "reconcile_unavailable"} {
		rec := httptest.NewRecorder()
		Deps{}.liveFail(rec, httptest.NewRequest("POST", "/internal/live-data/confirm", nil), 409, cause, "approve")
		want := ""
		if cause == "decision_revoked" {
			want = cause
		}
		if got := rec.Header().Get("X-Blerg-Live-Data-Cause"); got != want {
			t.Fatalf("cause header %q, want %q", got, want)
		}
	}
}
