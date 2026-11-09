package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type captureBoard struct {
	*planBoard
	summaries   []capturedSummary
	updates     int
	unavailable bool
	version     int
	lostReply   bool
}

type captureExchanger struct{ fakeBoardExchanger }

func (e *captureExchanger) ExchangeReadOnly(ctx context.Context, p mcpgw.Proof, board, ref string) (mcpgw.BoardToken, error) {
	return e.Exchange(ctx, p, board, ref)
}

func captureTransport(t *testing.T, f *planFx, b *captureBoard) *mcpgw.Gateway {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(400)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "board-fixture", "version": "1"}}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/call":
			raw, err := b.CallBoard(r.Context(), mcpgw.BoardCall{Tool: req.Params.Name, Arguments: req.Params.Arguments})
			if err != nil {
				result = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "board unavailable"}}}
			} else {
				result = raw
			}
		default:
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(up.Close)
	return mcpgw.New(mcpgw.Config{DB: f.pool, Board: &captureExchanger{}, BoardMCPURL: up.URL, Policy: netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}}})
}

func (b *captureBoard) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	if b.unavailable {
		return nil, errors.New("offline")
	}
	if c.Tool != "blerg_card_get" && c.Tool != "blerg_card_update" {
		return b.planBoard.CallBoard(ctx, c)
	}
	var args struct {
		Number  int               `json:"number"`
		Version int               `json:"if_match"`
		Summary []capturedSummary `json:"commit_summary"`
	}
	if err := json.Unmarshal(c.Arguments, &args); err != nil {
		return nil, err
	}
	if c.Tool == "blerg_card_update" {
		if args.Version != b.version {
			return nil, errors.New("version conflict")
		}
		b.summaries = args.Summary
		b.version++
		b.updates++
		if b.lostReply {
			b.lostReply = false
			return nil, errors.New("reply lost after commit")
		}
	}
	card := map[string]any{"number": args.Number, "board_id": "board-a", "title": "Parser", "version": b.version, "commit_summary": b.summaries}
	text, _ := json.Marshal(card)
	raw, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}})
	return raw, nil
}

