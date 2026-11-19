//go:build linux || darwin

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type codeSyncBoard struct {
	*planBoard
	card  liveBoardCard
	calls []string
	count int
}

func (b *codeSyncBoard) CallBoard(_ context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	b.calls = append(b.calls, c.Tool)
	var args map[string]any
	if err := json.Unmarshal(c.Arguments, &args); err != nil {
		return nil, err
	}
	var out any = map[string]any{}
	switch c.Tool {
	case "blerg_column_list":
		out = []map[string]any{{"id": "done", "name": "Done", "is_terminal": true}}
	case "blerg_release_list":
		out = []any{}
	case "blerg_card_search":
		cards := []liveBoardCard{b.card}
		next := ""
		if b.count > 0 {
			offset, _ := strconv.Atoi(fmt.Sprint(args["cursor"]))
			cards = nil
			for i := offset; i < min(offset+100, b.count); i++ {
				c := b.card
				c.Number = 528 + i
				c.Reference = fmt.Sprintf("BLG-%d", c.Number)
				cards = append(cards, c)
			}
			if offset+100 < b.count {
				next = strconv.Itoa(offset + 100)
			}
		}
		out = map[string]any{"cards": cards, "next_cursor": next}
	case "blerg_card_get":
		out = b.card
		if b.count > 0 {
			c := b.card
			c.Number = int(args["number"].(float64))
			c.Reference = fmt.Sprintf("BLG-%d", c.Number)
			out = c
		}
	case "blerg_card_comment":
		b.card.Version++
	case "blerg_card_move":
		if args["if_match"] != float64(b.card.Version) {
			return nil, fmt.Errorf("stale fixture move")
		}
		col := "done"
		b.card.ColumnID = &col
		b.card.ColumnName = "Done"
		b.card.Version++
	case "blerg_card_update":
		if args["if_match"] != float64(b.card.Version) {
			return nil, fmt.Errorf("stale fixture update")
		}
		b.card.Tags = nil
		for _, tag := range args["tags"].([]any) {
			b.card.Tags = append(b.card.Tags, tag.(string))
		}
		b.card.Version++
	default:
		return nil, fmt.Errorf("unexpected fixture tool")
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(raw)}}})
}

func TestBoardCodeMainTurnReconcilesFromBoundHistory(t *testing.T) {
	testBoardCodeTurn(t, boardCodeTurnOptions{})
}
func TestBoardCodeMainTurnReconcilesLegacyWorkspace(t *testing.T) {
	testBoardCodeTurn(t, boardCodeTurnOptions{legacy: true})
}
func TestBoardCodeTurnEndToDaemonGitToCardMove(t *testing.T) {
	testBoardCodeTurn(t, boardCodeTurnOptions{realGit: true})
}

type boardCodeLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *boardCodeLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *boardCodeLogBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type boardCodeSender func(any) error

func (s boardCodeSender) Send(msg any) error { return s(msg) }

type boardCodeTurnOptions struct {
	legacy, realGit, mismatch, attachment bool
	level                                 string
	count                                 int
	stalled, checklist, branchActivity    bool
}

func TestBoardCodeMemberMismatchIsVisible(t *testing.T) {
	testBoardCodeTurn(t, boardCodeTurnOptions{mismatch: true})
}
func TestBoardCodeReaderAttachmentHistory(t *testing.T) {
	testBoardCodeTurn(t, boardCodeTurnOptions{attachment: true})
}

