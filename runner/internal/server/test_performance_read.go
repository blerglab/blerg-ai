package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/testtiming"
	"net/http"
	"strconv"
	"time"
)

type performanceCursor struct {
	Finished  time.Time `json:"finished_at"`
	Run       string    `json:"run_id"`
	Workspace string    `json:"workspace_id"`
	Member    string    `json:"member_id"`
}

func encodePerformanceCursor(c performanceCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodePerformanceCursor(raw string) (performanceCursor, bool) {
	var c performanceCursor
	if len(raw) > 1024 {
		return c, false
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(data, &c) != nil || c.Finished.IsZero() || !validSessionID(c.Run) || !validSessionID(c.Workspace) || !performanceText(c.Member, 128) {
		return c, false
	}
	return c, true
}

type performanceProject struct {
	Workspace string `json:"workspace_id"`
	Member    string `json:"member_id"`
	Name      string `json:"name"`
}

// Inventory is independent of the run page: busy projects cannot hide quieter
// projects from the selector. Only currently selected, owned projects appear.
func (a *API) performanceProjects(ctx context.Context, owner string, workspace *string) ([]performanceProject, bool, error) {
	projects := []performanceProject{}
	var after *string
	afterMember := ""
	for {
		rows, err := a.dbPool.Query(ctx, `SELECT DISTINCT t.workspace_id,t.member_id FROM test_runs t JOIN sessions s ON s.id=t.session_id JOIN sessions root ON root.id=t.workspace_id WHERE s.spawning_account_id=$1 AND root.spawning_account_id=$1 AND ($2::uuid IS NULL OR t.workspace_id=$2::uuid) AND t.finished_at>=clock_timestamp()-interval '180 days' AND ($3::uuid IS NULL OR (t.workspace_id,t.member_id)>($3::uuid,$4)) ORDER BY t.workspace_id,t.member_id LIMIT 201`, owner, workspace, after, afterMember)
		if err != nil {
			return nil, false, err
		}
		batch := []performanceProject{}
		for rows.Next() {
			var p performanceProject
			if err = rows.Scan(&p.Workspace, &p.Member); err != nil {
				rows.Close()
				return nil, false, err
			}
			batch = append(batch, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, false, err
		}
		if len(batch) == 0 {
			return projects, false, nil
		}
		for _, p := range batch {
			root, err := a.performanceRoot(ctx, owner, p.Workspace)
			if err != nil {
				return nil, false, err
			}
			if root == nil || root.ID != p.Workspace {
				continue
			}
			selected, err := a.performanceMember(ctx, p.Workspace, p.Workspace, p.Member)
			if err != nil {
				return nil, false, err
			}
			if !selected {
				continue
			}
			if len(projects) == 2000 {
				return projects, true, nil
			}
			p.Name = p.Member
			if p.Member == "repository" {
				p.Name = "Repository"
			}
			projects = append(projects, p)
		}
		last := batch[len(batch)-1]
		after = &last.Workspace
		afterMember = last.Member
		if len(batch) < 201 {
			return projects, false, nil
		}
	}
}

func (a *API) HandleTestPerformance(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if e := checkGrantRequester(requesterOf(p)); e != nil {
		writeAPIError(w, e)
		return
	}
	if p.Project != "" || p.Sub == "" {
		performanceFailure("test_read_refused", nil)
		writeError(w, 403, "Test history requires an account-wide sign-in.")
		return
	}
	if a.dbPool == nil {
		performanceFailure("test_database_unavailable", nil)
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	query := r.URL.Query()
	var workspace *string
	if scope := query.Get("workspace"); scope != "" {
		root, err := a.performanceRoot(ctx, p.Sub, scope)
		if err != nil {
			performanceFailure("test_workspace_unavailable", err)
			writeError(w, 503, "Test history is unavailable.")
			return
		}
		if root == nil {
			performanceFailure("test_workspace_refused", nil)
			writeError(w, 404, "Workspace not found.")
			return
		}
		workspace = &root.ID
	}
	member := query.Get("member")
	if member != "" && (workspace == nil || !performanceText(member, 128)) {
		performanceFailure("test_member_filter_invalid", nil)
		writeError(w, 400, "A member filter requires its workspace.")
		return
	}
	limit := 200
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 2000 {
			performanceFailure("test_page_limit_invalid", err)
			writeError(w, 400, "Page size must be between 1 and 2,000.")
			return
		}
		limit = n
	}
	var cursor *performanceCursor
	if raw := query.Get("cursor"); raw != "" {
		c, valid := decodePerformanceCursor(raw)
		if !valid {
			performanceFailure("test_cursor_invalid", nil)
			writeError(w, 400, "Invalid test history cursor.")
			return
		}
		cursor = &c
	}
	var projects []performanceProject
	projectsTruncated := false
	if cursor == nil {
		var err error
		projects, projectsTruncated, err = a.performanceProjects(ctx, p.Sub, workspace)
		if err != nil {
			performanceFailure("test_projects_unavailable", err)
			writeError(w, 503, "Test history is unavailable.")
			return
		}
	}
	type scopedRun struct {
		run    performanceRun
		sid    string
		cursor performanceCursor
	}
	runs := []performanceRun{}
	next := ""
	admitted := map[string]bool{}
	checked := map[string]bool{}
	var lastReturned performanceCursor
	scanLimited := false
	for scannedBatches := 0; ; scannedBatches++ {
		var finish *time.Time
		var runID, rootID, memberID *string
		if cursor != nil {
			finish = &cursor.Finished
			runID = &cursor.Run
			rootID = &cursor.Workspace
			memberID = &cursor.Member
		}
		rows, err := a.dbPool.Query(ctx, `SELECT jsonb_set(t.report,'{suites}',coalesce((SELECT jsonb_agg(s.value-'slowest') FROM jsonb_array_elements(t.report->'suites') s),'[]'::jsonb)),t.session_id::text,t.finished_at,t.run_id::text,t.workspace_id::text,t.member_id FROM test_runs t JOIN sessions s ON s.id=t.session_id JOIN sessions root ON root.id=t.workspace_id WHERE s.spawning_account_id=$1 AND root.spawning_account_id=$1 AND ($2::uuid IS NULL OR t.workspace_id=$2::uuid) AND ($3='' OR t.member_id=$3) AND t.finished_at>=clock_timestamp()-interval '180 days' AND ($4::timestamptz IS NULL OR (t.finished_at,t.run_id,t.workspace_id,t.member_id)<($4,$5::uuid,$6::uuid,$7)) ORDER BY t.finished_at DESC,t.run_id DESC,t.workspace_id DESC,t.member_id DESC LIMIT 201`, p.Sub, workspace, member, finish, runID, rootID, memberID)
		if err != nil {
			performanceFailure("test_read_unavailable", err)
			writeError(w, 503, "Test history is unavailable.")
			return
		}
		batch := []scopedRun{}
		for rows.Next() {
			var entry scopedRun
			var raw []byte
			if err = rows.Scan(&raw, &entry.sid, &entry.cursor.Finished, &entry.cursor.Run, &entry.cursor.Workspace, &entry.cursor.Member); err != nil {
				break
			}
			if err = json.Unmarshal(raw, &entry.run); err != nil {
				break
			}
			batch = append(batch, entry)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			performanceFailure("test_read_invalid", err)
			writeError(w, 503, "Test history is unavailable.")
			return
		}
		if len(batch) == 0 {
			break
		}
		for _, entry := range batch {
			key := fmt.Sprintf("%s:%s:%s", entry.sid, entry.cursor.Workspace, entry.cursor.Member)
			if !checked[key] {
				root, err := a.performanceRoot(ctx, p.Sub, entry.sid)
				if err != nil {
					performanceFailure("test_scope_unavailable", err)
					writeError(w, 503, "Test history is unavailable.")
					return
				}
				if root != nil && root.ID == entry.cursor.Workspace {
					admitted[key], err = a.performanceMember(ctx, entry.sid, entry.cursor.Workspace, entry.cursor.Member)
					if err != nil {
						performanceFailure("test_member_unavailable", err)
						writeError(w, 503, "Test history is unavailable.")
						return
					}
				}
				checked[key] = true
			}
			if !admitted[key] {
				continue
			}
			if len(runs) == limit {
				next = encodePerformanceCursor(lastReturned)
				break
			}
			entry.run.WorkspaceID = entry.cursor.Workspace
			entry.run.MemberID = entry.cursor.Member
			for i := range entry.run.Suites {
				entry.run.Suites[i].Slowest = []testtiming.TestTiming{}
				entry.run.Suites[i].FlakeReruns = nil
			}
			runs = append(runs, entry.run)
			lastReturned = entry.cursor
		}
		if next != "" || len(batch) < 201 {
			break
		}
		last := batch[len(batch)-1].cursor
		if scannedBatches == 2 {
			// Bound authorization scans even when every stored row has lost access.
			// Continue after the last examined row, including on an empty page.
			next = encodePerformanceCursor(last)
			scanLimited = true
			break
		}
		cursor = &last
	}
	if ctx.Err() != nil {
		performanceFailure("test_read_deadline", ctx.Err())
		writeError(w, 503, "Test history is unavailable.")
		return
	}
	var nextCursor *string
	if next != "" {
		nextCursor = &next
	}
	reply := map[string]any{"runs": runs, "truncated": next != "" && !scanLimited, "next_cursor": nextCursor, "scan_limited": scanLimited}
	if projects != nil {
		reply["projects"] = projects
		reply["projects_truncated"] = projectsTruncated
	}
	writeJSON(w, 200, reply)
}