func TestCommitCaptureAfterWorkspaceEnd(t *testing.T) {
	for _, mode := range []string{"pin", "title", "cardless", "empty", "retry", "exhausted", "lost_reply", "update", "ungranted", "lowered", "file"} {
		t.Run(mode, func(t *testing.T) {
			level := db.BoardLevelFull
			if mode == "file" {
				level = db.BoardLevelFile
			}
			f := newPlanFx(t, level)
			b := &captureBoard{planBoard: f.board, version: 1}
			f.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: captureTransport(t, f, b)})
			ctx := t.Context()
			if err := db.SaveBoardFilingProof(ctx, f.pool, f.sess, "session_id", "fixture-login", ""); err != nil {
				t.Fatal(err)
			}
			if mode != "cardless" && mode != "title" {
				board := "board-a"
				if mode == "ungranted" {
					board = "ungranted-board"
				}
				code, out := f.postWork("tickets", f.sess, f.sessTok, map[string]any{"action": "pin", "ticket": map[string]any{"source": "blerg-board", "board_id": board, "key": "#12", "title": "Parser"}})
				if code != 201 {
					t.Fatalf("pin: %d %v", code, out)
				}
			}
			if mode == "title" {
				if _, err := f.pool.Exec(ctx, `UPDATE sessions SET title='Implement #12' WHERE id=$1`, f.sess); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.pool.Exec(ctx, `UPDATE sessions SET runtime='docker',private=$2 WHERE id=$1`, f.sess, mode == "ungranted"); err != nil {
				t.Fatal(err)
			}
			manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", Runtime: "sandbox", OwnerID: topicOwner, LaunchGeneration: "delivery-launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "app", Path: ".", Project: "example-org/app", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
			if err := db.InsertSessionWorkspace(ctx, f.pool, f.sess, manifest); err != nil {
				t.Fatal(err)
			}
			sha := strings.Repeat("a", 40)
			result := protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: manifest.LaunchGeneration}, Base: strings.Repeat("b", 40), Head: sha, Branch: "wip/session", Summary: &protocol.DeliveredSummary{Commits: []protocol.BranchCommit{{SHA: sha, Message: "Delivered", Author: "Fixture", Date: "2026-11-01T00:00:00Z"}}, Files: []protocol.CommitFile{{Path: "app.go", Adds: 1}}}}
			if mode == "empty" {
				result.Summary = nil
				result.Head = result.Base
			}
			if mode == "update" {
				if err := db.SetWorkspaceResults(ctx, f.pool, f.sess, manifest.LaunchGeneration, []protocol.WorkspaceMemberResult{result}); err != nil {
					t.Fatal(err)
				}
				if err := f.api.CaptureDeliveredCommits(ctx); err != nil {
					t.Fatal(err)
				}
				if b.updates != 1 {
					t.Fatalf("turn delivery not published: %d", b.updates)
				}
				sha = strings.Repeat("c", 40)
				result.Head = sha
				result.Summary.Commits = append([]protocol.BranchCommit{{SHA: sha, Message: "Next", Author: "Fixture", Date: "2026-11-01T00:01:00Z"}}, result.Summary.Commits...)
				result.Summary.Files[0].Adds = 2
			}
			if mode == "pin" {
				if err := db.SetWorkspaceResults(ctx, f.pool, f.sess, manifest.LaunchGeneration, []protocol.WorkspaceMemberResult{result}); err != nil {
					t.Fatal(err)
				}
				code, out := f.postWork("tickets", f.sess, f.sessTok, map[string]any{"action": "unpin", "ticket": map[string]any{"source": "blerg-board", "board_id": "board-a", "key": "#12", "title": "Parser"}})
				if code != 201 {
					t.Fatalf("unpin after delivery: %d %v", code, out)
				}
			}
			// Real daemon frame decoding, trusted workspace persistence and terminal effects.
			raw, _ := json.Marshal(protocol.SessionEnded{Type: "session_ended", SessionID: f.sess, WorkspaceResults: []protocol.WorkspaceMemberResult{result}})
			dc := &DaemonConn{ID: topicDaemonID}
			handleDaemonFrame(ctx, f.api.hub, dc, f.pool, raw, envelope{Type: "session_ended", SessionID: f.sess})
			row, err := db.GetSession(ctx, f.pool, f.sess)
			if err != nil || row.EndedAt == nil {
				t.Fatalf("workspace did not end: %+v %v", row, err)
			}
			// The snapshot must survive both an unpin and a title change.
			if _, err = f.pool.Exec(ctx, `UPDATE sessions SET title='Finished' WHERE id=$1`, f.sess); err != nil {
				t.Fatal(err)
			}
			b.unavailable = mode == "retry" || mode == "exhausted"
			if mode == "lowered" {
				if _, err := f.pool.Exec(ctx, `UPDATE session_proposal_grants SET level='read' WHERE session_id=$1 AND board_id='board-a'`, f.sess); err != nil {
					t.Fatal(err)
				}
			}
			b.lostReply = mode == "lost_reply"
			if err = f.api.CaptureDeliveredCommits(ctx); err != nil {
				t.Fatal(err)
			}
			if mode == "retry" || mode == "exhausted" || mode == "lost_reply" {
				if b.updates != 0 && mode != "lost_reply" {
					t.Fatal("offline write")
				}
				rounds := 1
				if mode == "exhausted" {
					rounds = 4
				}
				for range rounds {
					if _, err = f.pool.Exec(ctx, `UPDATE session_commit_deliveries SET next_attempt_at=now() WHERE session_id=$1`, f.sess); err != nil {
						t.Fatal(err)
					}
					if mode == "retry" {
						b.unavailable = false
					}
					fresh := NewAPI(f.api.hub, f.pool, "", nil, "")
					if err = fresh.CaptureDeliveredCommits(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := 1
			adds := 1
			if mode == "update" {
				want = 2
				adds = 2
			}
			if mode == "cardless" || mode == "empty" || mode == "exhausted" || mode == "ungranted" || mode == "lowered" {
				want = 0
			}
			if b.updates != want {
				t.Fatalf("updates=%d want %d", b.updates, want)
			}
			if want > 0 && (len(b.summaries) != 1 || b.summaries[0].Commits[0].SHA != sha || b.summaries[0].Files[0].Adds != adds) {
				t.Fatalf("card metadata: %+v", b.summaries)
			}
			if err = f.api.CaptureDeliveredCommits(ctx); err != nil {
				t.Fatal(err)
			}
			if b.updates != want {
				t.Fatal("duplicate publication")
			}
			if mode == "exhausted" {
				var count int
				if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM session_events WHERE session_id=$1 AND type='commit_publication_failed'`, f.sess).Scan(&count); err != nil || count != 1 {
					t.Fatalf("failure record %d %v", count, err)
				}
			}
			if mode == "exhausted" || mode == "ungranted" || mode == "lowered" {
				var count int
				if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE session_id=$1 AND kind='update' AND body LIKE 'Commit publication%' OR session_id=$1 AND kind='update' AND body LIKE 'commit publication skipped:%'`, f.sess).Scan(&count); err != nil || count != 1 {
					t.Fatalf("visible publication note: %d %v", count, err)
				}
			}
		})
	}
}

