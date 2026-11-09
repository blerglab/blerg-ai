package server

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestSessionEmojiValidation(t *testing.T) {
	for _, emoji := range []string{"", "🔎", "👩🏽‍💻", "🇺🇸", "1️⃣", "#️⃣", "❤️", "❤", "🏴‍☠️"} {
		if !sessionEmojis[emoji] {
			t.Errorf("refused %q", emoji)
		}
	}
	for _, emoji := range []string{"a", "1", "🔎🔎", " 🔎", "🔎\n", "🔎\u202e", "\u2066🔎", "🔎\x00", "🏽", "🔎‍🔎"} {
		if sessionEmojis[emoji] {
			t.Errorf("accepted %q", emoji)
		}
	}
}

// Exercise the shipped CLI against the authenticated HTTP route, then the same
// database snapshot used by the sidebar, including a human edit and clear.
func TestSessionEmojiCLIToSidebar(t *testing.T) {
	fx := newRenameFx(t)
	server := httptest.NewServer(fx.mux)
	defer server.Close()
	cmd := exec.CommandContext(t.Context(), "python3", "../../.claude/skills/session-messaging/blerg-runner", "title", "--emoji", "👩🏽‍💻")
	cmd.Env = append(os.Environ(), "BLERG_RUNNER_SERVER_HTTP="+server.URL, "BLERG_RUNNER_SESSION_ID="+fx.parent, "BLERG_RUNNER_SESSION_TOKEN="+fx.parentTok)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("CLI: %v %s", err, out)
	}
	row, err := db.GetSession(t.Context(), fx.pool, fx.parent)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRowToInfo(*row, "owner-1").Emoji; got != "👩🏽‍💻" {
		t.Fatalf("sidebar emoji %q", got)
	}
	rows, err := db.ListSessions(t.Context(), fx.pool)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, listed := range rows {
		if listed.ID == fx.parent {
			found = listed.Emoji == "👩🏽‍💻"
		}
	}
	if !found {
		t.Fatal("session list lost emoji")
	}
	for _, value := range []string{"🔎", ""} {
		code, body, _ := fx.do("PATCH", "/api/sessions/"+fx.parent, fx.human("owner-1"), map[string]any{"emoji": value}, nil)
		if code != 204 {
			t.Fatalf("edit: %d %v", code, body)
		}
	}
	for _, value := range []string{"x", "🔎🔎", "🔎\u202e", "🔎\x00"} {
		code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/title", fx.parentTok, map[string]any{"emoji": value}, nil)
		if code != 422 {
			t.Fatalf("invalid emoji: %d", code)
		}
	}
	other, token := fx.session(sessOpts{owner: "owner-2"})
	for _, req := range []struct{ method, id, token string }{{"PATCH", fx.parent, fx.human("owner-2")}, {"POST", other, fx.parentTok}, {"POST", fx.parent, token}} {
		path := "/api/sessions/" + req.id
		if req.method == "POST" {
			path += "/title"
		}
		code, _, _ := fx.do(req.method, path, req.token, map[string]any{"emoji": "🔎"}, nil)
		if code < 400 {
			t.Fatalf("unauthorized edit: %d", code)
		}
	}
}
