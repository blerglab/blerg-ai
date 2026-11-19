package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/ticket"
	"github.com/jackc/pgx/v5"
)

// These fields deliberately match the board's bounded metadata contract. No
// checkout path, commit body, patch or credential crosses the publication boundary.
type capturedSummary struct {
	Repository       string `json:"repository"`
	SessionID        string `json:"session_id"`
	MemberID         string `json:"member_id,omitempty"`
	Generation       int64  `json:"generation,omitempty"`
	LaunchGeneration string `json:"launch_generation,omitempty"`
	Branch           string `json:"branch"`
	Base             string `json:"base"`
	protocol.DeliveredSummary
}

type captureTarget struct {
	board  string
	number int
}

var errCapturePermission = errors.New("commit publication skipped: the person has not granted this session file or full access to the target board")

func (a *API) captureTargetAllowed(ctx context.Context, row *db.SessionRow, target captureTarget) error {
	var allowed bool
	err := a.dbPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_capture_launch_cards WHERE session_id=$1 AND board_id=$2 AND card_number=$3)`, row.ID, target.board, target.number).Scan(&allowed)
	if err != nil || allowed {
		return err
	}
	// Completion cleanup is not a person's revocation. Read the latest retained
	// permission at publication time, including changes made since delivery.
	err = a.dbPool.QueryRow(ctx, `SELECT coalesce((SELECT level IN ('file','full') AND (revoked_at IS NULL OR revoked_reason='session_ended') FROM session_proposal_grants WHERE session_id=$1 AND board_id=$2 AND kind='board_file' AND account_id=$3 ORDER BY created_at DESC,id DESC LIMIT 1),false)`, row.ID, target.board, derefOrEmpty(row.SpawningAccountID)).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return errCapturePermission
	}
	return nil
}

func (a *API) captureTargets(ctx context.Context, row *db.SessionRow, through int64) ([]captureTarget, error) {
	rows, err := db.ListTicketRows(ctx, a.dbPool, row.ID, through)
	if err != nil {
		return nil, err
	}
	seen := map[captureTarget]bool{}
	var targets []captureTarget
	add := func(board string, number int) {
		t := captureTarget{board, number}
		if board != "" && number > 0 && !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}
	for _, p := range ticket.Pinned(rows) {
		if p.Source == ticket.SourceBoard {
			if n, ok := cardNumberOf(p.Key); ok {
				add(p.BoardID, n)
			}
		}
	}
	board := derefOrEmpty(row.BoardID)
	if board == "" {
		board = a.planSessionBoard(ctx, row)
	}
	for _, n := range cardRefs(derefOrEmpty(row.Title), a.planBoardKey(ctx, row, board)) {
		add(board, n)
	}
	// Board-started sessions receive their pin through the existing launch path.
	var launch captureTarget
	err = a.dbPool.QueryRow(ctx, `SELECT board_id,card_number FROM session_capture_launch_cards WHERE session_id=$1`, row.ID).Scan(&launch.board, &launch.number)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	add(launch.board, launch.number)
	return targets, nil
}

func captureSummaries(sid string, ws *db.SessionWorkspace, generation int64) []capturedSummary {
	out := []capturedSummary{}
	if ws == nil || ws.Manifest == nil {
		return out
	}
	if generation < 1 {
		generation = 1
	} // legacy result writers predate runtime bindings
	for _, r := range ws.Results {
		if r.Pending || r.SyncError != "" || r.Summary == nil || len(r.Summary.Commits) == 0 {
			continue
		}
		for _, m := range ws.Manifest.Members {
			if m.ID != r.MemberID {
				continue
			}
			repo := m.Project
			if repo == "" {
				repo = m.Name
			}
			out = append(out, capturedSummary{Repository: repo, SessionID: sid, MemberID: m.ID, Generation: generation, LaunchGeneration: r.LaunchGeneration, Branch: r.Branch, Base: r.Base, DeliveredSummary: *r.Summary})
		}
	}
	return out
}

// Replace the cumulative snapshot for this session/member. Deduplicate delivered
// commits against retained entries, retaining other sessions' published work.
func mergeCaptured(old, incoming []capturedSummary) []capturedSummary {
	old = append([]capturedSummary(nil), old...)
	for _, next := range incoming {
		kept := old[:0]
		for _, prev := range old {
			if prev.SessionID != next.SessionID || prev.MemberID != next.MemberID || prev.LaunchGeneration != next.LaunchGeneration {
				kept = append(kept, prev)
			}
		}
		seen := map[string]bool{}
		for _, prev := range kept {
			if prev.Repository == next.Repository {
				for _, c := range prev.Commits {
					seen[c.SHA] = true
				}
			}
		}
		commits := make([]protocol.BranchCommit, 0, len(next.Commits))
		for _, c := range next.Commits {
			if !seen[c.SHA] {
				commits = append(commits, c)
				seen[c.SHA] = true
			}
		}
		next.Commits = commits
		old = kept
		if len(commits) > 0 {
			old = append(old, next)
		}
	}
	if len(old) > 20 {
		old = old[len(old)-20:]
		old[0].Truncated = true
	}
	// Keep the board's aggregate byte cap as well as its entry counts. A
	// single large snapshot is trimmed explicitly, never sent as an oversized write.
	for {
		raw, _ := json.Marshal(old)
		if len(raw) <= 120<<10 || len(old) == 0 {
			break
		}
		if len(old) > 1 {
			old = old[1:]
			old[0].Truncated = true
			continue
		}
		old[0].Truncated = true
		if len(old[0].Files) > 0 {
			old[0].Files = old[0].Files[:len(old[0].Files)-1]
		} else {
			old[0].Commits = old[0].Commits[:len(old[0].Commits)-1]
		}
	}
	return old
}

func (a *API) publishCaptured(ctx context.Context, row *db.SessionRow, target captureTarget, summaries []capturedSummary) error {
	if err := a.captureTargetAllowed(ctx, row, target); err != nil {
		return err
	}
	caller := a.sessionBoardCaller()
	if caller == nil {
		return errors.New("board unavailable")
	}
	proof, project, err := a.captureProof(ctx, row)
	if err != nil {
		return err
	}
	for _, s := range summaries {
		if project != "" && s.Repository != project {
			return errors.New("publication outside project scope")
		}
	}
	call := func(tool string, args any, read bool) (json.RawMessage, error) {
		raw, e := json.Marshal(args)
		if e != nil {
			return nil, e
		}
		return caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: target.board, SessionRef: row.ID, Tool: tool, Arguments: raw, ReadOnly: read})
	}
	raw, err := call("blerg_card_get", map[string]any{"board_id": target.board, "number": target.number}, true)
	if err != nil {
		return err
	}
	var card struct {
		Version       int               `json:"version"`
		CommitSummary []capturedSummary `json:"commit_summary"`
	}
	if e := boardToolResult(raw, &card); e != nil {
		return e
	}
	merged := mergeCaptured(card.CommitSummary, summaries)
	before, _ := json.Marshal(card.CommitSummary)
	after, _ := json.Marshal(merged)
	if bytes.Equal(before, after) {
		return nil
	}
	raw, err = call("blerg_card_update", map[string]any{"board_id": target.board, "number": target.number, "if_match": card.Version, "commit_summary": merged}, false)
	if err != nil {
		return err
	}
	var result struct {
		Number  int `json:"number"`
		Version int `json:"version"`
	}
	if e := boardToolResult(raw, &result); e != nil {
		return e
	}
	if result.Number != target.number || result.Version <= card.Version {
		return errors.New("board publication not confirmed")
	}
	return nil
}

// Publication is a server-owned completion effect, not an agent board tool.
// Its retained owner proof still has to pass core's live authorization and the
// board's write policy. Session grant cleanup must not cancel delivered work.
func (a *API) captureProof(ctx context.Context, row *db.SessionRow) (mcpgw.Proof, string, error) {
	if row == nil || row.SpawningAccountID == nil {
		return mcpgw.Proof{}, "", errors.New("publication owner unavailable")
	}
	owner := *row.SpawningAccountID
	for depth := 0; row.ParentSessionID != nil && depth < 16; depth++ {
		parent, err := db.GetSession(ctx, a.dbPool, *row.ParentSessionID)
		if err != nil || parent == nil || derefOrEmpty(parent.SpawningAccountID) != owner {
			return mcpgw.Proof{}, "", errors.New("publication owner unavailable")
		}
		row = parent
	}
	if row.ParentSessionID != nil {
		return mcpgw.Proof{}, "", errors.New("publication lineage unavailable")
	}
	kind, value, project, err := db.BoardFilingProof(ctx, a.dbPool, row.ID)
	if err != nil {
		return mcpgw.Proof{}, "", err
	}
	proof := mcpgw.ProofFromGrant(owner, kind, value)
	if !proof.Valid() {
		return mcpgw.Proof{}, "", errors.New("publication proof unavailable")
	}
	return proof, project, nil
}

// CaptureDeliveredCommits is a restart-safe sweep of persisted delivery results.
// Board I/O runs only in this bounded background job, never on a turn's path.
func (a *API) CaptureDeliveredCommits(ctx context.Context) error {
	// Small batches and SKIP LOCKED allow multiple servers without duplicate jobs.
	for range 32 {
		tx, err := a.dbPool.Begin(ctx)
		if err != nil {
			return err
		}
		var id, through, generation int64
		var sid, title string
		var board *string
		var manifest, results []byte
		var attempts int
		err = tx.QueryRow(ctx, `SELECT id,session_id::text,manifest,results,title,board_id,ticket_through,attempts,runtime_generation FROM session_commit_deliveries d WHERE completed_at IS NULL AND next_attempt_at<=now() AND NOT EXISTS(SELECT 1 FROM session_commit_deliveries older WHERE older.session_id=d.session_id AND older.id<d.id AND older.completed_at IS NULL) ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &sid, &manifest, &results, &title, &board, &through, &attempts, &generation)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return nil
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		row, err := db.GetSession(ctx, a.dbPool, sid)
		if err == nil && row == nil {
			err = errors.New("session unavailable")
		}
		ws := &db.SessionWorkspace{}
		if err == nil {
			err = json.Unmarshal(manifest, &ws.Manifest)
		}
		if err == nil {
			err = json.Unmarshal(results, &ws.Results)
		}
		var targets []captureTarget
		if err == nil {
			row.Title = &title
			row.BoardID = board
			targets, err = a.captureTargets(ctx, row, through)
		}
		if err == nil {
			summaries := captureSummaries(sid, ws, generation)
			for _, target := range targets {
				attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				e := a.publishCaptured(attemptCtx, row, target, summaries)
				cancel()
				if errors.Is(e, errCapturePermission) {
					_, e = tx.Exec(ctx, `INSERT INTO messages(session_id,kind,body,status,answered_at) VALUES($1,'update',$2,'answered',now())`, sid, errCapturePermission.Error())
				}
				err = errors.Join(err, e)
			}
		}
		attempts++
		failed := err != nil && attempts >= 5
		if failed {
			if _, noteErr := tx.Exec(ctx, `INSERT INTO messages(session_id,kind,body,status,answered_at) VALUES($1,'update',$2,'answered',now())`, sid, "Commit publication failed after five attempts. The saved workspace results remain available; no board update was confirmed."); noteErr != nil {
				_ = tx.Rollback(ctx)
				return noteErr
			}
			// Persist the final failure in the same transaction as completing the job.
			_, err = tx.Exec(ctx, `INSERT INTO session_events(session_id,type,data,seq) VALUES($1,'commit_publication_failed',$2,0)`, sid, fmt.Sprintf(`{"delivery_id":%d,"attempts":5}`, id))
			if err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
		}
		_, saveErr := tx.Exec(ctx, `UPDATE session_commit_deliveries SET attempts=$2,next_attempt_at=now()+interval '1 minute',completed_at=CASE WHEN $3 THEN now() END,failed=$4 WHERE id=$1`, id, attempts, err == nil || failed, failed)
		if saveErr == nil {
			saveErr = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if saveErr != nil {
			return saveErr
		}
		if failed {
			log.Printf("commit publication exhausted for session %s delivery %d", sid, id)
		}
	}
	return nil
}

func (a *API) RunCommitCapture(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		bounded, cancel := context.WithTimeout(ctx, time.Minute)
		err := a.CaptureDeliveredCommits(bounded)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("commit publication sweep unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
