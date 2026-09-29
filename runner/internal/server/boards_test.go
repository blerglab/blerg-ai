package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// TestBoards covers the board REST endpoints. DB-gated.
func TestBoards(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	api := NewAPI(NewHub(), pool, "tok", nil, "")

	// ── helpers ───────────────────────────────────────────────────────────────

	postBoard := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/boards", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		api.HandlePostBoards(rec, req)
		return rec
	}

	getBoard := func(id string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/boards/"+id, nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.HandleGetBoard(rec, req)
		return rec
	}

	deleteBoard := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/boards/"+id, nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.HandleDeleteBoard(rec, req)
		return rec
	}

	getBoardOrder := func(id string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/boards/"+id+"/order", nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.HandleGetBoardOrder(rec, req)
		return rec
	}

	// ── create board returns seeded columns ───────────────────────────────────

	t.Run("create board returns seeded columns", func(t *testing.T) {
		rec := postBoard(`{"name":"Alpha","repos":["repo-alpha"]}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Columns []struct {
				Name       string `json:"name"`
				IsTerminal bool   `json:"is_terminal"`
			} `json:"columns"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.ID == "" {
			t.Error("response missing id")
		}
		if resp.Name != "Alpha" {
			t.Errorf("name = %q, want Alpha", resp.Name)
		}
		if len(resp.Columns) != 3 {
			t.Fatalf("want 3 seeded columns, got %d: %v", len(resp.Columns), resp.Columns)
		}
		wantNames := []string{"Backlog", "Doing", "Done"}
		for i, col := range resp.Columns {
			if col.Name != wantNames[i] {
				t.Errorf("column[%d].name = %q, want %q", i, col.Name, wantNames[i])
			}
		}
		if resp.Columns[2].Name != "Done" || !resp.Columns[2].IsTerminal {
			t.Error("last column (Done) should be is_terminal=true")
		}
	})

	// ── validation: missing name → 400 ───────────────────────────────────────

	t.Run("create missing name → 400", func(t *testing.T) {
		rec := postBoard(`{"repos":["r"]}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("got %d, want 400", rec.Code)
		}
	})

	// ── validation: missing repos → 400 ──────────────────────────────────────

	t.Run("create missing repos → 400", func(t *testing.T) {
		rec := postBoard(`{"name":"no-repos"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("got %d, want 400", rec.Code)
		}
	})

	// ── validation: traversal repo → 422 ─────────────────────────────────────

	t.Run("create board with traversal repo → 422", func(t *testing.T) {
		rec := postBoard(`{"name":"traversal-board","repos":["../x"]}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("got %d, want 422; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── get board returns columns+tickets ─────────────────────────────────────

	t.Run("get board returns columns+tickets", func(t *testing.T) {
		// Create board via API.
		crRec := postBoard(`{"name":"Beta","repos":["repo-beta"]}`)
		if crRec.Code != http.StatusCreated {
			t.Fatalf("create Beta: %d %s", crRec.Code, crRec.Body.String())
		}
		var crResp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(crRec.Body.Bytes(), &crResp)
		boardID := crResp.ID

		// Seed a ticket directly via DB (handler is a later task).
		_, err := db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID: boardID,
			Title:   "first ticket",
			Repos:   []string{"repo-beta"},
		})
		if err != nil {
			t.Fatalf("CreateTicket: %v", err)
		}

		rec := getBoard(boardID, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			ID      string   `json:"id"`
			Name    string   `json:"name"`
			Repos   []string `json:"repos"`
			Columns []struct {
				Name string `json:"name"`
			} `json:"columns"`
			Tickets []struct {
				Title string `json:"title"`
			} `json:"tickets"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.ID != boardID {
			t.Errorf("id = %q, want %q", resp.ID, boardID)
		}
		if len(resp.Repos) != 1 || resp.Repos[0] != "repo-beta" {
			t.Errorf("repos = %v, want [repo-beta]", resp.Repos)
		}
		if len(resp.Columns) != 3 {
			t.Errorf("want 3 columns, got %d", len(resp.Columns))
		}
		if len(resp.Tickets) != 1 || resp.Tickets[0].Title != "first ticket" {
			t.Errorf("want 1 ticket titled 'first ticket', got %v", resp.Tickets)
		}
	})

	// ── repos round-trip on create ────────────────────────────────────────────

	t.Run("create board repos round-trip in GET", func(t *testing.T) {
		crRec := postBoard(`{"name":"ReposBoard","repos":["repo-x","repo-y"]}`)
		if crRec.Code != http.StatusCreated {
			t.Fatalf("create ReposBoard: %d %s", crRec.Code, crRec.Body.String())
		}
		var crResp struct {
			ID    string   `json:"id"`
			Repos []string `json:"repos"`
		}
		json.Unmarshal(crRec.Body.Bytes(), &crResp)
		// repos returned alphabetically.
		if len(crResp.Repos) != 2 || crResp.Repos[0] != "repo-x" || crResp.Repos[1] != "repo-y" {
			t.Errorf("create repos = %v, want [repo-x repo-y]", crResp.Repos)
		}

		getRec := getBoard(crResp.ID, nil)
		var getResp struct {
			Repos []string `json:"repos"`
		}
		json.Unmarshal(getRec.Body.Bytes(), &getResp)
		if len(getResp.Repos) != 2 || getResp.Repos[0] != "repo-x" || getResp.Repos[1] != "repo-y" {
			t.Errorf("GET repos = %v, want [repo-x repo-y]", getResp.Repos)
		}
	})

	// ── GetBoard returns all live tickets and excludes archived ──────────────

	t.Run("get board returns all live tickets, excludes archived", func(t *testing.T) {
		crRec := postBoard(`{"name":"TicketsBoard","repos":["repo-tb"]}`)
		var crResp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(crRec.Body.Bytes(), &crResp)
		boardID := crResp.ID

		// Create several live tickets.
		liveTitles := map[string]bool{}
		for _, title := range []string{"live-1", "live-2", "live-3"} {
			if _, err := db.CreateTicket(ctx, pool, db.TicketInput{
				BoardID: boardID, Title: title, Repos: []string{"repo-tb"},
			}); err != nil {
				t.Fatalf("CreateTicket %s: %v", title, err)
			}
			liveTitles[title] = true
		}
		// Create and archive one ticket.
		archived, err := db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID: boardID, Title: "archived-1", Repos: []string{"repo-tb"},
		})
		if err != nil {
			t.Fatalf("CreateTicket archived: %v", err)
		}
		if _, err := db.ArchiveTicket(ctx, pool, archived.ID); err != nil {
			t.Fatalf("ArchiveTicket: %v", err)
		}

		rec := getBoard(boardID, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Tickets []struct {
				Title string `json:"title"`
			} `json:"tickets"`
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if len(resp.Tickets) != 3 {
			t.Fatalf("want 3 live tickets, got %d: %v", len(resp.Tickets), resp.Tickets)
		}
		for _, tk := range resp.Tickets {
			if tk.Title == "archived-1" {
				t.Error("archived ticket must NOT appear in GetBoard payload")
			}
			if !liveTitles[tk.Title] {
				t.Errorf("unexpected ticket %q in payload", tk.Title)
			}
		}
	})

	// ── GET /api/boards/{id} with no token → 401 (no anonymous tier) ─────────

	t.Run("no token on GET /id → 401", func(t *testing.T) {
		crRec := postBoard(`{"name":"Gamma","repos":["repo-gamma"]}`)
		var crResp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(crRec.Body.Bytes(), &crResp)

		req := httptest.NewRequest(http.MethodGet, "/api/boards/"+crResp.ID, nil) // no Authorization header
		req.SetPathValue("id", crResp.ID)
		rec := httptest.NewRecorder()
		api.HandleGetBoard(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("got %d, want 401 (no credential is never trusted)", rec.Code)
		}
	})

	// ── board token for foreign board → 403 ──────────────────────────────────

	t.Run("board token for foreign board → 403", func(t *testing.T) {
		// Create two boards.
		crA := postBoard(`{"name":"TokenBoardA","repos":["repo-a"]}`)
		crB := postBoard(`{"name":"TokenBoardB","repos":["repo-b"]}`)
		var rA, rB struct {
			ID string `json:"id"`
		}
		json.Unmarshal(crA.Body.Bytes(), &rA)
		json.Unmarshal(crB.Body.Bytes(), &rB)

		// Need a daemon + session to mint the board token.
		daemonID := "00000000-0000-0000-0000-0000000000c1"
		sessionID := "00000000-0000-0000-0000-0000000000c2"
		if err := db.UpsertDaemon(ctx, pool, daemonID, "boards-test-daemon", "local", "/repos"); err != nil {
			t.Fatalf("UpsertDaemon: %v", err)
		}
		if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Boards Test", ""); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		// Mint token scoped to board A.
		rawToken, _, err := db.MintBoardToken(ctx, pool, rA.ID, sessionID, []string{"board"}, time.Hour)
		if err != nil {
			t.Fatalf("MintBoardToken: %v", err)
		}

		// Use that token to request board B → 403.
		rec := getBoard(rB.ID, map[string]string{"Authorization": "Bearer " + rawToken})
		if rec.Code != http.StatusForbidden {
			t.Errorf("foreign token on board B: got %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── order returns ready-first ─────────────────────────────────────────────

	t.Run("order returns ready-first", func(t *testing.T) {
		crRec := postBoard(`{"name":"OrderBoard","repos":["repo-ord"]}`)
		var crResp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(crRec.Body.Bytes(), &crResp)
		boardID := crResp.ID

		// Create two tickets. Blocker has no deps (ready); Dependent depends on Blocker (not ready).
		blocker, err := db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID: boardID,
			Title:   "blocker",
			Repos:   []string{"repo-ord"},
		})
		if err != nil {
			t.Fatalf("CreateTicket blocker: %v", err)
		}
		dependent, err := db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID: boardID,
			Title:   "dependent",
			Repos:   []string{"repo-ord"},
		})
		if err != nil {
			t.Fatalf("CreateTicket dependent: %v", err)
		}
		// dependent depends on blocker → blocker is ready, dependent is not.
		if err := db.AddDependency(ctx, pool, dependent.ID, blocker.ID, "human"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}

		rec := getBoardOrder(boardID, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("order = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Tickets []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"tickets"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.Tickets) != 2 {
			t.Fatalf("want 2 tickets, got %d", len(resp.Tickets))
		}
		// Blocker (ready) should be first.
		if resp.Tickets[0].ID != blocker.ID {
			t.Errorf("first ticket = %q (%s), want blocker %q; second = %q (%s)",
				resp.Tickets[0].ID, resp.Tickets[0].Title,
				blocker.ID,
				resp.Tickets[1].ID, resp.Tickets[1].Title)
		}
		if resp.Tickets[1].ID != dependent.ID {
			t.Errorf("second ticket = %q (%s), want dependent %q",
				resp.Tickets[1].ID, resp.Tickets[1].Title, dependent.ID)
		}
	})

	// ── order: board token for foreign board → 403 ───────────────────────────

	t.Run("order foreign board token → 403", func(t *testing.T) {
		crA := postBoard(`{"name":"OrderTokA","repos":["repo-ota"]}`)
		crB := postBoard(`{"name":"OrderTokB","repos":["repo-otb"]}`)
		var rA, rB struct {
			ID string `json:"id"`
		}
		json.Unmarshal(crA.Body.Bytes(), &rA)
		json.Unmarshal(crB.Body.Bytes(), &rB)

		daemonID := "00000000-0000-0000-0000-0000000000d1"
		sessionID := "00000000-0000-0000-0000-0000000000d2"
		if err := db.UpsertDaemon(ctx, pool, daemonID, "order-tok-daemon", "local", "/repos"); err != nil {
			t.Fatalf("UpsertDaemon: %v", err)
		}
		if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Order Tok", ""); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		rawToken, _, err := db.MintBoardToken(ctx, pool, rA.ID, sessionID, []string{"board"}, time.Hour)
		if err != nil {
			t.Fatalf("MintBoardToken: %v", err)
		}

		// Token scoped to board A used against board B's order → 403.
		rec := getBoardOrder(rB.ID, map[string]string{"Authorization": "Bearer " + rawToken})
		if rec.Code != http.StatusForbidden {
			t.Errorf("foreign token on board B order: got %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── delete cascades: get afterward → 404 ─────────────────────────────────

	t.Run("delete cascades get → 404", func(t *testing.T) {
		crRec := postBoard(`{"name":"ToDelete","repos":["repo-del"]}`)
		if crRec.Code != http.StatusCreated {
			t.Fatalf("create ToDelete: %d %s", crRec.Code, crRec.Body.String())
		}
		var crResp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(crRec.Body.Bytes(), &crResp)
		boardID := crResp.ID

		// Delete the board.
		delRec := deleteBoard(boardID)
		if delRec.Code != http.StatusNoContent {
			t.Fatalf("delete = %d, want 204; body=%s", delRec.Code, delRec.Body.String())
		}

		// GET the board afterward → 404.
		getRec := getBoard(boardID, nil)
		if getRec.Code != http.StatusNotFound {
			t.Errorf("get after delete = %d, want 404", getRec.Code)
		}
	})

	// ── delete unknown board → 404 ────────────────────────────────────────────

	t.Run("delete unknown board → 404", func(t *testing.T) {
		rec := deleteBoard("00000000-0000-0000-0000-000000000000")
		if rec.Code != http.StatusNotFound {
			t.Errorf("got %d, want 404", rec.Code)
		}
	})

	// ── get unknown board → 404 ───────────────────────────────────────────────

	t.Run("get unknown board → 404", func(t *testing.T) {
		rec := getBoard("00000000-0000-0000-0000-000000000000", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("got %d, want 404", rec.Code)
		}
	})

	// ── GET board tickets include tags, repos, blocked ────────────────────────

	t.Run("get board tickets include tags repos blocked", func(t *testing.T) {
		crRec := postBoard(`{"name":"EnrichBoard","repos":["repo-e1","repo-e2"]}`)
		if crRec.Code != http.StatusCreated {
			t.Fatalf("create EnrichBoard: %d %s", crRec.Code, crRec.Body.String())
		}
		var crResp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(crRec.Body.Bytes(), &crResp)
		boardID := crResp.ID

		enrichCols, err := db.ListColumns(ctx, pool, boardID)
		if err != nil {
			t.Fatalf("ListColumns EnrichBoard: %v", err)
		}
		terminalColID := enrichCols[2].ID // Done (is_terminal=true)

		// blocker ticket: two tags, two repos.
		blocker, err := db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID: boardID,
			Title:   "enrich-blocker",
			Repos:   []string{"repo-e1", "repo-e2"},
			Tags:    []string{"alpha", "beta"},
		})
		if err != nil {
			t.Fatalf("CreateTicket blocker: %v", err)
		}
		// dependent ticket: no tags, one repo.
		dep, err := db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID: boardID,
			Title:   "enrich-dep",
			Repos:   []string{"repo-e1"},
		})
		if err != nil {
			t.Fatalf("CreateTicket dep: %v", err)
		}
		// dep depends on blocker (blocker in non-terminal col → dep is blocked).
		if err := db.AddDependency(ctx, pool, dep.ID, blocker.ID, "human"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}

		type ticketShape struct {
			ID      string   `json:"id"`
			Tags    []string `json:"tags"`
			Repos   []string `json:"repos"`
			Blocked bool     `json:"blocked"`
		}
		getTickets := func() map[string]ticketShape {
			rec := getBoard(boardID, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("get board: %d %s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Tickets []ticketShape `json:"tickets"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			m := map[string]ticketShape{}
			for _, tk := range resp.Tickets {
				m[tk.ID] = tk
			}
			return m
		}

		// Phase 1: dep is blocked, tags and repos are populated.
		tks := getTickets()
		if len(tks) != 2 {
			t.Fatalf("want 2 tickets, got %d", len(tks))
		}
		bl := tks[blocker.ID]
		if len(bl.Tags) != 2 {
			t.Errorf("blocker Tags = %v, want 2 entries", bl.Tags)
		}
		if len(bl.Repos) != 2 {
			t.Errorf("blocker Repos = %v, want 2 entries", bl.Repos)
		}
		if bl.Blocked {
			t.Errorf("blocker Blocked = true, want false (no deps)")
		}
		dp := tks[dep.ID]
		// nil slice must serialize as [] not null.
		if dp.Tags == nil {
			t.Errorf("dep Tags is JSON null, want []")
		}
		if dp.Repos == nil {
			t.Errorf("dep Repos is JSON null, want []")
		}
		if !dp.Blocked {
			t.Errorf("dep Blocked = false, want true (unsatisfied dep on blocker)")
		}

		// Phase 2: move blocker to terminal column → dep Blocked=false.
		if _, err := db.MoveTicket(ctx, pool, blocker.ID, terminalColID, nil, nil, nil); err != nil {
			t.Fatalf("MoveTicket blocker to Done: %v", err)
		}
		tks2 := getTickets()
		if tks2[dep.ID].Blocked {
			t.Errorf("dep still Blocked after blocker in terminal column")
		}
	})
}

// TestBoardArchive covers GET /api/boards/{id}/archive. DB-gated.
func TestBoardArchive(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	api := NewAPI(NewHub(), pool, "tok", nil, "")

	board, err := db.CreateBoard(ctx, pool, "Archive Board", nil, []string{"repo-a"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	// Auth fixtures.
	daemonID := "00000000-0000-0000-0000-000000000a01"
	sessionID := "00000000-0000-0000-0000-000000000a02"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "arch-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Arch Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Foreign board for 403 test.
	board2, err := db.CreateBoard(ctx, pool, "Archive Board 2", nil, []string{"repo-b"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard2: %v", err)
	}
	rawToken2, _, err := db.MintBoardToken(ctx, pool, board2.ID, sessionID, []string{"board"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken2: %v", err)
	}

	// Create a live ticket and two archived tickets.
	live, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID: board.ID, Title: "live ticket", Repos: []string{"repo-a"},
	})
	if err != nil {
		t.Fatalf("CreateTicket live: %v", err)
	}
	arched1, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID: board.ID, Title: "archived ticket 1", Repos: []string{"repo-a"},
	})
	if err != nil {
		t.Fatalf("CreateTicket arched1: %v", err)
	}
	if _, err := db.ArchiveTicket(ctx, pool, arched1.ID); err != nil {
		t.Fatalf("ArchiveTicket 1: %v", err)
	}
	arched2, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID: board.ID, Title: "archived ticket 2", Repos: []string{"repo-a"},
	})
	if err != nil {
		t.Fatalf("CreateTicket arched2: %v", err)
	}
	if _, err := db.ArchiveTicket(ctx, pool, arched2.ID); err != nil {
		t.Fatalf("ArchiveTicket 2: %v", err)
	}

	getBoardArchive := func(id, query string, headers map[string]string) *httptest.ResponseRecorder {
		url := "/api/boards/" + id + "/archive"
		if query != "" {
			url += "?" + query
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.HandleGetBoardArchive(rec, req)
		return rec
	}

	// ── only archived tickets returned ────────────────────────────────────────
	t.Run("returns only archived tickets", func(t *testing.T) {
		rec := getBoardArchive(board.ID, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get archive = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Tickets []struct {
				ID         string   `json:"id"`
				ArchivedAt *string  `json:"archived_at"`
				ColumnID   *string  `json:"column_id"`
				Tags       []string `json:"tags"`
				Repos      []string `json:"repos"`
			} `json:"tickets"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.Tickets) != 2 {
			t.Fatalf("want 2 archived tickets, got %d: %v", len(resp.Tickets), resp.Tickets)
		}
		for _, tk := range resp.Tickets {
			if tk.ID == live.ID {
				t.Error("live ticket must NOT appear in archive")
			}
			if tk.ArchivedAt == nil {
				t.Errorf("ticket %s: archived_at is nil, want set", tk.ID)
			}
			if tk.ColumnID != nil {
				t.Errorf("ticket %s: column_id = %v, want nil (cleared on archive)", tk.ID, *tk.ColumnID)
			}
			// Tags and repos slices must not be JSON null.
			if tk.Tags == nil {
				t.Errorf("ticket %s: tags is JSON null, want []", tk.ID)
			}
			if tk.Repos == nil {
				t.Errorf("ticket %s: repos is JSON null, want []", tk.ID)
			}
		}
	})

	// ── pagination with limit=1 ───────────────────────────────────────────────
	t.Run("pagination limit=1 yields cursor", func(t *testing.T) {
		rec := getBoardArchive(board.ID, "limit=1", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("page1 = %d; body=%s", rec.Code, rec.Body.String())
		}
		var page1 struct {
			Tickets []struct {
				ID string `json:"id"`
			} `json:"tickets"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page1); err != nil {
			t.Fatalf("unmarshal page1: %v", err)
		}
		if len(page1.Tickets) != 1 {
			t.Fatalf("page1: want 1 ticket, got %d", len(page1.Tickets))
		}
		if page1.NextCursor == "" {
			t.Fatal("page1: next_cursor empty, want a cursor")
		}

		rec2 := getBoardArchive(board.ID, "limit=1&cursor="+page1.NextCursor, nil)
		if rec2.Code != http.StatusOK {
			t.Fatalf("page2 = %d; body=%s", rec2.Code, rec2.Body.String())
		}
		var page2 struct {
			Tickets []struct {
				ID string `json:"id"`
			} `json:"tickets"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec2.Body.Bytes(), &page2); err != nil {
			t.Fatalf("unmarshal page2: %v", err)
		}
		if len(page2.Tickets) != 1 {
			t.Fatalf("page2: want 1 ticket, got %d", len(page2.Tickets))
		}
		if page1.Tickets[0].ID == page2.Tickets[0].ID {
			t.Errorf("page1 and page2 same ticket %q; want different (pagination)", page1.Tickets[0].ID)
		}
	})

	// ── foreign-board token → 403 ─────────────────────────────────────────────
	t.Run("foreign-board token → 403", func(t *testing.T) {
		rec := getBoardArchive(board.ID, "", map[string]string{"Authorization": "Bearer " + rawToken2})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})

	_ = live // verified it doesn't appear in archive results
}
