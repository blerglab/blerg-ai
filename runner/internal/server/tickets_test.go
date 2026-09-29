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

// TestTickets covers all ticket REST endpoints. DB-gated.
func TestTickets(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	api := NewAPI(NewHub(), pool, "tok", nil, "")

	// ── fixtures ──────────────────────────────────────────────────────────────

	board, err := db.CreateBoard(ctx, pool, "Test Board", nil, []string{"repo-a", "repo-b"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	backlogID := cols[0].ID // Backlog (first non-terminal)
	doingID := cols[1].ID   // Doing

	// Board token for auth tests.
	daemonID := "00000000-0000-0000-0000-000000000d01"
	sessionID := "00000000-0000-0000-0000-000000000501"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "test-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	rawToken, _, err := db.MintBoardToken(ctx, pool, board.ID, sessionID, []string{"board"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken: %v", err)
	}

	// A second board + token for foreign-board test.
	board2, err := db.CreateBoard(ctx, pool, "Board2", nil, []string{"repo-c"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard2: %v", err)
	}
	rawToken2, _, err := db.MintBoardToken(ctx, pool, board2.ID, sessionID, []string{"board"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken2: %v", err)
	}
	_ = rawToken2

	// ── request helpers ───────────────────────────────────────────────────────

	postTicket := func(boardID string, body map[string]interface{}, headers map[string]string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/boards/"+boardID+"/tickets", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", boardID)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.HandlePostTicket(rec, req)
		return rec
	}

	listTickets := func(boardID string, query string, headers map[string]string) *httptest.ResponseRecorder {
		url := "/api/boards/" + boardID + "/tickets"
		if query != "" {
			url += "?" + query
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", boardID)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.HandleListTickets(rec, req)
		return rec
	}

	getTicket := func(id string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/tickets/"+id, nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.HandleGetTicket(rec, req)
		return rec
	}

	patchTicket := func(id string, body map[string]interface{}, headers map[string]string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPatch, "/api/tickets/"+id, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.HandlePatchTicket(rec, req)
		return rec
	}

	splitTicket := func(id string, body map[string]interface{}) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+id+"/split", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.HandleSplitTicket(rec, req)
		return rec
	}

	archiveTicket := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+id+"/archive", nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.HandleArchiveTicket(rec, req)
		return rec
	}

	addDependency := func(id string, body map[string]interface{}) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+id+"/dependencies", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.HandleAddDependency(rec, req)
		return rec
	}

	removeDependency := func(id, depID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/tickets/"+id+"/dependencies/"+depID, nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		req.SetPathValue("depId", depID)
		rec := httptest.NewRecorder()
		api.HandleRemoveDependency(rec, req)
		return rec
	}

	// ── POST /api/boards/{id}/tickets → 201 ──────────────────────────────────

	var createdTicketID string

	t.Run("create ticket 201", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title":     "First ticket",
			"repos":     []string{"repo-a"},
			"tags":      []string{"bug"},
			"priority":  "high",
			"column_id": backlogID,
		}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Priority string `json:"priority"`
			BoardID  string `json:"board_id"`
			Version  int    `json:"version"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.ID == "" {
			t.Fatal("missing id")
		}
		if resp.Title != "First ticket" {
			t.Errorf("title=%q, want 'First ticket'", resp.Title)
		}
		if resp.Priority != "high" {
			t.Errorf("priority=%q, want 'high'", resp.Priority)
		}
		if resp.BoardID != board.ID {
			t.Errorf("board_id=%q, want %q", resp.BoardID, board.ID)
		}
		createdTicketID = resp.ID
	})

	t.Run("create ticket bad priority → 400", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title":    "Bad prio",
			"repos":    []string{"repo-a"},
			"priority": "bogus",
		}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("create ticket repo not in board → 400", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "Bad repo",
			"repos": []string{"repo-unknown"},
		}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("create ticket zero repos → 400", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "No repos",
			"repos": []string{},
		}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── actor event on create ─────────────────────────────────────────────────

	t.Run("create ticket without token → human actor event", func(t *testing.T) {
		if createdTicketID == "" {
			t.Skip("depends on successful create")
		}
		events, _, err := db.ListTicketEvents(ctx, pool, createdTicketID, 10, "")
		if err != nil {
			t.Fatalf("ListTicketEvents: %v", err)
		}
		if len(events) == 0 {
			t.Fatal("expected at least one event, got none")
		}
		var found bool
		for _, e := range events {
			if e.Type == "created" && e.Actor == "human" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no created/human event; events=%v", events)
		}
	})

	t.Run("create ticket with token → assist actor event", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "Assist created",
			"repos": []string{"repo-a"},
		}, map[string]string{"Authorization": "Bearer " + rawToken})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp.ID == "" {
			t.Fatal("missing id")
		}
		events, _, err := db.ListTicketEvents(ctx, pool, resp.ID, 10, "")
		if err != nil {
			t.Fatalf("ListTicketEvents: %v", err)
		}
		var found bool
		for _, e := range events {
			if e.Type == "created" && e.Actor == "assist" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no created/assist event; events=%v", events)
		}
	})

	// ── GET /api/boards/{id}/tickets (list) ───────────────────────────────────

	t.Run("list tickets 200", func(t *testing.T) {
		rec := listTickets(board.ID, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Tickets []struct {
				ID string `json:"id"`
			} `json:"tickets"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.Tickets) == 0 {
			t.Error("expected at least one ticket")
		}
	})

	t.Run("list tickets filter by column", func(t *testing.T) {
		rec := listTickets(board.ID, "column="+backlogID, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list = %d; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Tickets []struct {
				ID       string  `json:"id"`
				ColumnID *string `json:"column_id"`
			} `json:"tickets"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, tk := range resp.Tickets {
			if tk.ColumnID == nil || *tk.ColumnID != backlogID {
				t.Errorf("ticket %s has column_id %v, want %s", tk.ID, tk.ColumnID, backlogID)
			}
		}
	})

	t.Run("list tickets filter by priority", func(t *testing.T) {
		rec := listTickets(board.ID, "priority=high", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list = %d; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Tickets []struct {
				Priority string `json:"priority"`
			} `json:"tickets"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, tk := range resp.Tickets {
			if tk.Priority != "high" {
				t.Errorf("priority=%q, want 'high'", tk.Priority)
			}
		}
	})

	// ── GET /api/tickets/{id} (full detail) ───────────────────────────────────

	t.Run("get ticket full detail 200", func(t *testing.T) {
		if createdTicketID == "" {
			t.Skip("depends on successful create")
		}
		rec := getTicket(createdTicketID, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Repos     []string `json:"repos"`
			Tags      []string `json:"tags"`
			DependsOn []string `json:"depends_on"`
			Blocks    []string `json:"blocks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.ID != createdTicketID {
			t.Errorf("id=%q, want %q", resp.ID, createdTicketID)
		}
		if resp.Title != "First ticket" {
			t.Errorf("title=%q, want 'First ticket'", resp.Title)
		}
		if len(resp.Repos) != 1 || resp.Repos[0] != "repo-a" {
			t.Errorf("repos=%v, want [repo-a]", resp.Repos)
		}
		if len(resp.Tags) != 1 || resp.Tags[0] != "bug" {
			t.Errorf("tags=%v, want [bug]", resp.Tags)
		}
		if resp.DependsOn == nil {
			t.Error("depends_on should be [] not null")
		}
		if resp.Blocks == nil {
			t.Error("blocks should be [] not null")
		}
	})

	// ── PATCH /api/tickets/{id} ───────────────────────────────────────────────

	t.Run("patch ticket valid 200", func(t *testing.T) {
		if createdTicketID == "" {
			t.Skip("depends on successful create")
		}
		rec := patchTicket(createdTicketID, map[string]interface{}{
			"title":    "Updated title",
			"add_tags": []string{"feature"},
		}, map[string]string{"If-Match": "0"})
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Title   string `json:"title"`
			Version int    `json:"version"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Title != "Updated title" {
			t.Errorf("title=%q, want 'Updated title'", resp.Title)
		}
		if resp.Version != 1 {
			t.Errorf("version=%d, want 1", resp.Version)
		}
	})

	t.Run("patch ticket stale If-Match → 409", func(t *testing.T) {
		if createdTicketID == "" {
			t.Skip("depends on successful create")
		}
		// Current version is 1 (after previous patch); send version 0 → stale.
		rec := patchTicket(createdTicketID, map[string]interface{}{
			"title": "Should conflict",
		}, map[string]string{"If-Match": "0"})
		if rec.Code != http.StatusConflict {
			t.Fatalf("patch = %d, want 409; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("patch ticket bad If-Match → 400", func(t *testing.T) {
		if createdTicketID == "" {
			t.Skip("depends on successful create")
		}
		rec := patchTicket(createdTicketID, map[string]interface{}{
			"title": "Bad if-match",
		}, map[string]string{"If-Match": "not-a-number"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("patch = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── PATCH with a traversal repo → 422 ─────────────────────────────────────

	t.Run("patch ticket with traversal repo in add_repos → 422", func(t *testing.T) {
		if createdTicketID == "" {
			t.Skip("depends on successful create")
		}
		rec := patchTicket(createdTicketID, map[string]interface{}{
			"add_repos": []string{"../etc"},
		}, nil)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("patch = %d, want 422; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("patch ticket with traversal repo in repos → 422", func(t *testing.T) {
		if createdTicketID == "" {
			t.Skip("depends on successful create")
		}
		rec := patchTicket(createdTicketID, map[string]interface{}{
			"repos": []string{"../etc"},
		}, nil)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("patch = %d, want 422; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── PATCH move via column_id ──────────────────────────────────────────────

	var movedTicketID string

	t.Run("patch move ticket to different column 200", func(t *testing.T) {
		// Create a ticket in backlog first.
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "To be moved",
			"repos": []string{"repo-a"},
		}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			ID       string  `json:"id"`
			ColumnID *string `json:"column_id"`
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		movedTicketID = resp.ID

		// Move to doing column.
		rec2 := patchTicket(resp.ID, map[string]interface{}{
			"column_id": doingID,
		}, nil)
		if rec2.Code != http.StatusOK {
			t.Fatalf("move = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
		}
		var moved struct {
			ColumnID *string `json:"column_id"`
		}
		if err := json.Unmarshal(rec2.Body.Bytes(), &moved); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if moved.ColumnID == nil || *moved.ColumnID != doingID {
			t.Errorf("column_id=%v, want %s", moved.ColumnID, doingID)
		}
	})

	// ── actor event on move ───────────────────────────────────────────────────

	t.Run("move appends moved event with expected actor and columns", func(t *testing.T) {
		if movedTicketID == "" {
			t.Skip("depends on move test")
		}
		events, _, err := db.ListTicketEvents(ctx, pool, movedTicketID, 10, "")
		if err != nil {
			t.Fatalf("ListTicketEvents: %v", err)
		}
		var found bool
		for _, e := range events {
			if e.Type == "moved" {
				found = true
				if e.Actor != "human" {
					t.Errorf("actor=%q, want 'human'", e.Actor)
				}
				if e.FromColumnID == nil {
					t.Error("from_column_id should be set")
				}
				if e.ToColumnID == nil || *e.ToColumnID != doingID {
					t.Errorf("to_column_id=%v, want %s", e.ToColumnID, doingID)
				}
				break
			}
		}
		if !found {
			t.Errorf("no 'moved' event found; events=%v", events)
		}
	})

	// ── move-only PATCH honors If-Match ───────────────────────────────────────

	t.Run("move-only PATCH stale If-Match → 409", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "Move stale", "repos": []string{"repo-a"},
		}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d; body=%s", rec.Code, rec.Body.String())
		}
		var created struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		}
		json.Unmarshal(rec.Body.Bytes(), &created)
		// Current version is 0; send 99 → stale.
		rec2 := patchTicket(created.ID, map[string]interface{}{
			"column_id": doingID,
		}, map[string]string{"If-Match": "99"})
		if rec2.Code != http.StatusConflict {
			t.Fatalf("move stale = %d, want 409; body=%s", rec2.Code, rec2.Body.String())
		}
	})

	t.Run("move-only PATCH correct If-Match → 200 and version +1", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "Move guarded", "repos": []string{"repo-a"},
		}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d; body=%s", rec.Code, rec.Body.String())
		}
		var created struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		}
		json.Unmarshal(rec.Body.Bytes(), &created)
		if created.Version != 0 {
			t.Fatalf("initial version = %d, want 0", created.Version)
		}
		rec2 := patchTicket(created.ID, map[string]interface{}{
			"column_id": doingID,
		}, map[string]string{"If-Match": "0"})
		if rec2.Code != http.StatusOK {
			t.Fatalf("move guarded = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
		}
		var moved struct {
			ColumnID *string `json:"column_id"`
			Version  int     `json:"version"`
		}
		json.Unmarshal(rec2.Body.Bytes(), &moved)
		if moved.Version != 1 {
			t.Errorf("version after move = %d, want 1", moved.Version)
		}
		if moved.ColumnID == nil || *moved.ColumnID != doingID {
			t.Errorf("column_id=%v, want %s", moved.ColumnID, doingID)
		}
	})

	t.Run("combined scalar+move correct If-Match → 200 and version +1 (not +2)", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "Combined", "repos": []string{"repo-a"},
		}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d; body=%s", rec.Code, rec.Body.String())
		}
		var created struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		}
		json.Unmarshal(rec.Body.Bytes(), &created)
		// Combined title change + move with correct If-Match.
		rec2 := patchTicket(created.ID, map[string]interface{}{
			"title":     "Combined updated",
			"column_id": doingID,
		}, map[string]string{"If-Match": "0"})
		if rec2.Code != http.StatusOK {
			t.Fatalf("combined = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
		}
		var result struct {
			Title    string  `json:"title"`
			ColumnID *string `json:"column_id"`
			Version  int     `json:"version"`
		}
		json.Unmarshal(rec2.Body.Bytes(), &result)
		if result.Version != 1 {
			t.Errorf("version after combined = %d, want 1 (single bump)", result.Version)
		}
		if result.Title != "Combined updated" {
			t.Errorf("title=%q, want 'Combined updated'", result.Title)
		}
		if result.ColumnID == nil || *result.ColumnID != doingID {
			t.Errorf("column_id=%v, want %s", result.ColumnID, doingID)
		}
	})

	// ── POST /api/tickets/{id}/split ──────────────────────────────────────────

	t.Run("split <2 titles → 400", func(t *testing.T) {
		// Need a ticket to split.
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "Split source",
			"repos": []string{"repo-a"},
		}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)

		rec2 := splitTicket(resp.ID, map[string]interface{}{
			"titles": []string{"only one"},
		})
		if rec2.Code != http.StatusBadRequest {
			t.Fatalf("split = %d, want 400; body=%s", rec2.Code, rec2.Body.String())
		}
	})

	t.Run("split ≥2 titles → 201 with children", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "Split source 2",
			"repos": []string{"repo-a", "repo-b"},
			"tags":  []string{"backend"},
		}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d; body=%s", rec.Code, rec.Body.String())
		}
		var created struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rec.Body.Bytes(), &created)

		// Subscribe a fake browser to the board so we can capture the emitted
		// ticket_split event and assert the origin's post-split state.
		bc := &BrowserConn{ID: newUUID(), send: make(chan []byte, 8)}
		api.hub.RegisterBrowser(bc)
		api.hub.SubscribeBoard(board.ID, bc.ID)
		defer api.hub.UnregisterBrowser(bc.ID)

		rec2 := splitTicket(created.ID, map[string]interface{}{
			"titles": []string{"Child A", "Child B", "Child C"},
		})
		if rec2.Code != http.StatusCreated {
			t.Fatalf("split = %d, want 201; body=%s", rec2.Code, rec2.Body.String())
		}
		var children []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if err := json.Unmarshal(rec2.Body.Bytes(), &children); err != nil {
			t.Fatalf("unmarshal children: %v", err)
		}
		if len(children) != 3 {
			t.Fatalf("want 3 children, got %d", len(children))
		}
		titles := []string{"Child A", "Child B", "Child C"}
		for i, c := range children {
			if c.Title != titles[i] {
				t.Errorf("children[%d].title=%q, want %q", i, c.Title, titles[i])
			}
		}

		// Assert the broadcast ticket_split event carries the origin's archived
		// state: archived_at set, column_id null.
		var evtRaw []byte
		select {
		case evtRaw = <-bc.send:
		case <-time.After(time.Second):
			t.Fatal("no ticket_split event broadcast to subscribed browser")
		}
		var evt struct {
			Type         string `json:"type"`
			OriginID     string `json:"origin_id"`
			OriginTicket struct {
				ID         string  `json:"id"`
				ColumnID   *string `json:"column_id"`
				ArchivedAt *string `json:"archived_at"`
			} `json:"origin_ticket"`
			Children []struct {
				ID string `json:"id"`
			} `json:"children"`
		}
		if err := json.Unmarshal(evtRaw, &evt); err != nil {
			t.Fatalf("unmarshal ticket_split event: %v", err)
		}
		if evt.Type != "ticket_split" {
			t.Errorf("event type = %q, want ticket_split", evt.Type)
		}
		if evt.OriginID != created.ID {
			t.Errorf("event origin_id = %q, want %q", evt.OriginID, created.ID)
		}
		if evt.OriginTicket.ID != created.ID {
			t.Errorf("origin_ticket.id = %q, want %q", evt.OriginTicket.ID, created.ID)
		}
		if evt.OriginTicket.ArchivedAt == nil {
			t.Error("origin_ticket.archived_at is nil; want set (origin is archived after split)")
		}
		if evt.OriginTicket.ColumnID != nil {
			t.Errorf("origin_ticket.column_id = %q, want nil (cleared on split)", *evt.OriginTicket.ColumnID)
		}
		if len(evt.Children) != 3 {
			t.Errorf("event children = %d, want 3", len(evt.Children))
		}
	})

	// ── POST /api/tickets/{id}/archive ────────────────────────────────────────

	t.Run("archive ticket → 200", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "To archive",
			"repos": []string{"repo-a"},
		}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)

		rec2 := archiveTicket(resp.ID)
		if rec2.Code != http.StatusOK {
			t.Fatalf("archive = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
		}
		var archived struct {
			ArchivedAt *string `json:"archived_at"`
		}
		if err := json.Unmarshal(rec2.Body.Bytes(), &archived); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if archived.ArchivedAt == nil {
			t.Error("archived_at should be set")
		}
	})

	// ── POST /api/tickets/{id}/dependencies ───────────────────────────────────

	var depTicketA, depTicketB string

	t.Run("add dependency 201", func(t *testing.T) {
		// Create two tickets.
		rec1 := postTicket(board.ID, map[string]interface{}{"title": "Dep A", "repos": []string{"repo-a"}}, nil)
		if rec1.Code != http.StatusCreated {
			t.Fatalf("create A = %d; body=%s", rec1.Code, rec1.Body.String())
		}
		var a struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rec1.Body.Bytes(), &a)
		depTicketA = a.ID

		rec2 := postTicket(board.ID, map[string]interface{}{"title": "Dep B", "repos": []string{"repo-b"}}, nil)
		if rec2.Code != http.StatusCreated {
			t.Fatalf("create B = %d; body=%s", rec2.Code, rec2.Body.String())
		}
		var b struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rec2.Body.Bytes(), &b)
		depTicketB = b.ID

		// A depends on B.
		rec3 := addDependency(a.ID, map[string]interface{}{"depends_on_ticket_id": b.ID})
		if rec3.Code != http.StatusCreated {
			t.Fatalf("add dep = %d, want 201; body=%s", rec3.Code, rec3.Body.String())
		}
	})

	t.Run("add self dependency → 400", func(t *testing.T) {
		if depTicketA == "" {
			t.Skip("depends on dep setup")
		}
		rec := addDependency(depTicketA, map[string]interface{}{"depends_on_ticket_id": depTicketA})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("self dep = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("add dependency cycle → 400", func(t *testing.T) {
		if depTicketA == "" || depTicketB == "" {
			t.Skip("depends on dep setup")
		}
		// A depends on B already. Now try B depends on A → cycle.
		rec := addDependency(depTicketB, map[string]interface{}{"depends_on_ticket_id": depTicketA})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("cycle dep = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── DELETE /api/tickets/{id}/dependencies/{depId} ─────────────────────────

	t.Run("remove dependency → 204", func(t *testing.T) {
		if depTicketA == "" || depTicketB == "" {
			t.Skip("depends on dep setup")
		}
		rec := removeDependency(depTicketA, depTicketB)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("remove dep = %d, want 204; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("cross-board dependency → 400", func(t *testing.T) {
		// Create a ticket on board (board.ID) and try to depend on a ticket
		// on board2 — the handler must return 400, not 500.
		rec1 := postTicket(board.ID, map[string]interface{}{
			"title": "Cross-Board-A",
			"repos": []string{"repo-a"},
		}, nil)
		if rec1.Code != http.StatusCreated {
			t.Fatalf("create Cross-Board-A = %d; body=%s", rec1.Code, rec1.Body.String())
		}
		var tA struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rec1.Body.Bytes(), &tA)

		// Create a ticket on board2 (no auth needed — it's open).
		rec2 := postTicket(board2.ID, map[string]interface{}{
			"title": "Cross-Board-B",
			"repos": []string{"repo-c"},
		}, nil)
		if rec2.Code != http.StatusCreated {
			t.Fatalf("create Cross-Board-B = %d; body=%s", rec2.Code, rec2.Body.String())
		}
		var tB struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rec2.Body.Bytes(), &tB)

		rec := addDependency(tA.ID, map[string]interface{}{"depends_on_ticket_id": tB.ID})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("cross-board dep = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("dependency via board token writes actor=assist", func(t *testing.T) {
		if depTicketA == "" || depTicketB == "" {
			t.Skip("depends on dep setup")
		}
		// Re-add the edge (it was removed above) using a board token.
		addDepWithToken := func(id string, body map[string]interface{}, token string) *httptest.ResponseRecorder {
			b, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+id+"/dependencies", bytes.NewReader(b))
			req.SetPathValue("id", id)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			api.HandleAddDependency(rec, req)
			return rec
		}
		rec := addDepWithToken(depTicketA, map[string]interface{}{"depends_on_ticket_id": depTicketB}, rawToken)
		if rec.Code != http.StatusCreated {
			t.Fatalf("add dep via board token = %d, want 201; body=%s", rec.Code, rec.Body.String())
		}
		// Check the event actor.
		events, _, err := db.ListTicketEvents(ctx, pool, depTicketA, 10, "")
		if err != nil {
			t.Fatalf("ListTicketEvents: %v", err)
		}
		var found bool
		for _, e := range events {
			if e.Type == "dependency_added" && e.Actor == "assist" {
				found = true
				break
			}
		}
		if !found {
			t.Error("no dependency_added event with actor=assist found")
		}
	})

	// ── unknown ticket → 404 ──────────────────────────────────────────────────

	t.Run("get unknown ticket → 404", func(t *testing.T) {
		fakeID := "00000000-0000-0000-0000-000000000000"
		rec := getTicket(fakeID, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("get unknown = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("patch unknown ticket → 404", func(t *testing.T) {
		fakeID := "00000000-0000-0000-0000-000000000000"
		rec := patchTicket(fakeID, map[string]interface{}{"title": "x"}, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("patch unknown = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("archive unknown ticket → 404", func(t *testing.T) {
		fakeID := "00000000-0000-0000-0000-000000000000"
		rec := archiveTicket(fakeID)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("archive unknown = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── foreign-board token → 403 ─────────────────────────────────────────────

	t.Run("create ticket with foreign-board token → 403", func(t *testing.T) {
		rec := postTicket(board.ID, map[string]interface{}{
			"title": "Foreign",
			"repos": []string{"repo-a"},
		}, map[string]string{"Authorization": "Bearer " + rawToken2})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("get ticket with foreign-board token → 403", func(t *testing.T) {
		if createdTicketID == "" {
			t.Skip("depends on successful create")
		}
		rec := getTicket(createdTicketID, map[string]string{"Authorization": "Bearer " + rawToken2})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})

}

// TestTicketGetEvents covers GET /api/tickets/{id}/events. DB-gated.
func TestTicketGetEvents(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	api := NewAPI(NewHub(), pool, "tok", nil, "")

	board, err := db.CreateBoard(ctx, pool, "Events Board", nil, []string{"repo-a"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	doingID := cols[1].ID

	// Auth fixtures.
	daemonID := "00000000-0000-0000-0000-000000000e01"
	sessionID := "00000000-0000-0000-0000-000000000e02"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "evt-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Evt Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	// Foreign board for 403 test.
	board2, err := db.CreateBoard(ctx, pool, "Events Board 2", nil, []string{"repo-b"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard2: %v", err)
	}
	rawToken2, _, err := db.MintBoardToken(ctx, pool, board2.ID, sessionID, []string{"board"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken2: %v", err)
	}

	// Request helpers.
	postTicket := func(boardID string, body map[string]interface{}) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/boards/"+boardID+"/tickets", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", boardID)
		rec := httptest.NewRecorder()
		api.HandlePostTicket(rec, req)
		return rec
	}
	patchTicket := func(id string, body map[string]interface{}) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPatch, "/api/tickets/"+id, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.HandlePatchTicket(rec, req)
		return rec
	}
	getTicketEvents := func(id, query string, headers map[string]string) *httptest.ResponseRecorder {
		url := "/api/tickets/" + id + "/events"
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
		api.HandleGetTicketEvents(rec, req)
		return rec
	}

	// Create a ticket (appends "created" event), then move it (appends "moved").
	crRec := postTicket(board.ID, map[string]interface{}{"title": "Events ticket", "repos": []string{"repo-a"}})
	if crRec.Code != http.StatusCreated {
		t.Fatalf("create = %d; body=%s", crRec.Code, crRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(crRec.Body.Bytes(), &created)
	ticketID := created.ID

	mvRec := patchTicket(ticketID, map[string]interface{}{"column_id": doingID})
	if mvRec.Code != http.StatusOK {
		t.Fatalf("move = %d; body=%s", mvRec.Code, mvRec.Body.String())
	}

	// ── events returned newest-first ──────────────────────────────────────────
	t.Run("events returned newest-first", func(t *testing.T) {
		rec := getTicketEvents(ticketID, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get events = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Events []struct {
				ID           int64   `json:"id"`
				TicketID     string  `json:"ticket_id"`
				Type         string  `json:"type"`
				Actor        string  `json:"actor"`
				FromColumnID *string `json:"from_column_id"`
				ToColumnID   *string `json:"to_column_id"`
				CreatedAt    string  `json:"created_at"`
			} `json:"events"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.Events) < 2 {
			t.Fatalf("want ≥2 events, got %d", len(resp.Events))
		}
		// Newest first: moved (most recent) then created.
		if resp.Events[0].Type != "moved" {
			t.Errorf("events[0].type = %q, want 'moved' (newest first)", resp.Events[0].Type)
		}
		if resp.Events[len(resp.Events)-1].Type != "created" {
			t.Errorf("events[last].type = %q, want 'created' (oldest)", resp.Events[len(resp.Events)-1].Type)
		}
		for _, e := range resp.Events {
			if e.TicketID != ticketID {
				t.Errorf("event ticket_id = %q, want %q", e.TicketID, ticketID)
			}
			if e.CreatedAt == "" {
				t.Errorf("event created_at is empty for event id=%d", e.ID)
			}
		}
		// The moved event should carry from_column_id and to_column_id.
		mv := resp.Events[0]
		if mv.FromColumnID == nil {
			t.Errorf("moved event from_column_id is nil, want set")
		}
		if mv.ToColumnID == nil || *mv.ToColumnID != doingID {
			t.Errorf("moved event to_column_id = %v, want %s", mv.ToColumnID, doingID)
		}
	})

	// ── limit=1 yields next_cursor; page 2 returns different event ────────────
	t.Run("pagination limit=1 yields cursor", func(t *testing.T) {
		rec := getTicketEvents(ticketID, "limit=1", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("page1 = %d; body=%s", rec.Code, rec.Body.String())
		}
		var page1 struct {
			Events []struct {
				Type string `json:"type"`
			} `json:"events"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page1); err != nil {
			t.Fatalf("unmarshal page1: %v", err)
		}
		if len(page1.Events) != 1 {
			t.Fatalf("page1: want 1 event, got %d", len(page1.Events))
		}
		if page1.NextCursor == "" {
			t.Fatal("page1: next_cursor empty, want a cursor")
		}

		rec2 := getTicketEvents(ticketID, "limit=1&cursor="+page1.NextCursor, nil)
		if rec2.Code != http.StatusOK {
			t.Fatalf("page2 = %d; body=%s", rec2.Code, rec2.Body.String())
		}
		var page2 struct {
			Events []struct {
				Type string `json:"type"`
			} `json:"events"`
		}
		if err := json.Unmarshal(rec2.Body.Bytes(), &page2); err != nil {
			t.Fatalf("unmarshal page2: %v", err)
		}
		if len(page2.Events) != 1 {
			t.Fatalf("page2: want 1 event, got %d", len(page2.Events))
		}
		if page1.Events[0].Type == page2.Events[0].Type {
			t.Errorf("page1 and page2 same event type %q; want different (pagination)", page1.Events[0].Type)
		}
	})

	// ── unknown ticket → 404 ──────────────────────────────────────────────────
	t.Run("unknown ticket → 404", func(t *testing.T) {
		rec := getTicketEvents("00000000-0000-0000-0000-000000000000", "", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("got %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── foreign-board token → 403 ─────────────────────────────────────────────
	t.Run("foreign-board token → 403", func(t *testing.T) {
		rec := getTicketEvents(ticketID, "", map[string]string{"Authorization": "Bearer " + rawToken2})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})
}

// wsTicket is the ticket payload shape captured from a broadcast board event.
type wsTicket struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags"`
	Repos   []string `json:"repos"`
	Blocked bool     `json:"blocked"`
}

// wsEvent is a captured board event with its ticket payload (plus split fields).
type wsEvent struct {
	Type         string     `json:"type"`
	Ticket       wsTicket   `json:"ticket"`
	OriginTicket wsTicket   `json:"origin_ticket"`
	Children     []wsTicket `json:"children"`
}

// TestTicketWSEventEnrichment verifies that the ticket_* board events carry the
// enriched card fields (tags, repos, blocked) — not the empty values that a bare
// mutation row would produce. Covers ticket_moved (tags + blocked),
// dependency add/remove (ticket_updated with recomputed blocked), and
// ticket_split children/origin (inherited tags). DB-gated.
func TestTicketWSEventEnrichment(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	hub := NewHub()
	api := NewAPI(hub, pool, "tok", nil, "")

	board, err := db.CreateBoard(ctx, pool, "WS Enrich Board", nil, []string{"repo-a", "repo-b"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	backlogID := cols[0].ID
	doingID := cols[1].ID

	// Subscribe a test browser to capture board events synchronously.
	bc, cleanup := newTestBrowser(hub)
	defer cleanup()
	hub.SubscribeBoard(board.ID, bc.ID)

	// drainEvents reads all currently-buffered events from the browser channel.
	drainEvents := func() []wsEvent {
		var evts []wsEvent
		for {
			msg, ok := drainMsg(bc)
			if !ok {
				return evts
			}
			var e wsEvent
			if err := json.Unmarshal(msg, &e); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}
			evts = append(evts, e)
		}
	}
	findEvent := func(evts []wsEvent, typ string) (wsEvent, bool) {
		for _, e := range evts {
			if e.Type == typ {
				return e, true
			}
		}
		return wsEvent{}, false
	}

	// Request helpers (fresh, since this test has its own api instance).
	postTicket := func(boardID string, body map[string]interface{}) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/boards/"+boardID+"/tickets", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", boardID)
		rec := httptest.NewRecorder()
		api.HandlePostTicket(rec, req)
		return rec
	}
	patchTicket := func(id string, body map[string]interface{}) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPatch, "/api/tickets/"+id, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.HandlePatchTicket(rec, req)
		return rec
	}
	splitTicket := func(id string, body map[string]interface{}) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+id+"/split", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.HandleSplitTicket(rec, req)
		return rec
	}
	addDependency := func(id string, body map[string]interface{}) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+id+"/dependencies", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.HandleAddDependency(rec, req)
		return rec
	}
	removeDependency := func(id, depID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/tickets/"+id+"/dependencies/"+depID, nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		req.SetPathValue("depId", depID)
		rec := httptest.NewRecorder()
		api.HandleRemoveDependency(rec, req)
		return rec
	}

	mustID := func(rec *httptest.ResponseRecorder) string {
		if rec.Code/100 != 2 {
			t.Fatalf("request failed: %d %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp.ID
	}

	// ── Seed: blocker (with a tag) and a tagged ticket to move ────────────────
	blockerID := mustID(postTicket(board.ID, map[string]interface{}{
		"title":     "blocker",
		"repos":     []string{"repo-a"},
		"column_id": backlogID,
	}))
	taggedID := mustID(postTicket(board.ID, map[string]interface{}{
		"title":     "tagged-ticket",
		"repos":     []string{"repo-a", "repo-b"},
		"tags":      []string{"frontend", "urgent-fix"},
		"column_id": backlogID,
	}))
	drainEvents() // clear ticket_created events

	// ── ticket_moved carries tags + correct blocked ──────────────────────────
	t.Run("ticket_moved carries tags and blocked", func(t *testing.T) {
		rec := patchTicket(taggedID, map[string]interface{}{"column_id": doingID})
		if rec.Code != http.StatusOK {
			t.Fatalf("patch move: %d %s", rec.Code, rec.Body.String())
		}
		evts := drainEvents()
		moved, ok := findEvent(evts, "ticket_moved")
		if !ok {
			t.Fatalf("no ticket_moved event; got %+v", evts)
		}
		if len(moved.Ticket.Tags) != 2 {
			t.Errorf("ticket_moved Tags = %v, want 2 (frontend, urgent-fix)", moved.Ticket.Tags)
		}
		if len(moved.Ticket.Repos) != 2 {
			t.Errorf("ticket_moved Repos = %v, want 2", moved.Ticket.Repos)
		}
		if moved.Ticket.Blocked {
			t.Errorf("ticket_moved Blocked = true, want false (no deps)")
		}
	})

	// ── dep add → ticket_updated with blocked=true ───────────────────────────
	t.Run("dep add broadcasts ticket_updated blocked=true", func(t *testing.T) {
		rec := addDependency(taggedID, map[string]interface{}{
			"depends_on_ticket_id": blockerID,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("add dependency: %d %s", rec.Code, rec.Body.String())
		}
		evts := drainEvents()
		if _, ok := findEvent(evts, "ticket_dependency_changed"); !ok {
			t.Errorf("expected a ticket_dependency_changed event; got %+v", evts)
		}
		upd, ok := findEvent(evts, "ticket_updated")
		if !ok {
			t.Fatalf("no ticket_updated after dep add; got %+v", evts)
		}
		if upd.Ticket.ID != taggedID {
			t.Errorf("ticket_updated ID = %q, want dependent %q", upd.Ticket.ID, taggedID)
		}
		if !upd.Ticket.Blocked {
			t.Errorf("ticket_updated Blocked = false, want true (unsatisfied dep)")
		}
		// Still carries tags.
		if len(upd.Ticket.Tags) != 2 {
			t.Errorf("ticket_updated Tags = %v, want 2", upd.Ticket.Tags)
		}
	})

	// ── dep remove → ticket_updated with blocked=false ───────────────────────
	t.Run("dep remove broadcasts ticket_updated blocked=false", func(t *testing.T) {
		rec := removeDependency(taggedID, blockerID)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("remove dependency: %d %s", rec.Code, rec.Body.String())
		}
		evts := drainEvents()
		upd, ok := findEvent(evts, "ticket_updated")
		if !ok {
			t.Fatalf("no ticket_updated after dep remove; got %+v", evts)
		}
		if upd.Ticket.Blocked {
			t.Errorf("ticket_updated Blocked = true after dep removed, want false")
		}
	})

	// ── ticket_split children inherit tags ───────────────────────────────────
	t.Run("ticket_split children carry inherited tags", func(t *testing.T) {
		// Origin has tags; split children inherit them.
		originID := mustID(postTicket(board.ID, map[string]interface{}{
			"title":     "split-origin",
			"repos":     []string{"repo-a"},
			"tags":      []string{"inherited"},
			"column_id": backlogID,
		}))
		drainEvents() // clear ticket_created

		rec := splitTicket(originID, map[string]interface{}{
			"titles": []string{"child-1", "child-2"},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("split: %d %s", rec.Code, rec.Body.String())
		}
		evts := drainEvents()
		split, ok := findEvent(evts, "ticket_split")
		if !ok {
			t.Fatalf("no ticket_split event; got %+v", evts)
		}
		if len(split.Children) != 2 {
			t.Fatalf("split Children = %d, want 2", len(split.Children))
		}
		for i, c := range split.Children {
			if len(c.Tags) != 1 || c.Tags[0] != "inherited" {
				t.Errorf("child[%d] Tags = %v, want [inherited]", i, c.Tags)
			}
		}
	})
}
