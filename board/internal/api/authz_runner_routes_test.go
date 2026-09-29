package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// A board-scoped token for board A must not read another board's runner
// sessions or its active-sessions summary, read a session's events, or
// inject a message/interrupt into a session that belongs to another board
// (task-3b brief: same defect class as Task 3's C1/M5, over the runner
// routes runnerRoutes registers with no capability or board-scope check).
func TestRunnerRoutesRequireBoardScope(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	admin := adminToken(t)
	member := memberToken(t)
	readOnly := testHumanToken(t, []string{"card.read"})

	var boardA, boardB db.Board
	r := request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "runner-a", "repos": []string{"blerg-board"}})
	decodeBody(t, r, &boardA)
	r = request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "runner-b"})
	decodeBody(t, r, &boardB)

	cardRes, err := db.CreateCard(ctx, pool, boardA.ID, db.CardParams{
		Title: strPtr("spawn me"), Repos: &[]string{"blerg-board"},
	}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	card := cardRes.Card

	// Spawn a real runner session on A's card (as admin, so it always
	// succeeds regardless of what's under test below).
	var spawned struct {
		RunnerSessionID string `json:"runner_session_id"`
	}
	resp := request(t, srv, "POST", "/api/cards/"+card.ID+"/spawn", admin, nil, map[string]any{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("admin spawn = %d", resp.StatusCode)
	}
	decodeBody(t, resp, &spawned)
	rsID := spawned.RunnerSessionID
	if rsID == "" {
		t.Fatal("no runner_session_id returned from spawn")
	}

	// A native token scoped to board B only.
	var mintedB struct {
		Secret string `json:"secret"`
	}
	r = request(t, srv, "POST", "/api/tokens", admin, nil, map[string]any{
		"label": "b-only", "board_id": boardB.ID, "capabilities": []string{"card.read", "card.write"},
	})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("mint scoped-to-B = %d", r.StatusCode)
	}
	decodeBody(t, r, &mintedB)

	type route struct {
		name, method, path string
		body               any
		okStatus           int
	}
	routes := []route{
		{"card-sessions", "GET", "/api/cards/" + card.ID + "/runner-sessions", nil, http.StatusOK},
		{"active-sessions", "GET", "/api/boards/" + boardA.ID + "/active-sessions", nil, http.StatusOK},
		{"events", "GET", "/api/runner-sessions/" + rsID + "/events", nil, http.StatusOK},
		{"message", "POST", "/api/runner-sessions/" + rsID + "/message", map[string]any{"text": "hi"}, http.StatusAccepted},
		{"interrupt", "POST", "/api/runner-sessions/" + rsID + "/interrupt", nil, http.StatusAccepted},
	}

	// (a) board-B-scoped native token → 403 on every route touching A's
	// card/board/session.
	for _, rt := range routes {
		resp := request(t, srv, rt.method, rt.path, mintedB.Secret, nil, rt.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("board-B-scoped token %s %s = %d, want 403", rt.method, rt.path, resp.StatusCode)
		}
	}

	// (b) a core "member" token (card.read + card.write) → success on all
	// five of A's routes.
	for _, rt := range routes {
		resp := request(t, srv, rt.method, rt.path, member, nil, rt.body)
		resp.Body.Close()
		if resp.StatusCode != rt.okStatus {
			t.Errorf("member %s %s = %d, want %d", rt.method, rt.path, resp.StatusCode, rt.okStatus)
		}
	}

	// (c) a core token with only card.read → reads still work, but message
	// and interrupt (card.write) are refused.
	for _, rt := range routes {
		resp := request(t, srv, rt.method, rt.path, readOnly, nil, rt.body)
		resp.Body.Close()
		switch rt.name {
		case "message", "interrupt":
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("read-only token %s %s = %d, want 403", rt.method, rt.path, resp.StatusCode)
			}
		default:
			if resp.StatusCode != rt.okStatus {
				t.Errorf("read-only token %s %s = %d, want %d", rt.method, rt.path, resp.StatusCode, rt.okStatus)
			}
		}
	}

	// (d) the native env service key → allowed everywhere.
	for _, rt := range routes {
		resp := request(t, srv, rt.method, rt.path, "svc-key-123", nil, rt.body)
		resp.Body.Close()
		if resp.StatusCode != rt.okStatus {
			t.Errorf("service key %s %s = %d, want %d", rt.method, rt.path, resp.StatusCode, rt.okStatus)
		}
	}
}

// A runner-session id that does not exist must 404 (fail closed) rather than
// leak a 200/403 distinction that would tell a caller whether the id is
// valid but foreign vs. simply absent.
func TestRunnerRoutesUnknownSession404(t *testing.T) {
	srv, _ := testServer(t)
	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})
	member := memberToken(t)
	unknown := "00000000-0000-0000-0000-000000000000"

	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/runner-sessions/" + unknown + "/events"},
		{"POST", "/api/runner-sessions/" + unknown + "/message"},
		{"POST", "/api/runner-sessions/" + unknown + "/interrupt"},
	} {
		var body any
		if rt.method == "POST" {
			body = map[string]any{"text": "hi"}
		}
		resp := request(t, srv, rt.method, rt.path, member, nil, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", rt.method, rt.path, resp.StatusCode)
		}
	}
}
