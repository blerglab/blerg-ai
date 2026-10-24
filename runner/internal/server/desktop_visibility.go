package server

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A collection response uses only the connections captured before collection.
// A newly connected desktop cannot enter a response whose final fences cover
// an earlier set of connections.
type desktopReadSnapshotKey struct{}

func withDesktopReadSnapshot(ctx context.Context, daemons []*DaemonConn) context.Context {
	allowed := make(map[*DaemonConn]bool, len(daemons))
	for _, dc := range daemons {
		allowed[dc] = true
	}
	return context.WithValue(ctx, desktopReadSnapshotKey{}, allowed)
}
func desktopInReadSnapshot(ctx context.Context, dc *DaemonConn) bool {
	allowed, limited := ctx.Value(desktopReadSnapshotKey{}).(map[*DaemonConn]bool)
	return !limited || allowed[dc]
}

// Enumerate persisted ownership before looking up any live metadata. Neither
// administrator permission nor a same-name repository is desktop authority.
func (h *Hub) ownedHostDaemons(ctx context.Context, account string) []*DaemonConn {
	account = desktopViewer(ctx, account)
	out := []*DaemonConn{}
	if account == "" || h.desktopPool == nil {
		return out
	}
	rows, err := db.ListDesktops(ctx, h.desktopPool, account, false)
	if err != nil {
		return out
	}
	for _, row := range rows {
		dc := h.GetHostDaemon(row.ID)
		if desktopInReadSnapshot(ctx, dc) && desktopConnectionMatches(dc, row, account) {
			out = append(out, dc)
		}
	}
	return out
}
func desktopConnectionMatches(dc *DaemonConn, row db.DesktopRow, account string) bool {
	if dc == nil || dc.DesktopBinding == nil || account == "" {
		return false
	}
	b := dc.DesktopBinding
	return row.OwnerID == account && b.OwnerID == account && b.DesktopID == row.ID && b.Revision == row.Revision && dc.DesktopGeneration == row.Generation && b.ExpiresAt.After(time.Now())
}
func (h *Hub) ownedHostDaemon(ctx context.Context, account, id string) *DaemonConn {
	account = desktopViewer(ctx, account)
	if h.desktopPool == nil || account == "" {
		return nil
	}
	row, err := db.GetDesktop(ctx, h.desktopPool, id)
	if err != nil || row == nil || row.OwnerID != account {
		return nil
	}
	dc := h.GetHostDaemon(id)
	if !desktopInReadSnapshot(ctx, dc) || !desktopConnectionMatches(dc, *row, account) {
		return nil
	}
	return dc
}
func (h *Hub) ownsDesktopID(ctx context.Context, account, id string) bool {
	account = desktopViewer(ctx, account)
	if h.desktopPool == nil || account == "" {
		return false
	}
	row, err := db.GetDesktop(ctx, h.desktopPool, id)
	return err == nil && row != nil && row.OwnerID == account
}
func (h *Hub) desktopSessionVisible(ctx context.Context, account string, row *db.SessionRow) bool {
	if row == nil {
		return false
	}
	if row.Runtime != nil && *row.Runtime == runnerRuntimeCluster {
		return true
	}
	// A desktop assignment transfers the machine, never prior session sources.
	return ownsSession(desktopViewer(ctx, account), row) && h.ownsDesktopID(ctx, account, row.DaemonID)
}
func (h *Hub) sessionInfoFor(ctx context.Context, row db.SessionRow, account string) protocol.SessionInfo {
	if !h.desktopSessionVisible(ctx, account, &row) {
		row.DaemonID, row.ProjectPath, row.Repo = "", "", ""
		row.ErrorReason = nil
	}
	info := sessionRowToInfo(row, account)
	// Membership and recovery locations are never borrowed from a public
	// conversation. They remain the launching person's immutable selection.
	if h.desktopPool != nil && ownsSession(account, &row) && h.desktopSessionVisible(ctx, account, &row) {
		if ws, err := db.GetSessionWorkspace(ctx, h.desktopPool, row.ID); err == nil && ws != nil {
			info.Workspace = ws.Manifest
			info.WorkspaceResults = ws.Results
		}
	}
	return info
}

// Build metadata only for the current exact owner. The final websocket filter
// also rechecks queued messages, fencing data prepared before an owner change.
func (h *Hub) broadcastOwnedDesktop(dc *DaemonConn, build func() any) {
	h.BroadcastJSONPerAccount(func(account string) any {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if dc == nil || h.desktopPool == nil || account == "" {
			return nil
		}
		row, err := db.GetDesktop(ctx, h.desktopPool, dc.ID)
		if err != nil || row == nil || !desktopConnectionMatches(dc, *row, account) {
			return nil
		}
		return build()
	})
}