func TestMergeCapturedIdempotentPerCommit(t *testing.T) {
	snapshot := func(sid, sha string) capturedSummary {
		return capturedSummary{Repository: "example-org/app", SessionID: sid, MemberID: "one", DeliveredSummary: protocol.DeliveredSummary{Commits: []protocol.BranchCommit{{SHA: sha}}, Files: []protocol.CommitFile{}}}
	}
	a := snapshot("session-a", strings.Repeat("a", 40))
	b := snapshot("session-b", strings.Repeat("a", 40))
	got := mergeCaptured([]capturedSummary{a}, []capturedSummary{b})
	if len(got) != 1 || got[0].SessionID != "session-a" {
		t.Fatalf("duplicate commit: %+v", got)
	}
	got = mergeCaptured(got, []capturedSummary{a})
	if len(got) != 1 || len(got[0].Commits) != 1 {
		t.Fatalf("replay duplicated: %+v", got)
	}
	a.Commits = append(a.Commits, protocol.BranchCommit{SHA: strings.Repeat("b", 40)})
	got = mergeCaptured(got, []capturedSummary{a})
	if len(got) != 1 || len(got[0].Commits) != 2 {
		t.Fatalf("snapshot not updated: %+v", got)
	}
}

func TestCommitCaptureProofAndProjectScope(t *testing.T) {
	f, board, _ := newBoardLevelFx(t)
	ctx := t.Context()
	if err := db.SaveBoardFilingProof(ctx, f.pool, f.ended, "session_id", "origin-login", "example-org/app"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, f.sess, f.ended); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(ctx, f.pool, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	proof, project, err := f.api.captureProof(ctx, row)
	if err != nil || proof.AccountID != topicOwner || proof.SessionID != "origin-login" || project != "example-org/app" {
		t.Fatalf("retained completion proof: %+v %q %v", proof, project, err)
	}
	if err := f.api.publishCaptured(ctx, row, captureTarget{"board-a", 12}, []capturedSummary{{Repository: "another-org/private"}}); err == nil || board.count() != 0 {
		t.Fatal("project scope broadened")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, f.sess, f.privSess); err != nil {
		t.Fatal(err)
	}
	row, err = db.GetSession(ctx, f.pool, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.api.captureProof(ctx, row); err == nil {
		t.Fatal("cross-owner ancestry accepted")
	}
}

func TestCommitCaptureRejectsUngrantedPin(t *testing.T) {
	f := newPlanFx(t, db.BoardLevelFull)
	ctx := t.Context()
	if err := db.SaveBoardFilingProof(ctx, f.pool, f.sess, "session_id", "fixture-login", ""); err != nil {
		t.Fatal(err)
	}
	b := &captureBoard{planBoard: f.board, version: 1}
	f.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: captureTransport(t, f, b)})
	row, err := db.GetSession(ctx, f.pool, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	err = f.api.publishCaptured(ctx, row, captureTarget{"ungranted-board", 12}, nil)
	if err == nil || b.updates != 0 {
		t.Fatal("ungranted board accepted")
	}
}

func TestCommitCaptureLaunchCardSurvivesPermissionCleanup(t *testing.T) {
	f := newPlanFx(t, "")
	ctx := t.Context()
	if err := f.api.planPinStarted(ctx, f.sess, "launch-board", 12, "Launch"); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(ctx, f.pool, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.api.captureTargetAllowed(ctx, row, captureTarget{"launch-board", 12}); err != nil {
		t.Fatal(err)
	}
	if err = f.api.captureTargetAllowed(ctx, row, captureTarget{"launch-board", 13}); err == nil {
		t.Fatal("launch authorization broadened to another card")
	}
}

// TestCommitCaptureLiveBoard drives the workspace result trigger through the
// production gateway and MCP server into a real isolated board database.
func TestCommitCaptureLiveBoard(t *testing.T) {
	f := newPlanFx(t, db.BoardLevelFull)
	ctx := t.Context()
	binary := filepath.Join(t.TempDir(), "board-capture.test")
	build := exec.CommandContext(ctx, "go", "test", "-c", "-o", binary, "./internal/mcp")
	build.Dir = "../../../board"
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build live board fixture: %v %s", err, output)
	}
	cmd := exec.CommandContext(ctx, binary, "-test.v", "-test.run=^TestCaptureBridge$")
	cmd.Env = append(os.Environ(), "BLERG_CAPTURE_BRIDGE=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = in.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	var fixture struct {
		URL    string `json:"url"`
		Board  string `json:"board"`
		Number int    `json:"number"`
	}
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		if raw, ok := strings.CutPrefix(scanner.Text(), "CAPTURE_BRIDGE "); ok {
			if err := json.Unmarshal([]byte(raw), &fixture); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if fixture.URL == "" {
		t.Fatalf("board fixture did not start: %s", stderr.String())
	}
	gateway := mcpgw.New(mcpgw.Config{DB: f.pool, Board: &liveCaptureExchanger{}, BoardMCPURL: fixture.URL, Policy: netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}}})
	f.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: gateway})
	if err := db.SaveBoardFilingProof(ctx, f.pool, f.sess, "session_id", "fixture-login", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.api.planPinStarted(ctx, f.sess, fixture.Board, fixture.Number, "Launch"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET runtime='docker' WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", Runtime: "sandbox", OwnerID: topicOwner, LaunchGeneration: "live-launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "app", Path: ".", Project: "example-org/app", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if err := db.InsertSessionWorkspace(ctx, f.pool, f.sess, manifest); err != nil {
		t.Fatal(err)
	}
	result := protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: manifest.LaunchGeneration}, Base: strings.Repeat("b", 40), Head: strings.Repeat("a", 40), Branch: "wip/session", Summary: &protocol.DeliveredSummary{Commits: []protocol.BranchCommit{{SHA: strings.Repeat("a", 40), Message: "Delivered live", Author: "Example", Date: "2026-11-01T00:00:00Z"}}, Files: []protocol.CommitFile{}}}
	if err := db.SetWorkspaceResults(ctx, f.pool, f.sess, manifest.LaunchGeneration, []protocol.WorkspaceMemberResult{result}); err != nil {
		t.Fatal(err)
	}
	if err := f.api.CaptureDeliveredCommits(ctx); err != nil {
		t.Fatal(err)
	}
	var completed bool
	if err := f.pool.QueryRow(ctx, `SELECT completed_at IS NOT NULL FROM session_commit_deliveries WHERE session_id=$1`, f.sess).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if !completed {
		row, err := db.GetSession(ctx, f.pool, f.sess)
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("live publication deferred: %v", f.api.publishCaptured(ctx, row, captureTarget{fixture.Board, fixture.Number}, []capturedSummary{{Repository: "example-org/app", SessionID: f.sess, MemberID: "one", Generation: 1, LaunchGeneration: manifest.LaunchGeneration, Branch: result.Branch, Base: result.Base, DeliveredSummary: *result.Summary}}))
	}
	if _, err := io.WriteString(in, "verify\n"); err != nil {
		t.Fatal(err)
	}
	for scanner.Scan() {
		t.Log(scanner.Text())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("live board verification: %v %s", err, stderr.String())
	}
}

type liveCaptureExchanger struct{ captureExchanger }

func (*liveCaptureExchanger) Exchange(_ context.Context, _ mcpgw.Proof, _, _ string) (mcpgw.BoardToken, error) {
	return mcpgw.BoardToken{Token: "svc-key-123", Sub: "fixture-live-board", ExpiresAt: time.Now().Add(10 * time.Minute)}, nil
}
func (e *liveCaptureExchanger) ExchangeReadOnly(ctx context.Context, p mcpgw.Proof, b, r string) (mcpgw.BoardToken, error) {
	return e.Exchange(ctx, p, b, r)
}

func TestPlanCaptureTargetsFullCardKeyTitle(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET title='Implement ENG-12' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := fx.api.captureTargets(t.Context(), row, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0] != (captureTarget{"board-a", 12}) {
		t.Fatalf("targets = %+v", targets)
	}
}
