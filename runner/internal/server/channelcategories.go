package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
)

type channelCategory struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Sessions []string `json:"sessions"`
}
type categoryState struct {
	Categories []channelCategory `json:"categories"`
}
type categoryAction struct {
	Action  string `json:"action"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Session string `json:"session"`
	Before  string `json:"before"`
}

func (a *API) RegisterCategoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/sessions/{id}/channels", a.HandlePostChannel)
	a.registerChannelRecommendationRoutes(mux)
	a.registerBlackboardRecommendationRoutes(mux)
	mux.HandleFunc("GET /api/sessions/{id}/categories", a.HandleCategories)
	mux.HandleFunc("POST /api/sessions/{id}/categories", a.HandleCategories)
}

// HandleCategories permits the workspace owner or Main's own messaging credential.
// Action-based writes are serialized on the root row, avoiding stale client replacements.
func (a *API) HandleCategories(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		writeError(w, 404, "workspace not found")
		return
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "read the workspace", "categories", err)
		return
	}
	if p, ok := a.verifyBrowserToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); ok {
		if !ownsSession(p.Sub, row) {
			writeError(w, 404, "workspace not found")
			return
		}
	} else {
		tokenSession, ok := a.authMessaging(w, r)
		if !ok {
			return
		}
		if tokenSession != sid {
			writeError(w, 403, "only Main can organize this workspace")
			return
		}
	}
	if row == nil || row.ParentSessionID != nil {
		writeError(w, 404, "workspace not found")
		return
	}
	var req categoryAction
	if r.Method == http.MethodPost {
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
			writeError(w, 400, "invalid category request")
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Action == "create" || req.Action == "rename" {
			if req.Name == "" || utf8.RuneCountInString(req.Name) > 80 || strings.ContainsAny(req.Name, "\n\r\x00") {
				writeError(w, 400, "use a category name of 1 to 80 characters")
				return
			}
		}
	}
	ctx := r.Context()
	var reader categoryReader = a.dbPool
	var tx pgx.Tx
	if r.Method == http.MethodPost {
		tx, err = a.dbPool.Begin(ctx)
		if err != nil {
			writeDatabaseFailure(w, "save categories", "begin categories", err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		// Serialize organization changes without blocking child foreign-key inserts.
		if _, err = tx.Exec(ctx, `SELECT id FROM sessions WHERE id=$1 FOR NO KEY UPDATE`, sid); err != nil {
			writeDatabaseFailure(w, "save categories", "lock categories", err)
			return
		}
		reader = tx
	}
	state, members, err := readCategoryState(ctx, reader, sid, row.SpawningAccountID)
	if err != nil {
		writeDatabaseFailure(w, "read categories", "load categories", err)
		return
	}
	if r.Method == http.MethodPost {
		index := slices.IndexFunc(state.Categories, func(c channelCategory) bool { return c.ID == req.ID })
		if req.Action != "create" && (req.Action != "file" || req.ID != "") && index < 0 {
			writeError(w, 404, "category not found")
			return
		}
		switch req.Action {
		case "create":
			if len(state.Categories) >= 100 {
				writeError(w, 400, "a workspace can have up to 100 categories")
				return
			}
			state.Categories = append(state.Categories, channelCategory{ID: newUUID(), Name: req.Name, Sessions: []string{}})
		case "rename":
			state.Categories[index].Name = req.Name
		case "delete":
			state.Categories = slices.Delete(state.Categories, index, index+1)
		case "move":
			if req.Before == req.ID {
				break
			}
			item := state.Categories[index]
			state.Categories = slices.Delete(state.Categories, index, index+1)
			before := slices.IndexFunc(state.Categories, func(c channelCategory) bool { return c.ID == req.Before })
			if before < 0 {
				before = len(state.Categories)
			}
			state.Categories = slices.Insert(state.Categories, before, item)
		case "file":
			if req.Session == sid {
				writeError(w, 400, "Main stays at the top")
				return
			}
			if !validSessionID(req.Session) {
				writeError(w, 404, "channel not found")
				return
			}
			if !members[req.Session] {
				writeError(w, 404, "channel not found")
				return
			}
			if index >= 0 && req.Before == req.Session && slices.Contains(state.Categories[index].Sessions, req.Session) {
				break
			}
			for i := range state.Categories {
				state.Categories[i].Sessions = slices.DeleteFunc(state.Categories[i].Sessions, func(id string) bool { return id == req.Session })
			}
			if index >= 0 {
				ids := state.Categories[index].Sessions
				before := slices.Index(ids, req.Before)
				if before < 0 {
					before = len(ids)
				}
				state.Categories[index].Sessions = slices.Insert(ids, before, req.Session)
			}
		default:
			writeError(w, 400, "unknown category action")
			return
		}
		raw, err := json.Marshal(state)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO workspace_channel_categories(session_id,state) VALUES($1,$2) ON CONFLICT(session_id) DO UPDATE SET state=EXCLUDED.state`, sid, raw)
		}
		if err != nil {
			writeDatabaseFailure(w, "save categories", "save categories", err)
			return
		}
	}
	if tx != nil {
		if err = tx.Commit(ctx); err != nil {
			writeDatabaseFailure(w, "save categories", "commit categories", err)
			return
		}
	}
	writeJSON(w, 200, state)
}

// Reads never lock or mutate the workspace. Stale IDs are omitted from responses;
// the next action also persists the pruned state under its write lock.
type categoryReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func readCategoryState(ctx context.Context, reader categoryReader, root string, owner *string) (categoryState, map[string]bool, error) {
	state := categoryState{Categories: []channelCategory{}}
	var raw []byte
	err := reader.QueryRow(ctx, `SELECT COALESCE((SELECT state FROM workspace_channel_categories WHERE session_id=$1), '{"categories":[]}'::jsonb)`, root).Scan(&raw)
	if err != nil {
		return state, nil, err
	}
	if err = json.Unmarshal(raw, &state); err != nil {
		return state, nil, err
	}
	// UNION terminates even when stored ancestry is cyclic.
	rows, err := reader.Query(ctx, `WITH RECURSIVE descendants AS (
 SELECT id FROM sessions WHERE parent_session_id=$1
 UNION SELECT s.id FROM sessions s JOIN descendants d ON s.parent_session_id=d.id
 ) SELECT s.id FROM descendants d JOIN sessions s ON s.id=d.id
 WHERE s.id<>$1 AND s.discard_requested_at IS NULL AND s.spawning_account_id IS NOT DISTINCT FROM $2`, root, owner)
	if err != nil {
		return state, nil, err
	}
	defer rows.Close()
	members := make(map[string]bool)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return state, nil, err
		}
		members[id] = true
	}
	if err = rows.Err(); err != nil {
		return state, nil, err
	}
	for i := range state.Categories {
		state.Categories[i].Sessions = slices.DeleteFunc(state.Categories[i].Sessions, func(id string) bool { return !members[id] })
	}
	return state, members, nil
}