func TestBoardCodeReadAccessDoesNotWrite(t *testing.T) {
	testBoardCodeTurn(t, boardCodeTurnOptions{level: db.BoardLevelRead})
}
func testBoardCodeTurn(t *testing.T, options boardCodeTurnOptions) {
	legacy := options.legacy
	level := options.level
	if level == "" {
		level = db.BoardLevelFull
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	fx := newPlanFx(t, level)
	var logs boardCodeLogBuffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(oldLogger)
	deploySHA := fixtureSHA
	var host *daemon.AgentHost
	root := t.TempDir()
	actual := options.realGit
	if actual {
		git := func(args ...string) string {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v", args, err)
			}
			return strings.TrimSpace(string(out))
		}
		git("init", "-b", "main")
		git("commit", "--allow-empty", "-m", "Base")
		git("checkout", "-b", "integration")
		git("checkout", "-b", "feature")
		git("commit", "--allow-empty", "-m", "Implementation")
		git("checkout", "integration")
		git("merge", "--no-ff", "feature", "-m", "Merge BLG-529: supporting increment")
		git("checkout", "main")
		git("merge", "--no-ff", "integration", "-m", "Merge BLG-528: reconciliation")
		deploySHA = git("rev-parse", "HEAD")
	}

	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if options.stalled {
		at = time.Now().UTC().Add(-time.Hour)
	}
	board := &codeSyncBoard{planBoard: newPlanBoard(), card: liveBoardCard{ID: "card", BoardID: "board-a", Reference: "BLG-528", Number: 528, Version: 3, ColumnName: "In progress", Repos: []string{"blerg"}}}
	if options.count > 0 {
		board.count = options.count
		board.card.ColumnName = "Done"
		col := "done"
		board.card.ColumnID = &col
		board.card.Tags = []string{"to-test"}
	}
	if options.mismatch {
		board.card.Repos = []string{"unmatched"}
	}
	board.card.Columns = append(board.card.Columns, struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsTerminal bool   `json:"is_terminal"`
	}{"done", "Done", true})
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &planBoardList{}, Board: board})
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", Runtime: "host", OwnerID: topicOwner, LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "app", Name: "blerg", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if options.attachment {
		manifest.LaunchGeneration = "legacy-reader"
	}
	if !legacy {
		if err := db.InsertSessionWorkspace(t.Context(), fx.pool, fx.sess, manifest); err != nil {
			t.Fatal(err)
		}
		generation, err := db.BeginWorkspaceRuntime(t.Context(), fx.pool, fx.sess, manifest.LaunchGeneration, 0)
		if err != nil {
			t.Fatal(err)
		}
		ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}
		binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, ManifestDigest: "digest", Root: protocol.WorkspaceRoot{Device: 1, Inode: 1}}
		if actual {
			info, err := os.Stat(root)
			if err != nil {
				t.Fatal(err)
			}
			st := info.Sys().(*syscall.Stat_t)
			device := uint64(st.Dev) //nolint:unconvert // Darwin uses a signed device.
			binding.Root = protocol.WorkspaceRoot{Device: device, Inode: st.Ino}
			reader, err := daemon.NewLocalExplorer(root, manifest, binding, func() bool { return true })
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			host = daemon.NewAgentHost(boardCodeSender(func(msg any) error {
				res, ok := msg.(protocol.CommitResult)
				if !ok {
					return fmt.Errorf("unexpected daemon reply")
				}
				fx.api.hub.deliverCommitResult(fx.api.hub.FindDaemonForSession(fx.sess), res)
				return nil
			}), daemon.AgentHostConfig{HomeDir: t.TempDir()})
			host.BindWorkspaceExplorer(fx.sess, reader)
		}
		if err := db.BindWorkspaceRuntime(t.Context(), fx.pool, fx.sess, ref, binding); err != nil {
			t.Fatal(err)
		}
	} else if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET repo='blerg' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	fixtureOwnDesktop(t, fx.pool, dc, topicOwner)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	if !legacy {
		dc.AuthorizeWorkspaceRuntime(fx.sess, protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: 1})
	}
	merge := protocol.CodeCommit{SHA: fixtureSHA, Date: at.Format(time.RFC3339), Subject: "Merge BLG-528", Parents: []string{"one", "two"}, Deployed: true}
	activity := []protocol.CodeActivity{}
	if options.stalled {
		merge = protocol.CodeCommit{SHA: fixtureSHA, Subject: "Base", Date: at.Add(-5 * time.Hour).Format(time.RFC3339)}
	}
	if options.branchActivity {
		activity = append(activity, protocol.CodeActivity{Subject: "BLG-528 worker branch activity", Date: at.Add(30 * time.Minute).Format(time.RFC3339)})
	}
	if options.checklist {
		fx.checklistSet("Resume ENG-528")
	}
	fx.event("mark", `{"kind":"milestone","text":"Deployed: staging","commits":[{"sha":"`+deploySHA+`"}]}`)
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case raw := <-dc.send:
				var frame struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(raw, &frame)
				if frame.Type != "read_commit" {
					continue
				}
				var req protocol.ReadCommit
				if err := json.Unmarshal(raw, &req); err != nil {
					done <- err
					return
				}
				if !req.BoardHistory || req.SHA != deploySHA || (!legacy && req.MemberID != "app") || (legacy && !req.IsZero()) {
					done <- fmt.Errorf("wrong bound history request: %+v", req)
					return
				}
				if actual {
					host.ReadCommit(ctx, req)
					done <- nil
					return
				}
				fx.api.hub.deliverCommitResult(dc, protocol.CommitResult{RequestID: req.RequestID, SessionID: fx.sess, WorkspaceGitRef: req.WorkspaceGitRef, OK: true, CodeHistory: &protocol.CodeHistory{Activity: activity, ActivityComplete: true, Tip: fixtureSHA, Commits: []protocol.CodeCommit{merge}}})
				done <- nil
				return
			case <-ctx.Done():
				done <- ctx.Err()
				return
			}
		}
	}()
	// Disabling conversational nudges does not disable board reconciliation.
	if err := db.SetPlanNudges(t.Context(), fx.pool, fx.sess, options.stalled); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	HandleAgentEvent(ctx, fx.api.hub, fx.pool, dc, protocol.AgentEvent{Type: "agent_event", SessionID: fx.sess, ClientEventID: newUUID(), Kind: "turn_done", Payload: json.RawMessage(`{"stop_reason":"end_turn"}`), Ts: at.Add(time.Hour).Format(time.RFC3339Nano)})
	fx.api.planFlights.mu.Lock()
	finished := fx.api.planFlights.done[fx.sess]
	fx.api.planFlights.mu.Unlock()
	if finished == nil {
		t.Fatal("turn-end hook did not run")
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("turn-end reconciliation did not finish")
	}

	if options.stalled {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		facts, err := db.PendingPlanFacts(t.Context(), fx.pool, fx.sess)
		if err != nil {
			t.Fatal(err)
		}
		stalled := false
		for _, f := range facts {
			if strings.Contains(f.Text, "invariant 2") {
				stalled = true
			}
		}
		if stalled != (!options.checklist && !options.branchActivity) {
			t.Fatalf("stalled=%t checklist=%t branch activity=%t", stalled, options.checklist, options.branchActivity)
		}
		return
	}

	if options.count > 0 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(started)
		t.Logf("600-card turn-end: %d Board calls, %s elapsed (canned Board transport, real runner database and bound daemon protocol)", len(board.calls), elapsed)
		if len(board.calls) != 8 {
			t.Fatalf("Board call budget: got %d want 8", len(board.calls))
		}
		return
	}

	if options.mismatch || level == db.BoardLevelRead {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		outcome := "no_changes"
		if options.mismatch {
			outcome = "repository_name_unmatched"
		}
		if board.card.ColumnName == "Done" || slices.Contains(board.card.Tags, "to-test") || !strings.Contains(logs.String(), `"outcome":"`+outcome+`"`) || !strings.Contains(logs.String(), `"writes":0`) {
			t.Fatalf("unexpected write or missing outcome: %+v; %s", board.card, logs.String())
		}
		return
	}
	if board.card.ColumnName != "Done" || !slices.Contains(board.card.Tags, "to-test") {
		t.Fatalf("Main did not reconcile: %+v; calls %v", board.card, board.calls)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), `"msg":"board_code"`) != 1 || !strings.Contains(logs.String(), `"outcome":"reconciled"`) || !strings.Contains(logs.String(), `"writes":2`) {
		t.Fatalf("missing run outcome: %s", logs.String())
	}
}

