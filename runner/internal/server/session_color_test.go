package server

import (
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestSessionColorPersistenceAndAuthorization(t *testing.T) {
	fx := newRenameFx(t)
	route := "/api/sessions/" + fx.parent
	for _, color := range []string{"blue", "orange", "purple", "teal", "silver", "indigo", "gold", "coral", "cyan", "violet", "lime", "rose", "", "purple"} {
		code, out, _ := fx.do(http.MethodPatch, route, fx.human("owner-1"), map[string]any{"color": color}, nil)
		if code != 204 {
			t.Fatalf("set %q: %d %v", color, code, out)
		}
		if got := fx.row(fx.parent).Color; got != color {
			t.Fatalf("saved %q want %q", got, color)
		}
	}
	rows, err := db.ListSessions(t.Context(), fx.pool)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ID == fx.parent {
			found = true
			if row.Color != "purple" {
				t.Fatalf("reload color %q", row.Color)
			}
		}
	}
	if !found {
		t.Fatal("missing session on reload")
	}
	code, _, _ := fx.do(http.MethodPatch, route, fx.human("owner-1"), map[string]any{"color": "#ff0000"}, nil)
	if code != 422 {
		t.Fatalf("invalid color: %d", code)
	}
	code, _, _ = fx.do(http.MethodPatch, route, fx.human("other"), map[string]any{"color": "blue"}, nil)
	if code != 403 {
		t.Fatalf("nonowner: %d", code)
	}
}
