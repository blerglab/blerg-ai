package api_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A real board process for runner's cut matrix: shipped HTTP authentication,
// MCP, archive admission, SQL guards, rollover and immutable receipts. Only the
// unrelated Core identity issuer/reviewer provider uses registered test fixtures.
func TestBoundCutBoardProcess(t *testing.T) {
	if os.Getenv("BLERG_CUT_BOARD_PROCESS") != "1" {
		t.Skip("runner-owned board process fixture")
	}
	var srv *httptest.Server
	var pool *pgxpool.Pool
	if os.Getenv("BLERG_CUT_BOARD_GATE") == "deny" {
		srv, pool = testServer(t)
	} else {
		srv, pool, _, _, _ = testServerHeld(t)
	}
	ctx := t.Context()
	no := false
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Cut fixture", Columns: []string{"Open", "Done"}, TerminalColumns: []string{"Done"}, RequireRepo: &no, GateEnabled: &no})
	if err != nil {
		t.Fatal(err)
	}
	columns, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	release, err := db.SaveRelease(ctx, pool, board.ID, "", db.ReleaseParams{Name: "0.6.0", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := db.SaveRelease(ctx, pool, board.ID, "", db.ReleaseParams{Name: "Next", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i, title := range []string{"Carry this forward", "Finished work"} {
		card, e := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: &title, ColumnID: &columns[i].ID, ReleaseID: &release.ID}, db.EventMeta{Actor: "human"})
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, card.Card.ID)
	}
	owner, login := os.Getenv("BLERG_CUT_OWNER"), os.Getenv("BLERG_CUT_LOGIN")
	if owner == "" || login == "" {
		t.Fatal("fixture owner/login required")
	}
	control := adminToken(t)
	var closedCalls, evidenceCalls atomic.Int64
	var dropClose atomic.Bool
	var loseAfterClose atomic.Bool
	var pauseEvidence, heldEvidence atomic.Bool
	resumeEvidence := make(chan struct{}, 1)
	var credentialGone atomic.Bool
	sign := func(read bool) string {
		caps := []string{"card.read"}
		if !read {
			caps = append(caps, "card.write", "column.write")
		}
		claims := identity.Claims{Kind: "agent", Sub: "cut-proxy-" + board.ID, Aud: "blerg-board", Project: board.ID, OnBehalfOf: owner, PersonBehind: true, Caps: caps, ExpiresAt: time.Now().Add(time.Hour).Unix()}
		header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": coreTestKID})
		payload, _ := json.Marshal(claims)
		input := identity.EncodeSigningInput(header, payload)
		return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(corePriv, []byte(input)))
	}
	original := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/tokens/exchange" {
			if r.Header.Get("X-Internal-Key") != "cut-fixture-key" {
				w.WriteHeader(401)
				return
			}
			var req struct {
				AccountID string `json:"account_id"`
				SessionID string `json:"session_id"`
				BoardID   string `json:"board_id"`
				ReadOnly  bool   `json:"read_only"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil || credentialGone.Load() || req.AccountID != owner || req.SessionID != login || req.BoardID != board.ID {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": sign(req.ReadOnly), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
			return
		}
		if r.URL.Path == "/internal/tokens/exchange/revoke" {
			if r.Header.Get("X-Internal-Key") != "cut-fixture-key" {
				w.WriteHeader(401)
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/fixture/state" && r.Header.Get("Authorization") == "Bearer "+control {
			view, e := db.ReadReleaseEvidence(r.Context(), pool, board.ID, release.ID)
			if e != nil {
				http.Error(w, "fixture read failed", 500)
				return
			}
			cards := []db.Card{}
			for _, id := range ids {
				card, e := db.GetCard(r.Context(), pool, id)
				if e != nil {
					http.Error(w, "fixture read failed", 500)
					return
				}
				cards = append(cards, card)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"release": view, "cards": cards, "close_calls": closedCalls.Load(), "evidence_calls": evidenceCalls.Load(), "evidence_held": heldEvidence.Load()})
			return
		}
		if r.URL.Path == "/fixture/mutate" && r.Header.Get("Authorization") == "Bearer "+control {
			var req struct {
				Kind string `json:"kind"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(400)
				return
			}
			var e error
			switch req.Kind {
			case "all_done":
				_, e = db.MoveCard(r.Context(), pool, ids[0], columns[1].ID, nil, nil, db.EventMeta{Actor: "human"})
			case "changed_card":
				title := "Changed after authorization"
				_, e = db.UpdateCard(r.Context(), pool, ids[0], db.CardParams{Title: &title}, db.EventMeta{Actor: "human"})
			case "active_session":
				_, e = pool.Exec(r.Context(), `INSERT INTO runner_sessions(card_id,board_id,runner,external_session_id,lifecycle,role) VALUES($1,$2,'fixture','cut-active','running','worker')`, ids[1], board.ID)
			case "archive_gate_hold", "archive_gate_deny":
				policy := "hold"
				_, e = pool.Exec(r.Context(), `UPDATE boards SET gate_enabled=true,gate_on_unavailable=$2 WHERE id=$1`, board.ID, policy)
			case "pause_evidence":
				pauseEvidence.Store(true)
			case "resume_evidence":
				resumeEvidence <- struct{}{}
			case "drop_close_and_lose_credentials":
				loseAfterClose.Store(true)
				dropClose.Store(true)
			case "restore_credentials":
				credentialGone.Store(false)
			case "drop_close_response":
				dropClose.Store(true)
			case "lost_credentials":
				credentialGone.Store(true)
			case "legacy_cut":
				_, e = pool.Exec(r.Context(), `UPDATE board_releases SET status='released',released_at=now() WHERE id=$1`, release.ID)
			case "direct_cut", "next_closed":
				target, destination := release.ID, next.ID
				if req.Kind == "next_closed" {
					target = next.ID
					destination = ""
				}
				body, _ := json.Marshal(map[string]string{"next_release_id": destination})
				request := httptest.NewRequest("POST", "/api/boards/"+board.ID+"/releases/"+target+"/close", bytes.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+control)
				request.Header.Set("Content-Type", "application/json")
				result := httptest.NewRecorder()
				original.ServeHTTP(result, request)
				if result.Code != 200 {
					http.Error(w, "fixture direct cut failed", 500)
					return
				}
			default:
				w.WriteHeader(400)
				return
			}
			if e != nil {
				http.Error(w, "fixture mutation failed", 500)
				return
			}
			w.WriteHeader(200)
			return
		}
		if r.URL.Path == "/mcp" {
			raw, e := io.ReadAll(r.Body)
			if e != nil {
				w.WriteHeader(400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var rpc struct {
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			_ = json.Unmarshal(raw, &rpc)
			if rpc.Method == "tools/call" && rpc.Params.Name == "blerg_release_get" {
				evidenceCalls.Add(1)
			}
			if rpc.Method == "tools/call" && rpc.Params.Name == "blerg_release_get" && pauseEvidence.CompareAndSwap(true, false) {
				capture := httptest.NewRecorder()
				original.ServeHTTP(capture, r)
				heldEvidence.Store(true)
				select {
				case <-resumeEvidence:
				case <-r.Context().Done():
					heldEvidence.Store(false)
					return
				}
				heldEvidence.Store(false)
				for key, values := range capture.Header() {
					w.Header()[key] = values
				}
				w.WriteHeader(capture.Code)
				_, _ = w.Write(capture.Body.Bytes())
				return
			}
			if rpc.Method == "tools/call" && rpc.Params.Name == "blerg_release_close" {
				closedCalls.Add(1)
				if dropClose.CompareAndSwap(true, false) {
					capture := httptest.NewRecorder()
					original.ServeHTTP(capture, r)
					if loseAfterClose.Load() {
						credentialGone.Store(true)
					}
					connection, _, e := w.(http.Hijacker).Hijack()
					if e == nil {
						_ = connection.Close()
					}
					return
				}
			}
		}
		original.ServeHTTP(w, r)
	})
	// The parent reads this private pipe; credentials never enter a test log.
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"url": srv.URL, "board_id": board.ID, "release_id": release.ID, "next_release_id": next.ID, "control_token": control})
	_, _ = io.Copy(io.Discard, os.Stdin)
}