func TestBoardCodeRunOutcomesAndFailureFact(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}

	// A v2 launch with no prepared binding must never downgrade to scalar Git.
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", Runtime: "host", OwnerID: topicOwner, LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "app", Name: "blerg", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if err := db.InsertSessionWorkspace(t.Context(), fx.pool, fx.sess, manifest); err != nil {
		t.Fatal(err)
	}

	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		change  func(*db.SessionRow)
		outcome string
		fact    bool
	}{
		{"child", func(r *db.SessionRow) { parent := "PRIVATE_CANARY"; r.ParentSessionID = &parent }, "child_session", false},
		{"account", func(r *db.SessionRow) { r.SpawningAccountID = nil }, "account_unavailable", true},
		{"workspace", func(r *db.SessionRow) {}, "workspace_unavailable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs boardCodeLogBuffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			rowCopy := *row
			tc.change(&rowCopy)
			facts := fx.api.reconcileBoardCode(t.Context(), &rowCopy, time.Now())
			if strings.Count(logs.String(), `"msg":"board_code"`) != 1 || !strings.Contains(logs.String(), `"outcome":"`+tc.outcome+`"`) || strings.Contains(logs.String(), "PRIVATE_CANARY") {
				t.Fatalf("unsafe or missing outcome: %s", logs.String())
			}
			if (len(facts) > 0) != tc.fact {
				t.Fatalf("failure fact: %+v", facts)
			}
		})
	}
}

func TestBoardCodeFailureReachesPlanDrift(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon', repo='' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if got := fx.turn("human"); !strings.Contains(got, "A board/code reconciliation run reported: workspace unavailable.") {
		t.Fatalf("missing reconciliation notice: %q", got)
	}
}

func TestBoardCode600CardTurnEndCallBudget(t *testing.T) {
	testBoardCodeTurn(t, boardCodeTurnOptions{count: 600})
}

func TestBoardCodeStallChecklistAndBranchActivity(t *testing.T) {
	for _, tc := range []struct {
		name              string
		checklist, branch bool
	}{{"stalled", false, false}, {"checklist", true, false}, {"worker branch commit", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			testBoardCodeTurn(t, boardCodeTurnOptions{stalled: true, checklist: tc.checklist, branchActivity: tc.branch})
		})
	}
}
