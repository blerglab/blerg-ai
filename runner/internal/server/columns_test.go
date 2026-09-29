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

// TestColumns covers the column REST endpoints. DB-gated.
func TestColumns(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	api := NewAPI(NewHub(), pool, "tok", nil, "")

	// ── helpers ───────────────────────────────────────────────────────────────

	createBoard := func(name string) string {
		body, _ := json.Marshal(map[string]interface{}{
			"name":  name,
			"repos": []string{"repo-" + name},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/boards", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		api.HandlePostBoards(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("createBoard %s: %d %s", name, rec.Code, rec.Body.String())
		}
		var resp struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp.ID
	}

	postColumn := func(boardID string, body map[string]interface{}, headers map[string]string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/boards/"+boardID+"/columns", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", boardID)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.HandlePostColumn(rec, req)
		return rec
	}

	patchColumn := func(colID string, body map[string]interface{}, headers map[string]string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPatch, "/api/columns/"+colID, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", colID)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.HandlePatchColumn(rec, req)
		return rec
	}

	deleteColumn := func(colID string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/columns/"+colID, nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", colID)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.HandleDeleteColumn(rec, req)
		return rec
	}

	// ── add a column → 201 ───────────────────────────────────────────────────

	t.Run("add column → 201", func(t *testing.T) {
		boardID := createBoard("add-col")
		rec := postColumn(boardID, map[string]interface{}{"name": "Sprint"}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("post column: %d %s", rec.Code, rec.Body.String())
		}
		var resp columnInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.ID == "" {
			t.Error("missing id")
		}
		if resp.Name != "Sprint" {
			t.Errorf("name = %q, want Sprint", resp.Name)
		}
		if resp.BoardID != boardID {
			t.Errorf("board_id = %q, want %q", resp.BoardID, boardID)
		}
		if resp.IsTerminal {
			t.Error("new column should not be terminal by default")
		}
	})

	// ── add column with terminal=true ────────────────────────────────────────

	t.Run("add column terminal=true", func(t *testing.T) {
		boardID := createBoard("add-term")
		rec := postColumn(boardID, map[string]interface{}{"name": "Archive", "terminal": true}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("post column: %d %s", rec.Code, rec.Body.String())
		}
		var resp columnInfo
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if !resp.IsTerminal {
			t.Error("column should be terminal")
		}
	})

	// ── rename column ─────────────────────────────────────────────────────────

	t.Run("rename column", func(t *testing.T) {
		boardID := createBoard("rename-col")
		cols, err := db.ListColumns(ctx, pool, boardID)
		if err != nil {
			t.Fatalf("ListColumns: %v", err)
		}
		colID := cols[0].ID // Backlog

		rec := patchColumn(colID, map[string]interface{}{"name": "Icebox"}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch rename: %d %s", rec.Code, rec.Body.String())
		}
		var resp columnInfo
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp.Name != "Icebox" {
			t.Errorf("name = %q, want Icebox", resp.Name)
		}
		if resp.ID != colID {
			t.Errorf("id = %q, want %q", resp.ID, colID)
		}
	})

	// ── reorder via after ────────────────────────────────────────────────────

	t.Run("reorder via after", func(t *testing.T) {
		boardID := createBoard("reorder-after")
		cols, _ := db.ListColumns(ctx, pool, boardID)
		// Default order: Backlog(0), Doing(1), Done(2)
		backlogID := cols[0].ID
		doingID := cols[1].ID
		doneID := cols[2].ID

		// Move Backlog after Done → new order: Doing, Done, Backlog
		rec := patchColumn(backlogID, map[string]interface{}{"after": doneID}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch reorder: %d %s", rec.Code, rec.Body.String())
		}

		updatedCols, _ := db.ListColumns(ctx, pool, boardID)
		if len(updatedCols) != 3 {
			t.Fatalf("want 3 cols, got %d", len(updatedCols))
		}
		if updatedCols[0].ID != doingID || updatedCols[1].ID != doneID || updatedCols[2].ID != backlogID {
			t.Errorf("order: [%s %s %s], want [%s %s %s]",
				updatedCols[0].Name, updatedCols[1].Name, updatedCols[2].Name,
				"Doing", "Done", "Backlog")
		}
	})

	// ── reorder via before ───────────────────────────────────────────────────

	t.Run("reorder via before", func(t *testing.T) {
		boardID := createBoard("reorder-before")
		cols, _ := db.ListColumns(ctx, pool, boardID)
		backlogID := cols[0].ID
		doingID := cols[1].ID
		doneID := cols[2].ID

		// Move Done before Backlog (first) → new order: Done, Backlog, Doing.
		// Only "before" is set; no "after" so prevRank="" (absolute start).
		rec := patchColumn(doneID, map[string]interface{}{"before": backlogID}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch reorder before: %d %s", rec.Code, rec.Body.String())
		}

		updatedCols, _ := db.ListColumns(ctx, pool, boardID)
		if updatedCols[0].ID != doneID || updatedCols[1].ID != backlogID || updatedCols[2].ID != doingID {
			t.Errorf("order: [%s %s %s], want [%s %s %s]",
				updatedCols[0].Name, updatedCols[1].Name, updatedCols[2].Name,
				"Done", "Backlog", "Doing")
		}
	})

	// ── toggle terminal ───────────────────────────────────────────────────────

	t.Run("toggle terminal", func(t *testing.T) {
		boardID := createBoard("toggle-term")
		cols, _ := db.ListColumns(ctx, pool, boardID)
		backlogID := cols[0].ID // Backlog is not terminal

		rec := patchColumn(backlogID, map[string]interface{}{"is_terminal": true}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch terminal: %d %s", rec.Code, rec.Body.String())
		}
		var resp columnInfo
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if !resp.IsTerminal {
			t.Error("is_terminal should be true")
		}

		// Toggle back to false.
		rec = patchColumn(backlogID, map[string]interface{}{"is_terminal": false}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch terminal false: %d %s", rec.Code, rec.Body.String())
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp.IsTerminal {
			t.Error("is_terminal should be false")
		}
	})

	// ── delete empty column → 204 ────────────────────────────────────────────

	t.Run("delete empty column → 204", func(t *testing.T) {
		boardID := createBoard("del-empty")
		addRec := postColumn(boardID, map[string]interface{}{"name": "Extra"}, nil)
		if addRec.Code != http.StatusCreated {
			t.Fatalf("add column: %d %s", addRec.Code, addRec.Body.String())
		}
		var addResp columnInfo
		json.Unmarshal(addRec.Body.Bytes(), &addResp)

		rec := deleteColumn(addResp.ID, nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete empty column: got %d, want 204; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── delete column with live ticket → 409 ─────────────────────────────────

	t.Run("delete column with live ticket → 409", func(t *testing.T) {
		boardID := createBoard("del-live")
		cols, _ := db.ListColumns(ctx, pool, boardID)
		backlogID := cols[0].ID

		_, err := db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID:  boardID,
			Title:    "live ticket",
			Repos:    []string{"repo-del-live"},
			ColumnID: &backlogID,
		})
		if err != nil {
			t.Fatalf("CreateTicket: %v", err)
		}

		rec := deleteColumn(backlogID, nil)
		if rec.Code != http.StatusConflict {
			t.Fatalf("delete with live ticket: got %d, want 409; body=%s", rec.Code, rec.Body.String())
		}
	})

	// ── unknown column → 404 ─────────────────────────────────────────────────

	t.Run("unknown column PATCH → 404", func(t *testing.T) {
		rec := patchColumn("00000000-0000-0000-0000-000000000000",
			map[string]interface{}{"name": "X"}, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("patch unknown: got %d, want 404", rec.Code)
		}
	})

	t.Run("unknown column DELETE → 404", func(t *testing.T) {
		rec := deleteColumn("00000000-0000-0000-0000-000000000000", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("delete unknown: got %d, want 404", rec.Code)
		}
	})

	// ── foreign-board token on PATCH/DELETE → 403 ────────────────────────────

	t.Run("foreign board token → 403", func(t *testing.T) {
		boardA := createBoard("tok-col-a")
		boardB := createBoard("tok-col-b")

		colsB, _ := db.ListColumns(ctx, pool, boardB)

		daemonID := "00000000-0000-0000-0000-0000000000f1"
		sessionID := "00000000-0000-0000-0000-0000000000f2"
		if err := db.UpsertDaemon(ctx, pool, daemonID, "col-tok-daemon", "local", "/repos"); err != nil {
			t.Fatalf("UpsertDaemon: %v", err)
		}
		if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Col Tok Test", ""); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		rawToken, _, err := db.MintBoardToken(ctx, pool, boardA, sessionID, []string{"board"}, time.Hour)
		if err != nil {
			t.Fatalf("MintBoardToken: %v", err)
		}

		authHeader := map[string]string{"Authorization": "Bearer " + rawToken}

		// Token for board A used against a column on board B → 403.
		rec := patchColumn(colsB[0].ID, map[string]interface{}{"name": "Hacked"}, authHeader)
		if rec.Code != http.StatusForbidden {
			t.Errorf("foreign token PATCH: got %d, want 403; body=%s", rec.Code, rec.Body.String())
		}

		// Add a column to board B, then try to delete with board A token.
		addRec := postColumn(boardB, map[string]interface{}{"name": "Extra"}, nil)
		var addResp columnInfo
		json.Unmarshal(addRec.Body.Bytes(), &addResp)

		rec = deleteColumn(addResp.ID, authHeader)
		if rec.Code != http.StatusForbidden {
			t.Errorf("foreign token DELETE: got %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})
}