var desktopSessionFields = []string{"daemon_id", "daemon_name", "project_path", "repo", "repo_path", "git_url", "repo_origins", "repo_remotes", "workspace", "workspace_results", "branch", "repos_root", "error_reason", "recovery_location"}

func clearDesktopSessionFields(m map[string]any) {
	for _, key := range desktopSessionFields {
		delete(m, key)
	}
	m["daemon_id"], m["project_path"], m["repo"] = "", "", ""
	if m["lifecycle"] != nil {
		m["workspace_results"] = []any{}
	}
}

// This is a delivery-time defense, not the collection filter. User-authored
// conversation payloads are not inspected or rewritten. Only protocol envelope
// metadata and session DTOs are subject to desktop ownership.
func (h *Hub) desktopPayloadFor(ctx context.Context, account string, raw []byte) ([]byte, bool) {
	if !desktopPayloadNeedsCheck(raw) {
		return raw, true
	}

	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, false
	}
	typ, _ := m["type"].(string)
	if typ == "desktop_ownership_changed" || typ == "desktop_inventory_invalidated" {
		return raw, true
	}
	changed := false
	if typ == "session_state_changed" || typ == "session_ended" || typ == "session_notice" {
		id, _ := m["session_id"].(string)
		visible := false
		if h.desktopPool != nil {
			row, err := db.GetSession(ctx, h.desktopPool, id)
			visible = err == nil && h.desktopSessionVisible(ctx, account, row)
		}
		if !visible {
			for _, key := range []string{"message", "error_reason", "detail", "text", "hint"} {
				if _, ok := m[key]; ok {
					delete(m, key)
					changed = true
				}
			}
		}
	}

	if kind, ok := m["kind"].(string); ok && isDesktopAutomaticEvent(kind) {
		sid, _ := m["session_id"].(string)
		if payload, ok := m["payload"].(map[string]any); ok && !h.automaticDesktopEventVisible(ctx, account, sid) {
			sanitizeDesktopAutomaticEvent(kind, payload)
			changed = true
		}
	}

	allowedDaemon := func(d map[string]any) bool {
		id, _ := d["daemon_id"].(string)
		if id == "" {
			id, _ = d["id"].(string)
		}
		if !h.ownsDesktopID(ctx, account, id) {
			return false
		}
		if revision, ok := d["ownership_revision"].(float64); ok {
			row, err := db.GetDesktop(ctx, h.desktopPool, id)
			if err != nil || row == nil || row.Revision != int64(revision) {
				return false
			}
			if generation, ok := d["connection_generation"].(string); ok && generation != row.Generation {
				return false
			}
		}
		return true
	}
	if d, ok := m["daemon"].(map[string]any); ok && !allowedDaemon(d) {
		return nil, false
	}
	if stringsID, ok := m["daemon_id"].(string); ok && stringsID != "" && typ != "session_started" && m["session_id"] == nil {
		if !allowedDaemon(m) {
			return nil, false
		}
	}
	if list, ok := m["daemons"].([]any); ok {
		kept := []any{}
		for _, v := range list {
			if d, ok := v.(map[string]any); ok && allowedDaemon(d) {
				kept = append(kept, v)
			} else {
				changed = true
			}
		}
		m["daemons"] = kept
	}
	filterSession := func(d map[string]any) {
		id, _ := d["id"].(string)
		if id == "" {
			id, _ = d["session_id"].(string)
		}
		if id == "" {
			return
		}
		if h.desktopPool == nil {
			clearDesktopSessionFields(d)
			changed = true
			return
		}
		row, err := db.GetSession(ctx, h.desktopPool, id)
		if err != nil || row == nil || !h.desktopSessionVisible(ctx, account, row) {
			clearDesktopSessionFields(d)
			changed = true
		} else if !ownsSession(account, row) || !workspacePayloadMatchesRow(d, row) {
			if _, ok := d["workspace"]; ok {
				delete(d, "workspace")
				changed = true
			}
			if _, ok := d["workspace_results"]; ok {
				delete(d, "workspace_results")
				changed = true
			}
		}
		if d["lifecycle"] != nil && d["workspace_results"] == nil {
			d["workspace_results"] = []any{}
			changed = true
		}
	}
	if d, ok := m["session"].(map[string]any); ok {
		filterSession(d)
	}
	if list, ok := m["sessions"].([]any); ok {
		for _, v := range list {
			if d, ok := v.(map[string]any); ok {
				filterSession(d)
			}
		}
	}
	if m["session_id"] != nil && (m["project_path"] != nil || m["repo"] != nil || m["daemon_id"] != nil || m["workspace"] != nil || m["workspace_results"] != nil) {
		filterSession(m)
	}

	if !changed {
		return raw, true
	}
	out, err := json.Marshal(m)
	return out, err == nil
}

