package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// An agent gets a bounded page of data, never a proposal approval surface.
func (a *API) agentBlackboardID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if id := r.URL.Query().Get("blackboard"); id != "" {
		if len(id) > 100 {
			writeError(w, 400, "Invalid blackboard ID.")
			return "", false
		}
		return id, true
	}
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return "", false
	}
	sid, ok := a.authMessaging(w, r)
	if !ok {
		return "", false
	}
	if sid == "" || sid != r.PathValue("session") {
		writeError(w, 403, "Use this session's own credential.")
		return "", false
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		blackboardError(w, err)
		return "", false
	}
	if row == nil || row.SpawningAccountID == nil {
		writeError(w, 404, "Session not found.")
		return "", false
	}
	ancestors := []string{}
	current := row
	for i := 0; i < 100 && current != nil; i++ {
		if !ownsSession(*row.SpawningAccountID, current) {
			break
		}
		ancestors = append(ancestors, current.ID)
		if current.ParentSessionID == nil {
			break
		}
		current, err = db.GetSession(r.Context(), a.dbPool, *current.ParentSessionID)
		if err != nil {
			blackboardError(w, err)
			return "", false
		}
	}
	rows, err := a.dbPool.Query(r.Context(), `SELECT id FROM blackboards WHERE record->>'workspace_id'=ANY($1::text[]) OR id IN (SELECT blackboard_id FROM blackboard_links WHERE workspace_id=ANY($1::text[])) ORDER BY id LIMIT 2`, ancestors)
	if err != nil {
		blackboardError(w, err)
		return "", false
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		blackboardError(w, err)
		return "", false
	}
	if len(ids) == 0 {
		writeError(w, 404, "This workspace has no blackboard.")
		return "", false
	}
	if len(ids) > 1 {
		writeError(w, 409, "Choose a blackboard with --blackboard.")
		return "", false
	}
	return ids[0], true
}
func agentMilestone(record blackboard.Record, selector string) (string, error) {
	if selector == "" {
		for _, m := range record.Milestones {
			if m.FrozenAt == nil {
				return m.ID, nil
			}
		}
		if len(record.Milestones) > 0 {
			return record.Milestones[len(record.Milestones)-1].ID, nil
		}
		return "", nil
	}
	found := ""
	for _, m := range record.Milestones {
		if m.ID == selector || m.Name == selector {
			if found != "" {
				return "", blackboard.ErrConflict
			}
			found = m.ID
		}
	}
	if found == "" {
		return "", blackboard.ErrNotFound
	}
	return found, nil
}
func (a *API) HandleBlackboardAgent(w http.ResponseWriter, r *http.Request) {
	bid, ok := a.agentBlackboardID(w, r)
	if !ok {
		return
	}
	record, row, ok := a.blackboardSessionAccess(w, r, bid)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		a.reconcileBlackboardCuts(r.Context(), record)
		var err error
		record, err = a.blackboardStore().LoadPartition(r.Context(), bid, blackboard.Partition{Kind: blackboard.PartitionCore})
		if err != nil {
			blackboardError(w, err)
			return
		}
	}
	mid, err := agentMilestone(record, r.URL.Query().Get("milestone"))
	if err != nil {
		blackboardError(w, err)
		return
	}
	if r.Method == http.MethodPost {
		if r.URL.Query().Get("operation") == "withdraw" {
			if row.ParentSessionID != nil || blackboard.WorkspaceKey(record) != row.ID {
				writeError(w, 403, "Only workspace Main can withdraw its plan proposal.")
				return
			}
			var body struct {
				ProposalID blackboard.ProposalID `json:"proposal_id"`
			}
			if !decodeBlackboard(w, r, &body) {
				return
			}
			if body.ProposalID == "" || len(body.ProposalID) > 100 {
				writeError(w, 400, "A proposal ID is required.")
				return
			}
			var proposalMid string
			if err := a.dbPool.QueryRow(r.Context(), `SELECT COALESCE(milestone_id,'') FROM plan_proposals WHERE blackboard_id=$1 AND id=$2`, bid, body.ProposalID).Scan(&proposalMid); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					err = blackboard.ErrNotFound
				}
				blackboardError(w, err)
				return
			}
			if err := (blackboard.Service{Store: a.blackboardStore(), BlackboardID: bid}).Withdraw(r.Context(), blackboard.Actor{ID: row.ID, Kind: "agent"}, proposalMid, body.ProposalID); err != nil {
				blackboardError(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"untrusted_data": map[string]any{"proposal_id": body.ProposalID, "status": "withdrawn"}})
			return
		}
		if op := r.URL.Query().Get("operation"); op != "" && op != "propose" {
			failurelog.Default.Event("blackboard_commit_assertion", slog.LevelWarn, "component", "runner", "stage", "commit_assertion", "cause", failurelog.Cause(blackboard.ErrForbidden))
			err := (blackboard.Service{Store: a.blackboardStore(), BlackboardID: bid}).RecordCommitAssertion(r.Context(), blackboard.Actor{ID: row.ID, Kind: "agent"}, mid)
			if err != nil {
				failurelog.Default.Event("blackboard_assertion_record", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
			}
			writeError(w, 403, "Agents can propose; only a person commits a plan.")
			return
		}
		var body struct {
			Changes []blackboard.Change `json:"changes"`
		}
		if !decodeBlackboard(w, r, &body) {
			return
		}
		id, err := (blackboard.Service{Store: a.blackboardStore(), BlackboardID: bid}).Propose(r.Context(), blackboard.Actor{ID: row.ID, Kind: "agent"}, mid, body.Changes)
		if err != nil {
			blackboardError(w, err)
			return
		}
		var revision int
		if err = a.dbPool.QueryRow(r.Context(), `SELECT revision FROM plan_proposals WHERE blackboard_id=$1 AND id=$2`, bid, id).Scan(&revision); err != nil {
			blackboardError(w, err)
			return
		}
		writeJSON(w, 201, map[string]any{"untrusted_data": map[string]any{"proposal_id": id, "status": "pending", "revision": revision}})
		return
	}
	op := r.URL.Query().Get("operation")
	if op == "" {
		op = "show"
	}
	if op != "show" && op != "history" {
		writeError(w, 400, "Choose show or history.")
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 1000000 {
			writeError(w, 400, "Invalid plan cursor.")
			return
		}
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, 400, "Choose 1–100 rows.")
			return
		}
	}
	workspace := record.WorkspaceID
	if workspace == "" && len(record.Links) > 0 {
		workspace = record.Links[0].WorkspaceID
	}
	service := a.blackboardSessionService(record, row.ID, workspace)
	data := map[string]any{"blackboard_id": bid, "name": record.Name, "vision": record.Vision, "plan_revision": record.PlanRevision}
	page := []any{}
	total := 0
	add := func(v any) {
		if total >= offset && len(page) < limit {
			page = append(page, v)
		}
		total++
	}
	if op == "show" {
		view, err := service.Slice(r.Context(), mid)
		if err != nil {
			blackboardError(w, err)
			return
		}
		view.Milestone.Frozen = nil
		data["milestone"] = view.Milestone
		data["counts"] = view.Counts
		data["complete"] = view.Complete
		data["observed_at"] = view.ObservedAt
		pending, err := a.blackboardStore().PendingProposals(r.Context(), record.ID)
		if err != nil {
			blackboardError(w, err)
			return
		}
		data["pending_proposals"] = pending
		for _, g := range record.Goals {
			add(map[string]any{"kind": "goal", "goal": g})
		}
		for _, t := range record.Thrusts {
			if t.MilestoneID != mid {
				continue
			}
			counts := map[string]int{}
			for _, c := range view.Cards {
				if c.Membership.ThrustID == t.ID {
					counts[c.Stage]++
					counts["all"]++
				}
			}
			add(map[string]any{"kind": "thrust", "thrust": t, "counts": counts})
		}
		for _, c := range view.Cards {
			add(map[string]any{"kind": "item", "item": c})
		}
	} else {
		cardBoard, cardNumber := "", 0
		if raw := r.URL.Query().Get("card"); raw != "" {
			var valid bool
			cardBoard, cardNumber, valid = parsePlanCard(raw)
			if !valid {
				writeError(w, 400, "Use board#number for a card.")
				return
			}
		}
		history, err := a.blackboardStore().LoadHistoryPage(r.Context(), record.ID, mid, cardBoard, cardNumber, offset, limit)
		if err != nil {
			blackboardError(w, err)
			return
		}
		total = history.Total
		events, snapshots := history.Events, history.Snapshots
		record.Proposals = history.Proposals
		for _, e := range events {
			changes := []blackboard.Change{}
			if e.Kind == "plan_set" {
				var payload struct {
					ID blackboard.ProposalID `json:"proposal_id"`
				}
				_ = json.Unmarshal(e.Payload, &payload)
				for _, q := range record.Proposals {
					if q.ID != payload.ID || q.MilestoneID != e.MilestoneID {
						continue
					}
					e.MetadataOmitted = e.MetadataOmitted || q.MetadataOmitted
					selected := map[blackboard.ItemID]bool{}
					for _, id := range q.Selected {
						selected[id] = true
					}
					for _, change := range q.Changes {
						if selected[change.ID] {
							changes = append(changes, change)
						}
					}
				}
			}

			if cardBoard != "" {
				filtered := changes[:0]
				for _, c := range changes {
					if c.Item.BoardID == cardBoard && c.Item.CardNumber == cardNumber {
						filtered = append(filtered, c)
					}
				}
				changes = filtered
			}
			page = append(page, map[string]any{"kind": "event", "event": e, "changes": changes})
		}
		if cardBoard == "" {
			for _, v := range snapshots {
				page = append(page, map[string]any{"kind": "snapshot", "snapshot": v})
			}
		}
	}
	data["rows"] = page
	next := ""
	if offset+len(page) < total {
		next = strconv.Itoa(offset + len(page))
	}
	truncated := false
	// Imported records can have larger metadata than browser-created records.
	// Check metadata independently: replacing the last row cannot shrink a large
	// vision or milestone name. Preserve bounded rows when omitting that metadata.
	data["rows"] = []any{}
	metadata, _ := json.Marshal(data)
	data["rows"] = page
	if len(metadata) > (64<<10)-1024 {
		truncated = true
		data = map[string]any{"blackboard_id": bid, "metadata_omitted": true, "rows": page}
	}
	// Bound the server response too. A smaller page resumes at the first omitted row.
	for {
		encoded, _ := json.Marshal(data)
		if len(encoded) <= (64<<10)-1024 {
			break
		}
		truncated = true
		if len(page) == 0 {
			data = map[string]any{"blackboard_id": bid, "message": "Record metadata exceeds the response limit."}
			break
		}
		if len(page) == 1 {
			page[0] = map[string]any{"kind": "oversized", "message": "This entry exceeds the response limit."}
			data["rows"] = page
			break
		}
		page = page[:len(page)-1]
		data["rows"] = page
		next = strconv.Itoa(offset + len(page))
	}
	writeJSON(w, 200, map[string]any{"untrusted_data": data, "next_cursor": next, "total": total, "truncated": truncated})
}
func parsePlanCard(raw string) (string, int, bool) {
	board, number, ok := strings.Cut(raw, "#")
	n, err := strconv.Atoi(number)
	return board, n, ok && board != "" && n > 0 && err == nil
}
