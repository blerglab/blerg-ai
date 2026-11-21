package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func (a *API) registerInboxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/sessions/{id}/inbox", a.HandlePostInbox)
	mux.HandleFunc("GET /api/inbox", a.HandleListInbox)
	mux.HandleFunc("GET /api/inbox/{item}", a.HandleGetInbox)
	mux.HandleFunc("DELETE /api/inbox/{item}", a.HandleDeleteInbox)
	mux.HandleFunc("PUT /api/inbox/{item}/read", a.HandleInboxRead)
}

// HandlePostInbox derives the recipient from the persisted Main session. There
// is no caller-controlled recipient or delegated posting path.
func (a *API) HandlePostInbox(w http.ResponseWriter, r *http.Request) {
	tokenSession, authorized := a.authMessaging(w, r)
	if !authorized {
		return
	}
	if tokenSession == "" || tokenSession != r.PathValue("id") {
		writeError(w, 403, "Only Main’s own session credential can post to the inbox.")
		return
	}

	sid, raw, ok := a.agentSessionWrite(w, r, "inbox", (2*256+16)<<10)
	if !ok {
		return
	}
	sess, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "post to inbox", "inbox session", err)
		return
	}
	if sess == nil || sess.ParentSessionID != nil || (sess.StartedByKind != "" && sess.StartedByKind != "human") || sess.CronID != nil || sess.SpawningAccountID == nil || *sess.SpawningAccountID == "" {
		writeError(w, 403, "Only workspace Main can post to the inbox. Workers must go through Main.")
		return
	}
	var req struct {
		ClientID string `json:"client_id"`
		Subject  string `json:"subject"`
		Body     string `json:"body"`
		Kind     string `json:"kind"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeError(w, 400, "Give a subject, markdown body and kind.")
		return
	}
	if len([]rune(req.ClientID)) > 128 || strings.IndexFunc(req.ClientID, unicode.IsControl) >= 0 {
		writeError(w, 400, "Client ID must be at most 128 characters without control characters.")
		return
	}
	req.Subject = strings.TrimSpace(req.Subject)
	if req.Subject == "" || len([]rune(req.Subject)) > 200 || strings.IndexFunc(req.Subject, unicode.IsControl) >= 0 || strings.TrimSpace(req.Body) == "" || len(req.Body) > 262144 {
		writeError(w, 400, "Give a subject up to 200 characters and a markdown body up to 256 KiB.")
		return
	}
	switch req.Kind {
	case "summary", "release", "failure", "review", "important":
	default:
		writeError(w, 400, "Kind must be summary, release, failure, review or important.")
		return
	}
	item, created, err := db.InsertInboxItem(r.Context(), a.dbPool, db.InboxItem{ID: newUUID(), ClientID: req.ClientID, AccountID: *sess.SpawningAccountID, SessionID: sid, Subject: req.Subject, Body: req.Body, Kind: req.Kind})
	if err != nil {
		writeDatabaseFailure(w, "post to inbox", "inbox insert", err)
		return
	}
	if !created {
		writeJSON(w, 200, item)
		return
	}
	a.broadcastInboxChanged(*sess.SpawningAccountID)                                                                            //nolint:contextcheck // post-write invalidation uses independent bounded browser delivery
	SendPushToAccount(a.dbPool, *sess.SpawningAccountID, "Inbox", req.Subject, "/sessions/"+sid+"?inbox="+item.ID, r.Context()) //nolint:contextcheck // the request context is passed through the optional parent argument; delivery has its own bounded deadline
	writeJSON(w, 201, item)
}

// Invalidation carries no content and is sent only to the recipient. Cards are
// reconstructed from the recipient-only list, never put in a shared transcript.
func (a *API) broadcastInboxChanged(account string) {
	a.hub.BroadcastJSONPerAccount(func(viewer string) any {

		if viewer != account {
			return nil
		}
		return map[string]any{"type": "inbox_changed"}
	})
}
func (a *API) HandleListInbox(w http.ResponseWriter, r *http.Request) {
	p, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	items, err := db.ListInboxItems(r.Context(), a.dbPool, p.Account, r.URL.Query().Get("before"), r.URL.Query().Get("workspace"))
	if err != nil {
		writeDatabaseFailure(w, "load inbox", "inbox list", err)
		return
	}
	unread, err := db.CountInboxUnread(r.Context(), a.dbPool, p.Account, r.URL.Query().Get("workspace"))
	if err != nil {
		writeDatabaseFailure(w, "count inbox items", "inbox unread count", err)
		return
	}
	next := ""
	if len(items) == 100 {
		next = items[len(items)-1].ID
	}
	writeJSON(w, 200, map[string]any{"items": items, "unread": unread, "next": next})
}
func (a *API) HandleGetInbox(w http.ResponseWriter, r *http.Request) {
	p, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	item, err := db.GetInboxItem(r.Context(), a.dbPool, p.Account, r.PathValue("item"), r.URL.Query().Get("workspace"))
	if err != nil {
		writeDatabaseFailure(w, "read inbox item", "inbox get", err)
		return
	}
	if item == nil {
		writeError(w, 404, "Inbox item not found.")
		return
	}
	writeJSON(w, 200, item)
}
func (a *API) HandleInboxRead(w http.ResponseWriter, r *http.Request) {
	p, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	var req struct {
		Read *bool `json:"read"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || req.Read == nil {
		writeError(w, 400, "Give a read state.")
		return
	}
	item, err := db.SetInboxRead(r.Context(), a.dbPool, p.Account, r.PathValue("item"), *req.Read, r.URL.Query().Get("workspace"))
	if err != nil {
		writeDatabaseFailure(w, "update inbox item", "inbox read state", err)
		return
	}
	if item == nil {
		writeError(w, 404, "Inbox item not found.")
		return
	}
	a.broadcastInboxChanged(p.Account) //nolint:contextcheck // post-write invalidation uses independent bounded browser delivery
	writeJSON(w, 200, item)
}

func (a *API) HandleDeleteInbox(w http.ResponseWriter, r *http.Request) {
	p, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	deleted, err := db.DeleteInboxItem(r.Context(), a.dbPool, p.Account, r.PathValue("item"), r.URL.Query().Get("workspace"))
	if err != nil {
		writeDatabaseFailure(w, "delete inbox item", "inbox delete", err)
		return
	}
	if !deleted {
		writeError(w, 404, "Inbox item not found.")
		return
	}
	a.broadcastInboxChanged(p.Account) //nolint:contextcheck // post-write invalidation uses independent bounded browser delivery
	writeJSON(w, 200, map[string]bool{"deleted": true})
}