func desktopScopeKey(daemons []*DaemonConn, account string) string {
	parts := []string{account}
	for _, dc := range daemons {
		if dc.DesktopBinding != nil {
			parts = append(parts, dc.ID+":"+strconv.FormatInt(dc.DesktopBinding.Revision, 10)+":"+dc.DesktopGeneration)
		}
	}
	sort.Strings(parts[1:])
	return strings.Join(parts, "\x00")
}

func (h *Hub) pickOwnedHost(ctx context.Context, account, repo, runtime string, scratch bool, requirements ...HostSelection) *DaemonConn {
	candidates := h.ownedHostDaemons(ctx, account)
	candidates = append([]*DaemonConn(nil), candidates...)
	candidates = filterOwnedCandidates(candidates, requirements)
	sort.Slice(candidates, func(i, j int) bool {
		if len(requirements) > 0 {
			a, b := hostEngineState(candidates[i], requirements[0]), hostEngineState(candidates[j], requirements[0])
			if a != b {
				return a == "available"
			}
		}
		return candidates[i].ID < candidates[j].ID
	})
	if !scratch {
		for _, dc := range candidates {
			for _, name := range dc.CheckedOutRepos() {
				if name == repo {
					return dc
				}
			}
		}
		if len(candidates) == 1 {
			return candidates[0]
		}
		return nil
	}
	if len(candidates) == 0 {
		return nil
	}
	if runtime != runnerRuntimeDaemon {
		for _, dc := range candidates {
			if dc.SandboxAvailable() && (len(requirements) == 0 || hostEngineState(dc, requirements[0]) == hostEngineState(candidates[0], requirements[0])) {
				return dc
			}
		}
	}
	return candidates[0]
}
func filterOwnedCandidates(in []*DaemonConn, requirements []HostSelection) []*DaemonConn {
	out := in[:0]
	for _, dc := range in {
		if hostMatchesSelection(dc, requirements) {
			out = append(out, dc)
		}
	}
	return out
}
func (h *Hub) ownedDaemonForRepo(ctx context.Context, account, repo string, requirements ...HostSelection) *DaemonConn {
	return h.pickOwnedHost(ctx, account, repo, "", false, requirements...)
}
func (h *Hub) ownedDaemonForScratch(ctx context.Context, account, runtime string, requirements ...HostSelection) *DaemonConn {
	return h.pickOwnedHost(ctx, account, "", runtime, true, requirements...)
}

func isDesktopAutomaticEvent(kind string) bool {
	return kind == "delegation_request" || kind == protocol.CapabilitiesKind || kind == "start_stage"
}
func sanitizeDesktopAutomaticEvent(kind string, payload map[string]any) {
	if kind == "start_stage" {
		if stages, ok := payload["stages"].([]any); ok {
			for _, value := range stages {
				if stage, ok := value.(map[string]any); ok {
					delete(stage, "detail")
					delete(stage, "hint")
					stage["label"] = "Preparing session"
				}
			}
		}
		payload["metadata_hidden"] = true
		return
	}

	if kind == protocol.CapabilitiesKind {
		for key := range payload {
			delete(payload, key)
		}
		return
	}
	for _, key := range []string{"repo", "daemon_id", "daemon_name", "repositories", "repo_names", "members", "workspace"} {
		delete(payload, key)
	}
	payload["metadata_hidden"] = true
}
func (h *Hub) automaticDesktopEventVisible(ctx context.Context, account, sid string) bool {
	if h.desktopPool == nil || account == "" {
		return false
	}
	row, err := db.GetSession(ctx, h.desktopPool, sid)
	return err == nil && row != nil && ownsSession(desktopViewer(ctx, account), row) && h.desktopSessionVisible(ctx, account, row)
}
func (h *Hub) desktopEventPayload(ctx context.Context, account, sid, kind, raw string) json.RawMessage {
	if !isDesktopAutomaticEvent(kind) || h.automaticDesktopEventVisible(ctx, account, sid) {
		return json.RawMessage(raw)
	}
	var payload map[string]any
	if json.Unmarshal([]byte(raw), &payload) != nil || payload == nil {
		return json.RawMessage(`{}`)
	}
	sanitizeDesktopAutomaticEvent(kind, payload)
	out, _ := json.Marshal(payload)
	return out
}

