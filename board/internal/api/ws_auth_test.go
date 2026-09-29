package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/gorilla/websocket"
)

// dialWSSubprotocol dials srv's /ws?board=<boardID> with the given
// subprotocol list and optional Origin header (empty string omits it) — the
// browser-facing auth path (no Authorization header), as opposed to
// hub_test.go's dialWS which authenticates via a header directly.
func dialWSSubprotocol(srv *httptest.Server, boardID string, protocols []string, origin string) (*websocket.Conn, *http.Response, error) {
	d := websocket.Dialer{Subprotocols: protocols}
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	return d.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?board="+boardID, header)
}

// TestHandleWS_NoSubprotocolUnauthorized: a browser dialing /ws with no
// ["bearer", <token>] subprotocol offers no credential at all — the same 401
// a REST request without an Authorization header gets. Rejected BEFORE the
// upgrade completes.
func TestHandleWS_NoSubprotocolUnauthorized(t *testing.T) {
	srv, _ := testServer(t)

	conn, resp, err := dialWSSubprotocol(srv, "some-board", nil, "")
	if err == nil {
		conn.Close()
		t.Fatal("dial succeeded without a token, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

// TestHandleWS_BearerSubprotocolUpgrades: a valid core-issued member token
// passed as ["bearer", <token>] authenticates the handshake and the server
// negotiates only the "bearer" marker as the subprotocol — never echoing the
// token itself.
func TestHandleWS_BearerSubprotocolUpgrades(t *testing.T) {
	srv, _ := testServer(t)
	tok := memberToken(t)

	conn, _, err := dialWSSubprotocol(srv, "some-board", []string{"bearer", tok}, "")
	if err != nil {
		t.Fatalf("dial with valid bearer subprotocol: %v", err)
	}
	defer conn.Close()

	if got := conn.Subprotocol(); got != "bearer" {
		t.Fatalf("negotiated subprotocol = %q, want %q", got, "bearer")
	}
	if strings.Contains(conn.Subprotocol(), tok) {
		t.Fatal("server echoed the access token back as the negotiated subprotocol")
	}
}

// TestHandleWS_EvilOriginRejected: CheckOrigin must reject a cross-origin
// handshake from a page the operator hasn't allowlisted — the CSRF-equivalent
// defence for the upgrade (an unconditional CheckOrigin: true, as it used to
// be, would let any page on the internet open this socket).
func TestHandleWS_EvilOriginRejected(t *testing.T) {
	srv, _ := testServer(t)
	tok := memberToken(t)

	conn, resp, err := dialWSSubprotocol(srv, "some-board", []string{"bearer", tok}, "https://evil.example")
	if err == nil {
		conn.Close()
		t.Fatal("dial succeeded from a hostile Origin, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

// TestHandleWS_RequiresCardReadOnTheBoard: /ws is board-scoped like every
// other read — a token scoped to another board, or a core token without
// card.read (password.change-only), is refused with 403 before the upgrade;
// the token's own board still upgrades.
func TestHandleWS_RequiresCardReadOnTheBoard(t *testing.T) {
	srv, _ := testServer(t)
	admin := adminToken(t)

	var a, b db.Board
	r := request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "ws-a"})
	decodeBody(t, r, &a)
	r = request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "ws-b"})
	decodeBody(t, r, &b)
	var minted struct {
		Secret string `json:"secret"`
	}
	r = request(t, srv, "POST", "/api/tokens", admin, nil, map[string]any{"label": "a-only", "board_id": a.ID, "capabilities": []string{"card.read"}})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("mint scoped = %d", r.StatusCode)
	}
	decodeBody(t, r, &minted)

	for _, tc := range []struct {
		name, token, board string
	}{
		{"agent scoped to board a on board b", minted.Secret, b.ID},
		{"password.change-only core token", testHumanToken(t, []string{"password.change"}), a.ID},
	} {
		conn, resp, err := dialWSSubprotocol(srv, tc.board, []string{"bearer", tc.token}, "")
		if err == nil {
			conn.Close()
			t.Fatalf("%s: dial succeeded, want 403", tc.name)
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: status = %v, want 403", tc.name, resp)
		}
	}

	conn, _, err := dialWSSubprotocol(srv, a.ID, []string{"bearer", minted.Secret}, "")
	if err != nil {
		t.Fatalf("scoped token on its own board: %v", err)
	}
	conn.Close()
}