func (dc *DaemonConn) SetInventoryLimited(limited bool) {
	dc.liveMu.Lock()
	dc.inventoryLimited = limited
	dc.liveMu.Unlock()
}
func (dc *DaemonConn) InventoryLimited() bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.inventoryLimited
}

// Hold sorted ownership fences through a bounded HTTP write. A snapshot made
// before reassignment is never returned after the assignment commits.
func (h *Hub) deliverDesktopSnapshot(ctx context.Context, owner string, daemons []*DaemonConn, send func()) error {
	ids := make([]string, 0, len(daemons))
	for _, dc := range daemons {
		ids = append(ids, dc.ID)
	}
	return db.WithDesktopSnapshotFences(ctx, h.desktopPool, ids, func(fenced context.Context) error {
		for _, dc := range daemons {
			if h.ownedHostDaemon(fenced, owner, dc.ID) != dc {
				return errDiscoveryUnavailable
			}
		}
		send()
		return nil
	})
}

func (a *API) replayedDesktopEvent(ctx context.Context, account string, row db.AgentEventRow) protocol.AgentEvent {
	event := replayedAgentEvent(row)
	event.Payload = a.hub.desktopEventPayload(ctx, account, row.SessionID, row.Kind, row.Payload)
	return event
}

// deliverDesktopPayload linearizes the last metadata check and transport write
// against ownership changes on every Runner replica. Enqueue-time filtering
// alone cannot revoke a payload already waiting in a socket's send queue.
func (h *Hub) deliverDesktopPayload(ctx context.Context, account string, raw []byte, send func([]byte) error) error {
	if !desktopPayloadNeedsCheck(raw) {
		return send(raw)
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return errDiscoveryUnavailable
	}
	ids := map[string]bool{}
	add := func(value any) {
		if id, ok := value.(string); ok && validSessionID(id) {
			ids[id] = true
		}
	}
	add(body["daemon_id"])
	if daemon, ok := body["daemon"].(map[string]any); ok {
		add(daemon["id"])
	}
	if daemons, ok := body["daemons"].([]any); ok {
		for _, value := range daemons {
			if daemon, ok := value.(map[string]any); ok {
				add(daemon["id"])
			}
		}
	}
	if session, ok := body["session"].(map[string]any); ok {
		add(session["daemon_id"])
	}
	if sessions, ok := body["sessions"].([]any); ok {
		for _, value := range sessions {
			if session, ok := value.(map[string]any); ok {
				add(session["daemon_id"])
			}
		}
	}
	if id, ok := body["session_id"].(string); ok && h.desktopPool != nil && validSessionID(id) {
		if row, err := db.GetSession(ctx, h.desktopPool, id); err == nil && row != nil && derefOrEmpty(row.Runtime) != "cluster" {
			add(row.DaemonID)
		}
	}
	names := make([]string, 0, len(ids))
	for id := range ids {
		names = append(names, id)
	}
	return db.WithDesktopSnapshotFences(ctx, h.desktopPool, names, func(fenced context.Context) error {
		filtered, ok := h.desktopPayloadFor(fenced, account, raw)
		if !ok {
			return nil
		}
		return send(filtered)
	})
}

func desktopPayloadNeedsCheck(raw []byte) bool {
	for _, marker := range []string{`"daemon`, `"project_path"`, `"workspace"`, `"repo"`, `"git_url"`, `"branch"`, `"capabilities"`, `"delegation_request"`, `"members"`, `"start_stage"`, `"session_state_changed"`, `"session_ended"`, `"session_notice"`} {
		if bytes.Contains(raw, []byte(marker)) {
			return true
		}
	}
	return false
}

// An ownership/runtime edit cannot turn an earlier desktop result into cluster
// metadata. Compare the serialized immutable launch as well as the current row.
func workspacePayloadMatchesRow(payload map[string]any, row *db.SessionRow) bool {
	workspace, ok := payload["workspace"].(map[string]any)
	if !ok {
		return true
	}
	runtime := derefOrEmpty(row.Runtime)
	switch runtime {
	case "daemon":
		runtime = "host"
	case "docker":
		runtime = "sandbox"
	}
	return workspace["owner_id"] == derefOrEmpty(row.SpawningAccountID) && workspace["runtime"] == runtime
}
